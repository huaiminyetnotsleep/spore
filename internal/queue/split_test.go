package queue

// 分卷拆分投递测试：
//   - 单元层：段数计算（含相册上限拒绝）、分段文件名与合并提示；
//   - 链路层：超过单文件上限的媒体经假下载通道完整落盘后切为 N 段，以
//     全 document 相册整组送达假 Sender（逐成员消费 reader，校验分段
//     内容拼接等于原文件），caption 只挂首段并带合并提示，
//     delivery_mode 归并为 split。

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/delivery"
	"github.com/huaiminyetnotsleep/spore/internal/media"
	"github.com/huaiminyetnotsleep/spore/internal/message"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// splitChunkInvoker 返回真实字节的假下载通道：把 UploadGetFile 的
// Offset/Limit 映射到预置 payload 切片（与 media 包的 chunkInvoker 同构，
// 越界偏移返回空字节——下载器据此判结束）。
type splitChunkInvoker struct{ payload []byte }

func (c splitChunkInvoker) Invoke(_ context.Context, in bin.Encoder, out bin.Decoder) error {
	req, ok := in.(*tg.UploadGetFileRequest)
	if !ok {
		return fmt.Errorf("splitChunkInvoker: 意外的请求类型 %T", in)
	}
	if req.Offset < 0 {
		return fmt.Errorf("splitChunkInvoker: 负偏移读取")
	}
	if req.Offset >= int64(len(c.payload)) {
		return encodeSplitFile(out, nil)
	}
	end := min(req.Offset+int64(req.Limit), int64(len(c.payload)))
	return encodeSplitFile(out, c.payload[req.Offset:end])
}

func encodeSplitFile(out bin.Decoder, data []byte) error {
	file := &tg.UploadFile{Type: &tg.StorageFilePartial{}, Bytes: data}
	b := new(bin.Buffer)
	if err := file.Encode(b); err != nil {
		return err
	}
	return out.Decode(b)
}

// splitMediaOptions 拆分链路测试参数：单文件上限 50B、段大小 40B、拆分
// 总上限 400B——95B 媒体拆 40/40/15 三段。
func splitMediaOptions(t *testing.T) media.Options {
	t.Helper()
	return media.Options{
		TmpDir:            t.TempDir(),
		MaxFileSize:       50,
		MaxSplitTotalSize: 400,
		SplitSegmentSize:  40,
	}
}

// oversizeMsg 构造超过单文件上限的视频文档消息（video 属性齐全，供
// 拆分路径按时长换算抽帧时间戳；ffmpeg 未配置时无封面）。
func oversizeMsg(id int, accessHash, size int) *tg.Message {
	return &tg.Message{
		ID: id, Message: "大文件",
		Media: &tg.MessageMediaDocument{Document: &tg.Document{
			ID: 9000 + int64(id), AccessHash: int64(accessHash), DCID: 2,
			FileReference: []byte{byte(accessHash)},
			MimeType:      "video/mp4",
			Size:          int64(size),
			Attributes: []tg.DocumentAttributeClass{
				&tg.DocumentAttributeVideo{W: 640, H: 480, Duration: 30},
				&tg.DocumentAttributeFilename{FileName: "big.mkv"},
			},
		}},
	}
}

// oversizeDocMsg 构造超限的"纯文件"消息（无视频属性 → KindDocument，
// 走字节分段路径，无需 ffmpeg）。
func oversizeDocMsg(id int, accessHash, size int) *tg.Message {
	return &tg.Message{
		ID: id, Message: "大文件",
		Media: &tg.MessageMediaDocument{Document: &tg.Document{
			ID: 9000 + int64(id), AccessHash: int64(accessHash), DCID: 2,
			FileReference: []byte{byte(accessHash)},
			MimeType:      "application/octet-stream",
			Size:          int64(size),
			Attributes: []tg.DocumentAttributeClass{
				&tg.DocumentAttributeFilename{FileName: "data.zip"},
			},
		}},
	}
}

// countedInvoker 统计下载请求数（断言前置校验失败时零下载）。
type countedInvoker struct {
	inner splitChunkInvoker
	mu    sync.Mutex
	calls int
}

func (c *countedInvoker) Invoke(ctx context.Context, in bin.Encoder, out bin.Decoder) error {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return c.inner.Invoke(ctx, in, out)
}

func (c *countedInvoker) snapshot() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func TestSplitSegmentCount(t *testing.T) {
	n, err := splitSegmentCount(95, 40)
	if err != nil || n != 3 {
		t.Fatalf("95B/40B 应拆 3 段: n=%d err=%v", n, err)
	}
	if n, err := splitSegmentCount(51, 40); err != nil || n != 2 {
		t.Fatalf("51B/40B 应拆 2 段: n=%d err=%v", n, err)
	}
	if _, err := splitSegmentCount(1000, 40); apperr.From(err).Code != apperr.CodeFileTooLarge {
		t.Fatalf("超相册上限应报 FILE_TOO_LARGE: %v", err)
	}
	if _, err := splitSegmentCount(95, 0); apperr.From(err).Code != apperr.CodeInternal {
		t.Fatalf("段大小未配置应防御报错: %v", err)
	}
}

