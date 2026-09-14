package joinmgr

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/mtproto"
	"github.com/huaiminyetnotsleep/spore/internal/store"
	"github.com/huaiminyetnotsleep/spore/internal/syscfg"
)

// fakeBridge 模拟 MTProto 成员管理能力。
type fakeBridge struct {
	checkInfo   mtproto.InviteInfo
	checkByHash map[string]mtproto.InviteInfo
	checkErr    error
	checkHashes []string

	joinResult  mtproto.JoinedChannel
	joinAlready bool
	joinErr     error
	joinedOpts  []mtproto.JoinOptions

	postJoinIDs  []int64
	postJoinOpts []mtproto.JoinOptions

	live    []mtproto.JoinedChannel
	listErr error

	leftIDs     []int64
	leaveErrFor map[int64]error
}

func (f *fakeBridge) CheckInvite(ctx context.Context, hash string) (mtproto.InviteInfo, error) {
	f.checkHashes = append(f.checkHashes, hash)
	if f.checkErr != nil {
		return mtproto.InviteInfo{}, f.checkErr
	}
	if info, ok := f.checkByHash[hash]; ok {
		return info, nil
	}
	return f.checkInfo, nil
}

func (f *fakeBridge) JoinInvite(ctx context.Context, hash string, opts mtproto.JoinOptions) (int64, string, bool, error) {
	f.joinedOpts = append(f.joinedOpts, opts)
	if f.joinErr != nil {
		return 0, "", false, f.joinErr
	}
	return f.joinResult.ChannelID, f.joinResult.Title, f.joinAlready, nil
}

func (f *fakeBridge) ApplyPostJoin(ctx context.Context, channelID, accessHash int64, opts mtproto.JoinOptions) error {
	f.postJoinIDs = append(f.postJoinIDs, channelID)
	f.postJoinOpts = append(f.postJoinOpts, opts)
	return nil
}

func (f *fakeBridge) LeaveChannel(ctx context.Context, channelID int64) error {
	if err, ok := f.leaveErrFor[channelID]; ok {
		return err
	}
	f.leftIDs = append(f.leftIDs, channelID)
	return nil
}

func (f *fakeBridge) ListJoined(ctx context.Context) ([]mtproto.JoinedChannel, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.live, nil
}

// fakeNotifier 收集通知。
type fakeNotifier struct{ messages map[int64][]string }

func (f *fakeNotifier) NotifyUser(ctx context.Context, userID int64, text string) error {
	if f.messages == nil {
		f.messages = map[int64][]string{}
	}
	f.messages[userID] = append(f.messages[userID], text)
	return nil
}

func newTestService(t *testing.T, bridge Bridge, notifier Notifier) (*Service, *store.Store) {
	t.Helper()
	s, err := store.Open(context.Background(), t.TempDir()+"/test.db", nil)
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, err := s.CreateUser(context.Background(), store.User{ID: 100}); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	svc, err := New(Options{Store: s, Bridge: bridge, Notifier: notifier})
	if err != nil {
		t.Fatalf("创建服务失败: %v", err)
	}
	return svc, s
}

func enableJoin(t *testing.T, s *store.Store, cfg syscfg.JoinConfig) {
	t.Helper()
	if err := syscfg.SaveJoinConfig(context.Background(), s, cfg); err != nil {
		t.Fatalf("写入配置失败: %v", err)
	}
}

var baseInviteText = "/join https://t.me/+AbCdEfGh12345678"

func TestSubmitDisabled(t *testing.T) {
	bridge := &fakeBridge{}
	svc, s := newTestService(t, bridge, nil)
	// 总开关默认关
	out, err := svc.Submit(context.Background(), 100, false, baseInviteText)
	if err != nil || out.Kind != SubmitDisabled {
		t.Fatalf("期望 SubmitDisabled: %+v %v", out, err)
	}
	enableJoin(t, s, syscfg.JoinConfig{RequireApproval: true, MaxChannels: 20,
		MuteEnabled: true, ArchiveEnabled: true, Enabled: false})
	out, err = svc.Submit(context.Background(), 100, true, baseInviteText)
	if err != nil || out.Kind != SubmitDisabled {
		t.Fatalf("显式关闭后应 SubmitDisabled: %+v %v", out, err)
	}
	if len(bridge.checkHashes) != 0 {
		t.Fatalf("关闭时不应触达 MTProto")
	}
}

