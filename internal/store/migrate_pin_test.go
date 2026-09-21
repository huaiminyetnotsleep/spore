package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
)

// TestMigrateV20UpgradesLegacyV19 构造停在 user_version=19 的旧库并写入
// 用户与请求行，升级后新列（requests.pin/pin_ok/pin_total、users.auto_pin）
// 应取默认值，既有行保留，置顶回写与偏好更新可用。
func TestMigrateV20UpgradesLegacyV19(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.db")
	raw, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		t.Fatalf("打开原始库失败: %v", err)
	}
	for v, script := range migrations[:19] {
		if _, err := raw.Exec(script); err != nil {
			t.Fatalf("执行旧迁移 v%d 失败: %v", v+1, err)
		}
		if _, err := raw.Exec(fmt.Sprintf("PRAGMA user_version = %d", v+1)); err != nil {
			t.Fatalf("写旧版本号失败: %v", err)
		}
	}
	if _, err := raw.Exec(`INSERT INTO users (id, status, is_owner, created_at)
		VALUES (42, 'enabled', 0, 1789900000000)`); err != nil {
		t.Fatalf("灌入旧用户失败: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO requests
		(user_id, channel_key, message_id, status, requested_at)
		VALUES (42, 'mychan', 7, 'succeeded', 1789900000000)`); err != nil {
		t.Fatalf("灌入旧请求失败: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(context.Background(), path, testLogger())
	if err != nil {
		t.Fatalf("旧 v19 库应经 v20 顺利迁移: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()

	req, err := s.GetRequest(ctx, 1)
	if err != nil {
		t.Fatalf("迁移后读取请求失败: %v", err)
	}
	if req.UserID != 42 || req.ChannelKey != "mychan" || req.Status != RequestSucceeded {
		t.Fatalf("既有行应保留: %+v", req)
	}
	if req.Pin || req.PinOK != 0 || req.PinTotal != 0 {
		t.Fatalf("新增置顶列应取默认值: %+v", req)
	}
	u, err := s.GetUser(ctx, 42)
	if err != nil {
		t.Fatalf("迁移后读取用户失败: %v", err)
	}
	if u.AutoPin {
		t.Fatalf("auto_pin 应默认关闭: %+v", u)
	}
	// 新列可写：置顶回写仅命中 pin 行；偏好开关可切换
	if err := s.SetRequestPinResult(ctx, 1, 2, 3); err == nil {
		t.Fatalf("非 pin 行回写应返回 ErrNotFound")
	}
	if _, err := s.CreateRequest(ctx, Request{UserID: 42, ChannelKey: "c2",
		MessageID: 8, Pin: true}); err != nil {
		t.Fatalf("创建 pin 请求失败: %v", err)
	}
	if err := s.SetRequestPinResult(ctx, 2, 2, 3); err != nil {
		t.Fatalf("pin 行回写失败: %v", err)
	}
	if err := s.UpdateUserAutoPin(ctx, 42, true); err != nil {
		t.Fatalf("更新 auto_pin 失败: %v", err)
	}
	got, err := s.GetUser(ctx, 42)
	if err != nil || !got.AutoPin {
		t.Fatalf("auto_pin 应已开启: %+v err=%v", got, err)
	}
}

// TestRequestPinRoundTrip 覆盖 pin 列读写、OnlyPin 筛选、置顶结果回写与
// 重试清零（pin 标记保留、结果清空）。
func TestRequestPinRoundTrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	mustUser(t, s, 7)

	plain, err := s.CreateRequest(ctx, Request{UserID: 7, ChannelKey: "chan",
		MessageID: 1})
	if err != nil {
		t.Fatalf("创建普通请求失败: %v", err)
	}
	pinReq, err := s.CreateRequest(ctx, Request{UserID: 7, ChannelKey: "chan",
		MessageID: 2, Pin: true})
	if err != nil {
		t.Fatalf("创建置顶请求失败: %v", err)
	}

	// OnlyPin 筛选只返回 pin 行；无筛选返回全部
	pinned, err := s.ListRequests(ctx, RequestFilter{UserID: 7, OnlyPin: true})
	if err != nil || len(pinned) != 1 || !pinned[0].Pin {
		t.Fatalf("OnlyPin 筛选应只返回置顶请求: n=%d err=%v", len(pinned), err)
	}
	all, err := s.ListRequests(ctx, RequestFilter{UserID: 7})
	if err != nil || len(all) != 2 {
		t.Fatalf("无筛选应返回全部请求: n=%d err=%v", len(all), err)
	}

	// 置顶结果回写：仅 pin 行生效
	if err := s.SetRequestPinResult(ctx, pinReq.ID, 1, 2); err != nil {
		t.Fatalf("回写置顶结果失败: %v", err)
	}
	if err := s.SetRequestPinResult(ctx, plain.ID, 9, 9); err == nil {
		t.Fatalf("非 pin 行回写应返回 ErrNotFound")
	}
	got, err := s.GetRequest(ctx, pinned[0].ID)
	if err != nil || got.PinOK != 1 || got.PinTotal != 2 {
		t.Fatalf("置顶结果应已落库: %+v err=%v", got, err)
	}

	// 重试：结果清零、pin 标记保留
	if err := s.RetryRequest(ctx, pinned[0].ID, 0); err != nil {
		t.Fatalf("重试失败: %v", err)
	}
	retried, err := s.GetRequest(ctx, pinned[0].ID)
	if err != nil {
		t.Fatalf("读取重试行失败: %v", err)
	}
	if !retried.Pin {
		t.Fatalf("重试后 pin 标记应保留: %+v", retried)
	}
	if retried.PinOK != 0 || retried.PinTotal != 0 {
		t.Fatalf("重试后置顶结果应清零: %+v", retried)
	}
}

// TestUpdateUserAutoPin 覆盖偏好开关的写读回环（默认关闭 → 开 → 关）。
func TestUpdateUserAutoPin(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	u := mustUser(t, s, 9)
	if u.AutoPin {
		t.Fatalf("新用户 auto_pin 应默认关闭: %+v", u)
	}
	if err := s.UpdateUserAutoPin(ctx, 9, true); err != nil {
		t.Fatalf("开启失败: %v", err)
	}
	if got, _ := s.GetUser(ctx, 9); !got.AutoPin {
		t.Fatalf("auto_pin 应已开启")
	}
	if err := s.UpdateUserAutoPin(ctx, 9, false); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}
	if got, _ := s.GetUser(ctx, 9); got.AutoPin {
		t.Fatalf("auto_pin 应已关闭")
	}
}
