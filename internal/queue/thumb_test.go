package queue

// prepareVideoThumb 的分层行为测试：
//   - 源缩略图坐标存在 → 独立小下载（thumbAwareInvoker 按 ThumbSize 区分
//     缩略图与主媒体字节），上传数据源原样透传（同一 reader、内容不动）；
//   - 无坐标 → 回退 ffmpeg 抽帧（假可执行脚本注入），消费的头部字节经
//     MultiReader 接回，上传数据源内容完整；
//   - ffmpeg 缺失/抽帧失败/流损坏/非视频类型 → 降级与传播语义。

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"

	"github.com/huaiminyetnotsleep/spore/internal/media"
	"github.com/huaiminyetnotsleep/spore/internal/message"
)

// thumbAwareInvoker 按 ThumbSize 区分缩略图与主媒体的假下载通道：
// 带 ThumbSize 的请求返回 thumb 字节，其余返回 main 字节。
type thumbAwareInvoker struct {
	main  []byte
	thumb []byte
}

func (c thumbAwareInvoker) Invoke(_ context.Context, in bin.Encoder, out bin.Decoder) error {
	req, ok := in.(*tg.UploadGetFileRequest)
	if !ok {
		return errors.New("thumbAwareInvoker: 意外的请求类型")
	}
	payload := c.main
	if loc, ok := req.Location.(*tg.InputDocumentFileLocation); ok && loc.ThumbSize != "" {
		payload = c.thumb
	}
	if req.Offset >= int64(len(payload)) {
		return encodeUploadFile(out, nil)
	}
	end := min(req.Offset+int64(req.Limit), int64(len(payload)))
	return encodeUploadFile(out, payload[req.Offset:end])
}

// thumbVideoMsg 同 docMsg，但可为视频文档附带缩略图档。
func thumbVideoMsg(id int, accessHash int64, thumbs []tg.PhotoSizeClass) *tg.Message {
	m := docMsg(id, accessHash)
	m.Media.(*tg.MessageMediaDocument).Document.(*tg.Document).Thumbs = thumbs
	return m
}

// videoMediaOf 经 ConvertOne 提取视频媒体（缩略图坐标随转换产生）。
func videoMediaOf(t *testing.T, m *tg.Message) *message.Media {
	t.Helper()
	it := message.ConvertOne(m)
	if it.Media == nil || it.Media.Kind != message.KindVideo {
		t.Fatalf("应转换为视频媒体: %+v", it.Media)
	}
	return it.Media
}

// writeExecScript 写入可执行的假 ffmpeg 脚本并返回其路径。
func writeExecScript(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fake-ffmpeg")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatalf("写假 ffmpeg 脚本失败: %v", err)
	}
	return p
}

func thumbTestDeps(t *testing.T, inv testInvoker, ffmpegPath string) Deps {
	t.Helper()
	return Deps{
		Fetcher: fetcherWith(inv),
		Media: media.Options{
			TmpDir:      t.TempDir(),
			MaxFileSize: 1 << 30,
			StreamLimit: 1 << 30,
			FFmpegPath:  ffmpegPath,
		},
		Log: testLog(),
	}
}

// errReader 模拟下载流损坏（非 EOF 错误）。
type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

func TestPrepareVideoThumbSourceDownload(t *testing.T) {
	thumbBytes := []byte("source-thumb-jpeg")
	inv := thumbAwareInvoker{main: []byte("main-video-bytes"), thumb: thumbBytes}
	d := thumbTestDeps(t, inv, "")
	m := videoMediaOf(t, thumbVideoMsg(7, 1401, []tg.PhotoSizeClass{
		&tg.PhotoSize{Type: "m", W: 320, H: 180, Size: len(thumbBytes)},
	}))
	if m.Thumb == nil {
		t.Fatal("fixture 应携带缩略图坐标")
	}

	src := bytes.NewReader([]byte("main-video-bytes"))
	got, err := prepareVideoThumb(context.Background(), d, m, src, "job-7-thumb")
	if err != nil {
		t.Fatalf("源缩略图下载应成功: %v", err)
	}
	if got != src {
		t.Error("源缩略图路径不应包装上传数据源")
	}
	if !bytes.Equal(m.ThumbJPEG, thumbBytes) {
		t.Errorf("ThumbJPEG 应为源缩略图字节: %q", m.ThumbJPEG)
	}
}

