package store

import (
	"context"
	"errors"
	"testing"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
)

func TestCancelRequestConditionalLifecycle(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	u := mustUser(t, s, 1)

	queued, err := s.CreateRequest(ctx, Request{UserID: u.ID, ChannelKey: "example", MessageID: 1, RequestedAt: 100, QueuedAt: 100})
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.CancelRequest(ctx, queued.ID, 200)
	if err != nil {
		t.Fatalf("取消 queued 失败: %v", err)
	}
	if before.Status != RequestQueued {
		t.Fatalf("取消前快照应为 queued: %+v", before)
	}
	got, err := s.GetRequest(ctx, queued.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != RequestCancelled || got.ErrorCode != "REQUEST_CANCELLED" || got.FinishedAt != 200 || got.DurationMs != 100 {
		t.Fatalf("取消后的字段不对: %+v", got)
	}
	if err := s.FinishRequest(ctx, queued.ID, RequestResult{Status: RequestSucceeded, At: 300}); err == nil {
		t.Fatal("自然完成不应覆盖 cancelled")
	} else if code := cancelTestErrorCode(err); code != apperr.CodeStoreConstraint {
		t.Fatalf("覆盖 cancelled 应为 STORE_CONSTRAINT，得到 %v", err)
	}
	got, _ = s.GetRequest(ctx, queued.ID)
	if got.Status != RequestCancelled {
		t.Fatalf("自然完成后状态不应变化: %+v", got)
	}

	processing, err := s.CreateRequest(ctx, Request{UserID: u.ID, ChannelKey: "example", MessageID: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkRequestStarted(ctx, processing.ID, 250); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CancelRequest(ctx, processing.ID, 400); err != nil {
		t.Fatalf("取消 processing 失败: %v", err)
	}
	got, _ = s.GetRequest(ctx, processing.ID)
	if got.Status != RequestCancelled || got.DurationMs != 150 {
		t.Fatalf("processing 取消字段不对: %+v", got)
	}

	finished, err := s.CreateRequest(ctx, Request{UserID: u.ID, ChannelKey: "example", MessageID: 3})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FinishRequest(ctx, finished.ID, RequestResult{Status: RequestFailed, At: 500}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CancelRequest(ctx, finished.ID, 600); cancelTestErrorCode(err) != apperr.CodeStoreConstraint {
		t.Fatalf("终态取消应返回 STORE_CONSTRAINT，得到 %v", err)
	}
	if _, err := s.CancelRequest(ctx, 404, 600); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在请求应返回 ErrNotFound，得到 %v", err)
	}
}

func cancelTestErrorCode(err error) apperr.Code {
	var ae *apperr.AppError
	if errors.As(err, &ae) {
		return ae.Code
	}
	return ""
}
