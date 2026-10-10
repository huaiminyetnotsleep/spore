// Package recovery 恢复已处理历史到显式目标；不改变原索引、绑定或普通请求。
package recovery

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/binding"
	"github.com/huaiminyetnotsleep/spore/internal/botpool"
	"github.com/huaiminyetnotsleep/spore/internal/delivery"
	"github.com/huaiminyetnotsleep/spore/internal/queue"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// Input 是预检与创建共用的HTTP请求契约。
type Input struct {
	Filter store.RecoveryFilter `json:"filter"`
	Target string               `json:"target"`
	BotID  int64                `json:"bot_id"`
}

// CacheChannel 是缓存权限提示，并不保证每条缓存消息仍存在。
type CacheChannel struct {
	ChannelID int64  `json:"channel_id"`
	Readable  bool   `json:"readable"`
	Message   string `json:"message"`
}

// Preview 是只读预检，不发送或删除试探消息。
type Preview struct {
	TargetChatID  int64          `json:"target_chat_id"`
	TargetTitle   string         `json:"target_title"`
	BotID         int64          `json:"bot_id"`
	Total         int            `json:"total"`
	WithCache     int            `json:"with_cache"`
	WithoutCache  int            `json:"without_cache"`
	CacheChannels []CacheChannel `json:"cache_channels"`
	Warnings      []string       `json:"warnings"`
}

// SourceSender 只复用源取数转换链路；返回已知ID和是否进入发送边界。
type SourceSender func(context.Context, store.RecoveryItem, int64, delivery.Sender) (queue.RecoveryResult, error)

// Runtime 的源客户端仅可在Run所拥有的生命周期内使用。
type Runtime struct {
	Source SourceSender
	Queue  *queue.Queue
}
type runtimeLease struct {
	ctx context.Context
	rt  Runtime
	ops sync.WaitGroup
}
type activeJob struct {
	id     int64
	cancel context.CancelFunc
	done   chan struct{}
}

// Service 承载持久化控制和唯一低速runner，不持锁执行网络或SQL。
type Service struct {
	st      *store.Store
	pool    *botpool.Pool
	mu      sync.Mutex
	runtime *runtimeLease
	active  *activeJob
	blocked map[int64]bool
	wake    chan struct{}
	control chan struct{}
	halt    error
	pace    time.Duration
}