func TestPrepareVideoThumbFFmpegFallback(t *testing.T) {
	videoBytes := []byte("video-head+video-tail")
	ffmpeg := writeExecScript(t, "printf 'fake-jpeg'\n")

	t.Run("抽帧成功且头部接回", func(t *testing.T) {
		d := thumbTestDeps(t, errInvoker{}, ffmpeg)
		m := videoMediaOf(t, thumbVideoMsg(7, 1402, nil)) // 无源缩略图
		src := bytes.NewReader(videoBytes)
		got, err := prepareVideoThumb(context.Background(), d, m, src, "job-7-thumb")
		if err != nil {
			t.Fatalf("抽帧回退应成功: %v", err)
		}
		if !bytes.Equal(m.ThumbJPEG, []byte("fake-jpeg")) {
			t.Errorf("ThumbJPEG 应为 ffmpeg 输出: %q", m.ThumbJPEG)
		}
		all, err := io.ReadAll(got)
		if err != nil {
			t.Fatalf("接回后的数据源应可读尽: %v", err)
		}
		if !bytes.Equal(all, videoBytes) {
			t.Errorf("头部字节应接回上传流（内容不变）: %q", all)
		}
	})

	t.Run("抽帧失败降级为无封面但数据流完整", func(t *testing.T) {
		d := thumbTestDeps(t, errInvoker{}, writeExecScript(t, "exit 1\n"))
		m := videoMediaOf(t, thumbVideoMsg(7, 1403, nil))
		src := bytes.NewReader(videoBytes)
		got, err := prepareVideoThumb(context.Background(), d, m, src, "job-7-thumb")
		if err != nil {
			t.Fatalf("抽帧失败应降级而不报错: %v", err)
		}
		if m.ThumbJPEG != nil {
			t.Errorf("抽帧失败不应设置 ThumbJPEG: %q", m.ThumbJPEG)
		}
		all, _ := io.ReadAll(got)
		if !bytes.Equal(all, videoBytes) {
			t.Errorf("已消费的头部字节仍应接回: %q", all)
		}
	})

	t.Run("ffmpeg 缺失透传数据源", func(t *testing.T) {
		d := thumbTestDeps(t, errInvoker{}, filepath.Join(t.TempDir(), "no-such-ffmpeg"))
		m := videoMediaOf(t, thumbVideoMsg(7, 1404, nil))
		src := bytes.NewReader(videoBytes)
		got, err := prepareVideoThumb(context.Background(), d, m, src, "job-7-thumb")
		if err != nil {
			t.Fatalf("ffmpeg 缺失应降级: %v", err)
		}
		if got != src || m.ThumbJPEG != nil {
			t.Error("ffmpeg 缺失时不应消费数据源、不应设置 ThumbJPEG")
		}
	})

	t.Run("FFmpegPath 为空直接跳过", func(t *testing.T) {
		d := thumbTestDeps(t, errInvoker{}, "")
		m := videoMediaOf(t, thumbVideoMsg(7, 1405, nil))
		src := bytes.NewReader(videoBytes)
		got, err := prepareVideoThumb(context.Background(), d, m, src, "job-7-thumb")
		if err != nil || got != src || m.ThumbJPEG != nil {
			t.Error("FFmpegPath 为空时应直接透传")
		}
	})

	t.Run("下载流损坏向外传播", func(t *testing.T) {
		d := thumbTestDeps(t, errInvoker{}, ffmpeg)
		m := videoMediaOf(t, thumbVideoMsg(7, 1406, nil))
		want := errors.New("下载失败")
		if _, err := prepareVideoThumb(context.Background(), d, m, errReader{err: want}, "job-7-thumb"); !errors.Is(err, want) {
			t.Errorf("流损坏错误应原样传播，得到 %v", err)
		}
	})
}

func TestPrepareVideoThumbNonVideo(t *testing.T) {
	d := thumbTestDeps(t, errInvoker{}, "")
	m := &message.Media{Kind: message.KindDocument, Thumb: &message.ThumbMeta{}}
	src := bytes.NewReader([]byte("data"))
	got, err := prepareVideoThumb(context.Background(), d, m, src, "job-7-thumb")
	if err != nil || got != src || m.ThumbJPEG != nil {
		t.Error("非视频媒体应直接透传")
	}
}

// 端到端：任务全链路（fetch → 下载 → 缩略图解析 → 发送）后，
// Sender 收到的媒体携带源缩略图字节（单发与相册成员两路径）。
func TestWorkerCarriesSourceThumb(t *testing.T) {
	thumbBytes := []byte("source-thumb-jpeg")
	videoMsg := thumbVideoMsg(7, 1501, []tg.PhotoSizeClass{
		&tg.PhotoSize{Type: "m", W: 320, H: 180, Size: len(thumbBytes)},
	})

	t.Run("单媒体", func(t *testing.T) {
		s := openStore(t)
		job, _ := newJobWithRequest(t, s, 0)
		inv := thumbAwareInvoker{main: []byte("main-video-bytes"), thumb: thumbBytes}
		sender := &fakeSender{consumeMediaReaders: true}
		d := uploadDeps(t, s, fetcherWith(inv, videoMsg), sender)
		runJobSync(t, d, job)
		calls := sender.mediaCalls
		if len(calls) != 1 {
			t.Fatalf("应恰好一次 SendMedia: %d", len(calls))
		}
		if calls[0].ThumbLen != len(thumbBytes) {
			t.Errorf("发送媒体应携带源缩略图: %d 字节", calls[0].ThumbLen)
		}
	})

	t.Run("相册成员", func(t *testing.T) {
		s := openStore(t)
		job, _ := newJobWithRequest(t, s, 0)
		msgs := []*tg.Message{
			thumbVideoMsg(7, 1601, []tg.PhotoSizeClass{
				&tg.PhotoSize{Type: "m", W: 320, H: 180, Size: len(thumbBytes)},
			}),
			thumbVideoMsg(8, 1602, []tg.PhotoSizeClass{
				&tg.PhotoSize{Type: "m", W: 320, H: 180, Size: len(thumbBytes)},
			}),
		}
		msgs[0].SetGroupedID(99)
		msgs[1].SetGroupedID(99)
		inv := thumbAwareInvoker{main: []byte("main-video-bytes"), thumb: thumbBytes}
		sender := &fakeSender{
			groupable:           func(message.Media) bool { return true },
			consumeAlbumReaders: true,
		}
		d := uploadDeps(t, s, fetcherWith(inv, msgs...), sender)
		runJobSync(t, d, job)
		calls := sender.albumCalls
		if len(calls) != 1 {
			t.Fatalf("应恰好一次 SendAlbum: %d", len(calls))
		}
		for i, n := range calls[0].ThumbLens {
			if n != len(thumbBytes) {
				t.Errorf("相册成员 %d 应携带源缩略图: %d 字节", i, n)
			}
		}
	})
}
