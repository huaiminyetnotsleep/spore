package watch

import (
	"context"
	"errors"
	"testing"

	"github.com/go-telegram/bot/models"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/mtproto"
	"github.com/huaiminyetnotsleep/spore/internal/store"
	"github.com/huaiminyetnotsleep/spore/internal/syscfg"
)

// setAdminBotAt 注入一个把目标解析为 channelID 且 bot 为管理员的假客户端
// （fakeBotClient 的 GetChat 固定返回构造时的 ID，按目标逐次注入）。
func setAdminBotAt(t *testing.T, svc *Service, botID, channelID int64) {
	t.Helper()
	svc.botMu.Lock()
	svc.botClients = []botClient{&fakeBotClient{
		id:     botID,
		chat:   &models.ChatFullInfo{ID: channelID, Type: models.ChatTypeChannel, Title: "目标"},
		member: models.ChatMember{Type: models.ChatMemberTypeAdministrator},
	}}
	svc.botMu.Unlock()
}

// fwdMustUser 建一个 enabled 用户（forward 测试专用；与 invite 测试的
// enabledUser 同语义，命名避让）。
func fwdMustUser(t *testing.T, svc *Service, id int64) {
	t.Helper()
	if _, err := svc.st.CreateUser(context.Background(), store.User{ID: id, Status: store.UserEnabled}); err != nil {
		t.Fatalf("建用户 %d 失败: %v", id, err)
	}
}

// fwdMustBinding 建一条归 userID、经 botID 路由的有效绑定。
func fwdMustBinding(t *testing.T, svc *Service, channelID, userID, botID int64) {
	t.Helper()
	if _, err := svc.st.UpsertChannelBinding(context.Background(), store.ChannelBinding{
		ChannelID: channelID, UserID: userID, BoundVia: store.BoundViaBot, BotID: botID,
	}); err != nil {
		t.Fatalf("建绑定 %d 失败: %v", channelID, err)
	}
}

// fwdInviteInfo 构造合法的邀请预检响应（私有频道）。
func fwdInviteInfo() mtproto.InviteInfo {
	return mtproto.InviteInfo{Title: "私有频道", Participants: 5, IsChannel: true}
}

// mustSource 写入一条归 userID 的用户监听源（受理 Bot=botID）。
func mustSource(t *testing.T, svc *Service, channelID, userID, botID int64) store.WatchSource {
	t.Helper()
	row, err := svc.st.UpsertWatchSource(context.Background(), store.WatchSource{
		ChannelID: channelID, Kind: "channel", Title: "源", Status: store.WatchApproved,
		Enabled: true, AddedBy: userID, BotID: botID, BotUsername: "bot",
	})
	if err != nil {
		t.Fatalf("写入监听源失败: %v", err)
	}
	return row
}

