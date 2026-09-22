package botapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/huaiminyetnotsleep/spore/internal/access"
	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/delivery"
	"github.com/huaiminyetnotsleep/spore/internal/message"
	"github.com/huaiminyetnotsleep/spore/internal/mtproto"
	"github.com/huaiminyetnotsleep/spore/internal/queue"
	"github.com/huaiminyetnotsleep/spore/internal/store"
	"github.com/huaiminyetnotsleep/spore/internal/syscfg"
	"github.com/huaiminyetnotsleep/spore/internal/tmeurl"
)

// ---- 测试替身 ----

// fakeAccess 记录调用并按配置返回结果。
type fakeAccess struct {
	mu         sync.Mutex
	submits    []access.Submission
	starts     []access.StartInput
	usageFor   int64
	cancelFor  []int64
	cancelRefs []tmeurl.SourceRef
	decide     func(in access.Submission) (access.Decision, error)
	startOut   access.StartOutcome
	usageData  access.Usage
	cancelOut  int
	cancelErr  error
	statusCode apperr.Code // UserDownloadStatus 返回码；零值 = 允许
	statusErr  error
	// 引用回复锚点路径（Resolve/CancelByID/MarkPin/RecordStatus）
	anchors        map[int64]store.SentMessage // replyMsgID → 锚点
	requestsByID   map[int64]store.Request     // requestID → 请求行
	resolveErr     error
	cancelByIDFor  []int64
	cancelByIDOut  int
	cancelByIDErr  error
	pinMarkedFor   []int64
	pinMarkedOut   bool
	pinMarkedErr   error
	recordedAnchor []store.SentMessage
	recordErr      error
}

func (f *fakeAccess) Submit(_ context.Context, in access.Submission) (access.Decision, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.submits = append(f.submits, in)
	if f.decide == nil {
		return access.Decision{Allowed: true, JobID: "1", RequestID: 1}, nil
	}
	return f.decide(in)
}

func (f *fakeAccess) UserDownloadStatus(context.Context, int64) (apperr.Code, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.statusCode, f.statusErr
}

func (f *fakeAccess) HandleStart(_ context.Context, in access.StartInput) (access.StartOutcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts = append(f.starts, in)
	return f.startOut, nil
}

func (f *fakeAccess) Usage(_ context.Context, userID int64) (access.Usage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.usageFor = userID
	return f.usageData, nil
}

func (f *fakeAccess) CancelOwnByLink(_ context.Context, userID int64, ref tmeurl.SourceRef) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelFor = append(f.cancelFor, userID)
	f.cancelRefs = append(f.cancelRefs, ref)
	return f.cancelOut, f.cancelErr
}

func (f *fakeAccess) RecordStatusMessage(_ context.Context, botID, chatID, messageID, requestID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordedAnchor = append(f.recordedAnchor, store.SentMessage{
		RequestID: requestID, BotID: botID, ChatID: chatID, MessageID: messageID, Kind: store.SentKindStatus,
	})
	return f.recordErr
}

func (f *fakeAccess) ResolveOwnSentMessage(_ context.Context, userID, botID, chatID, messageID int64) (store.SentMessage, store.Request, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.resolveErr != nil {
		return store.SentMessage{}, store.Request{}, f.resolveErr
	}
	m, ok := f.anchors[messageID]
	if !ok {
		return store.SentMessage{}, store.Request{}, store.ErrNotFound
	}
	r, ok := f.requestsByID[m.RequestID]
	if !ok || r.UserID != userID {
		return store.SentMessage{}, store.Request{}, store.ErrNotFound
	}
	return m, r, nil
}

func (f *fakeAccess) CancelOwnByID(_ context.Context, userID, requestID int64) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelByIDFor = append(f.cancelByIDFor, requestID)
	return f.cancelByIDOut, f.cancelByIDErr
}

func (f *fakeAccess) MarkOwnRequestPin(_ context.Context, userID, requestID int64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pinMarkedFor = append(f.pinMarkedFor, requestID)
	return f.pinMarkedOut, f.pinMarkedErr
}

func (f *fakeAccess) recordedAnchors() []store.SentMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]store.SentMessage(nil), f.recordedAnchor...)
}

func (f *fakeAccess) cancelledByIDs() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int64(nil), f.cancelByIDFor...)
}

func (f *fakeAccess) pinMarks() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int64(nil), f.pinMarkedFor...)
}

func (f *fakeAccess) submitted() []access.Submission {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]access.Submission(nil), f.submits...)
}

func (f *fakeAccess) startsOf() []access.StartInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]access.StartInput(nil), f.starts...)
}

// fakeSender 记录发送与删除。
type fakeSender struct {
	mu      sync.Mutex
	sent    []string
	deleted []int
	nextID  int
}

func (f *fakeSender) SendMessage(_ context.Context, _ int64, html string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	f.sent = append(f.sent, html)
	return f.nextID, nil
}

