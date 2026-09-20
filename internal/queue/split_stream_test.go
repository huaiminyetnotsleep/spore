package queue

// 超大视频单遍流式切段测试（需要 ffmpeg，缺失时跳过）：
//   - faststart（头 moov）视频走流式路径：一次下载零重复（下载字节恰好
//     1×，无回退重下）、段以 video 形态整组送达、段文件发送后清理；
//   - 合法头 moov 盒头 + 垃圾 mdat → ffmpeg 流式切段失败 → SPLIT_UNAVAILABLE、
//     段文件清理、零投递（整组原子）；
//   - 尾 moov 回退 + 2× 预算不足 → TEMP_DIR_FULL 且不重新下载（判定后
//     立即失败，盘上无任何落盘）。
//
// 尾 moov 的回退成功路径由 split_playable_test.go 的默认 genTestVideo
//（ffmpeg 默认 moov 在尾）覆盖。

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/media"
	"github.com/huaiminyetnotsleep/spore/internal/message"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// servedBytesInvoker 统计假下载通道已服务的字节总量（断言流式路径恰好
// 1×、回退路径未重新下载）。
type servedBytesInvoker struct {
	inner  splitChunkInvoker
	mu     sync.Mutex
	served int64
}

func (s *servedBytesInvoker) Invoke(ctx context.Context, in bin.Encoder, out bin.Decoder) error {
	if req, ok := in.(*tg.UploadGetFileRequest); ok {
		end := min(req.Offset+int64(req.Limit), int64(len(s.inner.payload)))
		if n := end - req.Offset; n > 0 {
			s.mu.Lock()
			s.served += n
			s.mu.Unlock()
		}
	}
	return s.inner.Invoke(ctx, in, out)
}

func (s *servedBytesInvoker) snapshot() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.served
}

// genStreamTestVideo 生成头 moov（faststart）且每秒强制关键帧的测试视频：
// segment muxer 按 segment_time 在关键帧处切段，2 秒边界可真实切出 2 段。
func genStreamTestVideo(t *testing.T, ffmpeg, dir string, seconds int) []byte {
	t.Helper()
	out := filepath.Join(dir, "src.mp4")
	cmd := exec.Command(ffmpeg,
		"-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=duration="+strconv.Itoa(seconds)+":size=128x72:rate=8",
		"-pix_fmt", "yuv420p",
		"-force_key_frames", "expr:gte(t,n_forced*1)",
		"-movflags", "+faststart",
		"-y", out)
	if outBytes, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("生成 faststart 测试视频失败: %v（%.200s）", err, outBytes)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("读取测试视频失败: %v", err)
	}
	if media.SniffMoovPosition(data) != media.MoovHead {
		t.Fatal("生成的 faststart 视频应为头 moov（测试前置失败）")
	}
	return data
}

// streamVideoMsg 构造超限视频消息（时长参数化：流式切段按元数据时长换算
// segment_time，须与真实视频时长一致才能切出预期段数）。
func streamVideoMsg(id int, accessHash, size, duration int) *tg.Message {
	msg := oversizeMsg(id, accessHash, size)
	doc := msg.Media.(*tg.MessageMediaDocument).Document.(*tg.Document)
	for i, attr := range doc.Attributes {
		if v, ok := attr.(*tg.DocumentAttributeVideo); ok {
			v.Duration = float64(duration)
			doc.Attributes[i] = v
			break
		}
	}
	return msg
}