func TestSubmitOwnerImmediateJoin(t *testing.T) {
	bridge := &fakeBridge{checkInfo: mtproto.InviteInfo{Title: "私有频道", IsChannel: true},
		joinResult: mtproto.JoinedChannel{ChannelID: 42, Title: "私有频道"}}
	notifier := &fakeNotifier{}
	svc, s := newTestService(t, bridge, notifier)
	enableJoin(t, s, syscfg.JoinConfig{Enabled: true, RequireApproval: true,
		MuteEnabled: true, ArchiveEnabled: true})

	out, err := svc.Submit(context.Background(), 100, true, baseInviteText)
	if err != nil || out.Kind != SubmitJoined || out.Title != "私有频道" {
		t.Fatalf("owner 应即时加入: %+v %v", out, err)
	}
	if len(bridge.joinedOpts) != 1 || !bridge.joinedOpts[0].Mute || !bridge.joinedOpts[0].Archive {
		t.Fatalf("加入后动作应执行静音+归档: %+v", bridge.joinedOpts)
	}
	// 留痕
	records, err := s.ListActiveJoinedChannels(context.Background())
	if err != nil || len(records) != 1 {
		t.Fatalf("应写一条留痕: %v %d", err, len(records))
	}
	if records[0].ChannelID != 42 || records[0].JoinedVia != store.JoinedViaCommand {
		t.Fatalf("留痕来源应为 join_command: %+v", records[0])
	}
}

func TestSubmitNormalUserPending(t *testing.T) {
	bridge := &fakeBridge{checkInfo: mtproto.InviteInfo{Title: "私有频道", IsChannel: true, Participants: 123}}
	svc, s := newTestService(t, bridge, nil)
	enableJoin(t, s, syscfg.JoinConfig{Enabled: true, RequireApproval: true})

	out, err := svc.Submit(context.Background(), 100, false, baseInviteText)
	if err != nil || out.Kind != SubmitPending || out.Title != "私有频道" {
		t.Fatalf("普通用户应落待审批: %+v %v", out, err)
	}
	// 重复提交
	out, err = svc.Submit(context.Background(), 100, false, baseInviteText)
	if err != nil || out.Kind != SubmitPendingDuplicate {
		t.Fatalf("重复提交应提示重复: %+v %v", out, err)
	}
	// 无 join 调用
	if len(bridge.joinedOpts) != 0 {
		t.Fatalf("待审批不应实际加入")
	}
}

func TestSubmitNoApprovalDirectJoin(t *testing.T) {
	bridge := &fakeBridge{checkInfo: mtproto.InviteInfo{Title: "X", IsChannel: true},
		joinResult: mtproto.JoinedChannel{ChannelID: 7, Title: "X"}}
	svc, s := newTestService(t, bridge, nil)
	enableJoin(t, s, syscfg.JoinConfig{Enabled: true, RequireApproval: false})

	out, err := svc.Submit(context.Background(), 100, false, baseInviteText)
	if err != nil || out.Kind != SubmitJoined {
		t.Fatalf("关闭审核时应直接加入: %+v %v", out, err)
	}
	records, _ := s.ListActiveJoinedChannels(context.Background())
	if len(records) != 1 || records[0].JoinedVia != store.JoinedViaApproved {
		t.Fatalf("留痕来源应为 approved: %+v", records)
	}
}

func TestSubmitAlreadyJoinedAndInvalid(t *testing.T) {
	bridge := &fakeBridge{checkInfo: mtproto.InviteInfo{AlreadyJoined: true, Title: "已在频道"}}
	svc, s := newTestService(t, bridge, nil)
	enableJoin(t, s, syscfg.JoinConfig{Enabled: true})

	out, err := svc.Submit(context.Background(), 100, false, baseInviteText)
	if err != nil || out.Kind != SubmitAlreadyJoined {
		t.Fatalf("已加入应直接提示: %+v %v", out, err)
	}

	// 普通群组拒绝
	bridge2 := &fakeBridge{checkInfo: mtproto.InviteInfo{IsChannel: false}}
	svc2, s2 := newTestService(t, bridge2, nil)
	enableJoin(t, s2, syscfg.JoinConfig{Enabled: true})
	if _, err := svc2.Submit(context.Background(), 100, false, baseInviteText); err == nil {
		t.Fatalf("普通群组应拒绝")
	}

	// 链接无效
	svc3, s3 := newTestService(t, &fakeBridge{}, nil)
	enableJoin(t, s3, syscfg.JoinConfig{Enabled: true})
	if _, err := svc3.Submit(context.Background(), 100, false, "/join 不是链接"); err == nil {
		t.Fatalf("无效链接应报错")
	} else if got := apperr.From(err).Code; got != apperr.CodeInvalidInviteURL {
		t.Fatalf("无效邀请链接应返回专用错误码，得到 %s", got)
	}
}