func (f *fakeSender) SendMedia(context.Context, int64, message.Media, message.Caption, io.Reader) (int, error) {
	return 1, nil
}
func (f *fakeSender) SendAlbum(context.Context, int64, []delivery.AlbumEntry) ([]int, error) {
	return []int{1, 2}, nil
}
func (f *fakeSender) AlbumGroupable(message.Media) bool { return false }
func (f *fakeSender) CopyMessages(_ context.Context, _, _ int64, messageIDs []int) ([]int, error) {
	return messageIDs, nil
}
func (f *fakeSender) CopyMessage(_ context.Context, _, _ int64, messageID int, _ string) (int, error) {
	return messageID, nil
}

func (f *fakeSender) EditMessageCaption(context.Context, int64, int, string) error { return nil }

func (f *fakeSender) EditMessageText(context.Context, int64, int, string) error { return nil }

func (f *fakeSender) DeleteMessage(_ context.Context, _ int64, messageID int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, messageID)
	return nil
}

func (f *fakeSender) texts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.sent...)
}
func (f *fakeSender) deletedIDs() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int(nil), f.deleted...)
}

// ---- 用例 ----

// newHarness 组装可测的 Options 与替身。
func newHarness(t *testing.T, capacity int) (Options, *fakeAccess, *fakeSender) {
	t.Helper()
	fa := &fakeAccess{}
	fs := &fakeSender{}
	opt := Options{
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Queue:  queue.New(capacity),
		Access: fa,
	}
	return opt, fa, fs
}

func run(opt Options, fs *fakeSender, text string) {
	handleUpdate(context.Background(), opt, fs,
		models.User{ID: 7, FirstName: "Alice", LastName: "Doe", Username: "alice"}, 7, text, 0)
}

func lastText(t *testing.T, fs *fakeSender) string {
	t.Helper()
	texts := fs.texts()
	if len(texts) == 0 {
		t.Fatal("期望至少一条回复，实际没有")
	}
	return texts[len(texts)-1]
}

func TestHandleLinkAllowed(t *testing.T) {
	opt, fa, fs := newHarness(t, 4)
	run(opt, fs, "https://t.me/example_channel/42")

	texts := fs.texts()
	if len(texts) != 1 || texts[0] != statusPrompt {
		t.Fatalf("通过路径只应发送占位提示: %v", texts)
	}
	subs := fa.submitted()
	if len(subs) != 1 {
		t.Fatalf("应恰好一次提交，得到 %d", len(subs))
	}
	if subs[0].UserID != 7 || subs[0].ChatID != 7 || subs[0].StatusMsgID != 1 || subs[0].Ref.MessageID != 42 {
		t.Errorf("提交字段不符: %+v", subs[0])
	}
}

func TestHandleLinkDenialsUseUserText(t *testing.T) {
	// 各拒绝原因 → 中文文案；占位提示被清理
	for _, reason := range []apperr.Code{
		apperr.CodeUserNotAuthorized, apperr.CodeUserPending, apperr.CodeUserDisabled,
		apperr.CodeDuplicateLink, apperr.CodeSubmitRateLimited, apperr.CodeQuotaExceeded,
		apperr.CodeConcurrentLimited, apperr.CodeQueueFull,
	} {
		opt, fa, fs := newHarness(t, 4)
		fa.decide = func(access.Submission) (access.Decision, error) {
			return access.Decision{Reason: reason}, nil
		}
		run(opt, fs, "https://t.me/example_channel/1")

		texts := fs.texts()
		if len(texts) != 2 || texts[0] != statusPrompt {
			t.Fatalf("%s：应先占位后提示，得到 %v", reason, texts)
		}
		if got := texts[1]; got != apperr.UserText(reason) {
			t.Errorf("%s：文案应经 UserText，得到 %q", reason, got)
		}
		if ids := fs.deletedIDs(); len(ids) != 1 || ids[0] != 1 {
			t.Errorf("%s：占位提示应被删除，得到 %v", reason, ids)
		}
	}
}

func TestHandleLinkStoreError(t *testing.T) {
	opt, fa, fs := newHarness(t, 4)
	fa.decide = func(access.Submission) (access.Decision, error) {
		// 模拟 store 层已分类的存储故障
		return access.Decision{}, apperr.Wrap(apperr.CodeStoreUnavailable, errors.New("db down"))
	}
	run(opt, fs, "https://t.me/example_channel/1")
	texts := fs.texts()
	if len(texts) != 2 || texts[1] != apperr.UserText(apperr.CodeStoreUnavailable) {
		t.Fatalf("存储错误应回复 STORE_UNAVAILABLE 文案: %v", texts)
	}
	if len(fs.deletedIDs()) != 1 {
		t.Errorf("存储错误也应清理占位提示: %v", fs.deletedIDs())
	}

	// 未分类错误：回落 INTERNAL_ERROR 文案
	opt2, fa2, fs2 := newHarness(t, 4)
	fa2.decide = func(access.Submission) (access.Decision, error) {
		return access.Decision{}, errors.New("boom")
	}
	run(opt2, fs2, "https://t.me/example_channel/1")
	if got := lastText(t, fs2); got != apperr.UserText(apperr.CodeInternal) {
		t.Errorf("未分类错误应回落 INTERNAL 文案，得到 %q", got)
	}
}

