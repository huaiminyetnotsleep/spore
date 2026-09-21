package watch

import (
	"context"
	"errors"
	"testing"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/mtproto"
	"github.com/huaiminyetnotsleep/spore/internal/store"
	"github.com/huaiminyetnotsleep/spore/internal/syscfg"
)

const (
	testInviteHash = "AbCdEfGh12345678"
	testInviteLink = "https://t.me/+" + testInviteHash
	testBareID     = int64(1234567890)
	testBotAPIID   = -1001234567890
)

// fakeMembership 记录调用并按配置返回（私有邀请状态机路径）。
type fakeMembership struct {
	checkInfo   mtproto.InviteInfo
	checkErr    error
	joinErr     error
	joinID      int64
	joinTitle   string
	joinAlready bool
	checkCalls  int
	joinCalls   int
}

func (f *fakeMembership) CheckInvite(context.Context, string) (mtproto.InviteInfo, error) {
	f.checkCalls++
	return f.checkInfo, f.checkErr
}

func (f *fakeMembership) JoinInvite(context.Context, string, mtproto.JoinOptions) (int64, string, bool, error) {
	f.joinCalls++
	if f.joinErr != nil {
		return 0, "", false, f.joinErr
	}
	return f.joinID, f.joinTitle, f.joinAlready, nil
}

// fakeBotClient 记录校验调用并按配置返回（Bot 管理员校验路径）。
type fakeBotClient struct {
	id        int64
	chat      *models.ChatFullInfo
	chatErr   error
	member    models.ChatMember
	memberErr error
}

func (f *fakeBotClient) ID() int64 { return f.id }

func (f *fakeBotClient) GetChat(context.Context, *tgbot.GetChatParams) (*models.ChatFullInfo, error) {
	if f.chatErr != nil {
		return nil, f.chatErr
	}
	return f.chat, nil
}

func (f *fakeBotClient) GetChatMember(context.Context, *tgbot.GetChatMemberParams) (*models.ChatMember, error) {
	if f.memberErr != nil {
		return nil, f.memberErr
	}
	m := f.member
	return &m, nil
}

// adminChat 构造一个 bot 为管理员的频道 GetChat/GetChatMember 响应。
func adminChat(botID int64) *fakeBotClient {
	return &fakeBotClient{
		id: botID,
		chat: &models.ChatFullInfo{
			ID: testBotAPIID, Type: models.ChatTypeChannel, Title: "私有频道", Username: "privchan",
		},
		member: models.ChatMember{Type: models.ChatMemberTypeAdministrator},
	}
}

func newInviteService(t *testing.T, m *fakeMembership, bots ...botClient) (*Service, *store.Store) {
	t.Helper()
	svc, st := newService(t)
	svc.member = m
	svc.botMu.Lock()
	for _, b := range bots {
		svc.botClients = append(svc.botClients, b)
	}
	svc.botMu.Unlock()
	return svc, st
}

func enableApply(t *testing.T, st *store.Store, requireApproval bool) {
	t.Helper()
	if err := syscfg.SaveWatchConfig(context.Background(), st, syscfg.WatchConfig{
		ApplyEnabled: true, RequireApproval: requireApproval, MaxSources: 20, PerUserLimit: 3,
	}); err != nil {
		t.Fatalf("保存监听配置失败: %v", err)
	}
}

func enabledUser(t *testing.T, st *store.Store, id int64) {
	t.Helper()
	if _, err := st.CreateUser(context.Background(), store.User{ID: id, Status: store.UserEnabled}); err != nil {
		t.Fatalf("建用户失败: %v", err)
	}
}