// New 在进程启动一次性核对遗留状态；不可在每次MT重连重新构造。
func New(ctx context.Context, st *store.Store, pool *botpool.Pool) (*Service, error) {
	if st == nil || pool == nil {
		return nil, apperr.New(apperr.CodeInternal, "恢复依赖缺失")
	}
	if err := st.ReconcileRecovery(ctx); err != nil {
		return nil, err
	}
	s := &Service{st: st, pool: pool, blocked: map[int64]bool{}, wake: make(chan struct{}, 1), control: make(chan struct{}, 1), pace: time.Second}
	s.control <- struct{}{}
	return s, nil
}
func (s *Service) notify() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
func unavailable() error { return apperr.New(apperr.CodeNetworkError, "恢复运行时离线") }
func (s *Service) lease(ctx context.Context) (context.Context, *runtimeLease, func(), error) {
	s.mu.Lock()
	r := s.runtime
	halt := s.halt
	if r == nil || r.ctx.Err() != nil || halt != nil {
		s.mu.Unlock()
		if halt != nil {
			return nil, nil, nil, halt
		}
		return nil, nil, nil, unavailable()
	}
	r.ops.Add(1)
	s.mu.Unlock()
	c, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(r.ctx, cancel)
	done := func() { stop(); cancel(); r.ops.Done() }
	return c, r, done, nil
}
func (s *Service) bot(id int64) (*botpool.Member, error) {
	if id < 0 {
		return nil, apperr.New(apperr.CodeInvalidURL, "机器人ID无效")
	}
	snapshots := s.pool.Snapshots()
	if id == 0 {
		for _, v := range snapshots {
			if v.Online && !v.Disabled {
				id = v.ID
				break
			}
		}
	}
	for _, v := range snapshots {
		if v.ID == id {
			if v.Disabled {
				return nil, apperr.New(apperr.CodeBotDisabled, "")
			}
			if !v.Online {
				return nil, unavailable()
			}
			m := s.pool.MemberByID(id)
			if m != nil && m.BotAPI != nil && m.Sender != nil && !m.IsDisabled() {
				return m, nil
			}
			return nil, unavailable()
		}
	}
	return nil, unavailable()
}
func verify(ctx context.Context, m *botpool.Member, target any) (*models.ChatFullInfo, error) {
	c, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	chat, err := m.BotAPI.GetChat(c, &tgbot.GetChatParams{ChatID: target})
	if err != nil {
		return nil, binding.ClassifyVerifyError(err, apperr.CodeChannelTargetInvalid)
	}
	if chat.Type != models.ChatTypeChannel && chat.Type != models.ChatTypeSupergroup || chat.IsForum {
		return nil, apperr.New(apperr.CodeChannelTargetInvalid, "目标必须是频道或非论坛超级群组")
	}
	member, err := m.BotAPI.GetChatMember(c, &tgbot.GetChatMemberParams{ChatID: chat.ID, UserID: m.ID})
	if err != nil {
		return nil, binding.ClassifyVerifyError(err, apperr.CodeChannelNotPostable)
	}
	if member.Type == models.ChatMemberTypeOwner {
		return chat, nil
	}
	if member.Type == models.ChatMemberTypeAdministrator && member.Administrator != nil && (chat.Type == models.ChatTypeSupergroup || member.Administrator.CanPostMessages) {
		return chat, nil
	}
	return nil, apperr.New(apperr.CodeChannelNotPostable, "")
}
func (s *Service) prepare(ctx context.Context, in Input) (Preview, []store.RecoveryItem, error) {
	var p Preview
	p.CacheChannels = []CacheChannel{}
	p.Warnings = []string{}
	m, err := s.bot(in.BotID)
	if err != nil {
		return p, nil, err
	}
	target, err := binding.ParseChannelTarget(in.Target)
	if err != nil {
		return p, nil, err
	}
	chat, err := verify(ctx, m, target.ChatParams().ChatID)
	if err != nil {
		return p, nil, err
	}
	items, warnings, err := s.st.RecoveryCandidates(ctx, in.Filter)
	if err != nil {
		return p, nil, err
	}
	p.TargetChatID = chat.ID
	p.TargetTitle = chat.Title
	p.BotID = m.ID
	p.Total = len(items)
	p.Warnings = warnings
	checked := map[int64]bool{}
	for _, it := range items {
		if len(it.CacheCopies) > 0 {
			p.WithCache++
		} else {
			p.WithoutCache++
		}
		for _, c := range it.CacheCopies {
			if checked[c.ChatID] {
				continue
			}
			checked[c.ChatID] = true
			readable := false
			probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			_, e := m.BotAPI.GetChat(probeCtx, &tgbot.GetChatParams{ChatID: c.ChatID})
			if e == nil {
				member, me := m.BotAPI.GetChatMember(probeCtx, &tgbot.GetChatMemberParams{ChatID: c.ChatID, UserID: m.ID})
				readable = me == nil && (member.Type == models.ChatMemberTypeOwner || member.Type == models.ChatMemberTypeAdministrator || member.Type == models.ChatMemberTypeMember)
			}
			cancel()
			msg := "机器人可访问缓存频道，消息存在性未逐条验证。"
			if !readable {
				msg = "无法确认缓存频道可读，将在明确无投递时尝试原来源。"
			}
			p.CacheChannels = append(p.CacheChannels, CacheChannel{c.ChatID, readable, msg})
		}
	}
	return p, items, nil
}

// Preview 只读目标与缓存权限预检。
func (s *Service) Preview(ctx context.Context, in Input) (Preview, error) {
	c, _, done, err := s.lease(ctx)
	if err != nil {
		return Preview{}, err
	}
	defer done()
	p, _, err := s.prepare(c, in)
	return p, err
}
func (s *Service) lockControl(ctx context.Context) (func(), error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.control:
		return func() { s.control <- struct{}{} }, nil
	}
}

