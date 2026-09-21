package access

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/store"
	"github.com/huaiminyetnotsleep/spore/internal/syscfg"
)

// 审批结果通知文案（经 Bot 主动私聊发送给申请用户）。
// 通过文案中的系统名称可配置（internal/syscfg，管理端修改即时生效）。
const rejectNotifyText = "很抱歉，你的申请暂未通过。如有疑问请联系管理员。"

func approveNotifyText(ctx context.Context, st *store.Store) string {
	return fmt.Sprintf("你的申请已通过，现在可以发送 Telegram 消息链接使用 %s 了。", syscfg.Name(ctx, st))
}

// Approve 批准用户为 enabled：更新状态、写审计，并经 Bot 通知申请用户；
// 通知发送失败不回滚审批，经 notified=false 告知调用方，
// 由 Web 页面提示管理员手动跟进（验收项）。actor 是操作来源标识
// （Web 登录管理员），写入审计。
func (s *Service) Approve(ctx context.Context, actor string, userID int64) (notified bool, err error) {
	return s.review(ctx, actor, userID, true)
}

// Reject 拒绝申请：用户置为 disabled（保留行与历史记录），并通知用户；
// 通知语义与 Approve 相同。
func (s *Service) Reject(ctx context.Context, actor string, userID int64) (notified bool, err error) {
	return s.review(ctx, actor, userID, false)
}

func (s *Service) review(ctx context.Context, actor string, userID int64, approve bool) (bool, error) {
	before, err := s.store.GetUser(ctx, userID)
	if err != nil {
		return false, err // 含 ErrNotFound，由调用方（Web）转"目标不存在"
	}
	next, action, text := store.UserDisabled, "user.reject", rejectNotifyText
	if approve {
		next, action, text = store.UserEnabled, "user.approve", approveNotifyText(ctx, s.store)
	}
	after, err := s.store.UpdateUserStatus(ctx, userID, next)
	if err != nil {
		return false, err
	}
	if err := s.store.AppendAudit(ctx, store.AuditEntry{
		Actor:      actor,
		Action:     action,
		Target:     fmt.Sprintf("user:%d", userID),
		BeforeJSON: mustJSON(map[string]string{"status": before.Status}),
		AfterJSON:  mustJSON(map[string]string{"status": after.Status}),
	}); err != nil {
		return false, err
	}
	snd := s.notifySender()
	if snd == nil {
		s.log.Warn("通知通道未注入，审批结果未送达", "user_id", userID)
		return false, nil
	}
	if _, err := snd.SendMessage(ctx, userID, text); err != nil {
		s.log.Warn("审批结果通知发送失败（不回滚状态）", "user_id", userID, "error", err.Error())
		return false, nil
	}
	return true, nil
}

// SetUserStatus 变更用户状态（Web 的启用/禁用/归档/恢复入口），写审计。
// 与审批（Approve/Reject）不同，本方法不向用户发通知（只要求审批结果通知）。
// 归档与恢复具有"删除用户"语义：立即失去权限、历史与统计保留。
// owner 身份不允许被停用或归档（防止误操作唯一管理员的 Telegram 身份）；
// 状态与目标一致时幂等返回（不写审计、不刷新 archived_at）。
func (s *Service) SetUserStatus(ctx context.Context, actor string, userID int64, status string) error {
	switch status {
	case store.UserEnabled, store.UserDisabled, store.UserArchived:
	default:
		return apperr.New(apperr.CodeInternal, fmt.Sprintf("非法用户状态 %q", status))
	}
	before, err := s.store.GetUser(ctx, userID)
	if err != nil {
		return err
	}
	if before.Status == status {
		return nil
	}
	if before.IsOwner && status != store.UserEnabled {
		return apperr.New(apperr.CodeStoreConstraint, "不能停用或归档 owner 身份，请先转移或取消 owner")
	}
	after, err := s.store.UpdateUserStatus(ctx, userID, status)
	if err != nil {
		return err
	}
	return s.store.AppendAudit(ctx, store.AuditEntry{
		Actor:      actor,
		Action:     "user.set_status",
		Target:     fmt.Sprintf("user:%d", userID),
		BeforeJSON: mustJSON(map[string]string{"status": before.Status}),
		AfterJSON:  mustJSON(map[string]string{"status": after.Status}),
	})
}