// 管理员邀请添加的完整即时链路：读取账号加入 → ID 转换 → Bot 校验通过 →
// 源激活 + 申请 approved + hash 清理 + joined_channels 留痕。
func TestAdminAddInviteImmediateActivation(t *testing.T) {
	m := &fakeMembership{
		checkInfo: mtproto.InviteInfo{Title: "私有频道", Participants: 12, IsChannel: true},
		joinID:    testBareID, joinTitle: "私有频道",
	}
	svc, st := newInviteService(t, m, adminChat(42))

	out, err := svc.AdminAdd(context.Background(), "admin", testInviteLink, true)
	if err != nil {
		t.Fatalf("管理员邀请添加失败: %v", err)
	}
	if out.Source == nil || out.InviteRequest == nil {
		t.Fatalf("应同时返回源与申请: %+v", out)
	}
	if out.Source.ChannelID != testBotAPIID {
		t.Errorf("源 channel_id 应为 Bot API -100 形态: %d", out.Source.ChannelID)
	}
	if out.InviteRequest.Status != store.WatchInviteApproved || out.InviteRequest.InviteHash != "" {
		t.Errorf("申请应 approved 且 hash 已清理: %+v", out.InviteRequest)
	}
	if out.InviteRequest.Participants != 12 || out.InviteRequest.MaskedHash == "" {
		t.Errorf("申请快照/脱敏 hash 缺失: %+v", out.InviteRequest)
	}
	if m.joinCalls != 1 {
		t.Errorf("应恰好加入一次: %d", m.joinCalls)
	}
	// 留痕：MTProto 裸正 ID + watch_source 来源
	recs, err := st.ListActiveJoinedChannels(context.Background())
	if err != nil || len(recs) != 1 || recs[0].ChannelID != testBareID ||
		recs[0].JoinedVia != store.JoinedViaWatchSource {
		t.Errorf("应写入 watch_source 加入留痕: %+v err=%v", recs, err)
	}
	// 正式源就位（listener 生效条件）
	if _, err := st.GetWatchSource(context.Background(), testBotAPIID); err != nil {
		t.Errorf("正式监听源未创建: %v", err)
	}
}

// 管理员重复添加同一邀请：幂等返回现有申请，不重复加入。
func TestAdminAddInviteIdempotent(t *testing.T) {
	m := &fakeMembership{
		checkInfo: mtproto.InviteInfo{Title: "私有频道", IsChannel: true},
		joinID:    testBareID, joinTitle: "私有频道",
	}
	svc, _ := newInviteService(t, m, adminChat(42))
	ctx := context.Background()
	if _, err := svc.AdminAdd(ctx, "admin", testInviteLink, true); err != nil {
		t.Fatalf("首次添加失败: %v", err)
	}
	// 激活后 hash 已清理，无法按 hash 查重；模拟重复添加时读取账号已加入
	//（CheckInvite 回带频道 ID），应按既有源幂等返回、不再加入
	m.checkInfo.AlreadyJoined = true
	m.checkInfo.ChannelID = testBareID
	out, err := svc.AdminAdd(ctx, "admin", testInviteLink, true)
	if err != nil {
		t.Fatalf("重复添加失败: %v", err)
	}
	if m.joinCalls != 1 || out.Source == nil || out.Source.ChannelID != testBotAPIID {
		t.Errorf("重复添加应按既有源幂等返回: join=%d %+v", m.joinCalls, out)
	}
}

// 请求制邀请：进入 waiting_telegram，重试（模拟频道批准后）自动激活。
func TestInviteWaitingTelegramRetryActivates(t *testing.T) {
	m := &fakeMembership{
		checkInfo: mtproto.InviteInfo{Title: "私有频道", IsChannel: true, RequestedToJoin: true},
		joinErr:   mtproto.ErrJoinRequestSent,
	}
	svc, _ := newInviteService(t, m, adminChat(42))
	ctx := context.Background()

	out, err := svc.AdminAdd(ctx, "admin", testInviteLink, true)
	if err != nil {
		t.Fatalf("添加失败: %v", err)
	}
	req := out.InviteRequest
	if req.Status != store.WatchInviteWaitingTelegram || req.InviteHash == "" {
		t.Fatalf("应转 waiting_telegram 且保留 hash: %+v", req)
	}
	if out.Source != nil {
		t.Fatalf("未激活不应返回源: %+v", out.Source)
	}

	// 模拟频道侧批准：再次 JoinInvite 成功
	m.joinErr = nil
	m.joinID = testBareID
	m.joinTitle = "私有频道"
	got, src, err := svc.RetryInviteRequest(ctx, req.ID)
	if err != nil || src == nil || got.Status != store.WatchInviteApproved {
		t.Fatalf("重试应激活: req=%+v src=%v err=%v", got, src, err)
	}
	if got.InviteHash != "" {
		t.Errorf("激活后 hash 应清理: %+v", got)
	}
}