// Create 重新预检并固定真实bot、目标和历史快照，runner忙时返回冲突。
func (s *Service) Create(ctx context.Context, in Input) (store.RecoveryJob, error) {
	unlock, err := s.lockControl(ctx)
	if err != nil {
		return store.RecoveryJob{}, err
	}
	defer unlock()
	c, _, done, err := s.lease(ctx)
	if err != nil {
		return store.RecoveryJob{}, err
	}
	defer done()
	if _, err = s.st.RunningRecoveryJob(c); err == nil {
		return store.RecoveryJob{}, apperr.New(apperr.CodeStoreConstraint, "另一个恢复任务正在运行")
	} else if !errors.Is(err, store.ErrNotFound) {
		return store.RecoveryJob{}, err
	}
	p, items, err := s.prepare(c, in)
	if err != nil {
		return store.RecoveryJob{}, err
	}
	if len(items) == 0 {
		return store.RecoveryJob{}, apperr.New(apperr.CodeInvalidURL, "历史范围为空")
	}
	key, err := store.NormalizeRecoveryKey(in.Filter.ChannelKey)
	if err != nil {
		return store.RecoveryJob{}, err
	}
	in.Filter.ChannelKey = key
	j, err := s.st.CreateRecoveryJob(c, store.RecoveryJob{TargetChatID: p.TargetChatID, TargetTitle: p.TargetTitle, BotID: p.BotID, Filter: in.Filter}, items)
	if err == nil {
		s.notify()
	}
	return j, err
}

// Control 暂停/取消先阻止新认领并等待在途结果落库；继续/重试重新校验目标。
func (s *Service) Control(ctx context.Context, id int64, action string) (store.RecoveryJob, error) {
	unlock, err := s.lockControl(ctx)
	if err != nil {
		return store.RecoveryJob{}, err
	}
	defer unlock()
	j, err := s.st.GetRecoveryJob(ctx, id)
	if err != nil {
		return j, err
	}
	switch action {
	case "pause", "cancel":
		// Once cancellation is initiated, finish its local durable transition even
		// if the HTTP request goes away; otherwise running+blocked can get stuck.
		controlCtx, stopControl := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer stopControl()
		committed := false
		defer func() {
			if !committed {
				s.mu.Lock()
				delete(s.blocked, id)
				s.mu.Unlock()
				s.notify()
			}
		}()
		ctx = controlCtx
		s.mu.Lock()
		s.blocked[id] = true
		a := s.active
		s.mu.Unlock()
		if a != nil && a.id == id {
			a.cancel()
			select {
			case <-a.done:
			case <-ctx.Done():
				err = apperr.Wrap(apperr.CodeNetworkError, ctx.Err())
				s.haltWith(err)
				return store.RecoveryJob{}, err
			}
		}
		s.mu.Lock()
		halt := s.halt
		s.mu.Unlock()
		if halt != nil {
			return store.RecoveryJob{}, halt
		}
		// Runtime cancellation may already have paused the job; still audit explicit pause.
		j, err = s.st.GetRecoveryJob(ctx, id)
		if err != nil {
			return j, err
		}
		if action == "pause" && j.Status == "paused" {
			return j, apperr.New(apperr.CodeStoreConstraint, "任务已经暂停")
		}
		j, err = s.st.ControlRecoveryJob(ctx, id, action)
		committed = err == nil
		if err != nil && apperr.From(err).Code == apperr.CodeStoreUnavailable {
			s.haltWith(err)
		}
	case "resume", "retry":
		c, _, done, e := s.lease(ctx)
		if e != nil {
			return j, e
		}
		defer done()
		m, e := s.bot(j.BotID)
		if e != nil {
			return j, e
		}
		if _, e = verify(c, m, j.TargetChatID); e != nil {
			return j, e
		}
		j, err = s.st.ControlRecoveryJob(c, id, action)
		if err == nil {
			s.mu.Lock()
			delete(s.blocked, id)
			s.mu.Unlock()
			s.notify()
		}
	default:
		return j, apperr.New(apperr.CodeInvalidURL, "恢复动作无效")
	}
	return j, err
}

// List 返回普通列表数据，HTTP信封由web封装。
func (s *Service) List(ctx context.Context, p, n int) ([]store.RecoveryJob, int, error) {
	return s.st.ListRecoveryJobs(ctx, p, n)
}

// Get 返回单任务。
func (s *Service) Get(ctx context.Context, id int64) (store.RecoveryJob, error) {
	return s.st.GetRecoveryJob(ctx, id)
}

// Items 返回分页条目。
func (s *Service) Items(ctx context.Context, id int64, status string, p, n int) ([]store.RecoveryItem, int, error) {
	if _, err := s.st.GetRecoveryJob(ctx, id); err != nil {
		return nil, 0, err
	}
	return s.st.ListRecoveryItems(ctx, id, status, p, n)
}