func TestHandleLinkInvalidText(t *testing.T) {
	opt, fa, fs := newHarness(t, 4)
	run(opt, fs, "hello world")
	if got := lastText(t, fs); got != apperr.UserText(apperr.CodeInvalidURL) {
		t.Errorf("无链接应回 INVALID_URL 文案，得到 %q", got)
	}
	if n := len(fa.submitted()); n != 0 {
		t.Errorf("不应触达 access，得到 %d 次", n)
	}
}

func TestHandleLinkMultipleLinksSubmittedIndependently(t *testing.T) {
	opt, fa, fs := newHarness(t, 4)
	run(opt, fs, "https://t.me/example_channel/1 https://t.me/example_channel/2")

	texts := fs.texts()
	if len(texts) != 3 || texts[0] != statusPrompt || texts[1] != statusPrompt ||
		texts[2] != "批量提交完成：成功 2 条，失败 0 条。" {
		t.Fatalf("多链接应逐条占位后汇总: %v", texts)
	}
	subs := fa.submitted()
	if len(subs) != 2 || subs[0].Ref.MessageID != 1 || subs[1].Ref.MessageID != 2 {
		t.Fatalf("应按顺序提交全部链接: %+v", subs)
	}
	if subs[0].StatusMsgID == 0 || subs[1].StatusMsgID == 0 || subs[0].StatusMsgID == subs[1].StatusMsgID {
		t.Fatalf("每条链接应使用独立占位消息: %+v", subs)
	}
	if subs[0].BatchContinuation || !subs[1].BatchContinuation {
		t.Fatalf("同一输入仅后续链接应标记为批量续项: %+v", subs)
	}
}

func TestHandleLinkTooManyLinksRejectedAsBatch(t *testing.T) {
	opt, fa, fs := newHarness(t, 20)
	opt.MaxLinksPerMessage = func(context.Context) int { return 2 }
	run(opt, fs, "https://t.me/example_channel/1 invalid https://t.me/example_channel/2 https://t.me/example_channel/3")

	if n := len(fa.submitted()); n != 0 {
		t.Fatalf("超限时不应提交任何链接，得到 %d 次", n)
	}
	if got := lastText(t, fs); !strings.Contains(got, "一次最多处理 2 条有效链接") || !strings.Contains(got, "检测到 3 条") {
		t.Fatalf("超限提示不符: %q", got)
	}
}

func TestHandleLinkBatchContinuesAfterRejection(t *testing.T) {
	opt, fa, fs := newHarness(t, 20)
	fa.decide = func(in access.Submission) (access.Decision, error) {
		if in.Ref.MessageID == 2 {
			return access.Decision{Reason: apperr.CodeDuplicateLink}, nil
		}
		return access.Decision{Allowed: true, JobID: "ok", RequestID: int64(in.Ref.MessageID)}, nil
	}
	run(opt, fs, "https://t.me/example_channel/1 https://t.me/example_channel/2 https://t.me/example_channel/3")

	if subs := fa.submitted(); len(subs) != 3 {
		t.Fatalf("单条被拒后仍应处理其余链接: %+v", subs)
	}
	if deleted := fs.deletedIDs(); len(deleted) != 1 || deleted[0] != 2 {
		t.Fatalf("只应清理被拒链接的占位消息: %v", deleted)
	}
	got := lastText(t, fs)
	if !strings.Contains(got, "成功 2 条，失败 1 条") ||
		!strings.Contains(got, apperr.UserText(apperr.CodeDuplicateLink)) {
		t.Fatalf("部分失败汇总不符: %q", got)
	}
}

func TestHandleLinkQueueFullPreflight(t *testing.T) {
	opt, fa, fs := newHarness(t, 1)
	// 占满队列：快速路径直接回繁忙，不触达 access、不发占位提示
	if err := opt.Queue.Enqueue(queue.NewJob(9, 9, tmeurl.SourceRef{Kind: tmeurl.PeerUsername, Username: "x", MessageID: 1}, 0, 0)); err != nil {
		t.Fatalf("占位失败: %v", err)
	}
	run(opt, fs, "https://t.me/example_channel/1")
	texts := fs.texts()
	if len(texts) != 1 || texts[0] != apperr.UserText(apperr.CodeQueueFull) {
		t.Fatalf("饱和时应直接回繁忙: %v", texts)
	}
	if n := len(fa.submitted()); n != 0 {
		t.Errorf("饱和快速路径不应触达 access: %d", n)
	}
}

