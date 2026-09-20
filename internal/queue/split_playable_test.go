package queue

// 可播放视频分段测试（需要 ffmpeg，缺失时跳过）：
//   - 端到端：真实 mp4 经假下载通道完整落盘 → ffmpeg 流复制切为 2 段 →
//     以 video 形态整组送达（可直接播放），段文件在发送后清理；
//   - 规划：planAlbumSend 三态（全员整组 / 含可拆视频的拆分整组 / 回退逐条）。
//
// 无 ffmpeg 环境（含 CI 极简镜像）自动回退字节分段，由 split_test.go 的
// 字节分段用例覆盖。

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gotd/td/tg"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"

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
	// 绑定频道脚注提供者：验证分段首段携带完整署名（正文/来源链接/脚注/说明）
	d.Channels = &fakeChannelLinks{links: []message.ChannelLink{
		{Label: "我的频道", URL: "https://t.me/mychannel"},
	}}
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
	// 首段 caption 必须完整：正文 + 来源链接 + 切段说明；频道脚注为延迟
	// 拼装（Channels 字段携带，MTProto 发送侧 Limited 才拼接文本）
	for _, want := range []string{"大文件", "https://t.me/example/7", "已切分为 2 段视频"} {
		if !strings.Contains(call.Captions[0].Text, want) {
			t.Errorf("首段 caption 缺少 %q: %q", want, call.Captions[0].Text)
		}
	}
	if len(call.Captions[0].Channels) != 1 || call.Captions[0].Channels[0].Label != "我的频道" {
		t.Errorf("首段应携带频道脚注: %+v", call.Captions[0].Channels)
	}
	if call.Captions[1].Text != "" || len(call.Captions[1].Channels) != 0 {
		t.Errorf("次段不应带 caption/脚注: %+v", call.Captions[1])
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
	// 分段必须是合法可解码的视频（流复制产物）：逐段落盘后用 ffmpeg 解析
	for i, c := range sender.albumContentSnapshot() {
		pf := filepath.Join(t.TempDir(), fmt.Sprintf("seg%d.mkv", i))
		if err := os.WriteFile(pf, c, 0o644); err != nil {
			t.Fatalf("写分段文件失败: %v", err)
		}
		probe := exec.Command("ffmpeg", "-hide_banner", "-i", pf)
		out, _ := probe.CombinedOutput() // 无输出映射必非零退出，看 stderr 是否解析出时长
		if !strings.Contains(string(out), "Duration: 00:00:0") {
			t.Errorf("分段 %d 应为可解码视频（stderr 含 Duration）: %.300s", i, out)
		}
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

// photoMsg 构造带 Progressive 尺寸的图片消息（Convert 提取最大尺寸为下载坐标）。
func photoMsg(id int, accessHash int64, size int) *tg.Message {
	return &tg.Message{
		ID: id, Message: "图注",
		Media: &tg.MessageMediaPhoto{Photo: &tg.Photo{
			ID: 7000 + int64(id), AccessHash: accessHash, DCID: 2,
			FileReference: []byte{byte(accessHash)},
			Sizes:         []tg.PhotoSizeClass{&tg.PhotoSizeProgressive{Type: "x", Sizes: []int{size}}},
		}},
	}
}

// 混合相册 [图片, 超限视频] 拆分整组：图片 + 2 个可播放分段合成同一条相册
// 原子投递。worker 逐成员构造语义 caption（正文/来源/脚注在各自归属成员上，
// 切段说明跟其视频成员）——整组 caption 由路由层发送前归一化合并到组首
// （见 delivery/router_test.go 的归一化测试）。断言四件事（真机 2026-09-20
// 回归）：
//   - 成员形态 [photo, video, video]，图片为组首且携带正文 + 来源链接 +
//     频道脚注；
//   - 后置视频成员自己的正文与切段说明保留在其首段（不静默丢弃，路由层
//     归一化时并入组首）；
//   - delivery_mode 归并为 split（此前混合拆分整组路径漏标 split，被归并
//     为 upload，管理端"分段投递"口径失真）。
func TestWorkerSplitMixedAlbumMarksSplitDelivery(t *testing.T) {
	requireFFmpeg(t)

	s := openStore(t)
	job, _ := newJobWithRequest(t, s, 0)
	payload := genTestVideo(t, "ffmpeg", t.TempDir(), 4)

	sender := &fakeSender{consumeAlbumReaders: true, captureAlbumContent: true,
		groupable: func(m message.Media) bool { return m.Kind == message.KindPhoto }}
	d := uploadDeps(t, s, fetcherWith(splitChunkInvoker{payload: payload}), sender)
	d.Channels = &fakeChannelLinks{links: []message.ChannelLink{
		{Label: "我的频道", URL: "https://t.me/mychannel"},
	}}
	d.Media = media.Options{
		TmpDir:            t.TempDir(),
		MaxFileSize:       int64(len(payload)) / 2,
		MaxSplitTotalSize: int64(len(payload)) * 10,
		SplitSegmentSize:  int64(len(payload))/2 + 1, // ceil(size/seg)=2 段
		StreamLimit:       int64(len(payload)),       // 小图片走流式路径（超限视频强制落盘不受影响）
		FFmpegPath:        "ffmpeg",
	}
	msgs := []*tg.Message{photoMsg(7, 1101, 10), oversizeMsg(8, 1102, len(payload))}
	msgs[0].SetGroupedID(42)
	msgs[1].SetGroupedID(42)
	d.Fetcher.(*fakeFetcher).msgs = msgs
	Process(d)(context.Background(), job)

	r, err := s.GetRequest(context.Background(), job.RequestID)
	if err != nil {
		t.Fatalf("读取请求失败: %v", err)
	}
	if r.Status != store.RequestSucceeded {
		t.Fatalf("任务应成功: %s/%s", r.Status, r.ErrorCode)
	}
	if r.DeliveryMode != store.DeliveryModeSplit {
		t.Fatalf("混合拆分整组的 delivery_mode 应为 split: %q", r.DeliveryMode)
	}

	calls := sender.albumCallsSnapshot()
	if len(calls) != 1 {
		t.Fatalf("应一次整组发送: %+v", calls)
	}
	call := calls[0]
	if len(call.Kinds) != 3 || call.Kinds[0] != message.KindPhoto ||
		call.Kinds[1] != message.KindVideo || call.Kinds[2] != message.KindVideo {
		t.Fatalf("成员形态应为 [图片, 段1, 段2]: %+v", call.Kinds)
	}
	// 组首图片携带正文 + 来源链接 + 频道脚注（无切段说明——说明归属视频成员）
	for _, want := range []string{"图注", "https://t.me/example/7"} {
		if !strings.Contains(call.Captions[0].Text, want) {
			t.Errorf("组首图片 caption 缺少 %q: %q", want, call.Captions[0].Text)
		}
	}
	if strings.Contains(call.Captions[0].Text, "已切分为") {
		t.Errorf("组首不应折叠切段说明（说明归属视频成员首段）: %q", call.Captions[0].Text)
	}
	if len(call.Captions[0].Channels) != 1 || call.Captions[0].Channels[0].Label != "我的频道" {
		t.Errorf("组首图片应携带频道脚注: %+v", call.Captions[0].Channels)
	}
	// 后置视频成员自己的正文与切段说明保留在其首段（路由归一化时并入组首）
	for _, want := range []string{"大文件", "已切分为 2 段"} {
		if !strings.Contains(call.Captions[1].Text, want) {
			t.Errorf("分段首段 caption 缺少 %q: %q", want, call.Captions[1].Text)
		}
	}
	if len(call.Captions[1].Channels) != 0 {
		t.Errorf("非组首分段不应携带频道脚注: %+v", call.Captions[1].Channels)
	}
	if call.Captions[2].Text != "" || len(call.Captions[2].Channels) != 0 {
		t.Errorf("次段不应带 caption/脚注: %+v", call.Captions[2])
	}
}

// 规划三态：全员可整组 → 常规；含可拆视频 → 拆分整组（展开计数）；
// 既不可整组也不可拆 → 回退逐条；展开超相册上限 → 回退逐条。
func TestPlanAlbumSend(t *testing.T) {
	d := Deps{
		Log: testLog(),
		Media: media.Options{
			MaxFileSize:       100,
			MaxSplitTotalSize: 1000,
			SplitSegmentSize:  400,
			// 裸名 false 经 PATH 解析（macOS/Linux 均存在），LookPath
			// 可执行即可过 splittableVideo（规划层不真正运行 ffmpeg）
			FFmpegPath: "false",
		},
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
		plans, planErr := planAlbumSend(d, job, items)
		if planErr != nil || plans == nil || len(plans) != 2 || plans[0].split || plans[1].split {
			t.Fatalf("常规相册应全员占 1 槽: %+v err=%v", plans, planErr)
		}
	})

	t.Run("含可拆视频：拆分整组展开计数", func(t *testing.T) {
		if !hasFFmpeg() {
			t.Skip("ffmpeg 不可用")
		}
		items := []message.Item{photo(), bigVideo(900)} // 900/400 → 3 段
		plans, planErr := planAlbumSend(d, job, items)
		if planErr != nil || plans == nil {
			t.Fatalf("可拆混合相册应可整组: %v", planErr)
		}
		if plans[0].split || plans[0].count != 1 || !plans[1].split || plans[1].count != 3 {
			t.Fatalf("规划不符: %+v", plans)
		}
	})

	t.Run("不可整组也不可拆：回退逐条", func(t *testing.T) {
		audio := message.Media{Kind: message.KindAudio, Size: 500}
		items := []message.Item{photo(), {ID: 2, Media: &audio}}
		plans, planErr := planAlbumSend(d, job, items)
		if planErr != nil || plans != nil {
			t.Fatalf("含 audio 成员应回退逐条: %+v err=%v", plans, planErr)
		}
	})

	t.Run("超限视频无法切段：整组原子报错", func(t *testing.T) {
		badVideo := message.Media{Kind: message.KindVideo, Size: 900} // 无 FFmpegPath
		items := []message.Item{photo(), {ID: 2, Media: &badVideo}}
		plans, planErr := planAlbumSend(d, job, items)
		if plans != nil || planErr == nil {
			t.Fatalf("无法切段的超限视频应整组报错: %+v err=%v", plans, planErr)
		}
		var ae *apperr.AppError
		if !errors.As(planErr, &ae) || ae.Code != apperr.CodeSplitUnavailable {
			t.Fatalf("应报 SPLIT_UNAVAILABLE: %v", planErr)
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
		if plans, planErr := planAlbumSend(d, job, items); planErr != nil || plans != nil {
			t.Fatalf("展开 13 槽超上限应回退逐条: %+v err=%v", plans, planErr)
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