// Run 装配MT就绪运行时并阻塞至取消。返回前取消并join所有租约，不复用失效API。
func (s *Service) Run(ctx context.Context, rt Runtime) (runErr error) {
	if rt.Source == nil {
		return apperr.New(apperr.CodeInternal, "恢复源适配缺失")
	}
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	r := &runtimeLease{ctx: rctx, rt: rt}
	s.mu.Lock()
	if s.runtime != nil {
		s.mu.Unlock()
		return apperr.New(apperr.CodeStoreConstraint, "恢复runner已经装配")
	}
	s.runtime = r
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.runtime = nil
		s.mu.Unlock()
		cancel()
		r.ops.Wait()
		c, stop := finalContext(ctx)
		defer stop()
		j, e := s.st.RunningRecoveryJob(c)
		if e == nil {
			e = s.pause(c, j, "运行时离线，复核后继续。")
		} else if errors.Is(e, store.ErrNotFound) {
			e = nil
		}
		if e != nil {
			s.haltWith(e)
			if runErr == nil {
				runErr = e
			}
		}
	}()
	ticker := time.NewTicker(s.pace)
	defer ticker.Stop()
	for {
		select {
		case <-rctx.Done():
			return nil
		case <-ticker.C:
		case <-s.wake:
		}
		s.mu.Lock()
		halt := s.halt
		s.mu.Unlock()
		if halt != nil {
			return halt
		}
		j, err := s.st.RunningRecoveryJob(rctx)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			if rctx.Err() != nil {
				return nil
			}
			s.haltWith(err)
			return err
		}
		s.mu.Lock()
		if s.blocked[j.ID] {
			s.mu.Unlock()
			continue
		}
		jctx, jcancel := context.WithCancel(rctx)
		a := &activeJob{id: j.ID, cancel: jcancel, done: make(chan struct{})}
		s.active = a
		s.mu.Unlock()
		err = s.runOne(jctx, r.rt, j)
		ticker.Reset(s.pace) // no accumulated ticker credit after a long send
		jcancel()
		s.mu.Lock()
		if s.active == a {
			s.active = nil
		}
		s.mu.Unlock()
		close(a.done)
		if err != nil {
			s.haltWith(err)
			return err
		}
	}
}
func (s *Service) haltWith(err error) { s.mu.Lock(); s.halt = err; s.mu.Unlock() }
func finalContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
}
func (s *Service) pause(ctx context.Context, j store.RecoveryJob, msg string) error {
	c, cancel := finalContext(ctx)
	defer cancel()
	err := s.st.SetRecoveryJobState(c, j.ID, "paused", msg)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	return err
}
func (s *Service) runOne(ctx context.Context, rt Runtime, j store.RecoveryJob) error {
	// Revalidation is read-only and before claim. Target loss does not consume a pending item.
	m, err := s.bot(j.BotID)
	if err != nil {
		return s.pause(ctx, j, "执行机器人不可用，请复核后继续。")
	}
	if _, err = verify(ctx, m, j.TargetChatID); err != nil {
		if ctx.Err() != nil {
			return s.cancelledRuntime(ctx, j)
		}
		return s.pause(ctx, j, "目标权限检查失败，请复核后继续。")
	}
	if ctx.Err() != nil {
		return s.cancelledRuntime(ctx, j)
	}
	it, err := s.st.ClaimRecoveryItem(ctx, j.ID)
	if errors.Is(err, store.ErrNotFound) {
		latest, e := s.st.GetRecoveryJob(ctx, j.ID)
		if e != nil {
			return e
		}
		if latest.Pending+latest.Processing == 0 {
			c, cancel := finalContext(ctx)
			defer cancel()
			return s.st.SetRecoveryJobState(c, j.ID, "completed", "")
		}
		return nil
	}
	if err != nil {
		if ctx.Err() != nil {
			return s.cancelledRuntime(ctx, j)
		}
		return err
	}
	if it.Status != "processing" {
		return nil
	} // claim reconciled another job's known output
	it.SentIDs = []int{}
	it.Status = "pending"
	pauseTarget := false
	if ctx.Err() == nil {
		pauseTarget = s.deliver(ctx, rt, j, m, &it)
	}
	c, cancel := finalContext(ctx)
	defer cancel()
	if err = s.st.FinishRecoveryItem(c, it); err != nil {
		return err
	}
	if pauseTarget {
		return s.pause(ctx, j, "目标不可用，请复核权限后继续。")
	}
	if ctx.Err() != nil {
		return s.cancelledRuntime(ctx, j)
	}
	return nil
}
func (s *Service) cancelledRuntime(ctx context.Context, j store.RecoveryJob) error {
	s.mu.Lock()
	blocked := s.blocked[j.ID]
	s.mu.Unlock()
	if blocked {
		return nil
	}
	return s.pause(ctx, j, "运行时离线，复核后继续。")
}