func TestSubmitAlreadyJoinedPostJoin(t *testing.T) {
	// 已是成员且携带频道定位信息：按配置补执行静音/归档
	bridge := &fakeBridge{checkInfo: mtproto.InviteInfo{
		AlreadyJoined: true, Title: "被拉入的频道", ChannelID: 7, AccessHash: 77}}
	svc, s := newTestService(t, bridge, nil)
	enableJoin(t, s, syscfg.JoinConfig{Enabled: true, MuteEnabled: true, ArchiveEnabled: true})

	out, err := svc.Submit(context.Background(), 100, false, baseInviteText)
	if err != nil || out.Kind != SubmitAlreadyJoined {
		t.Fatalf("已加入应直接提示: %+v %v", out, err)
	}
	if len(bridge.postJoinIDs) != 1 || bridge.postJoinIDs[0] != 7 {
		t.Fatalf("应补执行加入后动作: %v", bridge.postJoinIDs)
	}
	if opts := bridge.postJoinOpts[0]; !opts.Mute || !opts.Archive {
		t.Fatalf("加入后动作应随配置: %+v", opts)
	}

	// 自动退出开启时不归档（让位给 Enforce 的退出语义）
	bridge2 := &fakeBridge{checkInfo: mtproto.InviteInfo{
		AlreadyJoined: true, ChannelID: 7, AccessHash: 77}}
	svc2, s2 := newTestService(t, bridge2, nil)
	enableJoin(t, s2, syscfg.JoinConfig{Enabled: true, MuteEnabled: true,
		ArchiveEnabled: true, AutoLeaveExternal: true})
	if _, err := svc2.Submit(context.Background(), 100, false, baseInviteText); err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	if len(bridge2.postJoinIDs) != 0 {
		t.Fatalf("自动退出开启时不应归档: %v", bridge2.postJoinIDs)
	}
}

func TestSubmitLimitReached(t *testing.T) {
	bridge := &fakeBridge{checkInfo: mtproto.InviteInfo{IsChannel: true}}
	svc, s := newTestService(t, bridge, nil)
	enableJoin(t, s, syscfg.JoinConfig{Enabled: true, MaxChannels: 1})
	// 预置一条活跃留痕占满名额
	if err := s.UpsertJoinedChannel(context.Background(), store.JoinedChannelRecord{
		ChannelID: 1, Kind: "channel", JoinedVia: store.JoinedViaCommand, JoinedBy: 100}); err != nil {
		t.Fatalf("预置留痕失败: %v", err)
	}
	out, err := svc.Submit(context.Background(), 100, false, baseInviteText)
	if err != nil || out.Kind != SubmitLimitReached || out.Requests != 1 {
		t.Fatalf("应提示上限: %+v %v", out, err)
	}
}

func TestSubmitJoinRequested(t *testing.T) {
	bridge := &fakeBridge{checkInfo: mtproto.InviteInfo{Title: "审核频道", IsChannel: true},
		joinErr: mtproto.ErrJoinRequestSent}
	svc, s := newTestService(t, bridge, nil)
	enableJoin(t, s, syscfg.JoinConfig{Enabled: true})

	out, err := svc.Submit(context.Background(), 100, true, baseInviteText)
	if err != nil || out.Kind != SubmitJoinRequested || out.Title != "审核频道" {
		t.Fatalf("请求制链接应返回 SubmitJoinRequested: %+v %v", out, err)
	}
	// 账号尚未成为成员：不写留痕
	records, _ := s.ListActiveJoinedChannels(context.Background())
	if len(records) != 0 {
		t.Fatalf("请求制不应写留痕: %+v", records)
	}
}