func TestHandleStartOutcomes(t *testing.T) {
	// 待审批：创建申请并回复等待提示
	opt, fa, fs := newHarness(t, 4)
	fa.startOut = access.StartPending
	run(opt, fs, "/start")
	if got := lastText(t, fs); got != apperr.UserText(apperr.CodeUserPending) {
		t.Errorf("pending 应回复等待提示，得到 %q", got)
	}
	starts := fa.startsOf()
	if len(starts) != 1 || starts[0].UserID != 7 || starts[0].Username != "alice" || starts[0].DisplayName != "Alice Doe" {
		t.Errorf("申请信息应携带身份: %+v", starts[0])
	}

	// 已启用：保持欢迎文案
	opt2, fa2, fs2 := newHarness(t, 4)
	fa2.startOut = access.StartWelcome
	run(opt2, fs2, "/start")
	if got := lastText(t, fs2); got != helpText(syscfg.DefaultName) {
		t.Errorf("enabled 应回欢迎文案，得到 %q", got)
	}

	// 停用：账号已停用
	opt3, fa3, fs3 := newHarness(t, 4)
	fa3.startOut = access.StartDisabled
	run(opt3, fs3, "/start")
	if got := lastText(t, fs3); got != apperr.UserText(apperr.CodeUserDisabled) {
		t.Errorf("停用应回已停用提示，得到 %q", got)
	}
}

func TestHandleUsage(t *testing.T) {
	resetAt := time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC)

	// 普通用户：已用/额度/剩余/重置时间
	opt, fa, fs := newHarness(t, 4)
	fa.usageData = access.Usage{
		Status: "enabled", Used: 3, DailyLimit: 50, Remaining: 47, ResetAt: resetAt,
	}
	run(opt, fs, "/usage")
	got := lastText(t, fs)
	for _, want := range []string{"3/50", "47", "2026-08-28 00:00"} {
		if !strings.Contains(got, want) {
			t.Errorf("用量文案应包含 %q，得到 %q", want, got)
		}
	}

	// owner：不限额
	opt2, fa2, fs2 := newHarness(t, 4)
	fa2.usageData = access.Usage{Status: "enabled", IsOwner: true, Remaining: -1, Used: 5}
	run(opt2, fs2, "/usage")
	got = lastText(t, fs2)
	if !strings.Contains(got, "owner") || !strings.Contains(got, "5") {
		t.Errorf("owner 文案应说明不限额并显示已用，得到 %q", got)
	}

	// 未授权用户查询：状态提示而非数字
	opt3, fa3, fs3 := newHarness(t, 4)
	fa3.usageData = access.Usage{Status: ""} // 不存在
	run(opt3, fs3, "/usage")
	if got := lastText(t, fs3); got != apperr.UserText(apperr.CodeUserNotAuthorized) {
		t.Errorf("未授权 /usage 应回状态提示，得到 %q", got)
	}
}

func TestHandleCancel(t *testing.T) {
	// 无参数：用法提示，不触达 access
	opt, fa, fs := newHarness(t, 4)
	run(opt, fs, "/cancel")
	if got := lastText(t, fs); got != cancelUsage {
		t.Errorf("无参数应回用法提示，得到 %q", got)
	}
	if n := len(fa.cancelFor); n != 0 {
		t.Errorf("无参数不应触达 access，得到 %d 次", n)
	}

	// 无有效链接：INVALID_URL 文案，不触达 access
	opt, fa, fs = newHarness(t, 4)
	run(opt, fs, "/cancel hello world")
	if got := lastText(t, fs); got != apperr.UserText(apperr.CodeInvalidURL) {
		t.Errorf("无有效链接应回 INVALID_URL 文案，得到 %q", got)
	}
	if n := len(fa.cancelFor); n != 0 {
		t.Errorf("无效链接不应触达 access，得到 %d 次", n)
	}

	// 命中 1 个任务：已取消该任务，access 收到用户与链接
	opt, fa, fs = newHarness(t, 4)
	fa.cancelOut = 1
	run(opt, fs, "/cancel https://t.me/example_channel/42")
	if got := lastText(t, fs); got != "已取消该任务。" {
		t.Errorf("取消 1 个应回已取消该任务，得到 %q", got)
	}
	if len(fa.cancelFor) != 1 || fa.cancelFor[0] != 7 {
		t.Errorf("应以发起用户身份取消: %v", fa.cancelFor)
	}
	if len(fa.cancelRefs) != 1 || fa.cancelRefs[0].MessageID != 42 || fa.cancelRefs[0].Username != "example_channel" {
		t.Errorf("应携带解析后的链接: %+v", fa.cancelRefs)
	}

	// 命中多个（同链接重复提交）：逐个取消并报告数量
	opt2, fa2, fs2 := newHarness(t, 4)
	fa2.cancelOut = 3
	run(opt2, fs2, "/cancel https://t.me/example_channel/1")
	if got := lastText(t, fs2); got != "已取消 3 个任务。" {
		t.Errorf("取消多个应报告数量，得到 %q", got)
	}

	// 无匹配：没有进行中的任务
	opt3, _, fs3 := newHarness(t, 4)
	run(opt3, fs3, "/cancel https://t.me/example_channel/1")
	if got := lastText(t, fs3); got != "该链接当前没有进行中的任务。" {
		t.Errorf("无匹配应回无进行中任务，得到 %q", got)
	}

	// 存储故障：经 UserText 转文案
	opt4, fa4, fs4 := newHarness(t, 4)
	fa4.cancelErr = apperr.Wrap(apperr.CodeStoreUnavailable, errors.New("db down"))
	run(opt4, fs4, "/cancel https://t.me/example_channel/1")
	if got := lastText(t, fs4); got != apperr.UserText(apperr.CodeStoreUnavailable) {
		t.Errorf("存储故障应回 STORE_UNAVAILABLE 文案，得到 %q", got)
	}

	// 多链接：提示后只取第一条
	opt5, fa5, fs5 := newHarness(t, 4)
	fa5.cancelOut = 1
	run(opt5, fs5, "/cancel https://t.me/example_channel/1 https://t.me/example_channel/2")
	if got := fs5.texts()[0]; got != multiCancelNotice {
		t.Errorf("多链接取消应先提示，得到 %q", got)
	}
	if len(fa5.cancelRefs) != 1 || fa5.cancelRefs[0].MessageID != 1 {
		t.Errorf("只应取消第一条链接: %+v", fa5.cancelRefs)
	}
}

