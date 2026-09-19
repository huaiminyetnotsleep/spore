// 大文件分卷拆分投递：超过单文件 MTProto 上传上限（MaxFileSize，2000MB 级）
// 的媒体不再以 FILE_TOO_LARGE 失败，按 SplitSegmentSize 切为 N 个 ≤1900MB 的
// 分段 document，完整落盘后经相册整组直传为同一条消息（≤10 段，总量上限
// 约 19GB）。分段是纯字节切割、不可独立播放，因此以普通 document 发送
//（不挂 video 属性，避免客户端给出播放必败的假播放器）；用户下载全部分段
// 后按首段 caption 的提示合并。

package queue

import (
	"context"
	"fmt"
	"io"
	"os/exec"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/delivery"
	"github.com/huaiminyetnotsleep/spore/internal/media"
	"github.com/huaiminyetnotsleep/spore/internal/message"
)

// needsSplit 判断媒体是否需要分卷拆分：超过单文件上传上限且配置允许拆分
// （MaxSplitTotalSize 已在 media.Open 预检，能走到发送的必然可承载）。
func needsSplit(opt media.Options, m message.Media) bool {
	return opt.MaxSplitTotalSize > 0 && m.Size > opt.MaxFileSize
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

// splitPartName 生成分段文件名：通用约定 <原名>.part<i>of<n>，合并工具按
// 该命名识别顺序。
func splitPartName(base string, i, n int) string {
	return fmt.Sprintf("%s.part%dof%d", base, i, n)
}

// splitNote 是首段 caption 末尾的合并提示（纯文本，无 shell 变量插值——
// 文件名可能含空格与元字符，示例统一用占位写法）。
func splitNote(n int) string {
	return fmt.Sprintf("📦 原文件超过单文件上限，已分为 %d 段。请全部下载后按序号合并："+
		"macOS/Linux 终端执行 cat 文件.part1of%d 文件.part2of%d … > 文件，"+
		"Windows 命令行执行 copy /b 文件.part1of%d+文件.part2of%d+… 文件。",
		n, n, n, n, n)
}

// sendSplitDocument 把超过单文件上限的媒体切分为 N 个分段 document，经
// 相册整组直传为同一条消息。拆分依赖完整文件：分段区间读取与逐段定位
// 抽帧都要求已完整落盘，因此先 WaitDownloaded 再发送（放弃临时文件路径
// 的"边下边传"交叠——按段延迟等待需要把就绪感知织入 SendAlbum 的成员
// 循环，复杂度不成比例；段数 ≤10，总耗时仍远在任务超时预算内）。
// caption 与来源脚注只挂首段，并追加合并提示；成功按整组记一次送达，
// 投递观测标记 split 供 delivery_mode 归并。
func sendSplitDocument(ctx context.Context, d Deps, j Job, target int64, it message.Item, m message.Media, h *media.Handle, sourceURL string, links []message.ChannelLink, track *deliveryTrack, sent *sentIDs) error {
	if err := h.WaitDownloaded(ctx); err != nil {
		return err
	}
	seg := d.Media.SplitSegmentSize
	n, err := splitSegmentCount(m.Size, seg)
	if err != nil {
		return err
	}
	// ffmpeg 封面在发送前统一解析（完整文件已落盘，抽帧按时间戳定位）
	thumbs := splitThumbs(ctx, d, m, h, seg, n, tempKey(j, it)+"-thumb")

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
		start := int64(i-1) * seg
		length := min(seg, m.Size-start)
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
	sent.addAll(ids)
	track.split = true
	track.delivered() // 整组按一次送达计（与相册同语义）
	return nil
}

// splitThumbs 为 N 个分段解析封面字节（尽力而为，失败段无封面照常投递）：
//   - 非视频媒体不解析（分段封面只对视频有辨识意义）；
//   - 首段沿用单发视频的封面语义：源自带缩略图优先，失败或缺失时对完整
//     文件做 0 秒定位抽帧（单发路径的头部字节抽帧在这里不适用——分段以
//     document 形态发送，封面只是文档预览图）；
//   - 第 2 段起：分段字节流没有容器头不可解码，按"段起始字节 / 总大小 ×
//     总时长"换算时间戳，对完整文件用 ffmpeg 定位抽帧。VBR 视频按平均
//     码率近似，画面与段边界可能有小偏移，对辨识目的无影响；
//   - 源缺时长属性、ffmpeg 不可用或抽帧失败 → 该段降级无封面。
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
