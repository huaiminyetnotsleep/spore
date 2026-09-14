package queue

// 占位消息实时进度编辑测试：进度变化触发编辑、内容不变跳过、编辑失败
// 熔断、无占位/无记录时不启动。间隔注入为短值保证测试确定性。

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/progress"
)

func statusTestDeps(reg *progress.Registry, snd *fakeSender) Deps {
	return Deps{Sender: snd, Progress: reg, Log: testLog()}
}

func statusTestJob() Job {
	return Job{ID: "job-1", ChatID: 7, StatusMsgID: 42, RequestID: 9}
}

func TestRenderProgressStatus(t *testing.T) {
	got := renderProgressStatus(StatusPromptHTML, 1000, 500, 250)
	for _, want := range []string{
		StatusPromptHTML,
		"⬇️ 下载 50%（500 B / 1000 B）",
		"⬆️ 上传 25%（250 B / 1000 B）",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("进度文案应包含 %q，得到 %q", want, got)
		}
	}
	// 云盘任务前缀：进度编辑保持云盘占位文案
	if got := renderProgressStatus(StatusCloudPromptHTML, 100, 100, 0); !strings.Contains(got, StatusCloudPromptHTML) {
		t.Errorf("云盘进度文案应保留云盘前缀: %q", got)
	}
	// 边界：总量未知按 0%；溢出封顶 100%
	if p := percentText(0, 0); p != "0%" {
		t.Errorf("total=0 应为 0%%，得到 %q", p)
	}
	if p := percentText(2000, 1000); p != "100%" {
		t.Errorf("超量应封顶 100%%，得到 %q", p)
	}
	// 字节数格式与前端 fmtBytes 同源
	if b := bytesText(500); b != "500 B" {
		t.Errorf("500 B 格式不符: %q", b)
	}
	if b := bytesText(2 << 20); b != "2 MiB" {
		t.Errorf("2 MiB 格式（去尾零）不符: %q", b)
	}
	if b := bytesText(3 << 20); b != "3 MiB" {
		t.Errorf("3 MiB 格式不符: %q", b)
	}
}

func TestProgressEditorEditsOnChange(t *testing.T) {
	reg := progress.NewRegistry()
	reg.Register(9)
	reg.AddTotal(9, 1000)
	snd := &fakeSender{}
	stop := startProgressEditorWithInterval(context.Background(), statusTestDeps(reg, snd), statusTestJob(), 10*time.Millisecond)
	defer stop()

	waitEdits := func(n int) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			snd.mu.Lock()
			got := len(snd.edits)
			snd.mu.Unlock()
			if got >= n {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		snd.mu.Lock()
		defer snd.mu.Unlock()
		t.Fatalf("编辑次数未达 %d，得到 %d: %v", n, len(snd.edits), snd.edits)
	}

	// 下载推进 → 第一次编辑只含下载进度
	reg.AddDownloaded(9, 500)
	waitEdits(1)
	snd.mu.Lock()
	first := snd.edits[0]
	snd.mu.Unlock()
	if !strings.Contains(first, "⬇️ 下载 50%") || strings.Contains(first, "⬆️ 上传 50%") {
		t.Errorf("首次编辑文案不符: %q", first)
	}

	// 上传推进 → 内容变化触发第二次编辑
	reg.AddUploaded(9, 500)
	waitEdits(2)
	snd.mu.Lock()
	second := snd.edits[1]
	snd.mu.Unlock()
	if !strings.Contains(second, "⬆️ 上传 50%") {
		t.Errorf("第二次编辑应包含上传进度: %q", second)
	}
}

func TestProgressEditorSkipsUnchangedContent(t *testing.T) {
	reg := progress.NewRegistry()
	reg.Register(9)
	reg.AddTotal(9, 1000)
	reg.AddDownloaded(9, 500) // 进度在编辑前已就位，tick 期间不再变化
	snd := &fakeSender{}
	stop := startProgressEditorWithInterval(context.Background(), statusTestDeps(reg, snd), statusTestJob(), 10*time.Millisecond)
	time.Sleep(150 * time.Millisecond) // 约 15 个 tick
	stop()

	snd.mu.Lock()
	defer snd.mu.Unlock()
	if len(snd.edits) != 1 {
		t.Fatalf("内容不变应只编辑一次，得到 %d 次: %v", len(snd.edits), snd.edits)
	}
}

func TestProgressEditorCircuitBreaksOnFailure(t *testing.T) {
	reg := progress.NewRegistry()
	reg.Register(9)
	reg.AddTotal(9, 1000)
	reg.AddDownloaded(9, 500)
	snd := &fakeSender{editErr: errors.New("message to edit not found")}
	stop := startProgressEditorWithInterval(context.Background(), statusTestDeps(reg, snd), statusTestJob(), 10*time.Millisecond)
	time.Sleep(150 * time.Millisecond)
	stop()

	snd.mu.Lock()
	defer snd.mu.Unlock()
	if len(snd.edits) != 0 {
		t.Fatalf("编辑恒败不应记录成功编辑: %v", snd.edits)
	}
	// 熔断后不再尝试：无法直接观测调用次数（失败不记录），以不 panic、
	// 不阻塞 stop 为准；熔断逻辑由 last="\x00" 分支覆盖
}

func TestProgressEditorNoopWithoutStatusMessage(t *testing.T) {
	reg := progress.NewRegistry()
	reg.Register(9)
	reg.AddTotal(9, 1000)
	snd := &fakeSender{}
	j := statusTestJob()
	j.StatusMsgID = 0 // 无占位消息
	stop := startProgressEditorWithInterval(context.Background(), statusTestDeps(reg, snd), j, 10*time.Millisecond)
	if stop == nil {
		t.Fatal("stop 函数不应为 nil")
	}
	time.Sleep(30 * time.Millisecond)
	stop() // no-op 路径应立即返回
	snd.mu.Lock()
	defer snd.mu.Unlock()
	if len(snd.edits) != 0 {
		t.Fatalf("无占位消息不应编辑: %v", snd.edits)
	}
}

func TestProgressEditorStopsWithContextCancel(t *testing.T) {
	reg := progress.NewRegistry()
	reg.Register(9)
	reg.AddTotal(9, 1000)
	snd := &fakeSender{}
	ctx, cancel := context.WithCancel(context.Background())
	stop := startProgressEditorWithInterval(ctx, statusTestDeps(reg, snd), statusTestJob(), 10*time.Millisecond)
	cancel()
	done := make(chan struct{})
	go func() { stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("ctx 取消后 stop 应及时返回（goroutine 不残留）")
	}
}
