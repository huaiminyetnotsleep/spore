// 大文件分卷拆分投递：超过单文件 MTProto 上传上限（MaxFileSize，2000MB 级）
// 的媒体不再以 FILE_TOO_LARGE 失败，切段为 N 个 ≤1800MB 的分段，经相册整组
// 直传为同一条消息（≤10 段，总量上限约 17.6GB）。两种形态：
//
//  1. 可播放视频分段（视频唯一路径）：流复制（-c copy，不转码、秒级、无损）
//     切出 N 个真实视频文件，以 video 形态上传（挂 DocumentAttributeVideo），
//     每段点开即播、无需下载合并；切段边界对齐关键帧，段长有 ±GOP 级偏差。
//     切段能力（ffmpeg 含 matroska 封装器、源带时长）在下载开始前前置校验
//     （ensurePlayableSplit），不满足直接报错终止任务——不降级字节分段
//     （设计决策 2026-09-20，真机两轮教训）。视频分段双模式（按 moov 位置
//     判定，见 openVideoSegmentEntries）：
//     - 头 moov 的 MP4 → 单遍流式切段：下载流（有界窗口并行）直接喂
//       ffmpeg stdin 边下边切，源文件全程不落盘，峰值磁盘 1×；
//     - 尾 moov / 非 MP4 → 回退完整落盘后逐段 -ss/-t 切段（moov 在尾部的
//       视频 demuxer 必须读到文件尾），峰值磁盘 2×，切段完成即删源。
//  2. 字节分段 document（非视频媒体的唯一路径，非"降级"）：压缩包等无法
//     播放的媒体纯字节切割为普通 document，首段 caption 附合并提示。
//
// 任一形态、任一环节失败：清全部段文件、零字节已发、整组原子。

package queue

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/config"
	"github.com/huaiminyetnotsleep/spore/internal/delivery"
	"github.com/huaiminyetnotsleep/spore/internal/media"
	"github.com/huaiminyetnotsleep/spore/internal/message"
)

// splitCutTimeout 是单段流复制的时间上限：-c copy 为磁盘 IO 型（GB 级
// 秒级~分钟级），正常远低于该值；超时视为该源无法切段（任务失败，不降级）。
const splitCutTimeout = 10 * time.Minute

// needsSplit 判断媒体是否需要分卷拆分：超过单文件上传上限且配置允许拆分
// （MaxSplitTotalSize 已在 media.Open 预检，能走到发送的必然可承载）。
func needsSplit(opt media.Options, m message.Media) bool {
	return opt.MaxSplitTotalSize > 0 && m.Size > opt.MaxFileSize
}

// splittableVideo 判断媒体能否切为可播放的视频分段：视频、落在拆分上限内、
// 源带时长属性（切段点按时间换算）、ffmpeg 可用。不含 matroska 封装器探测
// （需要起进程）——由 ensurePlayableSplit 在下载前补充校验。
func splittableVideo(opt media.Options, m message.Media) bool {
	return splitVideoSkipReason(opt, m) == ""
}

// splitVideoSkipReason 返回无法切为可播放分段的原因（前置校验与诊断日志
// 共用）；空串表示可以切段（matroska 封装器探测除外）。
func splitVideoSkipReason(opt media.Options, m message.Media) string {
	switch {
	case m.Kind != message.KindVideo:
		return "非视频媒体"
	case m.Size <= opt.MaxFileSize:
		return "未超单文件上限"
	case opt.MaxSplitTotalSize <= 0:
		return "拆分投递未启用"
	case m.Size > opt.MaxSplitTotalSize:
		return "超出拆分总上限"
	case m.Video == nil || m.Video.Duration <= 0:
		return "源视频缺少时长属性"
	case opt.FFmpegPath == "":
		return "FFMPEG_PATH 未配置"
	default:
		if _, err := exec.LookPath(opt.FFmpegPath); err != nil {
			return fmt.Sprintf("ffmpeg 不可用（%s）", opt.FFmpegPath)
		}
		return ""
	}
}

