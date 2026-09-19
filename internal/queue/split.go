// 大文件分卷拆分投递：超过单文件 MTProto 上传上限（MaxFileSize，2000MB 级）
// 的媒体不再以 FILE_TOO_LARGE 失败，切段为 N 个 ≤1800MB 的分段，经相册整组
// 直传为同一条消息（≤10 段，总量上限约 17.6GB）。两种切段形态：
//
//  1. 可播放视频分段（首选）：视频且有时长且 ffmpeg 可用——完整落盘后用
//     ffmpeg 流复制（-c copy，不转码、秒级、无损）切出 N 个真实视频文件，
//     以 video 形态上传（挂 DocumentAttributeVideo），每段点开即播、无需
//     下载合并；切段边界对齐关键帧，段长有 ±GOP 级偏差；
//  2. 字节分段 document（兜底）：非视频、缺时长或 ffmpeg 不可用/切段失败
//     ——纯字节切割为普通 document（不挂 video 属性，避免假播放器），用户
//     按首段 caption 提示合并。
//
// 两种形态都要求完整落盘（流复制需要读到完整文件——moov 在尾部的视频必须
// 读到文件尾；字节分段依赖区间读取），先 WaitDownloaded 再切再传。

package queue

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/config"
	"github.com/huaiminyetnotsleep/spore/internal/delivery"
	"github.com/huaiminyetnotsleep/spore/internal/media"
	"github.com/huaiminyetnotsleep/spore/internal/message"
)

// splitCutTimeout 是单段流复制的时间上限：-c copy 为磁盘 IO 型（GB 级
// 秒级~分钟级），正常远低于该值；超时视为该源无法切段（调用方回退字节分段）。
const splitCutTimeout = 10 * time.Minute

// needsSplit 判断媒体是否需要分卷拆分：超过单文件上传上限且配置允许拆分
// （MaxSplitTotalSize 已在 media.Open 预检，能走到发送的必然可承载）。
func needsSplit(opt media.Options, m message.Media) bool {
	return opt.MaxSplitTotalSize > 0 && m.Size > opt.MaxFileSize
}

// splittableVideo 判断媒体能否切为可播放的视频分段：视频、落在拆分上限内、
// 源带时长属性（切段点按时间换算）、ffmpeg 可用。不满足时由字节分段兜底。
func splittableVideo(opt media.Options, m message.Media) bool {
	if m.Kind != message.KindVideo || opt.MaxSplitTotalSize <= 0 {
		return false
	}
	if m.Size <= opt.MaxFileSize || m.Size > opt.MaxSplitTotalSize {
		return false
	}
	if m.Video == nil || m.Video.Duration <= 0 {
		return false
	}
	if opt.FFmpegPath == "" {
		return false
	}
	_, err := exec.LookPath(opt.FFmpegPath)
	return err == nil
}

// splitSegmentCount 计算分段数：按段大小向上取整，防御性夹取到相册成员
// 上限内（≥2 段——进入拆分的媒体必然超过单文件上限）。
func splitSegmentCount(size, seg int64) (int, error) {
	if seg <= 0 {
		return 0, apperr.New(apperr.CodeInternal, "拆分段大小未配置")
	}
	n := int((size + seg - 1) / seg)
	if n < 2 {
		n = 2
	}
	if n > message.AlbumMaxItems {
		return 0, apperr.New(apperr.CodeFileTooLarge,
			fmt.Sprintf("size=%d 需拆 %d 段，超过相册单组上限 %d", size, n, message.AlbumMaxItems))
	}
	return n, nil
}

// splitPartName 生成分段文件名：通用约定 <原名>.part<i>of<n>（字节分段），
// 合并工具按该命名识别顺序。
func splitPartName(base string, i, n int) string {
	return fmt.Sprintf("%s.part%dof%d", base, i, n)
}

// videoSegmentName 生成可播放分段的名字：<去扩展名>.part<i>of<n>.mkv。
// 输出容器固定 matroska（与 cutVideoSegment 的 -f matroska 一致）：mkv 接受
// 几乎所有编解码流，且切段不依赖 ffmpeg 按输出扩展名猜 muxer——源文件名
// 的大写/尾部空白/特殊字符扩展名会让猜测失败（真机 2026-09-20：
// Unable to choose an output format）。
func videoSegmentName(base string, i, n int) string {
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	if stem == "" {
		stem = "video"
	}
	return fmt.Sprintf("%s.part%dof%d.mkv", stem, i, n)
}

