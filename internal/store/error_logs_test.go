package store

import (
	"context"
	"testing"
)

// mustErrorLog 落一条错误日志并在失败时终止测试。
func mustErrorLog(t *testing.T, s *Store, in ErrorLog) ErrorLog {
	t.Helper()
	row, err := s.InsertErrorLog(context.Background(), in)
	if err != nil {
		t.Fatalf("写入错误日志失败: %v", err)
	}
	if row.ID == 0 || row.CreatedAt == 0 {
		t.Fatalf("回填 ID 与时间戳缺失: %+v", row)
	}
	return row
}

func TestErrorLogInsertValidate(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if _, err := s.InsertErrorLog(ctx, ErrorLog{Source: "bogus", Message: "x"}); err == nil {
		t.Fatal("非法来源应被拒绝")
	}
	if _, err := s.InsertErrorLog(ctx, ErrorLog{Source: ErrorSourceRequest, Severity: "fatal", Message: "x"}); err == nil {
		t.Fatal("非法级别应被拒绝")
	}
	// 级别缺省回落 error；上下文空值序列化为 {}
	row := mustErrorLog(t, s, ErrorLog{Source: ErrorSourceRequest, Message: "任务失败"})
	if row.Severity != ErrorSeverityError {
		t.Fatalf("缺省级别应为 error: %+v", row)
	}
	got, _, err := s.ListErrorLogs(ctx, ErrorLogsQuery{})
	if err != nil || len(got) != 1 {
		t.Fatalf("应能读回一行: %v %d", err, len(got))
	}
	if got[0].Context == nil || len(got[0].Context) != 0 {
		t.Fatalf("空上下文应往返为空 map: %+v", got[0].Context)
	}
}

func TestErrorLogListFiltersAndPaging(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	base := nowMillis()
	a := mustErrorLog(t, s, ErrorLog{
		Source: ErrorSourceRequest, Code: "BOT_SEND_FAILED", Stage: "send",
		Message: "任务失败", Detail: "FLOOD_WAIT_9: 3000",
		Context: map[string]any{"job_id": "j1", "bot_id": int64(42)}, RequestID: 7,
		CreatedAt: base,
	})
	b := mustErrorLog(t, s, ErrorLog{
		Source: ErrorSourceWatch, Code: "BOT_SEND_FAILED", Severity: ErrorSeverityWarn,
		Message: "转储失败", CreatedAt: base + 5000,
	})
	c := mustErrorLog(t, s, ErrorLog{
		Source: ErrorSourceCloud, Code: "CLOUD_NETWORK", Message: "测试失败",
		CreatedAt: base + 10000,
	})

	// 全量：倒序 c,b,a
	rows, total, err := s.ListErrorLogs(ctx, ErrorLogsQuery{})
	if err != nil || total != 3 || len(rows) != 3 {
		t.Fatalf("全量应 3 行: %v %d/%d", err, len(rows), total)
	}
	if rows[0].ID != c.ID || rows[2].ID != a.ID {
		t.Fatalf("应按 id 倒序: %+v", rows)
	}
	// 上下文往返
	if rows[2].Context["bot_id"] != float64(42) {
		t.Fatalf("上下文参数应往返: %+v", rows[2].Context)
	}

	// 按来源 + 错误码筛选
	rows, total, err = s.ListErrorLogs(ctx, ErrorLogsQuery{Source: ErrorSourceWatch, Code: "BOT_SEND_FAILED"})
	if err != nil || total != 1 || rows[0].ID != b.ID {
		t.Fatalf("来源+码筛选应命中 b: %v %d", err, total)
	}
	// 按级别筛选
	rows, total, err = s.ListErrorLogs(ctx, ErrorLogsQuery{Severity: ErrorSeverityWarn})
	if err != nil || total != 1 || rows[0].ID != b.ID {
		t.Fatalf("warn 筛选应命中 b: %v %d", err, total)
	}
	// 按请求筛选
	rows, total, err = s.ListErrorLogs(ctx, ErrorLogsQuery{RequestID: 7})
	if err != nil || total != 1 || rows[0].ID != a.ID {
		t.Fatalf("请求筛选应命中 a: %v %d", err, total)
	}
	// 时间范围：after 含 b/c、before 排除 c（开区间上界）
	rows, total, err = s.ListErrorLogs(ctx, ErrorLogsQuery{
		CreatedAfter: b.CreatedAt, CreatedBefore: c.CreatedAt,
	})
	if err != nil || total != 1 || rows[0].ID != b.ID {
		t.Fatalf("时间范围应只含 b: %v %d", err, total)
	}

	// 非法筛选参数直接报错
	if _, _, err := s.ListErrorLogs(ctx, ErrorLogsQuery{Source: "bogus"}); err == nil {
		t.Fatal("非法来源筛选应被拒绝")
	}
	if _, _, err := s.ListErrorLogs(ctx, ErrorLogsQuery{Severity: "fatal"}); err == nil {
		t.Fatal("非法级别筛选应被拒绝")
	}

	// 分页：page_size=2 第一页 c,b
	rows, total, err = s.ListErrorLogs(ctx, ErrorLogsQuery{Page: 1, PageSize: 2})
	if err != nil || total != 3 || len(rows) != 2 || rows[1].ID != b.ID {
		t.Fatalf("分页第一页应为 c,b: %v %d %+v", err, total, rows)
	}
}

func TestErrorLogDelete(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	a := mustErrorLog(t, s, ErrorLog{Source: ErrorSourceRequest, Code: "X", Message: "a"})
	mustErrorLog(t, s, ErrorLog{Source: ErrorSourceRequest, Code: "X", Message: "b", CreatedAt: a.CreatedAt + 1000})
	c := mustErrorLog(t, s, ErrorLog{Source: ErrorSourceCloud, Code: "CLOUD_NETWORK", Message: "c", CreatedAt: a.CreatedAt + 2000})

	// 按 ID 批量删（不存在的不计入）
	n, err := s.DeleteErrorLogs(ctx, []int64{a.ID, 99999})
	if err != nil || n != 1 {
		t.Fatalf("按 ID 删除应删 1 行: %v %d", err, n)
	}
	// 空切片 no-op
	if n, err := s.DeleteErrorLogs(ctx, nil); err != nil || n != 0 {
		t.Fatalf("空删除应 no-op: %v %d", err, n)
	}

	// 按时间段删（可叠加来源）：只剩 b(request)、c(cloud)
	n, err = s.DeleteErrorLogsRange(ctx, ErrorLogsRangeQuery{Before: c.CreatedAt, Source: ErrorSourceRequest})
	if err != nil || n != 1 {
		t.Fatalf("时间段+来源删除应删 1 行: %v %d", err, n)
	}
	// 无时间界拒绝（防全表清空）
	if _, err := s.DeleteErrorLogsRange(ctx, ErrorLogsRangeQuery{Source: ErrorSourceCloud}); err == nil {
		t.Fatal("无时间界应被拒绝")
	}
	// 非法来源拒绝
	if _, err := s.DeleteErrorLogsRange(ctx, ErrorLogsRangeQuery{Before: c.CreatedAt, Source: "bogus"}); err == nil {
		t.Fatal("非法来源应被拒绝")
	}

	// 保留清理：cutoff 覆盖 c 之后全部删除
	n, err = s.DeleteErrorLogsBefore(ctx, c.CreatedAt+1)
	if err != nil || n != 1 {
		t.Fatalf("保留清理应删 1 行: %v %d", err, n)
	}
	if _, total, _ := s.ListErrorLogs(ctx, ErrorLogsQuery{}); total != 0 {
		t.Fatalf("应全部删除，剩 %d", total)
	}
}