// 读取账号已加入但 Bot 非管理员：waiting_bot；权限配置后重试激活。
func TestInviteWaitingBotRetryActivates(t *testing.T) {
	m := &fakeMembership{
		checkInfo: mtproto.InviteInfo{Title: "私有频道", IsChannel: true},
		joinID:    testBareID, joinTitle: "私有频道",
	}
	notAdmin := &fakeBotClient{
		id: 42,
		chat: &models.ChatFullInfo{
			ID: testBotAPIID, Type: models.ChatTypeChannel, Title: "私有频道",
		},
		member: models.ChatMember{Type: models.ChatMemberTypeMember},
	}
	svc, _ := newInviteService(t, m, notAdmin)
	ctx := context.Background()

	out, err := svc.AdminAdd(ctx, "admin", testInviteLink, true)
	if err != nil {
		t.Fatalf("添加失败: %v", err)
	}
	req := out.InviteRequest
	if req.Status != store.WatchInviteWaitingBot || req.ChannelID != testBotAPIID {
		t.Fatalf("应转 waiting_bot 并落频道 ID: %+v", req)
	}
	if req.InviteHash != "" {
		t.Errorf("频道已定位，hash 应清理: %+v", req)
	}

	// 模拟管理员已把 Bot 设为管理员
	admin := adminChat(42)
	svc.botMu.Lock()
	svc.botClients = []botClient{admin}
	svc.botMu.Unlock()
	got, src, err := svc.RetryInviteRequest(ctx, req.ID)
	if err != nil || src == nil || got.Status != store.WatchInviteApproved {
		t.Fatalf("重试应激活: req=%+v src=%v err=%v", got, src, err)
	}
}

// waiting_bot 申请由周期对账自动推进（无需人工重试）。
func TestReconcileAdvancesWaitingBot(t *testing.T) {
	m := &fakeMembership{
		checkInfo: mtproto.InviteInfo{Title: "私有频道", IsChannel: true},
		joinID:    testBareID, joinTitle: "私有频道",
	}
	notAdmin := &fakeBotClient{
		id: 42,
		chat: &models.ChatFullInfo{
			ID: testBotAPIID, Type: models.ChatTypeChannel, Title: "私有频道",
		},
		member: models.ChatMember{Type: models.ChatMemberTypeMember},
	}
	svc, _ := newInviteService(t, m, notAdmin)
	ctx := context.Background()
	out, err := svc.AdminAdd(ctx, "admin", testInviteLink, true)
	if err != nil {
		t.Fatalf("添加失败: %v", err)
	}

	svc.botMu.Lock()
	svc.botClients = []botClient{adminChat(42)}
	svc.botMu.Unlock()
	if err := svc.ReconcileInviteRequests(ctx); err != nil {
		t.Fatalf("对账失败: %v", err)
	}
	req, err := svc.st.GetWatchInviteRequest(ctx, out.InviteRequest.ID)
	if err != nil || req.Status != store.WatchInviteApproved {
		t.Fatalf("对账应激活 waiting_bot 申请: %+v err=%v", req, err)
	}
}

