package web

// 用户管理的共享业务核心：手动添加、资料刷新、owner 设置与限额校验边界。
// 当前 SSR 用户页面与表单已删除，本文件只保留 API 写 handler
// 与只读 DTO（api_users.go）仍在使用的核心函数。

import (
	"context"
	"strconv"
	"strings"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// addUserCore 手动添加用户（SPA API 核心）：ID 必须为正、
// 备注截断到 500 字符（按字节截断）；重复添加经
// STORE_CONSTRAINT 分类，由调用方转为各自形态的"已存在"提示。
// 依赖 access 服务，由调用方先行判空。
func (s *Server) addUserCore(ctx context.Context, id int64, note string) error {
	if id <= 0 {
		return apperr.New(apperr.CodeInternal, "用户 ID 必须为正整数")
	}
	if len(note) > 500 {
		note = note[:500]
	}
	return s.access.AddUser(ctx, "admin", id, note)
}

// profileRefreshOutcome 分类资料刷新结果（SPA API JSON 共用语义）。
type profileRefreshOutcome struct {
	OK     bool
	Reason string // no_context | lookup_failed（OK 时为空）
	Err    error  // 资料查询成功但落库失败时携带（调用方走通用 500/503 链路）
}

// refreshUserProfileCore 从当前可用 Telegram 上下文刷新资料（SPA API 核心）：
// 查询失败不覆盖旧资料，只写脱敏审计
// （reason=telegram_unavailable，不含 Telegram 原始错误）。
func (s *Server) refreshUserProfileCore(ctx context.Context, id int64) profileRefreshOutcome {
	if s.profile == nil {
		return profileRefreshOutcome{Reason: "no_context"}
	}
	profile, err := s.profile.LookupUserProfile(ctx, id)
	if err != nil || profile.ID != id || store.ValidateUserProfile(profile) != nil {
		s.log.Warn("刷新用户资料失败", "user_id", id)
		s.audit(ctx, "user.profile_refresh_failed", "user:"+strconv.FormatInt(id, 10), map[string]any{"reason": "telegram_unavailable"})
		return profileRefreshOutcome{Reason: "lookup_failed"}
	}
	if err := s.st.UpdateUserProfile(ctx, id, profile.Username, profile.DisplayName); err != nil {
		return profileRefreshOutcome{Err: err}
	}
	s.audit(ctx, "user.profile_refresh", "user:"+strconv.FormatInt(id, 10), map[string]any{"updated": true})
	return profileRefreshOutcome{OK: true}
}

// deniedText 把拒绝原因码转为中文标签（users.last_denied_reason 存 apperr 码）。
func deniedText(code string) string {
	if code == "" {
		return ""
	}
	return string(code) + "：" + apperr.UserText(apperr.Code(code))
}

// setOwnerCore 设置或取消 owner（SPA API 核心）。
// owner 是事件通知的 Telegram 管理员目标；设为 owner 后立即尝试补发
// 之前因无管理员身份而暂存的未通知事件。
func (s *Server) setOwnerCore(ctx context.Context, id int64, owner bool) error {
	if err := s.access.SetOwner(ctx, "admin", id, owner); err != nil {
		return err
	}
	if owner && s.hub != nil {
		s.hub.FlushPending(ctx)
	}
	return nil
}

// userLimitRule 是单个限额项的展示名与上限（限额写入的单一校验来源）。
type userLimitRule struct {
	Name string
	Max  int
}

// userLimitRules 是三项限额的校验边界（与前端提示一致）：
// 提交间隔 ≤86400 秒、每日额度 ≤100000、并发上限 ≤100。
// access.SetUserLimits 不做数值复核，本边界是写入口的唯一校验点。
var userLimitRules = []userLimitRule{
	{"提交间隔", 86400}, {"每日额度", 100000}, {"并发上限", 100},
}

// userBindLimitMax 是用户级频道绑定上限的显式值上界（0 = 跟随角色默认）。
const userBindLimitMax = 20

// userMatches 判断用户是否命中搜索词（ID 精确/前缀、用户名、显示名、备注）。
func userMatches(u store.User, q string) bool {
	ql := strings.ToLower(q)
	if id, err := strconv.ParseInt(ql, 10, 64); err == nil && u.ID == id {
		return true
	}
	if idStr := strconv.FormatInt(u.ID, 10); strings.HasPrefix(idStr, ql) {
		return true
	}
	return strings.Contains(strings.ToLower(u.Username), ql) ||
		strings.Contains(strings.ToLower(u.DisplayName), ql) ||
		strings.Contains(strings.ToLower(u.Note), ql)
}

// userOpText 把管理操作错误码转为简短中文提示。
func userOpText(ae *apperr.AppError) string {
	switch ae.Code {
	case apperr.CodeStoreConstraint:
		return "操作与现有数据冲突（如停用 owner 身份或重复添加）"
	case apperr.CodeStoreUnavailable:
		return "存储暂时不可用，请稍后重试"
	default:
		return "操作未能完成，请稍后重试"
	}
}