func TestApproveJoinRequested(t *testing.T) {
	bridge := &fakeBridge{checkInfo: mtproto.InviteInfo{Title: "审核频道", IsChannel: true},
		joinErr: mtproto.ErrJoinRequestSent}
	notifier := &fakeNotifier{}
	svc, s := newTestService(t, bridge, notifier)
	enableJoin(t, s, syscfg.JoinConfig{Enabled: true, RequireApproval: true})
	if _, err := svc.Submit(context.Background(), 100, false, baseInviteText); err != nil {
		t.Fatalf("提交失败: %v", err)
	}

	view, err := svc.Approve(context.Background(), "admin", 1)
	if err != nil {
		t.Fatalf("审批失败: %v", err)
	}
	if view.Status != store.JoinApproved {
		t.Fatalf("请求制审批应置 approved（非 failed）: %+v", view)
	}
	req, _ := s.GetJoinRequest(context.Background(), 1)
	if !strings.HasPrefix(req.Note, "已向频道管理员发送加入请求") {
		t.Fatalf("备注应带请求制前缀: %q", req.Note)
	}
	if len(notifier.messages[100]) != 1 {
		t.Fatalf("应通知申请人: %v", notifier.messages)
	}
}

func TestReconcilePendingJoins(t *testing.T) {
	bridge := &fakeBridge{
		checkByHash: map[string]mtproto.InviteInfo{
			"AbCdEfGh12345678": {AlreadyJoined: true, Title: "审核频道",
				ChannelID: 42, AccessHash: 99},
		},
	}
	notifier := &fakeNotifier{}
	svc, s := newTestService(t, bridge, notifier)
	enableJoin(t, s, syscfg.JoinConfig{Enabled: true, RequireApproval: true})
	// 预置一条"已发送加入请求"的 approved 申请
	if _, _, err := s.CreateJoinRequest(context.Background(), store.JoinRequest{
		UserID: 100, InviteHash: "AbCdEfGh12345678", ChannelTitle: "审核频道"}); err != nil {
		t.Fatalf("写申请失败: %v", err)
	}
	if _, err := s.ReviewJoinRequest(context.Background(), 1, store.JoinApproved, "admin",
		"已向频道管理员发送加入请求，等待频道管理员批准。", 0); err != nil {
		t.Fatalf("预置审批失败: %v", err)
	}

	if err := svc.ReconcilePendingJoins(context.Background()); err != nil {
		t.Fatalf("对账失败: %v", err)
	}
	// 补执行静音/归档 + 留痕 + 备注回写
	if len(bridge.postJoinIDs) != 1 || bridge.postJoinIDs[0] != 42 {
		t.Fatalf("应对频道 42 补执行动作: %v", bridge.postJoinIDs)
	}
	records, _ := s.ListActiveJoinedChannels(context.Background())
	if len(records) != 1 || records[0].ChannelID != 42 || records[0].JoinedVia != store.JoinedViaApproved {
		t.Fatalf("应补写留痕: %+v", records)
	}
	req, _ := s.GetJoinRequest(context.Background(), 1)
	if !strings.HasPrefix(req.Note, "频道管理员已批准") {
		t.Fatalf("备注应回写为已批准: %q", req.Note)
	}
	// 再次对账：备注不再匹配前缀 → 不重复执行
	if err := svc.ReconcilePendingJoins(context.Background()); err != nil {
		t.Fatalf("二次对账失败: %v", err)
	}
	if len(bridge.postJoinIDs) != 1 {
		t.Fatalf("回写后不应重复执行: %v", bridge.postJoinIDs)
	}
}

func TestApproveFlow(t *testing.T) {
	bridge := &fakeBridge{checkInfo: mtproto.InviteInfo{Title: "私有频道", IsChannel: true},
		joinResult: mtproto.JoinedChannel{ChannelID: 42, Title: "私有频道"}}
	notifier := &fakeNotifier{}
	svc, s := newTestService(t, bridge, notifier)
	enableJoin(t, s, syscfg.JoinConfig{Enabled: true, RequireApproval: true})

	if _, err := svc.Submit(context.Background(), 100, false, baseInviteText); err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	view, err := svc.Approve(context.Background(), "admin", 1)
	if err != nil {
		t.Fatalf("审批失败: %v", err)
	}
	if view.Status != store.JoinApproved || view.ChannelTitle != "私有频道" {
		t.Fatalf("审批视图不符: %+v", view)
	}
	// 通知申请人
	if len(notifier.messages[100]) != 1 {
		t.Fatalf("应通知申请人: %v", notifier.messages)
	}
	// 留痕
	records, _ := s.ListActiveJoinedChannels(context.Background())
	if len(records) != 1 || records[0].JoinedVia != store.JoinedViaApproved {
		t.Fatalf("审批留痕不符: %+v", records)
	}
	// 重复审批
	if _, err := svc.Approve(context.Background(), "admin", 1); err == nil {
		t.Fatalf("重复审批应失败")
	}
}