// 普通用户申请：需审批时绝不产生加入副作用；审批后才加入并激活。
func TestUserInvitePendingNoJoinUntilApprove(t *testing.T) {
	m := &fakeMembership{
		checkInfo: mtproto.InviteInfo{Title: "私有频道", Participants: 5, IsChannel: true},
		joinID:    testBareID, joinTitle: "私有频道",
	}
	svc, st := newInviteService(t, m, adminChat(42))
	enableApply(t, st, true)
	enabledUser(t, st, 7)
	ctx := context.Background()

	out, err := svc.Submit(ctx, 7, false, testInviteLink, 9, "bot9")
	if err != nil || out.Kind != SubmitInvitePending {
		t.Fatalf("应转待审批: %+v err=%v", out, err)
	}
	if m.joinCalls != 0 {
		t.Fatalf("审批前不得加入（无外部副作用）: %d", m.joinCalls)
	}

	req, src, err := svc.ApproveInviteRequest(ctx, "admin", 1)
	if err != nil || src == nil || req.Status != store.WatchInviteApproved {
		t.Fatalf("审批应激活: req=%+v src=%v err=%v", req, src, err)
	}
	if m.joinCalls != 1 {
		t.Errorf("审批后应加入一次: %d", m.joinCalls)
	}
	// 源归属申请人 + 受理 bot 快照
	row, err := st.GetWatchSource(ctx, testBotAPIID)
	if err != nil || row.AddedBy != 7 || row.BotID != 9 || row.BotUsername != "bot9" {
		t.Errorf("源归属/快照不符: %+v err=%v", row, err)
	}
}

// 同一邀请重复提交：本人幂等回当前状态，他人视为已占用。
func TestUserInviteDedup(t *testing.T) {
	m := &fakeMembership{
		checkInfo: mtproto.InviteInfo{Title: "私有频道", IsChannel: true},
		joinID:    testBareID, joinTitle: "私有频道",
	}
	svc, st := newInviteService(t, m, adminChat(42))
	enableApply(t, st, true)
	enabledUser(t, st, 7)
	enabledUser(t, st, 8)
	ctx := context.Background()

	if out, err := svc.Submit(ctx, 7, false, testInviteLink, 0, ""); err != nil || out.Kind != SubmitInvitePending {
		t.Fatalf("首提交应待审批: %+v err=%v", out, err)
	}
	out, err := svc.Submit(ctx, 7, false, testInviteLink, 0, "")
	if err != nil || out.Kind != SubmitInvitePending {
		t.Fatalf("本人重复提交应幂等: %+v err=%v", out, err)
	}
	out, err = svc.Submit(ctx, 8, false, testInviteLink, 0, "")
	if err != nil || out.Kind != SubmitAlreadyOthers {
		t.Fatalf("他人提交应拒绝: %+v err=%v", out, err)
	}
}

// 无效邀请：用户提交返回受控 outcome；管理员添加返回 400 类业务错误。
func TestInviteInvalid(t *testing.T) {
	m := &fakeMembership{checkErr: apperr.New(apperr.CodeInvalidInviteURL, "expired")}
	svc, st := newInviteService(t, m, adminChat(42))
	enableApply(t, st, true)
	enabledUser(t, st, 7)
	ctx := context.Background()

	out, err := svc.Submit(ctx, 7, false, testInviteLink, 0, "")
	if err != nil || out.Kind != SubmitInviteInvalid {
		t.Fatalf("用户路径应返回受控无效: %+v err=%v", out, err)
	}
	_, err = svc.AdminAdd(ctx, "admin", testInviteLink, true)
	if apperr.From(err).Code != apperr.CodeInvalidInviteURL {
		t.Fatalf("管理路径应返回 INVALID_INVITE_URL: %v", err)
	}
}

// 普通群组邀请不支持。
func TestInviteOrdinaryGroupRejected(t *testing.T) {
	m := &fakeMembership{checkInfo: mtproto.InviteInfo{Title: "普通群", IsChannel: false}}
	svc, _ := newInviteService(t, m, adminChat(42))
	ctx := context.Background()
	out, err := svc.AdminAdd(ctx, "admin", testInviteLink, true)
	if err == nil || out.Source != nil {
		t.Fatalf("普通群组应拒绝: %+v err=%v", out, err)
	}
}

