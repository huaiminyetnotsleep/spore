package queue

import (
	"context"
	"testing"

	"github.com/gotd/td/tg"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/errlog"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// TestProcessFailureRecordsErrorLog 任务失败时错误日志中心应落一行终态
// 记录：每次失败尝试一行（含码、根因、任务上下文与请求归属），中间尝试
// 的根因不再被 requests.error_detail 覆盖丢失。
func TestProcessFailureRecordsErrorLog(t *testing.T) {
	s := openStore(t)
	job, r := newJobWithRequest(t, s, 55)
	deps := Deps{
		Fetcher: &fakeFetcher{err: apperr.New(apperr.CodeChannelInaccessible, "读取账号未加入频道")},
		Sender:  &fakeSender{},
		Store:   s,
		ErrLog:  errlog.New(errlog.Options{Store: s, Log: testLog()}),
		Log:     testLog(),
	}

	done := make(chan struct{})
	go func() { Process(deps)(context.Background(), job); close(done) }()
	waitDone(t, done)

	if got, err := s.GetRequest(context.Background(), r.ID); err != nil || got.Status != store.RequestFailed {
		t.Fatalf("请求应为 failed: %v %+v", err, got)
	}
	rows, total, err := s.ListErrorLogs(context.Background(), store.ErrorLogsQuery{})
	if err != nil || total != 1 {
		t.Fatalf("失败应落一条错误日志: %v %d", err, total)
	}
	row := rows[0]
	if row.Source != store.ErrorSourceRequest || row.RequestID != r.ID {
		t.Fatalf("来源与请求归属不符: %+v", row)
	}
	if row.Code != string(apperr.CodeChannelInaccessible) || row.Stage != "fetch" {
		t.Fatalf("码与环节不符: %+v", row)
	}
	if row.Severity != store.ErrorSeverityError {
		t.Fatalf("终态失败应为 error 级: %+v", row)
	}
	// 根因保留 AppError 内部描述（无 cause 链时的回落）
	if row.Detail == "" {
		t.Fatalf("根因不应为空: %+v", row)
	}
	if _, ok := row.Context["channel_key"]; !ok {
		t.Fatalf("上下文应含 channel_key: %+v", row.Context)
	}
}

// TestProcessBotDisabledRecordsErrorLog 停用 bot 名下任务出队即失败，错误
// 日志应同步留痕（code=BOT_DISABLED、stage=claim）。
func TestProcessBotDisabledRecordsErrorLog(t *testing.T) {
	s := openStore(t)
	job, r := newJobWithRequest(t, s, 55)
	deps := Deps{
		Fetcher:     &fakeFetcher{msgs: []*tg.Message{{ID: 7}}},
		Sender:      &fakeSender{},
		Store:       s,
		ErrLog:      errlog.New(errlog.Options{Store: s, Log: testLog()}),
		BotDisabled: func(int64) bool { return true },
		Log:         testLog(),
	}

	done := make(chan struct{})
	go func() { Process(deps)(context.Background(), job); close(done) }()
	waitDone(t, done)

	rows, total, err := s.ListErrorLogs(context.Background(), store.ErrorLogsQuery{})
	if err != nil || total != 1 {
		t.Fatalf("停用失败应落一条: %v %d", err, total)
	}
	if rows[0].Code != string(apperr.CodeBotDisabled) || rows[0].Stage != "claim" {
		t.Fatalf("停用失败码/环节不符: %+v", rows[0])
	}
	if rows[0].RequestID != r.ID {
		t.Fatalf("应归属该请求: %+v", rows[0])
	}
}