// ensurePlayableSplit 下载前的前置校验：超限视频必须具备可播放切段条件
// （ffmpeg 可用且含 matroska 封装器、源带时长），不满足立即以
// SPLIT_UNAVAILABLE 报错终止任务——2GB 级文件下完才发现切段不可用，
// 白耗流量与磁盘；校验通过后切段仍运行期失败则按原样报错，同样不降级。
// 非视频媒体走字节分段，无前置能力需求。
func ensurePlayableSplit(ctx context.Context, opt media.Options, m message.Media) error {
	if !needsSplit(opt, m) || m.Kind != message.KindVideo {
		return nil
	}
	if reason := splitVideoSkipReason(opt, m); reason != "" {
		return apperr.New(apperr.CodeSplitUnavailable,
			fmt.Sprintf("超大视频可播放切段不可用：%s", reason))
	}
	if err := media.CheckMatroskaMuxer(ctx, opt.FFmpegPath); err != nil {
		return apperr.New(apperr.CodeSplitUnavailable,
			fmt.Sprintf("超大视频可播放切段不可用：%v", err))
	}
	return nil
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

// splitPartName 生成字节分段文件名：通用约定 <原名>.part<i>of<n>，合并工具
// 按该命名识别顺序。
func splitPartName(base string, i, n int) string {
	return fmt.Sprintf("%s.part%dof%d", base, i, n)
}

// videoSegmentName 生成可播放分段的名字：<去扩展名>.part<i>of<n>.mkv。
// 输出容器固定 matroska（与 cutVideoSegment 的 -f matroska 一致）：mkv 接受
// 几乎所有编解码流，且切段不依赖 ffmpeg 按输出扩展名猜 muxer。
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

// videoSegment 是一个切好的可播放分段：真实视频文件 + 上传元数据。
type videoSegment struct {
	media message.Media // Kind=video，Video 带段时长与源宽高
	path  string        // 段文件路径（TempName 约定，发送后删除）
	thumb []byte        // 尽力而为解析的封面（可为 nil）
}

// sendSplitDocument 单媒体拆分投递入口（openAndSend 分派，前置校验已在
// openAndSend 完成）：视频切段为可播放分段、非视频字节分段 document，
// 各自失败原样报错——没有"切段失败降级字节分段"的回退（设计决策
// 2026-09-20）。成功按整组记一次送达，观测标记 split。
func sendSplitDocument(ctx context.Context, d Deps, j Job, target int64, it message.Item, m message.Media, h *media.Handle, sourceURL string, links []message.ChannelLink, track *deliveryTrack, sent *sentIDs) error {
	if m.Kind == message.KindVideo {
		entries, cleanup, err := openVideoSegmentEntries(ctx, d, j, it, m, h, sourceURL, links, true)
		if err != nil {
			return err
		}
		defer cleanup()
		ids, err := d.senderFor(j).SendAlbum(ctx, target, entries)
		if err != nil {
			return err
		}
		sent.addSpan(ids)
		track.split = true
		track.delivered() // 整组按一次送达计（与相册同语义）
		return nil
	}
	return sendByteSectionDocument(ctx, d, j, target, it, m, h, sourceURL, links, track, sent)
}

// openVideoSegmentEntries 把可拆视频成员切段为 N 个可播放分段并构造相册
// 条目。双模式（按句柄形态与 moov 位置判定，头 moov 预判是确定性 box 头
// 解析而非试错）：
//
//   - 流式句柄（media.Open 的有界窗口路径）→ 头部缓冲 64KB 预判：
//     头 moov 的 MP4 → 单遍流式切段（streamVideoSegments）：下载流经
//     io.MultiReader 接回后直接喂 ffmpeg stdin，源不落盘、峰值磁盘 1×；
//     尾 moov / 非 MP4 / 无法判定 → 回退：立即取消在途流式下载（浪费 ≤
//     窗口大小），2× 预检装不下即 TEMP_DIR_FULL（此时仅消耗头部缓冲字节），
//     经 media.OpenSplitFile 重新完整落盘后走逐段 -ss/-t 路径。
//   - 文件句柄（内存预算降级或 OpenSplitFile 回退）→ 阻塞等待完整落盘
//     → 2× 预检 → 逐段流复制切段。
//
// 文件路径切段全部成功后立即删除源文件（上传阶段磁盘 2×→1×；Cleanup
// 幂等，调用方 defer 再调无害）。返回的 cleanup 关闭全部 reader 并删除段
// 文件，必须在整组发送消费完 reader 之后调用（单媒体路径 defer、整组路径
// 函数级 defer 统一兜底）。
//
// caption 语义：该拆分源成员自己的正文与切段说明始终挂在首段——路由层
// 发送前会把整组 caption 统一归一化合并到组首（normalizeAlbumCaptions），
// 归一化前每条语义 caption 都要在场，正文才不会丢；withSource 表示该成员
// 是整组组首（单媒体拆分、大视频领队），由它携带原消息链接与频道脚注。
// 其余分段一律不带 caption。
func openVideoSegmentEntries(ctx context.Context, d Deps, j Job, it message.Item, m message.Media, h *media.Handle, sourceURL string, links []message.ChannelLink, withSource bool) ([]delivery.AlbumEntry, func(), error) {
	key := tempKey(j, it)
	n, err := splitSegmentCount(m.Size, d.Media.SplitSegmentSize)
	if err != nil {
		return nil, nil, err
	}

	if src, ok := h.Path(); ok {
		segs, err := cutFromDownloadedFile(ctx, d, m, h, src, n, key)
		if err != nil {
			return nil, nil, err
		}
		return videoSegmentEntries(d, j, it, m, segs, sourceURL, links, withSource)
	}

	// 流式句柄：头部缓冲预判 moov 位置（判定字节经 MultiReader 接回消费流，
	// 与抽帧路径的"头部字节必须接回"红线同构）
	head := make([]byte, media.SniffHeadBytes)
	hn, err := io.ReadFull(h.Reader, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, nil, err // 下载失败（已按 apperr 分类，含引用过期的刷新重试链）
	}
	head = head[:hn]
	pos := media.SniffMoovPosition(head)
	d.Log.Info("超大视频容器判定",
		"file", m.FileName, "size", m.Size, "moov", pos.String(), "sniff_bytes", hn)

	if pos == media.MoovHead {
		stream := io.MultiReader(bytes.NewReader(head), h.Reader)
		segs, err := streamVideoSegments(ctx, d, m, stream, key)
		if err != nil {
			return nil, nil, err
		}
		if h.Cleanup != nil {
			h.Cleanup() // 流已耗尽：释放窗口内存（幂等，调用方 defer 再调无害）
		}
		return videoSegmentEntries(d, j, it, m, segs, sourceURL, links, withSource)
	}

	// 尾 moov / 非 MP4 / 无法判定：回退完整落盘。流式下载在此取消——
	// 判定发生在头部缓冲处，浪费带宽上限 ≈ 窗口大小
	d.Log.Info("超大视频回退完整落盘切段",
		"file", m.FileName, "size", m.Size, "moov", pos.String())
	if h.Cleanup != nil {
		h.Cleanup()
	}
	// 分段文件近似再落一份（源 + 分段 ≈ 2× 文件大小）：回退时源尚未落盘
	//（流式路径 used 里不含源），显式按 2× 预检；装不下立即失败、不重新
	// 下载——此时仅消耗了头部缓冲字节（浪费上限 ≈ 窗口大小）
	if err := media.CheckTempDir(d.Media, 2*m.Size, d.Log); err != nil {
		return nil, nil, err
	}
	fh, err := media.OpenSplitFile(ctx, d.Fetcher.API(), m, key+"-r", d.Media, d.Log, downloadReporter(d, j))
	if err != nil {
		return nil, nil, err
	}
	src, ok := fh.Path()
	if !ok {
		if fh.Cleanup != nil {
			fh.Cleanup()
		}
		return nil, nil, apperr.New(apperr.CodeInternal, "回退拆分未走临时文件路径")
	}
	segs, err := cutFromDownloadedFile(ctx, d, m, fh, src, n, key)
	if err != nil {
		if fh.Cleanup != nil {
			fh.Cleanup()
		}
		return nil, nil, err
	}
	return videoSegmentEntries(d, j, it, m, segs, sourceURL, links, withSource)
}

// cutFromDownloadedFile 完整落盘后的逐段流复制：等待落盘 → 2× 预检 →
// createVideoSegments → 立即删源（切段完成即释放 1×，上传阶段盘上只剩
// 分段；Cleanup 幂等）。句柄生命周期封闭在本函数内（含 OpenSplitFile
// 回退句柄，不经调用方 handles 列表）。
func cutFromDownloadedFile(ctx context.Context, d Deps, m message.Media, h *media.Handle, src string, n int, key string) ([]videoSegment, error) {
	if err := h.WaitDownloaded(ctx); err != nil {
		return nil, err
	}
	// 分段文件近似再落一份：峰值磁盘 ≈ 2× 文件大小（占用统计有 TTL 缓存，
	// 同任务重复预检廉价）
	if err := media.CheckTempDir(d.Media, m.Size, d.Log); err != nil {
		return nil, err
	}
	segs, err := createVideoSegments(ctx, d, m, src, n, key)
	if err != nil {
		return nil, err
	}
	if h.Cleanup != nil {
		h.Cleanup() // 提前删源：段文件所有权已移交发送方 cleanup
	}
	return segs, nil
}

// videoSegmentEntries 把切好的分段构造为相册条目并返回统一 cleanup（关闭
// 全部 reader 并删除段文件）。切段说明按实际段数生成（流式切段的关键帧
// 边界偏差会改变段数，n 以实际产出为准）。
func videoSegmentEntries(d Deps, j Job, it message.Item, m message.Media, segs []videoSegment, sourceURL string, links []message.ChannelLink, withSource bool) ([]delivery.AlbumEntry, func(), error) {
	caption := it.MediaCaption().WithQuotedBody()
	if withSource {
		caption = caption.WithSourceLink(sourceURL).WithChannels(links)
	}
	caption = caption.WithNote(splitVideoNote(len(segs)))

	entries := make([]delivery.AlbumEntry, 0, len(segs))
	readers := make([]io.Closer, 0, len(segs))
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
			return nil, nil, apperr.Wrap(apperr.CodeMediaDownloadFailed, err)
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
// 文件后以 SPLIT_UNAVAILABLE 报错（不降级）。段实际大小超单文件上限同样
// 报 FILE_TOO_LARGE——与其烧完上传再被服务器拒，不如提前失败。
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
		for _, seg := range segs { // 失败：删除已切段文件
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
			return nil, apperr.New(apperr.CodeSplitUnavailable, err.Error())
		}
		info, err := os.Stat(out)
		if err != nil {
			return nil, apperr.Wrap(apperr.CodeMediaDownloadFailed, err)
		}
		if info.Size() > config.MaxMediaFileSize {
			return nil, apperr.New(apperr.CodeFileTooLarge,
				fmt.Sprintf("分段 %s 实际大小 %d 超过单文件上限 %d（关键帧边界偏移）", name, info.Size(), config.MaxMediaFileSize))
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

	attachSegmentThumbs(ctx, d, m, segs, key)
	keep = true // 全部切段成功：段文件保留，交由发送方 cleanup 删除
	return segs, nil
}

// attachSegmentThumbs 逐段解析封面：每段本身是可解码视频——首段源缩略图
// 优先（与原视频画面一致），回退对段文件 0 秒抽帧；其余段对段文件 0 秒
// 抽帧。尽力而为，失败段无封面照常投递。流式与落盘两条切段路径共用。
func attachSegmentThumbs(ctx context.Context, d Deps, m message.Media, segs []videoSegment, key string) {
	ffmpegOK := false
	if d.Media.FFmpegPath != "" {
		if _, err := exec.LookPath(d.Media.FFmpegPath); err == nil {
			ffmpegOK = true
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
}

// cutVideoSegment 执行单段流复制：-ss 输入快定位（对齐关键帧，首帧可解码）
// + -c copy 不转码 + -avoid_negative_ts make_zero 修正复制切段的负时间戳。
// 输出 muxer 显式指定 matroska（镜像精简 ffmpeg 已含；不依赖输出扩展名
// 猜测）。
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

// streamVideoSegments 单遍流式切段（头 moov 的 MP4）：下载流直接喂 ffmpeg
// stdin，-c copy + segment muxer 边下边切——源文件全程不落盘，盘上只产生
// 分段（峰值磁盘 1×）。切段时长按平均码率近似（与落盘路径的切点公式同
// 源），实际切点对齐关键帧：产出段数以实际为准（可能有 ±1 偏差），逐段
// 防御大小上限、段数超相册上限报 FILE_TOO_LARGE。
//
// 失败语义与落盘路径一致（整组原子）：任一环失败（下载断/ffmpeg 死/
// stdin EPIPE/段超限）删除全部已产出段文件后报错，零字节已发。错误按
// 环节归因：读源失败原样外传（下载错误已按 apperr 分类，支撑引用过期
// 刷新重试链）；EPIPE（ffmpeg 提前死亡）与 ffmpeg 非零退出以
// SPLIT_UNAVAILABLE 报告。
func streamVideoSegments(ctx context.Context, d Deps, m message.Media, src io.Reader, key string) ([]videoSegment, error) {
	base := m.FileName
	if base == "" {
		base = "video"
	}
	duration := float64(m.Video.Duration)
	segTime := duration * float64(d.Media.SplitSegmentSize) / float64(m.Size) // 单段目标时长
	stem := fmt.Sprintf("%s-s-seg", key)
	pattern := filepath.Join(d.Media.TmpDir, media.TempName(fmt.Sprintf("%s-s", key), "seg%03d.mkv"))

	cmd := exec.CommandContext(ctx, d.Media.FFmpegPath,
		"-hide_banner", "-loglevel", "error",
		"-f", "mp4", // 嗅探已确认容器，显式声明消除管道探测歧义
		"-i", "pipe:0",
		"-c", "copy",
		"-avoid_negative_ts", "make_zero",
		"-f", "segment",
		"-segment_format", "matroska",
		"-segment_time", strconv.FormatFloat(segTime, 'f', 3, 64),
		"-reset_timestamps", "1",
		"-y", pattern,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, apperr.Wrap(apperr.CodeSplitUnavailable, err)
	}
	if err := cmd.Start(); err != nil {
		return nil, apperr.New(apperr.CodeSplitUnavailable, fmt.Sprintf("ffmpeg 启动失败: %v", err))
	}

	keep := false // 成功返回时置位，跳过失败清理
	defer func() {
		if keep {
			return
		}
		removeStreamSegments(d, stem)
	}()

	started := time.Now()
	copyDone := make(chan error, 1)
	go func() {
		_, cerr := io.Copy(stdin, src) // 下载→ffmpeg：读源错误与 EPIPE 都从这里浮出
		copyDone <- cerr
	}()
	if cerr := <-copyDone; cerr != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if errors.Is(cerr, syscall.EPIPE) {
			return nil, apperr.New(apperr.CodeSplitUnavailable,
				fmt.Sprintf("流式切段中断（ffmpeg 提前退出）: %v（输出: %.300s）", cerr, stderr.Bytes()))
		}
		return nil, cerr // 下载侧失败：原样外传（已按 apperr 分类）
	}
	if err := stdin.Close(); err != nil { // 关闭 stdin 让 ffmpeg 收到 EOF 并落定末段
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, apperr.New(apperr.CodeSplitUnavailable, fmt.Sprintf("流式切段关闭 stdin 失败: %v", err))
	}
	if werr := cmd.Wait(); werr != nil {
		return nil, apperr.New(apperr.CodeSplitUnavailable,
			fmt.Sprintf("流式切段失败: %v（输出: %.300s）", werr, stderr.Bytes()))
	}

	produced := collectStreamSegments(d, stem)
	if len(produced) == 0 {
		return nil, apperr.New(apperr.CodeSplitUnavailable, "流式切段未产出分段")
	}
	if len(produced) > message.AlbumMaxItems {
		return nil, apperr.New(apperr.CodeFileTooLarge,
			fmt.Sprintf("流式切段产出 %d 段，超过相册单组上限 %d（关键帧边界偏移）", len(produced), message.AlbumMaxItems))
	}

	segs := make([]videoSegment, 0, len(produced))
	var total int64
	for i, p := range produced {
		name := videoSegmentName(base, i+1, len(produced))
		final := filepath.Join(d.Media.TmpDir, media.TempName(fmt.Sprintf("%s-seg%d", key, i+1), name))
		if err := os.Rename(p, final); err != nil {
			return nil, apperr.Wrap(apperr.CodeMediaDownloadFailed, err)
		}
		info, err := os.Stat(final)
		if err != nil {
			return nil, apperr.Wrap(apperr.CodeMediaDownloadFailed, err)
		}
		if info.Size() > config.MaxMediaFileSize {
			return nil, apperr.New(apperr.CodeFileTooLarge,
				fmt.Sprintf("分段 %s 实际大小 %d 超过单文件上限 %d（关键帧边界偏移）", name, info.Size(), config.MaxMediaFileSize))
		}
		// 段时长按平均码率近似（元数据展示用，与落盘路径同近似级别）
		segDur := duration * float64(info.Size()) / float64(m.Size)
		seg := videoSegment{
			media: message.Media{
				Kind: message.KindVideo,
				Video: &message.VideoMeta{
					Width:    m.Video.Width,
					Height:   m.Video.Height,
					Duration: max(int(segDur), 1),
				},
				FileName: name,
				Size:     info.Size(),
			},
			path: final,
		}
		total += info.Size()
		segs = append(segs, seg)
	}

	attachSegmentThumbs(ctx, d, m, segs, key)
	keep = true
	d.Log.Info("流式切段完成",
		"file", m.FileName, "size", m.Size, "segments", len(segs),
		"total_seg_bytes", total, "elapsed_ms", time.Since(started).Milliseconds())
	return segs, nil
}

// collectStreamSegments 收集流式切段产出的段文件（seg%03d.mkv 连续编号，
// ffmpeg 进程退出后文件已落定）。空切片表示未产出任何段。
func collectStreamSegments(d Deps, stem string) []string {
	var produced []string
	for i := 0; ; i++ {
		p := filepath.Join(d.Media.TmpDir, fmt.Sprintf("%s%03d.mkv", stem, i))
		if _, err := os.Stat(p); err != nil {
			break
		}
		produced = append(produced, p)
	}
	return produced
}

// removeStreamSegments 删除流式切段已产出的段文件（失败清理路径，含
// ffmpeg 中途死亡时的部分产出）。
func removeStreamSegments(d Deps, stem string) {
	for _, p := range collectStreamSegments(d, stem) {
		if rmErr := os.Remove(p); rmErr != nil && !os.IsNotExist(rmErr) {
			d.Log.Warn("流式分段文件清理失败", "path", p, "error", rmErr.Error())
		}
	}
}

// sendByteSectionDocument 字节分段投递（非视频媒体的唯一路径）：按字节切
// 为 N 个普通 document（不可独立播放，首段 caption 附合并提示）。逻辑与
// 可播放分段同构：完整落盘 → 按段区间构造条目 → 整组直传。非视频无封面。
func sendByteSectionDocument(ctx context.Context, d Deps, j Job, target int64, it message.Item, m message.Media, h *media.Handle, sourceURL string, links []message.ChannelLink, track *deliveryTrack, sent *sentIDs) error {
	if err := h.WaitDownloaded(ctx); err != nil {
		return err
	}
	n, err := splitSegmentCount(m.Size, d.Media.SplitSegmentSize)
	if err != nil {
		return err
	}

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
			Kind:     message.KindDocument,
			FileName: splitPartName(base, i, n),
			Size:     length,
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
