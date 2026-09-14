package store

import (
	"context"
	"errors"
	"testing"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
)

func TestUpsertChannelBindingIdempotent(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	mustUser(t, s, 100)

	in := ChannelBinding{ChannelID: -1001234567890, UserID: 100, Username: "mychan",
		Title: "我的频道", BoundVia: BoundViaBot}
	got, err := s.UpsertChannelBinding(ctx, in)
	if err != nil {
		t.Fatalf("写入绑定失败: %v", err)
	}
	if got.ChannelID != in.ChannelID || got.UserID != 100 || got.Username != "mychan" {
		t.Fatalf("回读不一致: %+v", got)
	}
	if got.CreatedAt == 0 || got.UpdatedAt < got.CreatedAt {
		t.Fatalf("时间戳异常: %+v", got)
	}

	again, err := s.UpsertChannelBinding(ctx, ChannelBinding{ChannelID: in.ChannelID,
		UserID: 100, Username: "mychan", Title: "改名后的频道", BoundVia: BoundViaWeb})
	if err != nil {
		t.Fatalf("同一用户重复绑定应幂等: %v", err)
	}
	if again.Title != "改名后的频道" || again.BoundVia != BoundViaWeb {
		t.Fatalf("重复绑定应刷新标题与来源: %+v", again)
	}
	if again.CreatedAt != got.CreatedAt {
		t.Fatalf("重复绑定不应重置 created_at: %d != %d", again.CreatedAt, got.CreatedAt)
	}
}

func TestUpsertChannelBindingConflict(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	mustUser(t, s, 100)
	mustUser(t, s, 200)

	if _, err := s.UpsertChannelBinding(ctx, ChannelBinding{ChannelID: -1001,
		UserID: 100, BoundVia: BoundViaBot}); err != nil {
		t.Fatalf("首次绑定失败: %v", err)
	}
	_, err := s.UpsertChannelBinding(ctx, ChannelBinding{ChannelID: -1001, UserID: 200, BoundVia: BoundViaWeb})
	var ae *apperr.AppError
	if !errors.As(err, &ae) || ae.Code != apperr.CodeStoreConstraint {
		t.Fatalf("他人绑定同一频道应返回 STORE_CONSTRAINT，得到 %v", err)
	}
}

func TestUpsertChannelBindingValidation(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if _, err := s.UpsertChannelBinding(ctx, ChannelBinding{ChannelID: -1001, BoundVia: BoundViaBot}); err == nil {
		t.Fatal("缺少 user_id 应报错")
	}
	mustUser(t, s, 100)
	if _, err := s.UpsertChannelBinding(ctx, ChannelBinding{ChannelID: -1001, UserID: 100, BoundVia: "magic"}); err == nil {
		t.Fatal("非法 bound_via 应报错")
	}
}

func TestListChannelBindingsByUserAndAll(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	mustUser(t, s, 100)
	mustUser(t, s, 200)

	for _, in := range []ChannelBinding{
		{ChannelID: -1001, UserID: 100, Username: "a", Title: "频道A", BoundVia: BoundViaBot},
		{ChannelID: -1002, UserID: 100, Title: "频道B", BoundVia: BoundViaWeb},
		{ChannelID: -1003, UserID: 200, Username: "c", Title: "频道C", BoundVia: BoundViaBot},
	} {
		if _, err := s.UpsertChannelBinding(ctx, in); err != nil {
			t.Fatalf("写入绑定 %d 失败: %v", in.ChannelID, err)
		}
	}

	mine, err := s.ListChannelBindingsByUser(ctx, 100)
	if err != nil {
		t.Fatalf("按用户列出失败: %v", err)
	}
	if len(mine) != 2 {
		t.Fatalf("用户 100 应有 2 条绑定，得到 %d", len(mine))
	}

	all, err := s.ListChannelBindingsWithUser(ctx)
	if err != nil {
		t.Fatalf("全量列出失败: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("全量应有 3 条绑定，得到 %d", len(all))
	}
	last := all[0]
	if last.UserID != 200 || last.UserDisplayName != "" {
		t.Fatalf("应按绑定时间倒序且带出用户资料: %+v", last)
	}

	// 用户有资料时 LEFT JOIN 应带出 username/display_name
	if _, err := s.CreateUser(ctx, User{ID: 300, Username: "user300", DisplayName: "用户300"}); err != nil {
		t.Fatalf("创建用户 300 失败: %v", err)
	}
	if _, err := s.UpsertChannelBinding(ctx, ChannelBinding{ChannelID: -1004,
		UserID: 300, Title: "频道D", BoundVia: BoundViaBot}); err != nil {
		t.Fatalf("写入绑定失败: %v", err)
	}
	all, err = s.ListChannelBindingsWithUser(ctx)
	if err != nil {
		t.Fatalf("全量列出失败: %v", err)
	}
	latest := all[0]
	if latest.ChannelID != -1004 || latest.UserUsername != "user300" || latest.UserDisplayName != "用户300" {
		t.Fatalf("JOIN 应带出用户资料: %+v", latest)
	}
}

func TestDeleteChannelBindingScope(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	mustUser(t, s, 100)
	mustUser(t, s, 200)

	if _, err := s.UpsertChannelBinding(ctx, ChannelBinding{ChannelID: -1001,
		UserID: 100, BoundVia: BoundViaBot}); err != nil {
		t.Fatalf("写入绑定失败: %v", err)
	}

	// 其他用户解绑自己的频道：找不到（不暴露他人绑定存在）
	if _, err := s.DeleteChannelBinding(ctx, -1001, 200); !errors.Is(err, ErrNotFound) {
		t.Fatalf("他人解绑应返回 ErrNotFound，得到 %v", err)
	}
	// 本人解绑成功，并返回被删记录
	b, err := s.DeleteChannelBinding(ctx, -1001, 100)
	if err != nil || b.ChannelID != -1001 {
		t.Fatalf("本人解绑失败: %v %+v", err, b)
	}
	// Web 管理端（ownerID=0）可删除任意绑定
	if _, err := s.UpsertChannelBinding(ctx, ChannelBinding{ChannelID: -1002,
		UserID: 100, BoundVia: BoundViaWeb}); err != nil {
		t.Fatalf("写入绑定失败: %v", err)
	}
	if _, err := s.DeleteChannelBinding(ctx, -1002, 0); err != nil {
		t.Fatalf("管理端解绑失败: %v", err)
	}
	// 再删一次应 ErrNotFound
	if _, err := s.DeleteChannelBinding(ctx, -1002, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("重复解绑应返回 ErrNotFound，得到 %v", err)
	}
}
