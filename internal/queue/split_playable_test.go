package queue

// 可播放视频分段测试（需要 ffmpeg，缺失时跳过）：
//   - 端到端：真实 mp4 经假下载通道完整落盘 → ffmpeg 流复制切为 2 段 →
//     以 video 形态整组送达（可直接播放），段文件在发送后清理；
//   - 规划：planAlbumSend 三态（全员整组 / 含可拆视频的拆分整组 / 回退逐条）。
//
// 无 ffmpeg 环境（含 CI 极简镜像）自动回退字节分段，由 split_test.go 的
// 字节分段用例覆盖。

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/huaiminyetnotsleep/spore/internal/media"
	"github.com/huaiminyetnotsleep/spore/internal/message"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// requireFFmpeg 返回 ffmpeg 路径；缺失时跳过当前测试。
func requireFFmpeg(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg 不可用，跳过可播放分段测试")
	}
	return path
}

// hasFFmpeg 报告 ffmpeg 是否可用（规划测试的可拆分支前置）。
func hasFFmpeg() bool {
	_, err := exec.LookPath("ffmpeg")
	return err == nil
}

// genTestVideo 用 ffmpeg 现场生成测试视频（testsrc，可解码），返回字节。
func genTestVideo(t *testing.T, ffmpeg, dir string, seconds int) []byte {
	t.Helper()
	out := filepath.Join(dir, "src.mp4")
	cmd := exec.Command(ffmpeg,
		"-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=duration="+strconv.Itoa(seconds)+":size=128x72:rate=8",
		"-pix_fmt", "yuv420p", "-y", out)
	if outBytes, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("生成测试视频失败: %v（%.200s）", err, outBytes)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("读取测试视频失败: %v", err)
	}
	return data
}