// splitNote 是字节分段首段 caption 末尾的合并提示（纯文本，无 shell 变量
// 插值——文件名可能含空格与元字符，示例统一用占位写法）。
func splitNote(n int) string {
	return fmt.Sprintf("📦 原文件超过单文件上限，已分为 %d 段。请全部下载后按序号合并："+
		"macOS/Linux 终端执行 cat 文件.part1of%d 文件.part2of%d … > 文件，"+
		"Windows 命令行执行 copy /b 文件.part1of%d+文件.part2of%d+… 文件。",
		n, n, n, n, n)
}

// splitVideoNote 是可播放分段首段 caption 末尾的说明（各段可直接播放）。
func splitVideoNote(n int) string {
	return fmt.Sprintf("📦 原文件超过单文件上限，已切分为 %d 段视频，每段可直接播放，无需合并。", n)
}

// errSegmentCut 标记切段阶段失败（ffmpeg 执行/段文件落盘/单段超限），
// 与下载失败区分：整组路径据此自动回退逐条投递——切段发生在任何字节发出
// 之前，回退仍是原子的。errors.Is(err, errSegmentCut) 判定。
var errSegmentCut = errors.New("视频切段失败")

// videoSegment 是一个切好的可播放分段：真实视频文件 + 上传元数据。
type videoSegment struct {
	media message.Media // Kind=video，Video 带段时长与源宽高
	path  string        // 段文件路径（TempName 约定，发送后删除）
	thumb []byte        // 尽力而为解析的封面（可为 nil）
}

// sendSplitDocument 单媒体拆分投递入口（openAndSend 分派）：视频优先走
// 可播放分段；不满足条件或 ffmpeg 切段失败时回退字节分段 document。切段
// 成功后的整组发送失败不回退——重新以字节段上传只会重复传输且可能重复
// 投递，任务失败由用户重试。成功按整组记一次送达，观测标记 split。
func sendSplitDocument(ctx context.Context, d Deps, j Job, target int64, it message.Item, m message.Media, h *media.Handle, sourceURL string, links []message.ChannelLink, track *deliveryTrack, sent *sentIDs) error {
	if splittableVideo(d.Media, m) {
		entries, cleanup, err := openVideoSegmentEntries(ctx, d, j, it, m, h, sourceURL, links, true)
		if err == nil {
			defer cleanup()
			ids, sendErr := d.senderFor(j).SendAlbum(ctx, target, entries)
			if sendErr != nil {
				return sendErr
			}
			sent.addSpan(ids)
			track.split = true
			track.delivered() // 整组按一次送达计（与相册同语义）
			return nil
		}
		d.Log.Warn("视频切段失败，回退字节分段投递",
			"job_id", j.ID, "file", m.FileName, "error", err.Error())
	}
	return sendByteSectionDocument(ctx, d, j, target, it, m, h, sourceURL, links, track, sent)
}