func TestHandleStatusAndHealth(t *testing.T) {
	opt, _, fs := newHarness(t, 8)
	opt.Status = StatusFunc(func(context.Context) (RuntimeStatus, error) {
		return RuntimeStatus{
			MTProtoState:   mtproto.StateReady,
			LargeFileReady: true,
			StoreReady:     true,
			QueueLen:       2,
			QueueCap:       8,
			Processing:     1,
			WorkerCount:    2,
		}, nil
	})
	run(opt, fs, "/status@spore extra")
	got := lastText(t, fs)
	for _, want := range []string{"用户号 MTProto：已就绪", "大文件直传通道：可用", "数据库：正常", "队列：2/8", "处理中：1", "Worker：2"} {
		if !strings.Contains(got, want) {
			t.Errorf("/status 应包含 %q，得到 %q", want, got)
		}
	}
	run(opt, fs, "/health")
	if got = lastText(t, fs); got != "健康：OK" {
		t.Errorf("健康服务应为 OK，得到 %q", got)
	}

	opt.Status = StatusFunc(func(context.Context) (RuntimeStatus, error) {
		return RuntimeStatus{MTProtoState: mtproto.StateOffline, StoreReady: false, WorkerCount: 1}, nil
	})
	run(opt, fs, "/health@spore")
	if got = lastText(t, fs); got != "健康：DEGRADED" {
		t.Errorf("依赖未就绪应为 DEGRADED，得到 %q", got)
	}

	opt.Status = StatusFunc(func(context.Context) (RuntimeStatus, error) { return RuntimeStatus{}, errors.New("probe failed") })
	run(opt, fs, "/health")
	if got = lastText(t, fs); got != "健康：UNAVAILABLE\n服务状态暂时不可用，请稍后重试。" {
		t.Errorf("状态查询失败应为 UNAVAILABLE，得到 %q", got)
	}
}

func TestHandleStatusUnavailableWithoutProvider(t *testing.T) {
	opt, _, fs := newHarness(t, 4)
	run(opt, fs, "/status")
	if got := lastText(t, fs); got != statusUnavailableText {
		t.Errorf("未注入状态提供器应返回稳定文案，得到 %q", got)
	}
}

func TestHelpListsPublicCommands(t *testing.T) {
	opt, _, fs := newHarness(t, 4)
	run(opt, fs, "/help")
	got := lastText(t, fs)
	for _, command := range []string{"/start", "/help", "/status", "/health", "/usage", "/cancel", "/download"} {
		if !strings.Contains(got, command) {
			t.Errorf("帮助文案应列出 %s，得到 %q", command, got)
		}
	}
	// /download 用法说明：目的地由管理员配置、纯文本不支持
	for _, want := range []string{"目的地", "默认目的地", "纯文本"} {
		if !strings.Contains(got, want) {
			t.Errorf("帮助文案的 /download 说明应包含 %q，得到 %q", want, got)
		}
	}
	if strings.Contains(got, "/whoami") {
		t.Errorf("隐藏调试命令不应出现在帮助文案：%q", got)
	}
	if strings.Contains(got, "<用户名>") || strings.Contains(got, "<消息ID>") {
		t.Errorf("帮助文案不应包含会被 Telegram HTML 模式误解析的尖括号占位符：%q", got)
	}
}

func TestHandleUsageAndWhoamiMisc(t *testing.T) {
	// /help 恒定帮助文案
	opt, _, fs := newHarness(t, 4)
	run(opt, fs, "/help@somebot")
	if got := lastText(t, fs); got != helpText(syscfg.DefaultName) {
		t.Errorf("/help@bot 应回帮助文案，得到 %q", got)
	}

	// 空文本（如纯媒体消息）
	run(opt, fs, "")
	if got := lastText(t, fs); got != textOnlyMsg {
		t.Errorf("空文本应回 textOnlyMsg，得到 %q", got)
	}

	// /whoami 未注入
	run(opt, fs, "/whoami")
	if got := lastText(t, fs); got != "该功能当前未启用，请联系管理员开通。" {
		t.Errorf("未注入 /whoami 应回不可用，得到 %q", got)
	}

	// /whoami 注入后回显
	opt.Whoami = func(context.Context) (string, error) { return "someone", nil }
	run(opt, fs, "/whoami")
	if got := lastText(t, fs); got != "MTProto 账号：someone" {
		t.Errorf("/whoami 应回显账号，得到 %q", got)
	}
}