// TestSetForwardTargetValidations 覆盖设置专属目标的全部鉴权分支：本人
// 有效同路由绑定成功；他人源、他人绑定、软解绑、路由不匹配、循环目标
// （监听源/缓存频道）各自返回受控哨兵错误。
func TestSetForwardTargetValidations(t *testing.T) {
	svc, st := newService(t)
	ctx := context.Background()
	fwdMustUser(t, svc, 7)
	fwdMustUser(t, svc, 8)
	mustSource(t, svc, -1002001, 7, 42)

	// 本人同路由绑定 → 成功，视图 set
	fwdMustBinding(t, svc, -1003001, 7, 42)
	row, err := svc.SetForwardTarget(ctx, 7, -1002001, -1003001)
	if err != nil || row.ForwardBindingChannelID != -1003001 {
		t.Fatalf("本人有效绑定应设置成功: %+v err=%v", row, err)
	}
	if v := svc.ForwardTargetViewOf(ctx, row); v.State != ForwardTargetSet || v.ChannelID != -1003001 {
		t.Fatalf("设置后视图应为 set: %+v", v)
	}

	// 他人源 → ErrNotFound（不泄露存在性）
	mustSource(t, svc, -1002002, 8, 42)
	if _, err := svc.SetForwardTarget(ctx, 7, -1002002, -1003001); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("他人源应返回 ErrNotFound: %v", err)
	}
	// 管理员源（added_by=0）→ ErrNotFound
	mustSource(t, svc, -1002003, 0, 0)
	if _, err := svc.SetForwardTarget(ctx, 7, -1002003, -1003001); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("管理员源应返回 ErrNotFound: %v", err)
	}

	// 他人绑定 → ErrForwardTargetInvalid
	fwdMustBinding(t, svc, -1003002, 8, 42)
	if _, err := svc.SetForwardTarget(ctx, 7, -1002001, -1003002); !errors.Is(err, ErrForwardTargetInvalid) {
		t.Fatalf("他人绑定应拒绝: %v", err)
	}
	// 软解绑 → ErrForwardTargetInvalid
	fwdMustBinding(t, svc, -1003003, 7, 42)
	if _, _, err := st.MarkChannelBindingUnbound(ctx, -1003003, 7, store.UnbindReasonManual); err != nil {
		t.Fatalf("软解绑失败: %v", err)
	}
	if _, err := svc.SetForwardTarget(ctx, 7, -1002001, -1003003); !errors.Is(err, ErrForwardTargetInvalid) {
		t.Fatalf("软解绑绑定应拒绝: %v", err)
	}
	// 路由不匹配（绑定经其他 Bot 建立）→ ErrForwardRouteMismatch
	fwdMustBinding(t, svc, -1003004, 7, 99)
	if _, err := svc.SetForwardTarget(ctx, 7, -1002001, -1003004); !errors.Is(err, ErrForwardRouteMismatch) {
		t.Fatalf("路由不匹配应拒绝: %v", err)
	}
	// 源无受理 Bot（旧数据）→ 同样路由错误（引导重新 /watch）
	legacy := mustSource(t, svc, -1002004, 7, 0)
	_ = legacy
	if _, err := svc.SetForwardTarget(ctx, 7, -1002004, -1003001); !errors.Is(err, ErrForwardRouteMismatch) {
		t.Fatalf("无受理 Bot 的源应拒绝: %v", err)
	}

	// 目标是监听源 → ErrForwardTargetLoop（合法绑定 + 同时被加为监听源
	// 的脏数据形态：先建绑定再写入源行）
	fwdMustBinding(t, svc, -1003006, 7, 42)
	mustSource(t, svc, -1003006, 8, 42)
	if _, err := svc.SetForwardTarget(ctx, 7, -1002001, -1003006); !errors.Is(err, ErrForwardTargetLoop) {
		t.Fatalf("监听源作为目标应拒绝: %v", err)
	}
	// 目标是缓存频道 → ErrForwardTargetLoop（合法绑定 + 同时被配为缓存
	// 频道的脏数据形态）
	fwdMustBinding(t, svc, -1003005, 7, 42)
	if err := syscfg.SaveDumpChannels(ctx, st, []syscfg.DumpChannel{
		{ChannelID: -1003005, Title: "缓存频道", Enabled: true},
	}); err != nil {
		t.Fatalf("保存缓存频道失败: %v", err)
	}
	if _, err := svc.SetForwardTarget(ctx, 7, -1002001, -1003005); !errors.Is(err, ErrForwardTargetLoop) {
		t.Fatalf("缓存频道作为目标应拒绝: %v", err)
	}
	// 清空幂等成功
	row, err = svc.ClearForwardTarget(ctx, 7, -1002001)
	if err != nil || row.ForwardBindingChannelID != 0 {
		t.Fatalf("清空应成功: %+v err=%v", row, err)
	}
	if v := svc.ForwardTargetViewOf(ctx, row); v.State != ForwardTargetUnset {
		t.Fatalf("清空后视图应为 unset: %+v", v)
	}
}