// faststart 视频端到端：流式切段（源不落盘、恰好一次下载）→ 2 段 video
// 形态整组送达。下载字节 == 原文件大小是"无回退重下"的硬断言。
func TestWorkerSplitStreamFaststartVideo(t *testing.T) {
	requireFFmpeg(t)

	s := openStore(t)
	job, _ := newJobWithRequest(t, s, 0)
	payload := genStreamTestVideo(t, "ffmpeg", t.TempDir(), 4) // 4 秒 → 2 段（每段 2s）

	inv := &servedBytesInvoker{inner: splitChunkInvoker{payload: payload}}
	sender := &fakeSender{consumeAlbumReaders: true, captureAlbumContent: true}
	d := uploadDeps(t, s, fetcherWith(inv), sender)
	d.Media = media.Options{
		TmpDir:            t.TempDir(),
		MaxFileSize:       int64(len(payload)) / 2,
		MaxSplitTotalSize: int64(len(payload)) * 10,
		SplitSegmentSize:  int64(len(payload))/2 + 1, // ceil(size/seg)=2 段
		FFmpegPath:        "ffmpeg",
	}
	runOneMedia(t, d, job, streamVideoMsg(7, 1101, len(payload), 4))

	calls := sender.albumCallsSnapshot()
	if len(calls) != 1 {
		t.Fatalf("应一次整组发送: %+v", calls)
	}
	call := calls[0]
	if len(call.Kinds) != 2 {
		t.Fatalf("流式切段应产出 2 段: %d", len(call.Kinds))
	}
	for i, k := range call.Kinds {
		if k != message.KindVideo {
			t.Errorf("分段应以 video 形态发送: [%d]=%v", i, k)
		}
		if call.ReadLens[i] <= 0 {
			t.Errorf("分段 %d 内容为空: %d", i, call.ReadLens[i])
		}
	}
	content := sender.albumContentSnapshot()
	if len(content) != 2 {
		t.Fatalf("应留存 2 段内容: %d", len(content))
	}
	var total int
	for _, c := range content {
		total += len(c)
	}
	if total < len(payload) {
		t.Errorf("分段总字节应不少于原文件（流复制 + 容器开销）: %d < %d", total, len(payload))
	}
	// 逐段应为可解码视频
	for i, c := range sender.albumContentSnapshot() {
		pf := filepath.Join(t.TempDir(), fmt.Sprintf("sseg%d.mkv", i))
		if err := os.WriteFile(pf, c, 0o644); err != nil {
			t.Fatalf("写分段文件失败: %v", err)
		}
		probe := exec.Command("ffmpeg", "-hide_banner", "-i", pf)
		out, _ := probe.CombinedOutput()
		if !strings.Contains(string(out), "Duration: 00:00:0") {
			t.Errorf("分段 %d 应为可解码视频: %.300s", i, out)
		}
	}
	// 恰好一次下载：流式路径无回退重下
	if got := inv.snapshot(); got != int64(len(payload)) {
		t.Fatalf("流式路径下载字节应恰为 1×（%d），得到 %d（发生回退重下？）", len(payload), got)
	}
	// 段文件随任务清理（源从未落盘，目录应为空）
	if entries, _ := os.ReadDir(d.Media.TmpDir); len(entries) != 0 {
		t.Fatalf("临时目录应清空，残留 %d 个", len(entries))
	}
	r, err := s.GetRequest(context.Background(), job.RequestID)
	if err != nil {
		t.Fatalf("读取请求失败: %v", err)
	}
	if r.DeliveryMode != store.DeliveryModeSplit {
		t.Fatalf("delivery_mode 应为 split: %q", r.DeliveryMode)
	}
}