// definiteRejection requires a structured Telegram rejection. Unknown strings,
// transport failures and successful-but-short copy results never trigger fallback.
func definiteRejection(err error) bool {
	return errors.Is(err, tgbot.ErrorBadRequest) || errors.Is(err, tgbot.ErrorForbidden) || errors.Is(err, tgbot.ErrorNotFound) || errors.Is(err, tgbot.ErrorUnauthorized)
}
func setFailure(it *store.RecoveryItem, status string, err error) {
	ae := apperr.From(err)
	it.Status = status
	it.ErrorCode = string(ae.Code)
	it.ErrorMessage = apperr.UserText(ae.Code)
}
func (s *Service) deliver(ctx context.Context, rt Runtime, j store.RecoveryJob, m *botpool.Member, it *store.RecoveryItem) bool {
	for _, copy := range it.CacheCopies {
		if copy.ChatID == 0 || len(copy.MessageIDs) == 0 {
			continue
		}
		if ctx.Err() != nil {
			return false
		}
		it.Method = "cache"
		ids, err := m.Sender.CopyMessages(ctx, copy.ChatID, j.TargetChatID, copy.MessageIDs)
		it.SentIDs = append(it.SentIDs, positiveIDs(ids)...)
		if err == nil && len(ids) == len(copy.MessageIDs) && len(it.SentIDs) == len(copy.MessageIDs) {
			it.Status = "succeeded"
			return false
		}
		if len(it.SentIDs) > 0 || err == nil || !definiteRejection(err) {
			if err == nil {
				err = apperr.New(apperr.CodeSendFailed, "缓存复制返回数量不一致")
			}
			setFailure(it, "uncertain", err)
			return false
		}
		if errors.Is(err, tgbot.ErrorUnauthorized) {
			it.Status = "pending"
			return true
		}
		// Copy rejection can refer to the SOURCE chat. Confirm target permission
		// separately instead of misclassifying a lost cache as a lost target.
		if _, e := verify(ctx, m, j.TargetChatID); e != nil {
			it.Status = "pending"
			return ctx.Err() == nil
		}
	}
	if ctx.Err() != nil {
		it.Method = ""
		return false
	}
	// Source-heavy fallback gets an exclusive idle maintenance lease; this prevents
	// competing normal downloads and tmp-orphan cleanup while source media is alive.
	var release func()
	if rt.Queue != nil {
		for {
			var ok bool
			release, ok = rt.Queue.TryMaintenance()
			if ok {
				break
			}
			timer := time.NewTimer(s.pace)
			select {
			case <-ctx.Done():
				timer.Stop()
				it.Method = ""
				return false
			case <-timer.C:
			}
		}
		defer release()
	}
	// Revalidate after waiting for an idle window: cached permission can be stale.
	if _, err := verify(ctx, m, j.TargetChatID); err != nil {
		it.Method = ""
		return ctx.Err() == nil
	}
	if m.IsDisabled() || ctx.Err() != nil {
		return true
	}
	it.Method = "source"
	result, err := rt.Source(ctx, *it, j.TargetChatID, m.Sender)
	it.SentIDs = positiveIDs(result.SentIDs)
	if err == nil && len(it.SentIDs) >= len(it.MemberIDs) && len(it.SentIDs) > 0 {
		it.Status = "succeeded"
		return false
	}
	if err == nil {
		err = apperr.New(apperr.CodeSendFailed, "来源发送没有完整返回坐标")
	}
	if len(it.SentIDs) > 0 || result.Began && !definiteRejection(err) {
		setFailure(it, "uncertain", err)
		return false
	}
	if result.Began && definiteRejection(err) && apperr.From(err).Code == apperr.CodeSendTargetInvalid {
		it.Status = "pending"
		return true
	}
	status := "failed"
	switch apperr.From(err).Code {
	case apperr.CodeChannelInaccessible, apperr.CodeMessageNotFound, apperr.CodeServiceMessage, apperr.CodeMediaUnsupported:
		status = "unrecoverable"
	}
	setFailure(it, status, err)
	return false
}
func positiveIDs(ids []int) []int {
	out := []int{}
	for _, id := range ids {
		if id > 0 {
			out = append(out, id)
		}
	}
	return out
}

// TargetString 供装配/测试将已固定ID转为明确目标，不重新解析用户名。
func TargetString(id int64) string { return strconv.FormatInt(id, 10) }