func TestApproveJoinFailed(t *testing.T) {
	bridge := &fakeBridge{checkInfo: mtproto.InviteInfo{Title: "X", IsChannel: true},
		joinErr: apperr.New(apperr.CodeInvalidURL, "链接已失效")}
	svc, s := newTestService(t, bridge, nil)
	enableJoin(t, s, syscfg.JoinConfig{Enabled: true, RequireApproval: true})
	// 直接落一条 pending（绕过 Submit 的 CheckInvite）
	if _, _, err := s.CreateJoinRequest(context.Background(), store.JoinRequest{
		UserID: 100, InviteHash: "AbCdEfGh12345678", ChannelTitle: "X"}); err != nil {
		t.Fatalf("写申请失败: %v", err)
	}
	_, err := svc.Approve(context.Background(), "admin", 1)
	if err == nil {
		t.Fatalf("加入失败应返回错误")
	}
	req, _ := s.GetJoinRequest(context.Background(), 1)
	if req.Status != store.JoinFailed {
		t.Fatalf("失败申请应置 failed: %s", req.Status)
	}
}

func TestApproveDisabled(t *testing.T) {
	bridge := &fakeBridge{checkInfo: mtproto.InviteInfo{Title: "X", IsChannel: true}}
	svc, s := newTestService(t, bridge, nil)
	enableJoin(t, s, syscfg.JoinConfig{Enabled: true, RequireApproval: true})
	if _, err := svc.Submit(context.Background(), 100, false, baseInviteText); err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	// 审批前管理员关闭总开关
	enableJoin(t, s, syscfg.JoinConfig{Enabled: false, RequireApproval: true})
	if _, err := svc.Approve(context.Background(), "admin", 1); err == nil {
		t.Fatalf("总开关关闭后审批应拒绝")
	}
}

func TestRejectFlow(t *testing.T) {
	bridge := &fakeBridge{checkInfo: mtproto.InviteInfo{Title: "X", IsChannel: true}}
	notifier := &fakeNotifier{}
	svc, s := newTestService(t, bridge, notifier)
	enableJoin(t, s, syscfg.JoinConfig{Enabled: true, RequireApproval: true})
	if _, err := svc.Submit(context.Background(), 100, false, baseInviteText); err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	view, err := svc.Reject(context.Background(), "admin", 1)
	if err != nil || view.Status != store.JoinRejected {
		t.Fatalf("拒绝失败: %+v %v", view, err)
	}
	if len(notifier.messages[100]) != 1 {
		t.Fatalf("应通知申请人")
	}
	if _, err := svc.Reject(context.Background(), "admin", 1); err == nil {
		t.Fatalf("重复拒绝应失败")
	}
}

func TestListJoinedMarksExternal(t *testing.T) {
	bridge := &fakeBridge{live: []mtproto.JoinedChannel{
		{ChannelID: 1, Title: "审批加入的", Kind: "channel"},
		{ChannelID: 2, Title: "外部拉入的", Kind: "channel"},
	}}
	svc, s := newTestService(t, bridge, nil)
	if err := s.UpsertJoinedChannel(context.Background(), store.JoinedChannelRecord{
		ChannelID: 1, Kind: "channel", JoinedVia: store.JoinedViaApproved, JoinedBy: 100}); err != nil {
		t.Fatalf("预置留痕失败: %v", err)
	}

	views, err := svc.ListJoined(context.Background())
	if err != nil || len(views) != 2 {
		t.Fatalf("列表失败: %v %d", err, len(views))
	}
	for _, v := range views {
		switch v.ChannelID {
		case 1:
			if v.Source != store.JoinedViaApproved {
				t.Fatalf("频道 1 来源应为 approved: %s", v.Source)
			}
		case 2:
			if v.Source != store.JoinedViaExternal {
				t.Fatalf("频道 2 来源应为 external: %s", v.Source)
			}
		}
	}
	// 外部拉入被补写留痕
	records, _ := s.ListActiveJoinedChannels(context.Background())
	if len(records) != 2 {
		t.Fatalf("外部频道应补写留痕: %d", len(records))
	}
}