// openVideoSegmentEntries 把可拆视频成员（句柄已完整落盘）切段为 N 个可
// 播放分段并构造相册条目：阻塞等待完整落盘 → 临时目录余量预检（分段近似
// 再落一份）→ ffmpeg 流复制切段 → 逐段封面 → 打开段文件 reader。返回的
// cleanup 关闭全部 reader 并删除段文件，必须在整组发送消费完 reader 之后
// 调用（单媒体路径 defer、整组路径函数级 defer 统一兜底）。withSource 控制
// 首段是否织入来源链接与频道脚注（整组路径仅组首成员为 true）。
func openVideoSegmentEntries(ctx context.Context, d Deps, j Job, it message.Item, m message.Media, h *media.Handle, sourceURL string, links []message.ChannelLink, withSource bool) ([]delivery.AlbumEntry, func(), error) {
	if err := h.WaitDownloaded(ctx); err != nil {
		return nil, nil, err
	}
	src, ok := h.Path()
	if !ok {
		return nil, nil, apperr.New(apperr.CodeInternal, "拆分媒体未走临时文件路径")
	}
	n, err := splitSegmentCount(m.Size, d.Media.SplitSegmentSize)
	if err != nil {
		return nil, nil, err
	}
	// 分段文件近似再落一份：峰值磁盘 ≈ 2× 文件大小（占用统计有 TTL 缓存，
	// 同任务重复预检廉价）
	if err := media.CheckTempDir(d.Media, m.Size, d.Log); err != nil {
		return nil, nil, err
	}
	segs, err := createVideoSegments(ctx, d, m, src, n, tempKey(j, it))
	if err != nil {
		return nil, nil, err
	}

	caption := it.MediaCaption().WithQuotedBody()
	if withSource && sourceURL != "" {
		// 脚注与原消息链接同位：只出现在组首/带来源的条目上
		caption = caption.WithSourceLink(sourceURL).WithChannels(links)
	}
	caption = caption.WithNote(splitVideoNote(n))

	entries := make([]delivery.AlbumEntry, 0, n)
	readers := make([]io.Closer, 0, n)
	cleanup := func() {
		for _, c := range readers {
			_ = c.Close()
		}
		for _, seg := range segs {
			if rmErr := os.Remove(seg.path); rmErr != nil && !os.IsNotExist(rmErr) {
				d.Log.Warn("分段文件清理失败", "path", seg.path, "error", rmErr.Error())
			}
		}
	}
	for i, seg := range segs {
		f, err := os.Open(seg.path)
		if err != nil {
			cleanup()
			return nil, nil, errors.Join(errSegmentCut, apperr.Wrap(apperr.CodeMediaDownloadFailed, err))
		}
		readers = append(readers, f)
		ec := message.Caption{}
		if i == 0 {
			ec = caption
		}
		entries = append(entries, delivery.AlbumEntry{
			Media:   seg.media,
			Reader:  uploadReader(d, j, f),
			Caption: ec,
		})
	}
	return entries, cleanup, nil
}

// createVideoSegments 用 ffmpeg 流复制把完整视频切为 N 段（不转码）：
// 切段点按字节占比换算时间（t_i = 总时长 × i×段大小 / 总大小，平均码率
// 近似），实际切点对齐关键帧（-ss 输入快定位，保证首帧可解码）。段文件
// 落 TempDir（TempName 约定，纳入孤儿清理）；任一段失败删除全部已切段
// 文件后返回错误（调用方回退字节分段）。段实际大小超单文件上限时报
// FILE_TOO_LARGE——边界关键帧偏移理论上可能使单段超限，与其烧完上传再被
// 服务器拒，不如提前失败。
func createVideoSegments(ctx context.Context, d Deps, m message.Media, src string, n int, key string) ([]videoSegment, error) {
	base := m.FileName
	if base == "" {
		base = "video"
	}
	segSize := d.Media.SplitSegmentSize
	duration := float64(m.Video.Duration)
	boundary := func(i int64) float64 { // 第 i 段起点时刻（i 从 0 起）
		t := duration * float64(i*segSize) / float64(m.Size)
		return min(t, duration)
	}

	segs := make([]videoSegment, 0, n)
	keep := false // 成功返回时置位，跳过失败清理（段文件交由发送方 cleanup 删除）
	defer func() {
		if keep {
			return
		}
		for _, seg := range segs { // 失败回退：删除已切段文件
			if rmErr := os.Remove(seg.path); rmErr != nil && !os.IsNotExist(rmErr) {
				d.Log.Warn("分段文件清理失败", "path", seg.path, "error", rmErr.Error())
			}
		}
	}()
	for i := 1; i <= n; i++ {
		start, end := boundary(int64(i-1)), boundary(int64(i))
		name := media.TempName(fmt.Sprintf("%s-seg%d", key, i), videoSegmentName(base, i, n))
		out := filepath.Join(d.Media.TmpDir, name)
		if err := cutVideoSegment(ctx, d.Media.FFmpegPath, src, start, end-start, out); err != nil {
			return nil, fmt.Errorf("%w: %w", errSegmentCut, err)
		}
		info, err := os.Stat(out)
		if err != nil {
			return nil, errors.Join(errSegmentCut, apperr.Wrap(apperr.CodeMediaDownloadFailed, err))
		}
		if info.Size() > config.MaxMediaFileSize {
			return nil, errors.Join(errSegmentCut, apperr.New(apperr.CodeFileTooLarge,
				fmt.Sprintf("分段 %s 实际大小 %d 超过单文件上限 %d（关键帧边界偏移）", name, info.Size(), config.MaxMediaFileSize)))
		}
		seg := videoSegment{
			media: message.Media{
				Kind: message.KindVideo,
				Video: &message.VideoMeta{
					Width:    m.Video.Width,
					Height:   m.Video.Height,
					Duration: int(end - start),
				},
				FileName: videoSegmentName(base, i, n),
				Size:     info.Size(),
			},
			path: out,
		}
		if seg.media.Video.Duration <= 0 {
			seg.media.Video.Duration = 1 // 防御：极短末段取整为 0 时仍可播放
		}
		segs = append(segs, seg)
	}

	// 逐段封面：每段本身是可解码视频——首段源缩略图优先（与原视频画面
	// 一致），回退对段文件 0 秒抽帧；其余段对段文件 0 秒抽帧。尽力而为，
	// 失败段无封面照常投递。
	ffmpegOK := d.Media.FFmpegPath != ""
	if ffmpegOK {
		if _, err := exec.LookPath(d.Media.FFmpegPath); err != nil {
			ffmpegOK = false
		}
	}
	for i := range segs {
		switch {
		case i == 0 && m.Thumb != nil:
			if data, err := downloadThumb(ctx, d, m, key+"-thumb"); err == nil {
				segs[i].thumb = data
				continue
			} else {
				d.Log.Warn("源缩略图下载失败，首段回退抽帧",
					"file", m.FileName, "error", err.Error())
			}
			fallthrough
		default:
			if !ffmpegOK {
				continue
			}
			jpeg, err := media.ExtractFrameAtJPEG(ctx, d.Media.FFmpegPath, segs[i].path, 0)
			if err != nil {
				d.Log.Debug("分段抽帧失败，该段无封面发送",
					"file", segs[i].media.FileName, "error", err.Error())
				continue
			}
			segs[i].thumb = jpeg
		}
	}
	for i := range segs {
		segs[i].media.ThumbJPEG = segs[i].thumb
	}
	keep = true // 全部切段成功：段文件保留，交由发送方 cleanup 删除
	return segs, nil
}