// TestEligibleForwardTargets 候选列表只含本人 active 同路由绑定，且排除
// 监听源与缓存频道。
func TestEligibleForwardTargets(t *testing.T) {
	svc, _ := newService(t)
	ctx := context.Background()
	fwdMustUser(t, svc, 7)
	src := mustSource(t, svc, -1002001, 7, 42)

	fwdMustBinding(t, svc, -1003001, 7, 42) // 命中
	fwdMustBinding(t, svc, -1003002, 7, 99) // 路由不匹配
	fwdMustBinding(t, svc, -1002002, 7, 42) // 是监听源（下面写入）
	mustSource(t, svc, -1002002, 8, 42)
	fwdMustUser(t, svc, 8)
	fwdMustBinding(t, svc, -1003003, 8, 42) // 他人绑定（ListByUser 不返回）

	got, err := svc.EligibleForwardTargets(ctx, 7, src.ChannelID)
	if err != nil {
		t.Fatalf("查询候选失败: %v", err)
	}
	if len(got) != 1 || got[0].ChannelID != -1003001 {
		t.Fatalf("候选应只有同路由有效绑定: %+v", got)
	}
	if _, err := svc.EligibleForwardTargets(ctx, 8, src.ChannelID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("非申请人查询应 ErrNotFound: %v", err)
	}
}

// TestForwardTargetsPlan 投递计划解析：专属目标 + 条件全局镜像、运行时
// 失效停止专属副本（不回退全局）、循环运行时过滤、缓存/目标间去重。
func TestForwardTargetsPlan(t *testing.T) {
	svc, st := newService(t)
	ctx := context.Background()
	fwdMustUser(t, svc, 7)
	src := mustSource(t, svc, -1002001, 7, 42)
	fwdMustBinding(t, svc, -1003001, 7, 42)
	src, err := st.SetWatchSourceForwardBinding(ctx, src.ChannelID, -1003001)
	if err != nil {
		t.Fatalf("设置专属目标失败: %v", err)
	}

	// 无镜像：仅专属目标
	got := svc.ForwardTargets(ctx, src, nil)
	if len(got) != 1 || got[0].Kind != ForwardTargetUser || got[0].BotID != 42 || got[0].ChannelID != -1003001 {
		t.Fatalf("应仅含专属目标（精确 Bot 路由）: %+v", got)
	}
	// 开镜像：全局目标加入，与专属目标重合时去重（专属优先，仅一份）
	if err := syscfg.SaveWatchForwardChannels(ctx, st, []syscfg.WatchForwardChannel{
		{ChannelID: -1003001, Title: "重合目标"}, {ChannelID: -1004001, Title: "全局目标"},
	}); err != nil {
		t.Fatalf("保存全局转发频道失败: %v", err)
	}
	on, err := st.SetWatchSourceGlobalMirror(ctx, src.ChannelID, true)
	if err != nil {
		t.Fatalf("开启镜像失败: %v", err)
	}
	got = svc.ForwardTargets(ctx, on, nil)
	if len(got) != 2 {
		t.Fatalf("重合目标应去重为两份: %+v", got)
	}
	if got[0].Kind != ForwardTargetUser || got[1].Kind != ForwardTargetGlobal || got[1].ChannelID != -1004001 {
		t.Fatalf("专属目标应优先于全局重合项: %+v", got)
	}
	// 缓存频道去重：全局目标与缓存频道重合时不重复投递
	got = svc.ForwardTargets(ctx, on, []int64{-1004001})
	if len(got) != 1 || got[0].Kind != ForwardTargetUser {
		t.Fatalf("与缓存频道重合的全局目标应剔除: %+v", got)
	}
	// 全局目标是监听源 → 运行时过滤（脏数据兜底：绕过保存侧校验直写
	// settings，模拟历史脏数据/并发窗口）
	fwdMustUser(t, svc, 8)
	mustSource(t, svc, -1004002, 8, 42)
	if err := st.SetSetting(ctx, "watch_forward_channels",
		`[{"channel_id":-1004002,"title":"脏数据目标"}]`); err != nil {
		t.Fatalf("直写脏数据配置失败: %v", err)
	}
	got = svc.ForwardTargets(ctx, on, nil)
	if len(got) != 1 || got[0].Kind != ForwardTargetUser {
		t.Fatalf("作为监听源的全局目标应运行时过滤: %+v", got)
	}
	// 还原合法全局配置供后续断言
	if err := syscfg.SaveWatchForwardChannels(ctx, st, []syscfg.WatchForwardChannel{
		{ChannelID: -1004001, Title: "全局目标"},
	}); err != nil {
		t.Fatalf("还原全局转发频道失败: %v", err)
	}

	// 绑定软解绑：专属副本停止且不回退全局（先把镜像关回默认，隔离断言）
	if _, err := st.SetWatchSourceGlobalMirror(ctx, src.ChannelID, false); err != nil {
		t.Fatalf("关闭镜像失败: %v", err)
	}
	if _, _, err := st.MarkChannelBindingUnbound(ctx, -1003001, 7, store.UnbindReasonManual); err != nil {
		t.Fatalf("软解绑失败: %v", err)
	}
	stale, err := st.GetWatchSource(ctx, src.ChannelID)
	if err != nil {
		t.Fatal(err)
	}
	if v := svc.ForwardTargetViewOf(ctx, stale); v.State != ForwardTargetStale {
		t.Fatalf("软解绑后视图应为 stale: %+v", v)
	}
	if got := svc.ForwardTargets(ctx, stale, nil); len(got) != 0 {
		t.Fatalf("软解绑后不应有任何额外目标（不回退全局）: %+v", got)
	}
	// 同用户同路由重绑后恢复
	fwdMustBinding(t, svc, -1003001, 7, 42)
	got = svc.ForwardTargets(ctx, stale, nil)
	if len(got) != 1 || got[0].Kind != ForwardTargetUser {
		t.Fatalf("重绑后专属投递应恢复: %+v", got)
	}
}