func TestListJoinedArchivesExternal(t *testing.T) {
	bridge := &fakeBridge{live: []mtproto.JoinedChannel{
		{ChannelID: 1, AccessHash: 11, Title: "审批加入未归档", Kind: "channel"},
		{ChannelID: 2, AccessHash: 22, Title: "外部未归档", Kind: "channel"},
		{ChannelID: 3, AccessHash: 33, Title: "外部已归档", Kind: "channel", Archived: true},
		{ChannelID: 4, AccessHash: 44, Title: "外部留痕未归档", Kind: "channel"},
	}}
	svc, s := newTestService(t, bridge, nil)
	enableJoin(t, s, syscfg.JoinConfig{Enabled: true, MuteEnabled: true, ArchiveEnabled: true})
	for _, rec := range []store.JoinedChannelRecord{
		{ChannelID: 1, Kind: "channel", JoinedVia: store.JoinedViaApproved, JoinedBy: 100},
		{ChannelID: 4, Kind: "channel", JoinedVia: store.JoinedViaExternal},
	} {
		if err := s.UpsertJoinedChannel(context.Background(), rec); err != nil {
			t.Fatalf("预置留痕失败: %v", err)
		}
	}

	if _, err := svc.ListJoined(context.Background()); err != nil {
		t.Fatalf("列表失败: %v", err)
	}
	// 只归档未归档的外部频道（无留痕或 external 留痕）：2 与 4；
	// approved 留痕（1）与已归档（3）不触发
	if len(bridge.postJoinIDs) != 2 || bridge.postJoinIDs[0] != 2 || bridge.postJoinIDs[1] != 4 {
		t.Fatalf("应只归档未归档的外部频道: %v", bridge.postJoinIDs)
	}
	for _, opts := range bridge.postJoinOpts {
		if !opts.Mute || !opts.Archive {
			t.Fatalf("加入后动作应随配置: %+v", opts)
		}
	}
}

func TestOnChannelsSeen(t *testing.T) {
	bridge := &fakeBridge{}
	svc, s := newTestService(t, bridge, nil)
	enableJoin(t, s, syscfg.JoinConfig{Enabled: true, MuteEnabled: true, ArchiveEnabled: true})
	if err := s.UpsertJoinedChannel(context.Background(), store.JoinedChannelRecord{
		ChannelID: 1, Kind: "channel", JoinedVia: store.JoinedViaCommand, JoinedBy: 100}); err != nil {
		t.Fatalf("预置留痕失败: %v", err)
	}

	svc.OnChannelsSeen(context.Background(), []mtproto.JoinedChannel{
		{ChannelID: 1, AccessHash: 11, Title: "系统加入的", Kind: "channel"},
		{ChannelID: 2, AccessHash: 22, Title: "外部拉入的", Kind: "channel"},
		{ChannelID: 3, Title: "无定位信息的", Kind: "channel"},
	})
	// 系统加入的跳过、无 accessHash 的留给惰性对账，只实时归档 2
	if len(bridge.postJoinIDs) != 1 || bridge.postJoinIDs[0] != 2 {
		t.Fatalf("应只实时归档外部新见频道: %v", bridge.postJoinIDs)
	}
	// 实时归档的外部频道补留痕
	records, _ := s.ListActiveJoinedChannels(context.Background())
	if len(records) != 2 {
		t.Fatalf("实时归档后应补留痕: %d", len(records))
	}

	// 自动退出开启时不归档（退出优先，由 Enforce 惰性处理）
	bridge2 := &fakeBridge{}
	svc2, s2 := newTestService(t, bridge2, nil)
	enableJoin(t, s2, syscfg.JoinConfig{Enabled: true, MuteEnabled: true,
		ArchiveEnabled: true, AutoLeaveExternal: true})
	svc2.OnChannelsSeen(context.Background(), []mtproto.JoinedChannel{
		{ChannelID: 9, AccessHash: 99, Title: "外部拉入的", Kind: "channel"},
	})
	if len(bridge2.postJoinIDs) != 0 {
		t.Fatalf("自动退出开启时不应归档: %v", bridge2.postJoinIDs)
	}

	// 静音/归档全关时不动作
	bridge3 := &fakeBridge{}
	svc3, s3 := newTestService(t, bridge3, nil)
	enableJoin(t, s3, syscfg.JoinConfig{Enabled: true})
	svc3.OnChannelsSeen(context.Background(), []mtproto.JoinedChannel{
		{ChannelID: 8, AccessHash: 88, Title: "外部拉入的", Kind: "channel"},
	})
	if len(bridge3.postJoinIDs) != 0 {
		t.Fatalf("静音/归档全关时不应动作: %v", bridge3.postJoinIDs)
	}
}