// 读取账号离线：用户提交回受控 outcome；管理添加透传离线错误（Web 映射 503）。
func TestInviteReaderUnavailable(t *testing.T) {
	m := &fakeMembership{checkErr: mtproto.ErrMembershipUnavailable}
	svc, st := newInviteService(t, m, adminChat(42))
	enableApply(t, st, true)
	enabledUser(t, st, 7)
	ctx := context.Background()

	out, err := svc.Submit(ctx, 7, false, testInviteLink, 0, "")
	if err != nil || out.Kind != SubmitReaderUnavailable {
		t.Fatalf("应返回读取账号不可用: %+v err=%v", out, err)
	}
	if _, err := svc.AdminAdd(ctx, "admin", testInviteLink, true); !errors.Is(err, mtproto.ErrMembershipUnavailable) {
		t.Fatalf("管理路径应透传离线错误: %v", err)
	}
}

// 加入永久失败：申请转 failed 且保留 hash 供重试；修复后重试可激活。
func TestInviteFailedThenRetry(t *testing.T) {
	m := &fakeMembership{
		checkInfo: mtproto.InviteInfo{Title: "私有频道", IsChannel: true},
		joinErr:   apperr.New(apperr.CodeInvalidInviteURL, "expired"),
	}
	svc, _ := newInviteService(t, m, adminChat(42))
	ctx := context.Background()

	out, err := svc.AdminAdd(ctx, "admin", testInviteLink, true)
	if err != nil {
		t.Fatalf("添加失败: %v", err)
	}
	req := out.InviteRequest
	if req.Status != store.WatchInviteFailed || req.InviteHash == "" {
		t.Fatalf("应 failed 且保留 hash: %+v", req)
	}
	if req.Note == "" {
		t.Errorf("failed 应带受控备注: %+v", req)
	}

	m.joinErr = nil
	m.joinID = testBareID
	m.joinTitle = "私有频道"
	if _, _, err := svc.RetryInviteRequest(ctx, req.ID); err != nil {
		t.Fatalf("重试失败: %v", err)
	}
}

// 频道冲突：他人已有监听源时用户申请激活失败（终态 failed + 备注）。
func TestUserInviteConflictFails(t *testing.T) {
	m := &fakeMembership{
		checkInfo: mtproto.InviteInfo{Title: "私有频道", IsChannel: true},
		joinID:    testBareID, joinTitle: "私有频道",
	}
	svc, st := newInviteService(t, m, adminChat(42))
	enableApply(t, st, true)
	enabledUser(t, st, 7)
	enabledUser(t, st, 8)
	ctx := context.Background()
	// 他人已有正式源
	if _, err := st.UpsertWatchSource(ctx, store.WatchSource{
		ChannelID: testBotAPIID, Status: store.WatchApproved, Enabled: true, AddedBy: 99,
	}); err != nil {
		t.Fatalf("预置源失败: %v", err)
	}
	if out, err := svc.Submit(ctx, 7, false, testInviteLink, 0, ""); err != nil || out.Kind != SubmitInvitePending {
		t.Fatalf("提交失败: %+v err=%v", out, err)
	}
	active, err := st.ListActiveWatchInviteRequests(ctx)
	if err != nil || len(active) != 1 {
		t.Fatalf("活动申请应存在: %+v err=%v", active, err)
	}
	id := active[0].ID

	if _, _, err := svc.ApproveInviteRequest(ctx, "admin", id); err != nil {
		t.Fatalf("审批执行失败: %v", err)
	}
	req, err := st.GetWatchInviteRequest(ctx, id)
	if err != nil || req.Status != store.WatchInviteFailed || req.Note == "" {
		t.Fatalf("冲突应转 failed 带备注: %+v err=%v", req, err)
	}
}