// TestSourceChannelBlockedAsTarget 源创建侧循环防护：频道被选为专属目标或
// 配置为全局转发目标后，AdminAdd 拒绝其成为监听源；Submit 同一不变量经
// sourceChannelBlockedAsTarget 实现（此处直接断言检查函数两侧语义）。
func TestSourceChannelBlockedAsTarget(t *testing.T) {
	svc, st := newService(t)
	ctx := context.Background()
	fwdMustUser(t, svc, 7)
	mustSource(t, svc, -1002001, 7, 42)
	const fwdTarget = int64(-1001234567801)
	fwdMustBinding(t, svc, fwdTarget, 7, 42)
	if _, err := svc.SetForwardTarget(ctx, 7, -1002001, fwdTarget); err != nil {
		t.Fatalf("设置专属目标失败: %v", err)
	}

	// 已被选为专属目标的频道不能成为监听源（AdminAdd 路径）
	setAdminBotAt(t, svc, 42, fwdTarget)
	_, err := svc.AdminAdd(ctx, "admin", "-1001234567801", true)
	if !errors.Is(err, ErrForwardTargetConflict) {
		t.Fatalf("已被用作专属目标的频道应拒绝成为监听源: %v", err)
	}
	// 用户 /watch 同一不变量：SubmitOutcome 走 SubmitForwardConflict
	enableApply(t, st, true)
	out, err := svc.Submit(ctx, 7, false, "-1001234567801", 42, "bot42")
	if err != nil || out.Kind != SubmitForwardConflict {
		t.Fatalf("用户提交已被用作专属目标的频道应 SubmitForwardConflict: %+v err=%v", out, err)
	}
	// 全局转发目标同样拒绝；且 syscfg 保存侧拒绝写入监听源频道
	if err := syscfg.SaveWatchForwardChannels(ctx, st, []syscfg.WatchForwardChannel{
		{ChannelID: -1002001, Title: "冲突"},
	}); err == nil {
		t.Fatalf("syscfg 保存侧应拒绝监听源频道作为全局转发目标")
	}
	mustSource(t, svc, -1005001, 0, 0)
	if err := syscfg.SaveWatchForwardChannels(ctx, st, []syscfg.WatchForwardChannel{
		{ChannelID: -1005001, Title: "冲突"},
	}); err == nil {
		t.Fatalf("syscfg 保存侧应拒绝监听源频道作为全局转发目标")
	}
	const legalGlobal = int64(-1001234567802)
	if err := syscfg.SaveWatchForwardChannels(ctx, st, []syscfg.WatchForwardChannel{
		{ChannelID: legalGlobal, Title: "合法"},
	}); err != nil {
		t.Fatalf("合法频道应可保存: %v", err)
	}
	setAdminBotAt(t, svc, 42, legalGlobal)
	_, err = svc.AdminAdd(ctx, "admin", "-1001234567802", true)
	if !errors.Is(err, ErrForwardTargetConflict) {
		t.Fatalf("全局转发目标频道应拒绝成为监听源: %v", err)
	}
}