// SetOwner 设置或取消 owner 标记（全局唯一；设为 owner 会同时取消原 owner），写审计。
// 状态与目标一致时幂等返回。
func (s *Service) SetOwner(ctx context.Context, actor string, userID int64, owner bool) error {
	before, err := s.store.GetUser(ctx, userID)
	if err != nil {
		return err
	}
	if before.IsOwner == owner {
		return nil
	}
	if err := s.store.SetOwner(ctx, userID, owner); err != nil {
		return err
	}
	return s.store.AppendAudit(ctx, store.AuditEntry{
		Actor:      actor,
		Action:     "user.set_owner",
		Target:     fmt.Sprintf("user:%d", userID),
		BeforeJSON: mustJSON(map[string]bool{"is_owner": before.IsOwner}),
		AfterJSON:  mustJSON(map[string]bool{"is_owner": owner}),
	})
}

// AddUser 手动添加白名单用户（默认 enabled、限额取默认值），写审计。
// ID 非正或已存在时返回错误（重复经 STORE_CONSTRAINT 分类，Web 转友好提示）。
func (s *Service) AddUser(ctx context.Context, actor string, userID int64, note string) error {
	if userID <= 0 {
		return apperr.New(apperr.CodeInternal, "用户 ID 必须为正整数")
	}
	if _, err := s.store.CreateUser(ctx, store.User{
		ID:     userID,
		Status: store.UserEnabled,
		Note:   note,
	}); err != nil {
		return err
	}
	return s.store.AppendAudit(ctx, store.AuditEntry{
		Actor:     actor,
		Action:    "user.add",
		Target:    fmt.Sprintf("user:%d", userID),
		AfterJSON: mustJSON(map[string]string{"status": store.UserEnabled, "note": note}),
	})
}

// limitsSnapshot 是限额审计快照（仅业务字段）。
type limitsSnapshot struct {
	SubmitIntervalSec int `json:"submit_interval_sec"`
	DailyLimit        int `json:"daily_limit"`
	ConcurrentLimit   int `json:"concurrent_limit"`
	BindLimit         int `json:"bind_limit"` // 0 = 跟随角色默认
}

// SetUserLimits 调整单用户限额并写审计；传 0 的字段保持原值，即时生效
// （下一次 Submit 即按新值校验）。bindLimit 为 nil 表示不变更；0 表示
// 频道绑定数量上限恢复跟随角色默认，1–20 为显式值（合法性由调用方校验）。
func (s *Service) SetUserLimits(ctx context.Context, actor string, userID int64, intervalSec, daily, concurrent int, bindLimit *int) error {
	before, err := s.store.GetUser(ctx, userID)
	if err != nil {
		return err
	}
	if err := s.store.UpdateUserLimits(ctx, userID, intervalSec, daily, concurrent); err != nil {
		return err
	}
	if bindLimit != nil {
		if err := s.store.UpdateUserBindLimit(ctx, userID, *bindLimit); err != nil {
			return err
		}
	}
	after, err := s.store.GetUser(ctx, userID)
	if err != nil {
		return err
	}
	return s.store.AppendAudit(ctx, store.AuditEntry{
		Actor:      actor,
		Action:     "user.set_limits",
		Target:     fmt.Sprintf("user:%d", userID),
		BeforeJSON: mustJSON(limitsSnapshot{before.SubmitIntervalSec, before.DailyLimit, before.ConcurrentLimit, before.BindLimit}),
		AfterJSON:  mustJSON(limitsSnapshot{after.SubmitIntervalSec, after.DailyLimit, after.ConcurrentLimit, after.BindLimit}),
	})
}