// ---- /download 命令 ----

// fakeCloudStatus 可编程的云盘状态假实现（/download 预检）。
type fakeCloudStatus struct {
	enabled bool
	avail   bool
	dests   []string
	def     string
}

func (f fakeCloudStatus) Enabled() bool              { return f.enabled }
func (f fakeCloudStatus) Available() bool            { return f.avail }
func (f fakeCloudStatus) DefaultDestination() string { return f.def }
func (f fakeCloudStatus) DestinationEnabled(name string) bool {
	for _, d := range f.dests {
		if d == name {
			return true
		}
	}
	return false
}
func (f fakeCloudStatus) EnabledDestinations() []string { return append([]string(nil), f.dests...) }

// 命令分流与 @bot 后缀：/download@bot 链接 正常进入云盘路径并透传默认目的地。
func TestHandleDownloadDispatch(t *testing.T) {
	opt, fa, fs := newHarness(t, 4)
	opt.CloudStatus = fakeCloudStatus{enabled: true, avail: true, def: "mega-1", dests: []string{"mega-1", "mega-2"}}
	run(opt, fs, "/download@spore https://t.me/example_channel/42")

	texts := fs.texts()
	if len(texts) != 1 || texts[0] != cloudStatusPrompt {
		t.Fatalf("通过路径只应发送云盘占位提示: %v", texts)
	}
	subs := fa.submitted()
	if len(subs) != 1 {
		t.Fatalf("应恰好一次提交，得到 %d", len(subs))
	}
	if subs[0].CloudDest != "mega-1" || subs[0].Ref.MessageID != 42 || subs[0].StatusMsgID != 1 {
		t.Errorf("提交应透传默认目的地与占位: %+v", subs[0])
	}

	// 指定目的地形式：两段 → 目的地 + 链接
	opt2, fa2, fs2 := newHarness(t, 4)
	opt2.CloudStatus = fakeCloudStatus{enabled: true, avail: true, def: "mega-1", dests: []string{"mega-1", "mega-2"}}
	run(opt2, fs2, "/download mega-2 https://t.me/example_channel/7")
	subs2 := fa2.submitted()
	if len(subs2) != 1 || subs2[0].CloudDest != "mega-2" {
		t.Fatalf("两段形式应透传指定目的地: %+v", subs2)
	}
}

// 未授权/待审/停用：/download 的申请引导与裸链接逐字节一致，且不出现
// 任何云盘功能字样（AC1）；不为被拒用户发云盘占位提示。
func TestHandleDownloadUnauthorizedMatchesBareLink(t *testing.T) {
	for _, code := range []apperr.Code{
		apperr.CodeUserNotAuthorized, apperr.CodeUserPending, apperr.CodeUserDisabled,
	} {
		// 裸链接：Submit 第一步拒绝同一状态码
		opt, fa, fs := newHarness(t, 4)
		fa.decide = func(access.Submission) (access.Decision, error) {
			return access.Decision{Reason: code}, nil
		}
		run(opt, fs, "https://t.me/example_channel/1")
		bare := lastText(t, fs)

		// /download：状态预检返回同一码
		opt2, fa2, fs2 := newHarness(t, 4)
		fa2.statusCode = code
		opt2.CloudStatus = fakeCloudStatus{enabled: true, avail: true, def: "mega-1", dests: []string{"mega-1"}}
		run(opt2, fs2, "/download https://t.me/example_channel/1")
		texts := fs2.texts()
		if len(texts) != 1 {
			t.Fatalf("%s：预检拒绝只应回一条申请引导: %v", code, texts)
		}
		if got := texts[0]; got != bare {
			t.Errorf("%s：文案应与裸链接逐字节一致\nbare: %q\ngot:  %q", code, bare, got)
		}
		for _, forbidden := range []string{"网盘", "云盘", "目的地", "download"} {
			if strings.Contains(texts[0], forbidden) {
				t.Errorf("%s：申请引导不应出现功能字样 %q: %q", code, forbidden, texts[0])
			}
		}
		// 未触达 Submit（预检即止）
		if n := len(fa2.submitted()); n != 0 {
			t.Errorf("%s：预检拒绝不应触达 Submit，得到 %d 次", code, n)
		}
	}
}

// 无参/只给名称不给链接：用法提示，不触达 access 与云盘状态。
func TestHandleDownloadUsage(t *testing.T) {
	for _, text := range []string{"/download", "/download   ", "/download mega-1", "/download@bot"} {
		opt, fa, fs := newHarness(t, 4)
		opt.CloudStatus = fakeCloudStatus{enabled: true, avail: true, def: "mega-1", dests: []string{"mega-1"}}
		run(opt, fs, text)
		if got := lastText(t, fs); got != downloadUsage {
			t.Errorf("%q 应回用法提示，得到 %q", text, got)
		}
		if n := len(fa.submitted()); n != 0 {
			t.Errorf("%q 不应触达 access", text)
		}
	}
}