// TestInviteForwardTargetStagingAndTransfer 私有邀请「提交后立即选择」：
// 活动申请暂存 → 激活时复验转移 → 申请行清空；被拒终态清空暂存。
func TestInviteForwardTargetStagingAndTransfer(t *testing.T) {
	m := &fakeMembership{
		checkInfo: fwdInviteInfo(),
		joinID:    testBareID, joinTitle: "私有频道",
	}
	svc, st := newInviteService(t, m, adminChat(42), adminChat(9))
	enableApply(t, st, true)
	fwdMustUser(t, svc, 7)
	ctx := context.Background()

	// 受理 bot9 名下绑定 → 提交申请后立即暂存
	fwdMustBinding(t, svc, -1003001, 7, 9)
	out, err := svc.Submit(ctx, 7, false, testInviteLink, 9, "bot9")
	if err != nil || out.Kind != SubmitInvitePending {
		t.Fatalf("应转待审批: %+v err=%v", out, err)
	}
	inviteID := int64(1)
	staged, err := svc.StageInviteForwardTarget(ctx, 7, inviteID, -1003001)
	if err != nil || staged.ForwardBindingChannelID != -1003001 {
		t.Fatalf("暂存失败: %+v err=%v", staged, err)
	}
	// 他人申请上暂存 → ErrNotFound
	fwdMustUser(t, svc, 8)
	if _, err := svc.StageInviteForwardTarget(ctx, 8, inviteID, -1003001); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("他人申请应 ErrNotFound: %v", err)
	}
	// 路由不匹配（bot42 的绑定）→ 拒绝
	fwdMustBinding(t, svc, -1003002, 7, 42)
	if _, err := svc.StageInviteForwardTarget(ctx, 7, inviteID, -1003002); !errors.Is(err, ErrForwardRouteMismatch) {
		t.Fatalf("路由不匹配应拒绝: %v", err)
	}

	// 审批激活：暂存目标转移到源行，申请暂存清空
	req, src, err := svc.ApproveInviteRequest(ctx, "admin", inviteID)
	if err != nil || src == nil || req.Status != store.WatchInviteApproved {
		t.Fatalf("审批应激活: req=%+v src=%v err=%v", req, src, err)
	}
	if src.ForwardBindingChannelID != -1003001 {
		t.Fatalf("激活应转移暂存目标: %+v", src)
	}
	fresh, err := st.GetWatchInviteRequest(ctx, inviteID)
	if err != nil || fresh.ForwardBindingChannelID != 0 {
		t.Fatalf("激活后申请暂存应清空: %+v err=%v", fresh, err)
	}
	// 投递计划立即包含专属目标（受理 bot9 精确路由）
	plan := svc.ForwardTargets(ctx, *src, nil)
	if len(plan) != 1 || plan[0].Kind != ForwardTargetUser || plan[0].BotID != 9 {
		t.Fatalf("激活后计划应含专属目标: %+v", plan)
	}
}