// SetUserCloudDownload 调整单用户的云盘下载权限三态（0 = 跟随角色默认，
// 1 = 显式允许，2 = 显式拒绝）并写审计；即时生效（下一次 /download 预检与
// Submit 复核即按新值校验）。owner 无豁免：显式拒绝同样生效。全局开关
// （cloud-drive.json enabled）不受本设置影响，两者是 AND 关系。
func (s *Service) SetUserCloudDownload(ctx context.Context, actor string, userID int64, mode int) error {
	switch mode {
	case store.CloudDownloadDefault, store.CloudDownloadAllow, store.CloudDownloadDeny:
	default:
		return apperr.New(apperr.CodeInternal, fmt.Sprintf("非法云盘下载权限值 %d", mode))
	}
	before, err := s.store.GetUser(ctx, userID)
	if err != nil {
		return err
	}
	if before.CloudDownload == mode {
		return nil // 幂等：不写审计
	}
	if err := s.store.UpdateUserCloudDownload(ctx, userID, mode); err != nil {
		return err
	}
	return s.store.AppendAudit(ctx, store.AuditEntry{
		Actor:      actor,
		Action:     "user.set_cloud_download",
		Target:     fmt.Sprintf("user:%d", userID),
		BeforeJSON: mustJSON(map[string]int{"cloud_download": before.CloudDownload}),
		AfterJSON:  mustJSON(map[string]int{"cloud_download": mode}),
	})
}

// SetUserAutoPin 调整单用户的自动置顶偏好并写审计；即时生效（下一次普通
// 提交即默认标记置顶，/pin 单次指定不受影响；云盘/缓存补写任务不适用）。
// 幂等（状态一致不写审计）。
func (s *Service) SetUserAutoPin(ctx context.Context, actor string, userID int64, on bool) error {
	before, err := s.store.GetUser(ctx, userID)
	if err != nil {
		return err
	}
	if before.AutoPin == on {
		return nil // 幂等：不写审计
	}
	if err := s.store.UpdateUserAutoPin(ctx, userID, on); err != nil {
		return err
	}
	return s.store.AppendAudit(ctx, store.AuditEntry{
		Actor:      actor,
		Action:     "user.set_auto_pin",
		Target:     fmt.Sprintf("user:%d", userID),
		BeforeJSON: mustJSON(map[string]bool{"auto_pin": before.AutoPin}),
		AfterJSON:  mustJSON(map[string]bool{"auto_pin": on}),
	})
}

// ResetDailyUsage 置零用户当日（运营时区）已用额度并写审计；
// 仅影响当日分桶，限额上限不变。
func (s *Service) ResetDailyUsage(ctx context.Context, actor string, userID int64) error {
	rt := s.loadRuntime(ctx)
	day := s.now().In(rt.loc).Format(dayFormat)
	before, err := s.store.GetUsage(ctx, userID, day)
	if err != nil {
		return err
	}
	if err := s.store.ResetUsage(ctx, userID, day); err != nil {
		return err
	}
	return s.store.AppendAudit(ctx, store.AuditEntry{
		Actor:      actor,
		Action:     "user.reset_usage",
		Target:     fmt.Sprintf("user:%d", userID),
		BeforeJSON: mustJSON(map[string]any{"day": day, "used": before.Used}),
		AfterJSON:  mustJSON(map[string]any{"day": day, "used": 0}),
	})
}

// ImportLegacyWhitelist 兼容迁移：users 表为空且 env 白名单非空时，把
// ALLOWED_USER_IDS 导入为 enabled 用户；此后以数据库为准，env 变化不再生效
// 空库 + 空 env 时保持空库，Bot 对链接按未授权处理。
func ImportLegacyWhitelist(ctx context.Context, st *store.Store, ids map[int64]struct{}, log *slog.Logger) error {
	if len(ids) == 0 {
		return nil
	}
	users, err := st.ListUsers(ctx)
	if err != nil {
		return err
	}
	if len(users) > 0 {
		return nil // 已有用户：数据库接管白名单
	}
	for id := range ids {
		if _, err := st.CreateUser(ctx, store.User{ID: id, Status: store.UserEnabled}); err != nil {
			return err
		}
	}
	log.Info("已导入环境变量白名单为启用用户", "count", len(ids))
	return nil
}
