package errlog

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// openStore 在临时目录建独立测试库（DAO 语义测试用真实 SQLite）。
func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// TestNilServiceNoOp 断言 nil *Service 全部方法安全 no-op（装配缺省态）。
func TestNilServiceNoOp(t *testing.T) {
	var s *Service
	s.Record(context.Background(), Record{Source: store.ErrorSourceRequest, Message: "x"})
	s.Cleanup(context.Background())
	done := make(chan struct{})
	go func() { s.Run(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("nil Run 应立即返回")
	}
}

// TestRecordStoresAndTruncates 断言 Record 落库字段透传、detail 截断与
// 不可序列化上下文的降级（丢上下文不丢行）。
func TestRecordStoresAndTruncates(t *testing.T) {
	st := openStore(t)
	svc := New(Options{Store: st, Log: quietLog()})
	ctx := context.Background()

	svc.Record(ctx, Record{
		Source:    store.ErrorSourceRequest,
		Code:      "BOT_SEND_FAILED",
		Stage:     "send",
		Message:   "任务失败",
		Detail:    strings.Repeat("根", detailLimit+10),
		Context:   map[string]any{"job_id": "j1", "bot_id": int64(42)},
		RequestID: 7,
	})
	rows, total, err := st.ListErrorLogs(ctx, store.ErrorLogsQuery{})
	if err != nil || total != 1 {
		t.Fatalf("应落一条: %v %d", err, total)
	}
	row := rows[0]
	if row.Code != "BOT_SEND_FAILED" || row.Stage != "send" || row.RequestID != 7 {
		t.Fatalf("字段应透传: %+v", row)
	}
	if runes := []rune(row.Detail); len(runes) != detailLimit+1 || !strings.HasSuffix(row.Detail, "…") {
		t.Fatalf("detail 应截断，得到 %d 字符", len(runes))
	}
	if row.Context["bot_id"] != float64(42) {
		t.Fatalf("上下文应往返: %+v", row.Context)
	}

	// 不可序列化上下文（channel）降级为空，行仍写入
	svc.Record(ctx, Record{
		Source:  store.ErrorSourceRequest,
		Message: "异常上下文",
		Context: map[string]any{"ch": make(chan int)},
	})
	if rows, total, _ = st.ListErrorLogs(ctx, store.ErrorLogsQuery{}); total != 2 || len(rows[0].Context) != 0 {
		t.Fatalf("不可序列化上下文应降级为空且不丢行: %d %+v", total, rows[0].Context)
	}
}

// TestThrottleWindow 无归属请求同键窗口内去重；带请求 ID 直通；键不同互不影响。
func TestThrottleWindow(t *testing.T) {
	st := openStore(t)
	now := time.UnixMilli(1_700_000_000_000)
	svc := New(Options{Store: st, Log: quietLog(), Now: func() time.Time { return now }})
	ctx := context.Background()

	base := Record{Source: store.ErrorSourceMTProto, Code: "X", Stage: "maintenance", Message: "m"}
	svc.Record(ctx, base)
	svc.Record(ctx, base) // 窗口内同键重复：丢弃
	if _, total, _ := st.ListErrorLogs(ctx, store.ErrorLogsQuery{}); total != 1 {
		t.Fatalf("窗口内同键应只记一条，得到 %d", total)
	}

	// 带请求 ID 直通（不受节流）
	withReq := base
	withReq.RequestID = 7
	svc.Record(ctx, withReq)
	if _, total, _ := st.ListErrorLogs(ctx, store.ErrorLogsQuery{}); total != 2 {
		t.Fatalf("带请求 ID 应直通，得到 %d", total)
	}

	// 键不同（换码）互不影响
	other := base
	other.Code = "Y"
	svc.Record(ctx, other)
	if _, total, _ := st.ListErrorLogs(ctx, store.ErrorLogsQuery{}); total != 3 {
		t.Fatalf("不同键应互不影响，得到 %d", total)
	}

	// 窗口过后同键重新放行
	now = now.Add(throttleWindow + time.Second)
	svc.Record(ctx, base)
	if _, total, _ := st.ListErrorLogs(ctx, store.ErrorLogsQuery{}); total != 4 {
		t.Fatalf("窗口过后应重新记录，得到 %d", total)
	}
}

// TestCleanupRespectsRetention 断言清理按 syscfg 保留天数删除过期行。
func TestCleanupRespectsRetention(t *testing.T) {
	st := openStore(t)
	now := time.UnixMilli(1_700_000_000_000)
	svc := New(Options{Store: st, Log: quietLog(), Now: func() time.Time { return now }})
	ctx := context.Background()

	for _, tc := range []struct {
		msg  string
		at   int64
		keep bool
	}{
		{"过期", now.Add(-31 * 24 * time.Hour).UnixMilli(), false}, // 超过缺省 30 天保留期
		{"新鲜", now.Add(-time.Hour).UnixMilli(), true},
	} {
		if _, err := st.InsertErrorLog(ctx, store.ErrorLog{
			Source: store.ErrorSourceRequest, Message: tc.msg, CreatedAt: tc.at,
		}); err != nil {
			t.Fatal(err)
		}
	}
	svc.Cleanup(ctx) // 缺省保留 30 天：只删过期行
	rows, total, err := st.ListErrorLogs(ctx, store.ErrorLogsQuery{})
	if err != nil || total != 1 || rows[0].Message != "新鲜" {
		t.Fatalf("应只剩新鲜行: %v %d %+v", err, total, rows)
	}
}

// TestFromError 断言便捷构造：apperr 码透传、根因取 Cause 链最内层、
// 未分类错误码回落 INTERNAL_ERROR 且 detail 保留原始文本。
func TestFromError(t *testing.T) {
	cause := errors.New("PEER_FLOOD: Too many requests")
	rec := FromError(store.ErrorSourceRequest, "send", "任务失败", apperr.Wrap(apperr.CodeSendFailed, cause), nil)
	if rec.Code != string(apperr.CodeSendFailed) {
		t.Fatalf("码应透传: %+v", rec)
	}
	if rec.Detail != cause.Error() {
		t.Fatalf("根因应取 cause 原文: %q", rec.Detail)
	}

	raw := errors.New("disk exploded")
	rec = FromError(store.ErrorSourceBackup, "maintenance", "备份失败", raw, nil)
	if rec.Code != string(apperr.CodeInternal) {
		t.Fatalf("未分类应回落 INTERNAL_ERROR: %+v", rec)
	}
	if rec.Detail != raw.Error() {
		t.Fatalf("根因应保留原始错误文本: %q", rec.Detail)
	}
}

// TestTruncateDetail 与 queue.errorDetailText（v23）同规则：按字符截断加省略号。
func TestTruncateDetail(t *testing.T) {
	short := "FLOOD_WAIT_9: 3000"
	if got := TruncateDetail(short); got != short {
		t.Fatalf("短文本应原样: %q", got)
	}
	long := strings.Repeat("错", detailLimit+50)
	got := TruncateDetail(long)
	if runes := []rune(got); len(runes) != detailLimit+1 || !strings.HasSuffix(got, "…") {
		t.Fatalf("超长应截断到上限加省略号，得到 %d 字符", len(runes))
	}
}
