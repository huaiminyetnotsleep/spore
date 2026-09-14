package store

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
)

// testLogger 返回丢弃输出的 logger，保持测试输出干净。
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// openTestStore 在临时目录创建独立数据库。测试绝不触碰真实 data/ 目录。
func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "test.db"), testLogger())
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Fatalf("关闭测试数据库失败: %v", err)
		}
	})
	return s
}

func TestOpenUnwritablePath(t *testing.T) {
	// 目录不存在时驱动无法创建数据库文件，应归类为 STORE_UNAVAILABLE
	_, err := Open(context.Background(),
		filepath.Join(t.TempDir(), "no-such-dir", "test.db"), testLogger())
	if err == nil {
		t.Fatal("目录不存在时应返回错误")
	}
	var ae *apperr.AppError
	if !errors.As(err, &ae) || ae.Code != apperr.CodeStoreUnavailable {
		t.Fatalf("应归类为 STORE_UNAVAILABLE，得到 %v", err)
	}
}

// mustUser 创建测试前置用户，失败即中止当前测试。
func mustUser(t *testing.T, s *Store, id int64) User {
	t.Helper()
	u, err := s.CreateUser(context.Background(), User{ID: id})
	if err != nil {
		t.Fatalf("创建用户 %d 失败: %v", id, err)
	}
	return u
}