// TestInviteForwardTargetRejectedClearsStaging 被拒终态不保留暂存。
func TestInviteForwardTargetRejectedClearsStaging(t *testing.T) {
	m := &fakeMembership{checkInfo: fwdInviteInfo()}
	svc, st := newInviteService(t, m)
	enableApply(t, st, true)
	fwdMustUser(t, svc, 7)
	ctx := context.Background()

	fwdMustBinding(t, svc, -1003001, 7, 9)
	if _, err := svc.Submit(ctx, 7, false, testInviteLink, 9, "bot9"); err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	inviteID := int64(1)
	if _, err := svc.StageInviteForwardTarget(ctx, 7, inviteID, -1003001); err != nil {
		t.Fatalf("暂存失败: %v", err)
	}
	if _, err := svc.RejectInviteRequest(ctx, "admin", inviteID); err != nil {
		t.Fatalf("拒绝失败: %v", err)
	}
	fresh, err := st.GetWatchInviteRequest(ctx, inviteID)
	if err != nil || fresh.ForwardBindingChannelID != 0 {
		t.Fatalf("被拒后暂存应清空: %+v err=%v", fresh, err)
	}
	// 终态申请上暂存 → 受控拒绝
	if _, err := svc.StageInviteForwardTarget(ctx, 7, inviteID, -1003001); apperr.From(err).Code != apperr.CodeStoreConstraint {
		t.Fatalf("终态申请应拒绝暂存: %v", err)
	}
}

// TestSetGlobalMirrorService 管理员镜像开关服务层透传。
func TestSetGlobalMirrorService(t *testing.T) {
	svc, st := newService(t)
	ctx := context.Background()
	fwdMustUser(t, svc, 7)
	src := mustSource(t, svc, -1002001, 7, 42)
	if src.GlobalMirrorEnabled {
		t.Fatalf("用户源默认关闭镜像: %+v", src)
	}
	row, err := svc.SetGlobalMirror(ctx, src.ChannelID, true)
	if err != nil || !row.GlobalMirrorEnabled {
		t.Fatalf("开启镜像失败: %+v err=%v", row, err)
	}
	// 重复 /watch 不重置开关
	if _, err := svc.st.UpsertWatchSource(ctx, store.WatchSource{
		ChannelID: src.ChannelID, Kind: "channel", Status: store.WatchApproved,
		Enabled: true, AddedBy: 7, BotID: 42,
	}); err != nil {
		t.Fatalf("重复提交失败: %v", err)
	}
	fresh, err := st.GetWatchSource(ctx, src.ChannelID)
	if err != nil || !fresh.GlobalMirrorEnabled {
		t.Fatalf("重复 /watch 不应重置镜像开关: %+v err=%v", fresh, err)
	}
}

// TestSubmitInviteOutcomeCarriesRequestID 邀请提交结果带申请 ID（Bot 端
// 「提交后立即选择」据此定位暂存目标）；他人占用与他人结果不带。
func TestSubmitInviteOutcomeCarriesRequestID(t *testing.T) {
	m := &fakeMembership{checkInfo: fwdInviteInfo()}
	svc, st := newInviteService(t, m)
	enableApply(t, st, true)
	fwdMustUser(t, svc, 7)
	ctx := context.Background()

	out, err := svc.Submit(ctx, 7, false, testInviteLink, 9, "bot9")
	if err != nil || out.Kind != SubmitInvitePending {
		t.Fatalf("应转待审批: %+v err=%v", out, err)
	}
	if out.RequestID == 0 {
		t.Fatalf("待审批结果应带申请 ID: %+v", out)
	}
	// 幂等重发同一邀请：仍带同一申请 ID
	again, err := svc.Submit(ctx, 7, false, testInviteLink, 9, "bot9")
	if err != nil || again.RequestID != out.RequestID {
		t.Fatalf("幂等重发应带同一申请 ID: %+v err=%v", again, err)
	}
	// 他人提交同一邀请：AlreadyOthers，不带申请 ID（不泄露可操作上下文）
	fwdMustUser(t, svc, 8)
	others, err := svc.Submit(ctx, 8, false, testInviteLink, 9, "bot9")
	if err != nil || others.Kind != SubmitAlreadyOthers || others.RequestID != 0 {
		t.Fatalf("他人提交应 AlreadyOthers 且无申请 ID: %+v err=%v", others, err)
	}
}