// 每用户上限：活动邀请申请与监听源合并计数。
func TestUserInvitePerUserLimit(t *testing.T) {
	m := &fakeMembership{
		checkInfo: mtproto.InviteInfo{Title: "私有频道", IsChannel: true},
		joinID:    testBareID, joinTitle: "私有频道",
	}
	svc, st := newInviteService(t, m, adminChat(42))
	enableApply(t, st, true) // PerUserLimit=3
	enabledUser(t, st, 7)
	ctx := context.Background()

	// 占满：2 个监听源 + 1 个活动邀请申请
	for _, id := range []int64{-10011, -10012} {
		if _, err := st.UpsertWatchSource(ctx, store.WatchSource{
			ChannelID: id, Status: store.WatchApproved, Enabled: true, AddedBy: 7,
		}); err != nil {
			t.Fatalf("预置源失败: %v", err)
		}
	}
	out, err := svc.Submit(ctx, 7, false, testInviteLink, 0, "")
	if err != nil || out.Kind != SubmitInvitePending {
		t.Fatalf("第 3 个名额应可提交邀请: %+v err=%v", out, err)
	}
	// 第 4 个：超限（命中 hash 去重之前已由不同链接触发，这里用第二链接）
	out, err = svc.Submit(ctx, 7, false, "https://t.me/+zzzzzzzzzzzzzzzz", 0, "")
	if err != nil || out.Kind != SubmitUserLimit || out.Limit != 3 {
		t.Fatalf("应命中每用户上限: %+v err=%v", out, err)
	}
}

// 公开用户名不会被误判为邀请链接（裸 hash 与用户名冲突的回归防护）。
func TestPublicUsernameNotTreatedAsInvite(t *testing.T) {
	m := &fakeMembership{}
	svc, st := newInviteService(t, m, adminChat(42))
	enableApply(t, st, false)
	enabledUser(t, st, 7)
	ctx := context.Background()
	// "mychannel" 不是链接形态邀请：走普通目标解析（GetChat 命中管理员频道）
	out, err := svc.Submit(ctx, 7, false, "privchan", 0, "")
	if err != nil || out.Kind != SubmitActive {
		t.Fatalf("公开用户名应走普通路径: %+v err=%v", out, err)
	}
	if m.checkCalls != 0 || m.joinCalls != 0 {
		t.Errorf("公开路径不得触发邀请能力: check=%d join=%d", m.checkCalls, m.joinCalls)
	}
}

// 拒绝邀请申请：清理 hash、置 rejected、通知语义由 service 保证不外泄 hash。
func TestRejectInviteClearsHash(t *testing.T) {
	m := &fakeMembership{
		checkInfo: mtproto.InviteInfo{Title: "私有频道", IsChannel: true},
		joinID:    testBareID, joinTitle: "私有频道",
	}
	svc, st := newInviteService(t, m, adminChat(42))
	enableApply(t, st, true)
	enabledUser(t, st, 7)
	ctx := context.Background()
	if _, err := svc.Submit(ctx, 7, false, testInviteLink, 0, ""); err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	req, err := svc.RejectInviteRequest(ctx, "admin", 1)
	if err != nil || req.Status != store.WatchInviteRejected {
		t.Fatalf("拒绝失败: %+v err=%v", req, err)
	}
	if req.InviteHash != "" {
		t.Errorf("拒绝后 hash 应清理: %+v", req)
	}
	// 拒绝后的申请不再参与同 hash 活动查重（他人可重新提交）
	if _, err := svc.Submit(ctx, 8, false, testInviteLink, 0, ""); err != nil {
		t.Fatalf("拒绝后他人应可重新申请: %v", err)
	}
}

// 删除邀请申请：任意状态硬删除。
func TestDeleteInviteRequest(t *testing.T) {
	m := &fakeMembership{
		checkInfo: mtproto.InviteInfo{Title: "私有频道", IsChannel: true},
		joinID:    testBareID, joinTitle: "私有频道",
	}
	svc, st := newInviteService(t, m, adminChat(42))
	ctx := context.Background()
	out, err := svc.AdminAdd(ctx, "admin", testInviteLink, true)
	if err != nil {
		t.Fatalf("添加失败: %v", err)
	}
	if err := svc.DeleteInviteRequest(ctx, out.InviteRequest.ID); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if _, err := st.GetWatchInviteRequest(ctx, out.InviteRequest.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("删除后应不存在: %v", err)
	}
}