// 云盘状态分支：rclone 不可用 / 开关关闭 / 目的地无效（回可用列表）。
func TestHandleDownloadCloudStates(t *testing.T) {
	// rclone 不可用（优先于开关判定）
	opt, _, fs := newHarness(t, 4)
	opt.CloudStatus = fakeCloudStatus{enabled: true, avail: false, def: "mega-1", dests: []string{"mega-1"}}
	run(opt, fs, "/download https://t.me/example_channel/1")
	if got := lastText(t, fs); got != cloudUnavailableText {
		t.Errorf("rclone 不可用应回暂不可用，得到 %q", got)
	}

	// 开关关闭
	opt2, _, fs2 := newHarness(t, 4)
	opt2.CloudStatus = fakeCloudStatus{enabled: false, avail: true, def: "mega-1", dests: []string{"mega-1"}}
	run(opt2, fs2, "/download https://t.me/example_channel/1")
	if got := lastText(t, fs2); got != cloudDisabledText {
		t.Errorf("开关关闭应回未开启，得到 %q", got)
	}

	// 指定不存在的目的地：回可用列表
	opt3, fa3, fs3 := newHarness(t, 4)
	opt3.CloudStatus = fakeCloudStatus{enabled: true, avail: true, def: "mega-1", dests: []string{"mega-1", "mega-2"}}
	run(opt3, fs3, "/download nosuch https://t.me/example_channel/1")
	if got := lastText(t, fs3); got != "未找到该下载目的地，可用：mega-1、mega-2。" {
		t.Errorf("无效目的地应回可用列表，得到 %q", got)
	}
	if n := len(fa3.submitted()); n != 0 {
		t.Errorf("无效目的地不应触达 Submit，得到 %d 次", n)
	}
}

// 用户级下载权限拒绝：授权用户无权限（预检返回 CLOUD_DOWNLOAD_DENIED）
// 回专门文案，优先于云盘状态判定，不触达 Submit。
func TestHandleDownloadPermissionDenied(t *testing.T) {
	opt, fa, fs := newHarness(t, 4)
	opt.CloudStatus = fakeCloudStatus{enabled: true, avail: true, def: "mega-1", dests: []string{"mega-1"}}
	fa.statusCode = apperr.CodeCloudDownloadDenied
	run(opt, fs, "/download https://t.me/example_channel/1")
	if got := lastText(t, fs); got != apperr.UserText(apperr.CodeCloudDownloadDenied) {
		t.Errorf("无下载权限应回专门拒绝文案，得到 %q", got)
	}
	if n := len(fa.submitted()); n != 0 {
		t.Errorf("权限拒绝不应触达 Submit，得到 %d 次", n)
	}

	// 权限预检在云盘状态之前：全局开关关闭时权限拒绝文案优先级不变
	// （预检先回，开关文案不出现）
	opt2, fa2, fs2 := newHarness(t, 4)
	opt2.CloudStatus = fakeCloudStatus{enabled: false, avail: true, def: "mega-1", dests: []string{"mega-1"}}
	fa2.statusCode = apperr.CodeCloudDownloadDenied
	run(opt2, fs2, "/download https://t.me/example_channel/1")
	if got := lastText(t, fs2); got != apperr.UserText(apperr.CodeCloudDownloadDenied) {
		t.Errorf("权限拒绝应先于开关判定，得到 %q", got)
	}
}

// 链接解析与裸链接同规则：非法链接回 INVALID_URL；多链接逐条提交到同一目的地。
func TestHandleDownloadLinkParsing(t *testing.T) {
	opt, fa, fs := newHarness(t, 4)
	opt.CloudStatus = fakeCloudStatus{enabled: true, avail: true, def: "mega-1", dests: []string{"mega-1"}}
	run(opt, fs, "/download mega-1 hello world")
	if got := lastText(t, fs); got != apperr.UserText(apperr.CodeInvalidURL) {
		t.Errorf("无有效链接应回 INVALID_URL，得到 %q", got)
	}
	if n := len(fa.submitted()); n != 0 {
		t.Errorf("非法链接不应触达 access，得到 %d 次", n)
	}

	opt2, fa2, fs2 := newHarness(t, 4)
	opt2.CloudStatus = fakeCloudStatus{enabled: true, avail: true, def: "mega-1", dests: []string{"mega-1"}}
	run(opt2, fs2, "/download https://t.me/example_channel/1 https://t.me/example_channel/2")
	texts := fs2.texts()
	if len(texts) != 3 || texts[0] != cloudStatusPrompt || texts[1] != cloudStatusPrompt ||
		texts[2] != "批量提交完成：成功 2 条，失败 0 条。" {
		t.Fatalf("多链接应逐条创建云盘占位后汇总: %v", texts)
	}
	subs := fa2.submitted()
	if len(subs) != 2 || subs[0].Ref.MessageID != 1 || subs[1].Ref.MessageID != 2 ||
		subs[0].CloudDest != "mega-1" || subs[1].CloudDest != "mega-1" {
		t.Fatalf("应按顺序提交全部链接到同一目的地: %+v", subs)
	}
}