// 可播放分段端到端：真实视频 → 2 段 → video 形态整组送达，段文件清理干净。
func TestWorkerSplitPlayableVideoSegments(t *testing.T) {
	requireFFmpeg(t)

	s := openStore(t)
	job, _ := newJobWithRequest(t, s, 0)
	payload := genTestVideo(t, "ffmpeg", t.TempDir(), 4) // 4 秒测试视频

	sender := &fakeSender{consumeAlbumReaders: true, captureAlbumContent: true}
	d := uploadDeps(t, s, fetcherWith(splitChunkInvoker{payload: payload}), sender)
	d.Media = media.Options{
		TmpDir:            t.TempDir(),
		MaxFileSize:       int64(len(payload)) / 2,
		MaxSplitTotalSize: int64(len(payload)) * 10,
		SplitSegmentSize:  int64(len(payload))/2 + 1, // ceil(size/seg)=2 段
		FFmpegPath:        "ffmpeg",
	}
	runOneMedia(t, d, job, oversizeMsg(7, 1101, len(payload)))

	calls := sender.albumCallsSnapshot()
	if len(calls) != 1 {
		t.Fatalf("应一次整组发送: %+v", calls)
	}
	call := calls[0]
	if len(call.Kinds) != 2 {
		t.Fatalf("应切 2 段: %d", len(call.Kinds))
	}
	for i, k := range call.Kinds {
		if k != message.KindVideo {
			t.Errorf("分段应以 video 形态发送（可内联播放）: [%d]=%v", i, k)
		}
		if call.ReadLens[i] <= 0 {
			t.Errorf("分段 %d 内容为空: %d", i, call.ReadLens[i])
		}
		if call.ThumbLens[i] <= 0 {
			t.Errorf("分段 %d 应有 ffmpeg 抽帧封面: %d", i, call.ThumbLens[i])
		}
	}
	if !bytes.Contains([]byte(call.Captions[0].Text), []byte("已切分为 2 段视频")) {
		t.Errorf("首段 caption 应含可播放说明: %q", call.Captions[0].Text)
	}
	if call.Captions[1].Text != "" {
		t.Errorf("次段不应带 caption: %q", call.Captions[1].Text)
	}
	// 分段内容拼接应不少于原视频字节（流复制无损 + 容器封装开销）
	content := sender.albumContentSnapshot()
	if len(content) != 2 {
		t.Fatalf("应留存 2 段内容: %d", len(content))
	}
	var total int
	for _, c := range content {
		total += len(c)
	}
	if total < len(payload) {
		t.Errorf("分段总字节应不少于原文件: %d < %d", total, len(payload))
	}
	// 段文件随任务清理
	entries, err := os.ReadDir(d.Media.TmpDir)
	if err != nil {
		t.Fatalf("读取临时目录失败: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("分段文件应随发送完成清理，残留 %d 个", len(entries))
	}
	// 观测：delivery_mode = split
	r, err := s.GetRequest(context.Background(), job.RequestID)
	if err != nil {
		t.Fatalf("读取请求失败: %v", err)
	}
	if r.DeliveryMode != store.DeliveryModeSplit {
		t.Fatalf("delivery_mode 应为 split: %q", r.DeliveryMode)
	}
}

// 规划三态：全员可整组 → 常规；含可拆视频 → 拆分整组（展开计数）；
// 既不可整组也不可拆 → 回退逐条；展开超相册上限 → 回退逐条。
func TestPlanAlbumSend(t *testing.T) {
	d := Deps{
		Log: testLog(),
		Sender: &fakeSender{groupable: func(m message.Media) bool {
			return m.Size <= 100 && m.Kind == message.KindPhoto
		}},
	}
	job := Job{}
	photo := func() message.Item {
		m := message.Media{Kind: message.KindPhoto, Size: 10}
		return message.Item{ID: 1, Media: &m}
	}
	bigVideo := func(size int64) message.Item {
		m := message.Media{Kind: message.KindVideo, Size: size,
			Video: &message.VideoMeta{Width: 640, Height: 480, Duration: 100}}
		return message.Item{ID: 2, Media: &m}
	}

	t.Run("全员可整组：常规规划", func(t *testing.T) {
		items := []message.Item{photo(), photo()}
		plans := planAlbumSend(d, job, items)
		if plans == nil || len(plans) != 2 || plans[0].split || plans[1].split {
			t.Fatalf("常规相册应全员占 1 槽: %+v", plans)
		}
	})

	t.Run("含可拆视频：拆分整组展开计数", func(t *testing.T) {
		if !hasFFmpeg() {
			t.Skip("ffmpeg 不可用")
		}
		items := []message.Item{photo(), bigVideo(900)} // 900/400 → 3 段
		plans := planAlbumSend(d, job, items)
		if plans == nil {
			t.Fatal("可拆混合相册应可整组")
		}
		if plans[0].split || plans[0].count != 1 || !plans[1].split || plans[1].count != 3 {
			t.Fatalf("规划不符: %+v", plans)
		}
	})

	t.Run("不可整组也不可拆：回退逐条", func(t *testing.T) {
		audio := message.Media{Kind: message.KindAudio, Size: 500}
		items := []message.Item{photo(), {ID: 2, Media: &audio}}
		if plans := planAlbumSend(d, job, items); plans != nil {
			t.Fatalf("含 audio 成员应回退逐条: %+v", plans)
		}
	})

	t.Run("展开超相册上限：回退逐条", func(t *testing.T) {
		if !hasFFmpeg() {
			t.Skip("ffmpeg 不可用")
		}
		items := make([]message.Item, 0, 11)
		for i := 0; i < 10; i++ {
			items = append(items, photo())
		}
		items = append(items, bigVideo(900))
		if plans := planAlbumSend(d, job, items); plans != nil {
			t.Fatalf("展开 13 槽超上限应回退逐条: %+v", plans)
		}
	})
}

// sentIDs 的 spans 语义：普通成员各占一组；整组发送按成员分组成 span
// （拆分段跟其源成员连续成组）。
func TestSentIDSpans(t *testing.T) {
	var s sentIDs
	s.add(1)               // 单条：一组
	s.addSpan([]int{2, 3}) // 拆分段：一组两条
	s.addSpan([]int{4})    // 单条整组

	if got := s.items; len(got) != 4 || got[0] != 1 || got[1] != 2 || got[2] != 3 || got[3] != 4 {
		t.Fatalf("摊平列表不符: %v", got)
	}
	want := [][]int{{1}, {2, 3}, {4}}
	if len(s.spans) != len(want) {
		t.Fatalf("分组数不符: %v", s.spans)
	}
	for i, span := range want {
		if len(s.spans[i]) != len(span) {
			t.Fatalf("分组 %d 不符: %v", i, s.spans[i])
		}
		for j := range span {
			if s.spans[i][j] != span[j] {
				t.Fatalf("分组 %d 内容不符: %v", i, s.spans[i])
			}
		}
	}
	// 全零 ID（防御）不产生组
	var z sentIDs
	z.add(0)
	z.addSpan(nil)
	if len(z.items) != 0 || len(z.spans) != 0 {
		t.Fatalf("零 ID 不应入列: %v %v", z.items, z.spans)
	}
}