// 合法头 moov 盒头 + 垃圾 mdat：ffmpeg 解析失败 → SPLIT_UNAVAILABLE，
// 段文件清理、零投递、下载完整（失败发生在切段环节而非下载环节）。
func TestWorkerSplitStreamGarbageFailsAtomically(t *testing.T) {
	requireFFmpeg(t)

	// ftyp + moov 盒头（声明超大 moov，内容截断）+ 垃圾填充
	var buf []byte
	buf = appendBox4(buf, "ftyp", []byte("isom\x00\x00\x02\x00isomiso2"))
	var moovSize [4]byte
	binary.BigEndian.PutUint32(moovSize[:], 1<<20)
	buf = append(buf, moovSize[:]...)
	buf = append(buf, "moov"...)
	garbage := make([]byte, 256)
	for i := range garbage {
		garbage[i] = byte(i * 13)
	}
	payload := append(buf, garbage...)
	if media.SniffMoovPosition(payload) != media.MoovHead {
		t.Fatal("构造的垃圾负载应判定为头 moov（测试前置失败）")
	}

	s := openStore(t)
	job, _ := newJobWithRequest(t, s, 0)
	inv := &servedBytesInvoker{inner: splitChunkInvoker{payload: payload}}
	sender := &fakeSender{consumeAlbumReaders: true}
	d := uploadDeps(t, s, fetcherWith(inv), sender)
	d.Media = media.Options{
		TmpDir:            t.TempDir(),
		MaxFileSize:       int64(len(payload)) / 2,
		MaxSplitTotalSize: int64(len(payload)) * 10,
		SplitSegmentSize:  int64(len(payload))/2 + 1,
		FFmpegPath:        "ffmpeg",
	}
	runOneMedia(t, d, job, streamVideoMsg(7, 1101, len(payload), 4))

	r, err := s.GetRequest(context.Background(), job.RequestID)
	if err != nil {
		t.Fatalf("读取请求失败: %v", err)
	}
	if r.Status != store.RequestFailed || r.ErrorCode != string(apperr.CodeSplitUnavailable) {
		t.Fatalf("任务应失败且错误码为 SPLIT_UNAVAILABLE: %s/%s", r.Status, r.ErrorCode)
	}
	if calls := sender.albumCallsSnapshot(); len(calls) != 0 {
		t.Fatalf("不应有任何投递: %+v", calls)
	}
	if entries, _ := os.ReadDir(d.Media.TmpDir); len(entries) != 0 {
		t.Fatalf("失败分段应清理，残留 %d 个", len(entries))
	}
	if got := inv.snapshot(); got != int64(len(payload)) {
		t.Fatalf("下载应完整（失败在切段环节）: 期望 %d 得到 %d", len(payload), got)
	}
}

// appendBox4 向 buf 追加 32 位 size 的顶层 box（queue 侧测试自用，与
// media 包的解析构成两份独立实现——解析正确性由 media/box_test.go 覆盖）。
func appendBox4(buf []byte, typ string, payload []byte) []byte {
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(8+len(payload)))
	buf = append(buf, size[:]...)
	buf = append(buf, typ...)
	return append(buf, payload...)
}

// 尾 moov 回退 + 2× 预算不足：判定后立即 TEMP_DIR_FULL，不重新下载
// （下载字节恰为 1×），盘上零落盘、零投递。
func TestWorkerSplitFallbackBudgetFullBeforeRedownload(t *testing.T) {
	requireFFmpeg(t)

	s := openStore(t)
	job, _ := newJobWithRequest(t, s, 0)
	payload := genTestVideo(t, "ffmpeg", t.TempDir(), 4) // 默认尾 moov

	inv := &servedBytesInvoker{inner: splitChunkInvoker{payload: payload}}
	sender := &fakeSender{consumeAlbumReaders: true}
	d := uploadDeps(t, s, fetcherWith(inv), sender)
	d.Media = media.Options{
		TmpDir:            t.TempDir(),
		MaxFileSize:       int64(len(payload)) / 2,
		MaxSplitTotalSize: int64(len(payload)) * 10,
		SplitSegmentSize:  int64(len(payload))/2 + 1,
		FFmpegPath:        "ffmpeg",
		// 1×（流式预检）放行、2×（回退源 + 分段）拒绝：判定后立即失败
		MaxDirSize: int64(len(payload)),
		DirUsage:   func(string) (int64, error) { return 0, nil },
	}
	runOneMedia(t, d, job, streamVideoMsg(7, 1101, len(payload), 4))

	r, err := s.GetRequest(context.Background(), job.RequestID)
	if err != nil {
		t.Fatalf("读取请求失败: %v", err)
	}
	if r.Status != store.RequestFailed || r.ErrorCode != string(apperr.CodeTempDirFull) {
		t.Fatalf("任务应失败且错误码为 TEMP_DIR_FULL: %s/%s", r.Status, r.ErrorCode)
	}
	if got := inv.snapshot(); got != int64(len(payload)) {
		t.Fatalf("预算不足应在重新下载前失败（下载恰 1×），得到 %d（期望 %d）", got, len(payload))
	}
	if entries, _ := os.ReadDir(d.Media.TmpDir); len(entries) != 0 {
		t.Fatalf("回退被预算拦截，盘上应零落盘，残留 %d 个", len(entries))
	}
	if calls := sender.albumCallsSnapshot(); len(calls) != 0 {
		t.Fatalf("不应有任何投递: %+v", calls)
	}
}