func TestReconcileExternal(t *testing.T) {
	bridge := &fakeBridge{live: []mtproto.JoinedChannel{
		{ChannelID: 2, AccessHash: 22, Title: "外部未归档", Kind: "channel"},
		{ChannelID: 3, AccessHash: 33, Title: "外部已归档", Kind: "channel", Archived: true},
	}}
	svc, s := newTestService(t, bridge, nil)
	enableJoin(t, s, syscfg.JoinConfig{Enabled: true, MuteEnabled: true, ArchiveEnabled: true})

	if err := svc.ReconcileExternal(context.Background()); err != nil {
		t.Fatalf("周期对账失败: %v", err)
	}
	if len(bridge.postJoinIDs) != 1 || bridge.postJoinIDs[0] != 2 {
		t.Fatalf("周期对账应归档未归档的外部频道: %v", bridge.postJoinIDs)
	}
}

func TestLeaveBatch(t *testing.T) {
	bridge := &fakeBridge{leaveErrFor: map[int64]error{
		9: mtproto.ErrChannelCreator,
		8: apperr.New(apperr.CodeChannelInaccessible, "未加入"),
	}}
	svc, s := newTestService(t, bridge, nil)
	for _, id := range []int64{1, 8, 9} {
		_ = s.UpsertJoinedChannel(context.Background(), store.JoinedChannelRecord{
			ChannelID: id, Kind: "channel", JoinedVia: store.JoinedViaCommand, JoinedBy: 100})
	}
	out := svc.Leave(context.Background(), []int64{1, 8, 9})
	if len(out) != 3 || !out[0].OK || out[1].OK || out[2].OK {
		t.Fatalf("批量退出结果不符: %+v", out)
	}
	n, _ := s.CountActiveJoinedChannels(context.Background())
	if n != 2 {
		t.Fatalf("仅成功退出的留痕应更新: %d", n)
	}
}

func TestEnforceLeavesExternalOnly(t *testing.T) {
	t.Run("开关开启时退出外部拉入", testEnforceLeavesExternalOnly)
	t.Run("开关关闭时不动任何频道", testEnforceNoopWhenAutoLeaveOff)
}

func testEnforceLeavesExternalOnly(t *testing.T) {
	bridge := &fakeBridge{live: []mtproto.JoinedChannel{
		{ChannelID: 1, Title: "审批加入", Kind: "channel"},
		{ChannelID: 2, Title: "外部拉入", Kind: "channel"},
		{ChannelID: 3, Title: "创建者", Kind: "channel", Creator: true},
	}}
	svc, s := newTestService(t, bridge, nil)
	// 自动退出开关显式开启（总开关无关）
	enableJoin(t, s, syscfg.JoinConfig{Enabled: true, AutoLeaveExternal: true})
	if err := s.UpsertJoinedChannel(context.Background(), store.JoinedChannelRecord{
		ChannelID: 1, Kind: "channel", JoinedVia: store.JoinedViaApproved, JoinedBy: 100}); err != nil {
		t.Fatalf("预置留痕失败: %v", err)
	}

	if err := svc.Enforce(context.Background()); err != nil {
		t.Fatalf("熔断失败: %v", err)
	}
	// 只有频道 2 被退出（1 有 approved 留痕，3 是创建者）
	if len(bridge.leftIDs) != 1 || bridge.leftIDs[0] != 2 {
		t.Fatalf("熔断应只退外部拉入: %v", bridge.leftIDs)
	}
	// 补写 + 置 left：频道 2 退出留痕（left_at 置位）；频道 3（创建者）
	// 不会被退出也不写留痕——活跃留痕只剩频道 1
	records, _ := s.ListActiveJoinedChannels(context.Background())
	if len(records) != 1 || records[0].ChannelID != 1 {
		t.Fatalf("留痕数量不符: %+v", records)
	}
}

