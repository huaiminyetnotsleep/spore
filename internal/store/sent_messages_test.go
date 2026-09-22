package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
)

// TestMigrateV22UpgradesLegacyV21 构造停在 user_version=21 的旧库并写入
// 用户与请求行，升级后 sent_messages 表可用（引用回复锚点），既有行保留。
func TestMigrateV22UpgradesLegacyV21(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.db")
	raw, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		t.Fatalf("打开原始库失败: %v", err)
	}
	for v, script := range migrations[:21] {
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
		t.Fatalf("旧 v21 库应经 v22 顺利迁移: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()

	if req, err := s.GetRequest(ctx, 1); err != nil || req.Status != RequestSucceeded {
		t.Fatalf("既有请求应保留: %+v err=%v", req, err)
	}
	// 新表可写可查
	if err := s.InsertSentMessages(ctx, []SentMessage{{
		RequestID: 1, BotID: 9, ChatID: 42, MessageID: 501, Kind: SentKindChannelCopy,
	}}); err != nil {
		t.Fatalf("写入锚点失败: %v", err)
	}
	copies, err := s.ListChannelCopiesByRequest(ctx, 1)
	if err != nil || len(copies) != 1 || copies[0].MessageID != 501 {
		t.Fatalf("迁移后应能查询频道副本坐标: %+v err=%v", copies, err)
	}
}

// TestSentMessagesRoundTrip 覆盖批量写入、坐标反查、唯一索引去重与按请求
// 查频道副本（kind 过滤 + 写入序）。
func TestSentMessagesRoundTrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	mustUser(t, s, 7)
	req, err := s.CreateRequest(ctx, Request{UserID: 7, ChannelKey: "chan", MessageID: 1})
	if err != nil {
		t.Fatalf("创建请求失败: %v", err)
	}

	rows := []SentMessage{
		{RequestID: req.ID, BotID: 9, ChatID: 7, MessageID: 101, Kind: SentKindMedia},
		{RequestID: req.ID, BotID: 9, ChatID: 7, MessageID: 102, Kind: SentKindMedia},
		{RequestID: req.ID, BotID: 9, ChatID: -100123, MessageID: 201, Kind: SentKindChannelCopy},
		{RequestID: req.ID, BotID: 0, ChatID: 7, MessageID: 103, Kind: SentKindStatus},
	}
	if err := s.InsertSentMessages(ctx, rows); err != nil {
		t.Fatalf("批量写入失败: %v", err)
	}
	// 空切片 no-op
	if err := s.InsertSentMessages(ctx, nil); err != nil {
		t.Fatalf("空切片应直接成功: %v", err)
	}
	// 重复坐标（OR IGNORE）：不报错、不增行
	if err := s.InsertSentMessages(ctx, rows[:1]); err != nil {
		t.Fatalf("重复坐标应静默去重: %v", err)
	}

	got, err := s.FindSentMessage(ctx, 9, 7, 102)
	if err != nil || got.RequestID != req.ID || got.Kind != SentKindMedia {
		t.Fatalf("坐标反查不符: %+v err=%v", got, err)
	}
	// bot 隔离：跨 bot 查不到同 chat/message 坐标
	if _, err := s.FindSentMessage(ctx, 8, 7, 102); !errors.Is(err, ErrNotFound) {
		t.Fatalf("跨 bot 查询应返回 ErrNotFound，得到 %v", err)
	}
	if _, err := s.FindSentMessage(ctx, 9, 7, 999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("未命中坐标应返回 ErrNotFound，得到 %v", err)
	}

	copies, err := s.ListChannelCopiesByRequest(ctx, req.ID)
	if err != nil || len(copies) != 1 {
		t.Fatalf("频道副本应只含 channel_copy 行: %+v err=%v", copies, err)
	}
	if copies[0].ChatID != -100123 || copies[0].MessageID != 201 || copies[0].BotID != 9 {
		t.Fatalf("频道副本坐标不符: %+v", copies[0])
	}
	// 无关请求查不到
	if other, err := s.ListChannelCopiesByRequest(ctx, req.ID+1); err != nil || len(other) != 0 {
		t.Fatalf("无副本请求应返回空: %+v err=%v", other, err)
	}
}

// TestMarkRequestPinQueued 覆盖在途补标的条件矩阵：queued/processing 命中、
// 终态不命中、归属不命中；MarkRequestPin 无条件置位后结果可回写。
func TestMarkRequestPinQueued(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	mustUser(t, s, 7)
	mustUser(t, s, 8)

	queued, _ := s.CreateRequest(ctx, Request{UserID: 7, ChannelKey: "c", MessageID: 1})
	processing, _ := s.CreateRequest(ctx, Request{UserID: 7, ChannelKey: "c", MessageID: 2})
	if err := s.MarkRequestStarted(ctx, processing.ID, nowMillis()); err != nil {
		t.Fatalf("标记开始失败: %v", err)
	}
	done, _ := s.CreateRequest(ctx, Request{UserID: 7, ChannelKey: "c", MessageID: 3})
	if _, err := s.CancelRequest(ctx, done.ID, nowMillis()); err != nil {
		t.Fatalf("预置终态失败: %v", err)
	}

	if ok, err := s.MarkRequestPinQueued(ctx, 7, queued.ID); err != nil || !ok {
		t.Fatalf("queued 应补标成功: ok=%v err=%v", ok, err)
	}
	if ok, err := s.MarkRequestPinQueued(ctx, 7, processing.ID); err != nil || !ok {
		t.Fatalf("processing 应补标成功: ok=%v err=%v", ok, err)
	}
	if ok, err := s.MarkRequestPinQueued(ctx, 7, done.ID); err != nil || ok {
		t.Fatalf("终态请求不应补标: ok=%v err=%v", ok, err)
	}
	if ok, err := s.MarkRequestPinQueued(ctx, 8, queued.ID); err != nil || ok {
		t.Fatalf("他人请求不应补标: ok=%v err=%v", ok, err)
	}
	if r, _ := s.GetRequest(ctx, queued.ID); !r.Pin {
		t.Fatalf("补标后 pin 应为 true: %+v", r)
	}

	// 无条件置位：让 SetRequestPinResult（WHERE pin=1）可以回写
	plain, _ := s.CreateRequest(ctx, Request{UserID: 7, ChannelKey: "c", MessageID: 4})
	if err := s.MarkRequestPin(ctx, 7, plain.ID); err != nil {
		t.Fatalf("无条件置 pin 失败: %v", err)
	}
	if err := s.SetRequestPinResult(ctx, plain.ID, 1, 2); err != nil {
		t.Fatalf("置 pin 后结果回写应生效: %v", err)
	}
	if r, _ := s.GetRequest(ctx, plain.ID); r.PinOK != 1 || r.PinTotal != 2 {
		t.Fatalf("回写结果不符: %+v", r)
	}
}