// cutVideoSegment 执行单段流复制：-ss 输入快定位（对齐关键帧，首帧可解码）
// + -c copy 不转码 + -avoid_negative_ts make_zero 修正复制切段的负时间戳。
// 输出 muxer 显式指定 matroska（不依赖输出扩展名猜测——真机 2026-09-20：
// 源文件名的大写/空白扩展名导致 Unable to choose an output format）。
func cutVideoSegment(ctx context.Context, ffmpegPath, src string, start, dur float64, out string) error {
	ctx, cancel := context.WithTimeout(ctx, splitCutTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffmpegPath,
		"-hide_banner", "-loglevel", "error", "-nostdin",
		"-ss", strconv.FormatFloat(start, 'f', 3, 64),
		"-i", src,
		"-t", strconv.FormatFloat(dur, 'f', 3, 64),
		"-c", "copy",
		"-avoid_negative_ts", "make_zero",
		"-f", "matroska",
		"-y", out,
	)
	if outBytes, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg 切段失败: %w（输出: %.300s）", err, outBytes)
	}
	return nil
}

// sendByteSectionDocument 字节分段兜底：非视频、缺时长、ffmpeg 不可用或
// 切段失败的媒体按字节切为 N 个普通 document（不可独立播放，首段 caption
// 附合并提示）。逻辑与可播放分段同构：完整落盘 → 按段区间构造条目 →
// 整组直传。
func sendByteSectionDocument(ctx context.Context, d Deps, j Job, target int64, it message.Item, m message.Media, h *media.Handle, sourceURL string, links []message.ChannelLink, track *deliveryTrack, sent *sentIDs) error {
	if err := h.WaitDownloaded(ctx); err != nil {
		return err
	}
	n, err := splitSegmentCount(m.Size, d.Media.SplitSegmentSize)
	if err != nil {
		return err
	}
	// 封面在发送前统一解析（首段原逻辑、后续段按时间戳定位抽帧，见 splitThumbs）
	thumbs := splitThumbs(ctx, d, m, h, d.Media.SplitSegmentSize, n, tempKey(j, it)+"-thumb")

	caption := it.MediaCaption().WithQuotedBody().WithSourceLink(sourceURL)
	if sourceURL != "" {
		// 脚注与原消息链接同位：只出现在组首/带来源的条目上
		caption = caption.WithChannels(links)
	}
	caption = caption.WithNote(splitNote(n))

	entries := make([]delivery.AlbumEntry, 0, n)
	sections := make([]io.Closer, 0, n)
	defer func() {
		for _, c := range sections {
			_ = c.Close()
		}
	}()
	base := m.FileName
	if base == "" {
		base = "file"
	}
	for i := 1; i <= n; i++ {
		start := int64(i-1) * d.Media.SplitSegmentSize
		length := min(d.Media.SplitSegmentSize, m.Size-start)
		sr, err := h.OpenSection(start, length)
		if err != nil {
			return err
		}
		sections = append(sections, sr)
		pm := message.Media{
			Kind:      message.KindDocument,
			FileName:  splitPartName(base, i, n),
			Size:      length,
			ThumbJPEG: thumbs[i-1],
		}
		ec := message.Caption{}
		if i == 1 {
			ec = caption
		}
		entries = append(entries, delivery.AlbumEntry{
			Media:   pm,
			Reader:  uploadReader(d, j, sr),
			Caption: ec,
		})
	}
	ids, err := d.senderFor(j).SendAlbum(ctx, target, entries)
	if err != nil {
		return err
	}
	sent.addSpan(ids)
	track.split = true
	track.delivered() // 整组按一次送达计（与相册同语义）
	return nil
}