// 队列饱和快速路径：直接回繁忙，不触达 access、不发占位提示。
func TestHandleDownloadQueueFullPreflight(t *testing.T) {
	opt, fa, fs := newHarness(t, 1)
	opt.CloudStatus = fakeCloudStatus{enabled: true, avail: true, def: "mega-1", dests: []string{"mega-1"}}
	if err := opt.Queue.Enqueue(queue.NewJob(9, 9, tmeurl.SourceRef{Kind: tmeurl.PeerUsername, Username: "x", MessageID: 1}, 0, 0)); err != nil {
		t.Fatalf("占位失败: %v", err)
	}
	run(opt, fs, "/download https://t.me/example_channel/1")
	texts := fs.texts()
	if len(texts) != 1 || texts[0] != apperr.UserText(apperr.CodeQueueFull) {
		t.Fatalf("饱和时应直接回繁忙: %v", texts)
	}
	if n := len(fa.submitted()); n != 0 {
		t.Errorf("饱和快速路径不应触达 access: %d", n)
	}
}

// Submit 拒绝/失败路径与 handleLinkWithProfile 同构：清理云盘占位 + UserText。
func TestHandleDownloadDenialsUseUserText(t *testing.T) {
	for _, reason := range []apperr.Code{
		apperr.CodeDuplicateLink, apperr.CodeSubmitRateLimited, apperr.CodeQuotaExceeded,
		apperr.CodeConcurrentLimited, apperr.CodeQueueFull,
	} {
		opt, fa, fs := newHarness(t, 4)
		fa.decide = func(access.Submission) (access.Decision, error) {
			return access.Decision{Reason: reason}, nil
		}
		opt.CloudStatus = fakeCloudStatus{enabled: true, avail: true, def: "mega-1", dests: []string{"mega-1"}}
		run(opt, fs, "/download https://t.me/example_channel/1")

		texts := fs.texts()
		if len(texts) != 2 || texts[0] != cloudStatusPrompt {
			t.Fatalf("%s：应先云盘占位后提示，得到 %v", reason, texts)
		}
		if got := texts[1]; got != apperr.UserText(reason) {
			t.Errorf("%s：文案应经 UserText，得到 %q", reason, got)
		}
		if ids := fs.deletedIDs(); len(ids) != 1 || ids[0] != 1 {
			t.Errorf("%s：云盘占位应被删除，得到 %v", reason, ids)
		}
	}

	// 存储故障：清理占位并回 STORE_UNAVAILABLE 文案
	opt, fa, fs := newHarness(t, 4)
	fa.decide = func(access.Submission) (access.Decision, error) {
		return access.Decision{}, apperr.Wrap(apperr.CodeStoreUnavailable, errors.New("db down"))
	}
	opt.CloudStatus = fakeCloudStatus{enabled: true, avail: true, def: "mega-1", dests: []string{"mega-1"}}
	run(opt, fs, "/download https://t.me/example_channel/1")
	texts := fs.texts()
	if len(texts) != 2 || texts[1] != apperr.UserText(apperr.CodeStoreUnavailable) {
		t.Fatalf("存储错误应回 STORE_UNAVAILABLE 文案: %v", texts)
	}
	if len(fs.deletedIDs()) != 1 {
		t.Errorf("存储错误也应清理云盘占位: %v", fs.deletedIDs())
	}
}

// 未注入 CloudStatus：按既有可选依赖惯例回不可用；状态预检存储故障走 UserText。
func TestHandleDownloadEdgeStates(t *testing.T) {
	opt, _, fs := newHarness(t, 4)
	run(opt, fs, "/download https://t.me/example_channel/1")
	if got := lastText(t, fs); got != "该功能当前未启用，请联系管理员开通。" {
		t.Errorf("未注入云盘状态应回不可用，得到 %q", got)
	}

	// 状态预检存储故障：受控文案，不暴露功能细节
	opt2, _, fs2 := newHarness(t, 4)
	opt2.Access = statusErrAccess{&fakeAccess{
		statusErr: apperr.Wrap(apperr.CodeStoreUnavailable, errors.New("db down")),
	}}
	opt2.CloudStatus = fakeCloudStatus{enabled: true, avail: true, def: "mega-1", dests: []string{"mega-1"}}
	run(opt2, fs2, "/download https://t.me/example_channel/1")
	if got := lastText(t, fs2); got != apperr.UserText(apperr.CodeStoreUnavailable) {
		t.Errorf("预检存储故障应回 STORE_UNAVAILABLE 文案，得到 %q", got)
	}
}

// statusErrAccess 固定预检返回存储故障的假实现。
type statusErrAccess struct{ *fakeAccess }

func (f statusErrAccess) UserDownloadStatus(context.Context, int64) (apperr.Code, error) {
	return "", apperr.Wrap(apperr.CodeStoreUnavailable, errors.New("db down"))
}