// TestEligibleForwardTargetsForInvite 活动申请候选查询：同路由过滤、循环
// 目标剔除、归属与终态防护。
func TestEligibleForwardTargetsForInvite(t *testing.T) {
	m := &fakeMembership{checkInfo: fwdInviteInfo()}
	svc, st := newInviteService(t, m, adminChat(42), adminChat(9))
	enableApply(t, st, true)
	fwdMustUser(t, svc, 7)
	ctx := context.Background()

	fwdMustBinding(t, svc, -1003001, 7, 9)  // 同路由：候选
	fwdMustBinding(t, svc, -1003002, 7, 42) // 路由不符：剔除
	fwdMustBinding(t, svc, -1003101, 7, 9)  // 同路由但已被占用为监听源：循环剔除
	const loopSrc = -1003101
	if _, err := svc.st.UpsertWatchSource(ctx, store.WatchSource{
		ChannelID: loopSrc, Kind: "channel", Title: "已是源", Status: store.WatchApproved,
		Enabled: true, AddedBy: 0, BotID: 42,
	}); err != nil {
		t.Fatalf("写入占用源失败: %v", err)
	}
	out, err := svc.Submit(ctx, 7, false, testInviteLink, 9, "bot9")
	if err != nil || out.RequestID == 0 {
		t.Fatalf("提交失败: %+v err=%v", out, err)
	}
	candidates, err := svc.EligibleForwardTargetsForInvite(ctx, 7, out.RequestID)
	if err != nil {
		t.Fatalf("候选查询失败: %v", err)
	}
	if len(candidates) != 1 || candidates[0].ChannelID != -1003001 {
		t.Fatalf("候选应只含同路由且非循环目标: %+v", candidates)
	}
	// 非申请人查询 → ErrNotFound
	fwdMustUser(t, svc, 8)
	if _, err := svc.EligibleForwardTargetsForInvite(ctx, 8, out.RequestID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("非申请人应 ErrNotFound: %v", err)
	}
	// 终态申请 → ErrNotFound
	if _, err := svc.RejectInviteRequest(ctx, "admin", out.RequestID); err != nil {
		t.Fatalf("拒绝失败: %v", err)
	}
	if _, err := svc.EligibleForwardTargetsForInvite(ctx, 7, out.RequestID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("终态申请应 ErrNotFound: %v", err)
	}
}

// TestUserSourceByTarget /watchtarget 的目标解析：本人源命中，他人源/
// 管理员源一律 ErrNotFound（不泄露存在性）。
func TestUserSourceByTarget(t *testing.T) {
	svc, _ := newService(t)
	fwdMustUser(t, svc, 7)
	fwdMustUser(t, svc, 8)
	src := mustSource(t, svc, -1001234567801, 7, 42)
	ctx := context.Background()

	row, err := svc.UserSourceByTarget(ctx, 7, "-1001234567801")
	if err != nil || row.ChannelID != src.ChannelID {
		t.Fatalf("本人源应命中: %+v err=%v", row, err)
	}
	if _, err := svc.UserSourceByTarget(ctx, 8, "-1001234567801"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("他人源应 ErrNotFound: %v", err)
	}
	// 管理员源（added_by=0）同样按不存在处理
	adminSrc := mustSource(t, svc, -1001234567802, 0, 42)
	if _, err := svc.UserSourceByTarget(ctx, 8, "-1001234567802"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("管理员源应 ErrNotFound: %+v", adminSrc)
	}
	// 不存在的源 → ErrNotFound
	if _, err := svc.UserSourceByTarget(ctx, 7, "-1001234567899"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("不存在源应 ErrNotFound: %v", err)
	}
}