func TestSplitPartNameAndNote(t *testing.T) {
	if got := splitPartName("big.mkv", 2, 3); got != "big.mkv.part2of3" {
		t.Fatalf("分段文件名不符: %q", got)
	}
	note := splitNote(3)
	for _, want := range []string{"已分为 3 段", "cat 文件.part1of3", "copy /b 文件.part1of3+文件.part2of3"} {
		if !strings.Contains(note, want) {
			t.Errorf("合并提示应含 %q: %q", want, note)
		}
	}
}

func TestWorkerSplitDelivery(t *testing.T) {
	s := openStore(t)
	job, _ := newJobWithRequest(t, s, 0)
	payload := make([]byte, 95)
	for i := range payload {
		payload[i] = byte(i)
	}
	sender := &fakeSender{consumeAlbumReaders: true, captureAlbumContent: true}
	d := uploadDeps(t, s, fetcherWith(splitChunkInvoker{payload: payload}), sender)
	d.Media = splitMediaOptions(t)

	// 纯文件（无视频属性）走字节分段：非视频媒体的唯一拆分路径
	runOneMedia(t, d, job, oversizeDocMsg(7, 1101, len(payload)))

	calls := sender.albumCallsSnapshot()
	if len(calls) != 1 {
		t.Fatalf("应恰好一次整组发送: %+v", calls)
	}
	call := calls[0]
	if len(call.Kinds) != 3 {
		t.Fatalf("95B/40B 应拆 3 段: %d", len(call.Kinds))
	}
	for i, k := range call.Kinds {
		if k != message.KindDocument {
			t.Errorf("分段应以普通 document 形态发送（不挂 video 属性）: [%d]=%v", i, k)
		}
	}
	wantLens := []int{40, 40, 15}
	for i, want := range wantLens {
		if call.ReadLens[i] != want {
			t.Errorf("分段 %d 字节数不符: want=%d got=%d", i, want, call.ReadLens[i])
		}
		if call.ThumbLens[i] != 0 {
			t.Errorf("分段 %d 未配置 ffmpeg 应无封面: %d", i, call.ThumbLens[i])
		}
	}
	// 分段内容拼接 = 原文件（核心正确性：区间切分无遗漏无重叠）
	content := sender.albumContentSnapshot()
	if len(content) != 3 {
		t.Fatalf("应留存 3 段内容: %d", len(content))
	}
	if got := bytes.Join(content, nil); !bytes.Equal(got, payload) {
		t.Fatalf("分段拼接应等于原文件: got %d 字节", len(got))
	}
	// caption 只挂首段，带来源脚注与合并提示
	if !strings.Contains(call.Captions[0].Text, "已分为 3 段") {
		t.Errorf("首段 caption 应含合并提示: %q", call.Captions[0].Text)
	}
	if !strings.Contains(call.Captions[0].Text, "大文件") {
		t.Errorf("首段 caption 应含原正文: %q", call.Captions[0].Text)
	}
	if call.Captions[1].Text != "" || call.Captions[2].Text != "" {
		t.Errorf("其余段不应带 caption: %q %q", call.Captions[1].Text, call.Captions[2].Text)
	}
	// 观测：delivery_mode 归并为 split
	r, err := s.GetRequest(context.Background(), job.RequestID)
	if err != nil {
		t.Fatalf("读取请求失败: %v", err)
	}
	if r.DeliveryMode != store.DeliveryModeSplit {
		t.Fatalf("delivery_mode 应为 split: %q", r.DeliveryMode)
	}
}

// 拆分发送失败：任务失败、错误码上抛，模式回落 upload（成功才标 split）。
func TestWorkerSplitDeliveryFailure(t *testing.T) {
	s := openStore(t)
	job, _ := newJobWithRequest(t, s, 0)
	payload := make([]byte, 95)
	sender := &fakeSender{consumeAlbumReaders: true, albumErr: func([]delivery.AlbumEntry) error {
		return apperr.New(apperr.CodeSendFailed, "split send boom")
	}}
	d := uploadDeps(t, s, fetcherWith(splitChunkInvoker{payload: payload}, oversizeDocMsg(7, 1101, len(payload))), sender)
	d.Media = splitMediaOptions(t)

	Process(d)(context.Background(), job)

	r, err := s.GetRequest(context.Background(), job.RequestID)
	if err != nil {
		t.Fatalf("读取请求失败: %v", err)
	}
	if r.Status != store.RequestFailed || r.ErrorCode != string(apperr.CodeSendFailed) {
		t.Fatalf("任务应失败且错误码为 BOT_SEND_FAILED: %s/%s", r.Status, r.ErrorCode)
	}
	if r.DeliveryMode != store.DeliveryModeUpload {
		t.Fatalf("未成功的拆分应回落 upload: %q", r.DeliveryMode)
	}
}

