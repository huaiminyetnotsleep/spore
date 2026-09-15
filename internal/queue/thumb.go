// 视频封面解析（发送前的尽力而为步骤）：MTProto 服务器不为上传的
// document 自动生成缩略图，Bot API 服务器对部分容器/编码的自动生成也会
// 失败——重发视频须主动携带封面。优先下载源文档自带的缩略图（Telegram
// 客户端上传时生成，与原视频画面一致）；源无缩略图或下载失败时回退
// ffmpeg 从视频头部字节抽首帧；两条路都不通则不带封面照常投递。

package queue

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/media"
	"github.com/huaiminyetnotsleep/spore/internal/message"
)

// thumbHeadBytes 是 ffmpeg 抽帧读取的视频头部字节数：需覆盖容器头与首帧
// （moov 在前的 MP4 数 MB 足够）；小于该值的视频整体读入。moov 在尾部的
// 视频无法从头解析，ffmpeg 失败后降级为无封面（头部字节照常接回上传流）。
const thumbHeadBytes = 8 << 20

// thumbDownloadTimeout 是源缩略图独立下载的时间窗（缩略图 ≤200KB 级，
// 超时视为不可得，回退 ffmpeg 抽帧）。
const thumbDownloadTimeout = 30 * time.Second

// prepareVideoThumb 为视频媒体就地解析缩略图字节（m.ThumbJPEG），并返回
// （可能被包装的）上传数据源：
//  1. 源自带缩略图坐标（m.Thumb）→ 独立小下载（不占进度计数——进度总量
//     按主媒体声明，缩略图字节忽略不计）；
//  2. 无坐标或下载失败 → 检查 ffmpeg 可用后从 src 顺序读出头部字节抽帧，
//     消费掉的头部经 MultiReader 接回，保持 reader 单次消费契约；
//  3. 均不可得 → 不带封面，数据源原样返回。
//
// 只有主下载流自身的读取错误（MEDIA_DOWNLOAD_FAILED 等）才向外传播——
// 流已损坏时上传必然同样失败，sendMediaItem 的过期刷新路径依赖该语义。
// 非视频媒体直接透传；kind 判断与 m.ThumbJPEG 的填充对相册逐成员生效。
func prepareVideoThumb(ctx context.Context, d Deps, m *message.Media, src io.Reader, key string) (io.Reader, error) {
	if m.Kind != message.KindVideo || src == nil {
		return src, nil
	}
	if m.Thumb != nil {
		data, err := downloadThumb(ctx, d, *m, key)
		if err == nil {
			m.ThumbJPEG = data
			return src, nil
		}
		d.Log.Warn("源缩略图下载失败，回退 ffmpeg 抽帧",
			"file", m.FileName, "error", err.Error())
	}
	if d.Media.FFmpegPath == "" {
		return src, nil
	}
	if _, err := exec.LookPath(d.Media.FFmpegPath); err != nil {
		d.Log.Debug("ffmpeg 不可用，视频不带封面发送",
			"ffmpeg", d.Media.FFmpegPath, "error", err.Error())
		return src, nil
	}

	head := make([]byte, thumbHeadBytes)
	n, err := io.ReadFull(src, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		// 下载流自身损坏（含 file reference 过期经 reader 传播）：
		// 原样上抛，由 openAndSend/sendMediaItem 的既有错误路径处理
		return src, err
	}
	head = head[:n]
	// 头部字节已被消费：无论抽帧成败都必须接回上传流
	joined := io.MultiReader(bytes.NewReader(head), src)
	jpeg, err := media.ExtractFrameJPEG(ctx, d.Media.FFmpegPath, d.Media.TmpDir, head)
	if err != nil {
		d.Log.Debug("ffmpeg 抽帧失败，视频不带封面发送",
			"file", m.FileName, "error", err.Error())
		return joined, nil
	}
	m.ThumbJPEG = jpeg
	return joined, nil
}

// downloadThumb 经 media.Open 下载源文档自带缩略图（带 ThumbSize 的文档
// 坐标，服务器只返回该档缩略图字节）。复用主下载通道的 DC 粘性路由；
// file reference 过期不在此刷新——主媒体的刷新重试（sendMediaItem）会带
// 着新坐标重新走到这里。
func downloadThumb(ctx context.Context, d Deps, m message.Media, key string) ([]byte, error) {
	tctx, cancel := context.WithTimeout(ctx, thumbDownloadTimeout)
	defer cancel()
	tm := message.Media{
		Kind:     message.KindPhoto,
		Location: m.Thumb.Location,
		DCID:     m.DCID,
		FileName: message.ThumbFileName,
		Size:     m.Thumb.Size,
	}
	h, err := media.Open(tctx, d.Fetcher.API(), tm, key, d.Media, d.Log, nil)
	if err != nil {
		return nil, err
	}
	defer func() {
		if h.Cleanup != nil {
			h.Cleanup()
		}
	}()
	return io.ReadAll(h.Reader)
}