// splitThumbs 为字节分段的 N 段解析封面字节（尽力而为，失败段无封面照常
// 投递）：分段字节流没有容器头不可解码，第 2 段起按"段起始字节 / 总大小 ×
// 总时长"换算时间戳，对完整落盘文件用 ffmpeg 定位抽帧。VBR 视频按平均
// 码率近似，画面与段边界可能有小偏移；源缺时长属性、ffmpeg 不可用或抽帧
// 失败 → 该段降级无封面。
func splitThumbs(ctx context.Context, d Deps, m message.Media, h *media.Handle, seg int64, n int, key string) [][]byte {
	thumbs := make([][]byte, n)
	if m.Kind != message.KindVideo {
		return thumbs
	}
	path, ok := h.Path()
	if !ok {
		return thumbs // 防御：拆分必然走临时文件路径
	}
	ffmpegOK := false
	if d.Media.FFmpegPath != "" {
		if _, err := exec.LookPath(d.Media.FFmpegPath); err == nil {
			ffmpegOK = true
		} else {
			d.Log.Debug("ffmpeg 不可用，分段封面降级为无封面",
				"file", m.FileName, "ffmpeg", d.Media.FFmpegPath)
		}
	}
	if m.Thumb != nil {
		if data, err := downloadThumb(ctx, d, m, key); err == nil {
			thumbs[0] = data
		} else {
			d.Log.Warn("源缩略图下载失败，首段回退抽帧",
				"file", m.FileName, "error", err.Error())
		}
	}
	if thumbs[0] == nil && ffmpegOK {
		if jpeg, err := media.ExtractFrameAtJPEG(ctx, d.Media.FFmpegPath, path, 0); err == nil {
			thumbs[0] = jpeg
		} else {
			d.Log.Debug("首段抽帧失败，无封面发送",
				"file", m.FileName, "error", err.Error())
		}
	}
	if m.Video == nil || m.Video.Duration <= 0 || !ffmpegOK {
		return thumbs
	}
	for i := 2; i <= n; i++ {
		start := int64(i-1) * seg
		sec := float64(m.Video.Duration) * float64(start) / float64(m.Size)
		jpeg, err := media.ExtractFrameAtJPEG(ctx, d.Media.FFmpegPath, path, sec)
		if err != nil {
			d.Log.Debug("分段抽帧失败，该段无封面发送",
				"file", m.FileName, "part", i, "sec", sec, "error", err.Error())
			continue
		}
		thumbs[i-1] = jpeg
	}
	return thumbs
}
