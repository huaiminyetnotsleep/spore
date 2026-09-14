package store

import (
	"context"
	"errors"
	"testing"
)

func TestWebSessionLifecycle(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	sess := WebSession{
		IDHash:    "hash-1",
		ExpiresAt: 10_000,
		CSRFToken: "csrf-1",
		IP:        "127.0.0.1",
		UserAgent: "TestAgent",
	}
	if err := s.CreateWebSession(ctx, sess); err != nil {
		t.Fatalf("创建会话失败: %v", err)
	}

	got, err := s.GetWebSession(ctx, "hash-1")
	if err != nil {
		t.Fatalf("读取会话失败: %v", err)
	}
	if got.CSRFToken != "csrf-1" || got.ExpiresAt != 10_000 || got.IP != "127.0.0.1" || got.UserAgent != "TestAgent" {
		t.Errorf("会话字段应往返一致: %+v", got)
	}
	if got.CreatedAt == 0 {
		t.Error("CreatedAt 应回填")
	}

	// 滑动续期：过期时间被推到新的绝对时间
	if err := s.TouchWebSession(ctx, "hash-1", 20_000); err != nil {
		t.Fatalf("续期失败: %v", err)
	}
	got, _ = s.GetWebSession(ctx, "hash-1")
	if got.ExpiresAt != 20_000 {
		t.Errorf("续期后 expires_at 应为 20000，得到 %d", got.ExpiresAt)
	}
	// 其余字段不受续期影响
	if got.CSRFToken != "csrf-1" {
		t.Errorf("续期不应改动其他字段，得到 %+v", got)
	}

	// 不存在的会话
	if _, err := s.GetWebSession(ctx, "hash-x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("应返回 ErrNotFound，得到 %v", err)
	}
	if err := s.TouchWebSession(ctx, "hash-x", 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("续期不存在会话应返回 ErrNotFound，得到 %v", err)
	}

	// 登出：仅删除当前会话
	if err := s.CreateWebSession(ctx, WebSession{IDHash: "hash-2", ExpiresAt: 10_000, CSRFToken: "csrf-2"}); err != nil {
		t.Fatalf("创建第二会话失败: %v", err)
	}
	if err := s.DeleteWebSession(ctx, "hash-1"); err != nil {
		t.Fatalf("删除会话失败: %v", err)
	}
	if _, err := s.GetWebSession(ctx, "hash-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("删除后应查不到，得到 %v", err)
	}
	if _, err := s.GetWebSession(ctx, "hash-2"); err != nil {
		t.Fatalf("多设备并行：另一会话不应受登出影响: %v", err)
	}

	// 空字段拒绝
	if err := s.CreateWebSession(ctx, WebSession{IDHash: "", CSRFToken: "x"}); err == nil {
		t.Fatal("空 ID 哈希应被拒绝")
	}
	if err := s.CreateWebSession(ctx, WebSession{IDHash: "h", CSRFToken: ""}); err == nil {
		t.Fatal("空 CSRF token 应被拒绝")
	}
}

func TestWebSessionDeleteAllAndCleanExpired(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	mk := func(id string, expires int64) {
		if err := s.CreateWebSession(ctx, WebSession{IDHash: id, ExpiresAt: expires, CSRFToken: "t"}); err != nil {
			t.Fatalf("创建 %s 失败: %v", id, err)
		}
	}
	mk("a", 1000) // 已过期
	mk("b", 5000) // 已过期
	mk("c", 9000) // 仍有效

	n, err := s.CleanExpiredWebSessions(ctx, 6000)
	if err != nil {
		t.Fatalf("清理过期会话失败: %v", err)
	}
	if n != 2 {
		t.Errorf("应清理 2 条，得到 %d", n)
	}
	if _, err := s.GetWebSession(ctx, "c"); err != nil {
		t.Fatalf("有效会话不应被清理: %v", err)
	}

	// 全部失效
	mk("d", 9000)
	if err := s.DeleteAllWebSessions(ctx); err != nil {
		t.Fatalf("清空会话失败: %v", err)
	}
	for _, id := range []string{"c", "d"} {
		if _, err := s.GetWebSession(ctx, id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("清空后 %s 应不存在，得到 %v", id, err)
		}
	}
}
