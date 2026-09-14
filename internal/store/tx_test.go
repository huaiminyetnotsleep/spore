package store

import (
	"context"
	"errors"
	"testing"
)

func TestTxCommitsWhenFnSucceeds(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	err := s.Tx(ctx, func(tx *Store) error {
		_, err := tx.CreateUser(ctx, User{ID: 1, Status: UserEnabled})
		return err
	})
	if err != nil {
		t.Fatalf("事务提交失败: %v", err)
	}
	// 提交后在外层可见
	if _, err := s.GetUser(ctx, 1); err != nil {
		t.Fatalf("提交后的写入应可见: %v", err)
	}
}

func TestTxRollsBackOnError(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	mustUser(t, s, 1)

	sentinel := errors.New("boom")
	err := s.Tx(ctx, func(tx *Store) error {
		if err := tx.UpdateUserNote(ctx, 1, "事务内备注"); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("应透传 fn 的错误，得到 %v", err)
	}
	// 回滚：备注与额度扣减都不落库
	got, err := s.GetUser(ctx, 1)
	if err != nil {
		t.Fatalf("读取用户失败: %v", err)
	}
	if got.Note != "" {
		t.Errorf("回滚后事务内写入不应可见，得到备注 %q", got.Note)
	}
}

// TestTxCheckAndWriteAtomic 验证 Tx 的核心用途：
// "检查 + 扣减"在同一事务内完成时，并发调用不会产生超扣窗口。
func TestTxCheckAndWriteAtomic(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	mustUser(t, s, 1)

	const limit = 10
	// 预置额度：已用 limit-1 次
	if err := s.IncrementUsage(ctx, 1, "2026-08-27", limit-1); err != nil {
		t.Fatalf("预置用量失败: %v", err)
	}

	// 并发 8 个事务各自"检查剩余并 +1"：因检查与扣减同事务，至多 1 个成功
	const workers = 8
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		go func() {
			errs <- s.Tx(ctx, func(tx *Store) error {
				u, err := tx.GetUsage(ctx, 1, "2026-08-27")
				if err != nil {
					return err
				}
				if u.Used >= limit {
					return nil // 拒绝：不扣减
				}
				return tx.IncrementUsage(ctx, 1, "2026-08-27", 1)
			})
		}()
	}
	for i := 0; i < workers; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("并发事务失败: %v", err)
		}
	}
	got, err := s.GetUsage(ctx, 1, "2026-08-27")
	if err != nil {
		t.Fatalf("读取用量失败: %v", err)
	}
	if got.Used != limit {
		t.Fatalf("并发扣减后 used 应恰为 %d，得到 %d", limit, got.Used)
	}
}