// videoSegmentName 固定 .mkv 后缀（muxer 显式 matroska，不依赖输出扩展名
// 猜测——源文件名的大写/空白扩展名会让猜测失败）。
func TestVideoSegmentName(t *testing.T) {
	cases := []struct{ base, want string }{
		{"big.mp4", "big.part1of2.mkv"},
		{"big.MP4", "big.part1of2.mkv"}, // 大写扩展名同样归一
		{"noext", "noext.part1of2.mkv"},
		{".mp4", "video.part1of2.mkv"}, // 防御：裸扩展名视为无名字
	}
	for _, tc := range cases {
		if got := videoSegmentName(tc.base, 1, 2); got != tc.want {
			t.Errorf("videoSegmentName(%q) = %q, want %q", tc.base, got, tc.want)
		}
	}
	if got := videoSegmentName("movie.mkv", 2, 3); got != "movie.part2of3.mkv" {
		t.Errorf("videoSegmentName 序号不符: %q", got)
	}
}

// 混合相册含"超限视频但无法切段"（无 ffmpeg）→ 整组原子失败（零下载、
// 零投递）——不降级字节分段，图片也不会先发出。
func TestWorkerSplitAlbumVideoPrecheckFailsAtomically(t *testing.T) {
	s := openStore(t)
	job, _ := newJobWithRequest(t, s, 0)
	payload := make([]byte, 95)

	inv := &countedInvoker{inner: splitChunkInvoker{payload: payload}}
	sender := &fakeSender{consumeAlbumReaders: true,
		groupable: func(m message.Media) bool { return m.Size <= 50 }}
	d := uploadDeps(t, s, fetcherWith(inv), sender)
	d.Media = splitMediaOptions(t) // 无 FFmpegPath → 视频无法切段

	msgs := []*tg.Message{oversizeMsg(7, 1101, 40), oversizeMsg(8, 1102, 95)}
	msgs[0].SetGroupedID(42)
	msgs[1].SetGroupedID(42)
	d.Fetcher.(*fakeFetcher).msgs = msgs
	Process(d)(context.Background(), job)

	r, err := s.GetRequest(context.Background(), job.RequestID)
	if err != nil {
		t.Fatalf("读取请求失败: %v", err)
	}
	if r.Status != store.RequestFailed || r.ErrorCode != string(apperr.CodeSplitUnavailable) {
		t.Fatalf("任务应失败且错误码为 SPLIT_UNAVAILABLE: %s/%s", r.Status, r.ErrorCode)
	}
	if inv.snapshot() != 0 {
		t.Fatalf("前置校验失败应零下载: %d 次下载请求", inv.snapshot())
	}
	if calls := sender.mediaCallsSnapshot(); len(calls) != 0 {
		t.Fatalf("图片不应先发出: %+v", calls)
	}
	if calls := sender.albumCallsSnapshot(); len(calls) != 0 {
		t.Fatalf("不应有任何整组投递: %+v", calls)
	}
}

// 单个超限视频无法切段 → 下载开始前直接报错（零下载、零投递、不降级）。
func TestWorkerSplitVideoPrecheckFailsBeforeDownload(t *testing.T) {
	s := openStore(t)
	job, _ := newJobWithRequest(t, s, 0)
	payload := make([]byte, 95)

	inv := &countedInvoker{inner: splitChunkInvoker{payload: payload}}
	sender := &fakeSender{consumeAlbumReaders: true}
	d := uploadDeps(t, s, fetcherWith(inv), sender)
	d.Media = splitMediaOptions(t)

	runOneMedia(t, d, job, oversizeMsg(7, 1101, 95))

	r, err := s.GetRequest(context.Background(), job.RequestID)
	if err != nil {
		t.Fatalf("读取请求失败: %v", err)
	}
	if r.Status != store.RequestFailed || r.ErrorCode != string(apperr.CodeSplitUnavailable) {
		t.Fatalf("任务应失败且错误码为 SPLIT_UNAVAILABLE: %s/%s", r.Status, r.ErrorCode)
	}
	if inv.snapshot() != 0 {
		t.Fatalf("前置校验失败应零下载: %d 次下载请求", inv.snapshot())
	}
	if calls := sender.mediaCallsSnapshot(); len(calls) != 0 {
		t.Fatalf("不应有任何投递: %+v", calls)
	}
}