func testEnforceNoopWhenAutoLeaveOff(t *testing.T) {
	bridge := &fakeBridge{live: []mtproto.JoinedChannel{{ChannelID: 2, Kind: "channel"}}}
	svc, s := newTestService(t, bridge, nil)
	// 默认即关：即使总开关开启也不自动退出
	enableJoin(t, s, syscfg.JoinConfig{Enabled: true})
	if err := svc.Enforce(context.Background()); err != nil {
		t.Fatalf("自动退出关闭时应为 no-op: %v", err)
	}
	if len(bridge.leftIDs) != 0 {
		t.Fatalf("自动退出关闭时不应退出任何频道: %v", bridge.leftIDs)
	}
}

func TestListRequestsMasksHash(t *testing.T) {
	bridge := &fakeBridge{checkInfo: mtproto.InviteInfo{Title: "X", IsChannel: true}}
	svc, s := newTestService(t, bridge, nil)
	enableJoin(t, s, syscfg.JoinConfig{Enabled: true, RequireApproval: true})
	if _, err := svc.Submit(context.Background(), 100, false, baseInviteText); err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	views, total, err := svc.ListRequests(context.Background(),
		RequestsQuery{Status: store.JoinPending, Page: 1, PageSize: 20})
	if err != nil || len(views) != 1 || total != 1 {
		t.Fatalf("列表失败: %v %d %d", err, len(views), total)
	}
	if _, _, err := svc.ListRequests(context.Background(), RequestsQuery{Status: "bogus"}); err == nil {
		t.Fatalf("非法筛选应报错")
	}
	if views[0].MaskedHash == "AbCdEfGh12345678" || views[0].MaskedHash == "" {
		t.Fatalf("hash 应脱敏: %q", views[0].MaskedHash)
	}
}

func TestDeleteRequests(t *testing.T) {
	bridge := &fakeBridge{checkInfo: mtproto.InviteInfo{Title: "X", IsChannel: true}}
	svc, s := newTestService(t, bridge, nil)
	enableJoin(t, s, syscfg.JoinConfig{Enabled: true, RequireApproval: true})
	if _, err := svc.Submit(context.Background(), 100, false, baseInviteText); err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	// 造两条终态：approved + rejected
	if _, err := svc.Approve(context.Background(), "admin", 1); err != nil {
		t.Fatalf("审批失败: %v", err)
	}
	bridge2 := &fakeBridge{checkInfo: mtproto.InviteInfo{Title: "Y", IsChannel: true}}
	_ = bridge2
	if _, _, err := s.CreateJoinRequest(context.Background(), store.JoinRequest{
		UserID: 100, InviteHash: "ZZZZZZZZ12345678", ChannelTitle: "Y"}); err != nil {
		t.Fatalf("写申请失败: %v", err)
	}
	if _, err := svc.Reject(context.Background(), "admin", 2); err != nil {
		t.Fatalf("拒绝失败: %v", err)
	}
	// 再落一条 pending
	if _, _, err := s.CreateJoinRequest(context.Background(), store.JoinRequest{
		UserID: 100, InviteHash: "PPPPPPPP12345678", ChannelTitle: "P"}); err != nil {
		t.Fatalf("写 pending 失败: %v", err)
	}

	out := svc.DeleteRequests(context.Background(), []int64{1, 2, 3, 999})
	if len(out) != 4 {
		t.Fatalf("应返回 4 条结果: %d", len(out))
	}
	if !out[0].OK || !out[1].OK {
		t.Fatalf("终态记录应可删除: %+v", out)
	}
	if out[2].OK || out[2].Error == "" {
		t.Fatalf("pending 应拒绝删除: %+v", out[2])
	}
	if out[3].OK || out[3].Error == "" {
		t.Fatalf("不存在应报错: %+v", out[3])
	}
	// 删除后查无
	if _, err := s.GetJoinRequest(context.Background(), 1); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("删除后应不存在: %v", err)
	}
}

func TestBridgeUnavailable(t *testing.T) {
	bridge := &fakeBridge{checkErr: mtproto.ErrMembershipUnavailable,
		listErr: mtproto.ErrMembershipUnavailable}
	svc, s := newTestService(t, bridge, nil)
	enableJoin(t, s, syscfg.JoinConfig{Enabled: true})

	if _, err := svc.Submit(context.Background(), 100, false, baseInviteText); !errors.Is(err, mtproto.ErrMembershipUnavailable) {
		t.Fatalf("MTProto 离线应透传 ErrMembershipUnavailable: %v", err)
	}
	if _, err := svc.ListJoined(context.Background()); !errors.Is(err, mtproto.ErrMembershipUnavailable) {
		t.Fatalf("ListJoined 离线应透传")
	}
}
