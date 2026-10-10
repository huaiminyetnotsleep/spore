package recovery

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/botpool"
	"github.com/huaiminyetnotsleep/spore/internal/delivery"
	"github.com/huaiminyetnotsleep/spore/internal/queue"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

type fakeCopy struct {
	delivery.Sender
	copy  func(context.Context, int64, int64, []int) ([]int, error)
	calls atomic.Int64
}

func (f *fakeCopy) CopyMessages(c context.Context, from, to int64, ids []int) ([]int, error) {
	f.calls.Add(1)
	return f.copy(c, from, to, ids)
}

type permissionServer struct {
	mu       sync.Mutex
	methods  []string
	chatType string
	forum    bool
	admin    bool
	post     bool
}

func testMember(t *testing.T, id int64, sender delivery.Sender) (*botpool.Member, *permissionServer) {
	t.Helper()
	p := &permissionServer{chatType: "channel", admin: true, post: true}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.methods = append(p.methods, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/getMe"):
			fmt.Fprintf(w, `{"ok":true,"result":{"id":%d,"is_bot":true,"first_name":"test"}}`, id)
		case strings.HasSuffix(r.URL.Path, "/getChat"):
			fmt.Fprintf(w, `{"ok":true,"result":{"id":-100999,"type":%q,"title":"target","is_forum":%t}}`, p.chatType, p.forum)
		case strings.HasSuffix(r.URL.Path, "/getChatMember"):
			status := "member"
			if p.admin {
				status = "administrator"
			}
			fmt.Fprintf(w, `{"ok":true,"result":{"status":%q,"user":{"id":%d,"is_bot":true},"can_post_messages":%t}}`, status, id, p.post)
		default:
			t.Errorf("unexpected write call %s", r.URL.Path)
			fmt.Fprint(w, `{"ok":false,"error_code":400,"description":"bad"}`)
		}
	}))
	t.Cleanup(srv.Close)
	b, err := tgbot.New(fmt.Sprintf("%d:test", id), tgbot.WithServerURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	return &botpool.Member{ID: id, BotAPI: b, Sender: sender}, p
}
func testService(t *testing.T, withCache bool) (*Service, *store.Store, *fakeCopy, *permissionServer) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if withCache {
		_, err = st.InsertDumpEntry(context.Background(), store.DumpEntry{ChannelKey: "news", MessageID: 1, DumpIDs: []int{10, 11}, DumpChannelID: -10088})
	} else {
		_, err = st.InsertWatchEvent(context.Background(), store.WatchEvent{ChannelID: -100123, MessageID: 1, MemberIDs: []int{1}, Path: store.WatchPathFallback})
	}
	if err != nil {
		t.Fatal(err)
	}
	if withCache {
		if _, err = st.InsertWatchEvent(context.Background(), store.WatchEvent{ChannelID: -100123, Username: "news", MessageID: 1, MemberIDs: []int{1, 2}, Path: store.WatchPathCopy}); err != nil {
			t.Fatal(err)
		}
		if _, err = st.InsertDumpEntry(context.Background(), store.DumpEntry{ChannelKey: "news", MessageID: 2, DumpIDs: []int{10, 11}, DumpChannelID: -10088}); err != nil {
			t.Fatal(err)
		}
	}
	f := &fakeCopy{copy: func(context.Context, int64, int64, []int) ([]int, error) { return []int{100, 101}, nil }}
	m, p := testMember(t, 100, f)
	pool := botpool.New()
	pool.Reset([]*botpool.Member{m})
	s, err := New(context.Background(), st, pool)
	if err != nil {
		t.Fatal(err)
	}
	s.pace = 5 * time.Millisecond
	return s, st, f, p
}
func attachTest(s *Service) {
	s.mu.Lock()
	s.runtime = &runtimeLease{ctx: context.Background()}
	s.mu.Unlock()
}
func createFixture(t *testing.T, s *Service) store.RecoveryJob {
	t.Helper()
	items, _, err := s.st.RecoveryCandidates(context.Background(), store.RecoveryFilter{})
	if err != nil {
		t.Fatal(err)
	}
	j, err := s.st.CreateRecoveryJob(context.Background(), store.RecoveryJob{TargetChatID: -100999, BotID: 100, TargetTitle: "target"}, items)
	if err != nil {
		t.Fatal(err)
	}
	return j
}
func itemOf(t *testing.T, s *Service, j store.RecoveryJob) store.RecoveryItem {
	t.Helper()
	items, _, err := s.Items(context.Background(), j.ID, "", 1, 20)
	if err != nil || len(items) != 1 {
		t.Fatalf("items %+v %v", items, err)
	}
	return items[0]
}
func noSource(c context.Context, i store.RecoveryItem, target int64, sender delivery.Sender) (queue.RecoveryResult, error) {
	return queue.RecoveryResult{}, errors.New("source must not run")
}

func TestPreviewReadOnlyAndStrictBot(t *testing.T) {
	s, _, first, p := testService(t, true)
	attachTest(s)
	second := &fakeCopy{copy: first.copy}
	m, _ := testMember(t, 200, second)
	s.pool.Reset([]*botpool.Member{s.pool.MemberByID(100), m})
	preview, err := s.Preview(context.Background(), Input{Target: "@target", BotID: 200})
	if err != nil || preview.BotID != 200 || preview.Total != 1 {
		t.Fatalf("preview %+v %v", preview, err)
	}
	if first.calls.Load()+second.calls.Load() != 0 {
		t.Fatal("preview sent messages")
	}
	p.mu.Lock()
	for _, method := range p.methods {
		if !strings.HasSuffix(method, "/getMe") {
			t.Fatal("explicit bot fell back", method)
		}
	}
	p.mu.Unlock()
	if _, err = s.Preview(context.Background(), Input{Target: "@target", BotID: 300}); apperr.From(err).Code != apperr.CodeNetworkError {
		t.Fatal(err)
	}
	m.SetDisabled(true)
	if _, err = s.Preview(context.Background(), Input{Target: "@target", BotID: 200}); apperr.From(err).Code != apperr.CodeBotDisabled {
		t.Fatal(err)
	}
}
func TestTargetPermissions(t *testing.T) {
	for _, tc := range []struct {
		name, kind         string
		forum, admin, post bool
		ok                 bool
	}{{"channel admin", "channel", false, true, true, true}, {"no channel post", "channel", false, true, false, false}, {"supergroup admin", "supergroup", false, true, false, true}, {"supergroup member conservative", "supergroup", false, false, true, false}, {"forum", "supergroup", true, true, true, false}, {"private", "private", false, true, true, false}} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _, p := testService(t, true)
			p.mu.Lock()
			p.chatType = tc.kind
			p.forum = tc.forum
			p.admin = tc.admin
			p.post = tc.post
			p.mu.Unlock()
			_, err := verify(context.Background(), s.pool.MemberByID(100), int64(-100999))
			if (err == nil) != tc.ok {
				t.Fatal(err)
			}
		})
	}
}
func TestCreatePinsActualBotAndAudit(t *testing.T) {
	s, st, _, _ := testService(t, true)
	attachTest(s)
	j, err := s.Create(context.Background(), Input{Target: "@target"})
	if err != nil || j.BotID != 100 || j.Status != "running" {
		t.Fatalf("create %+v %v", j, err)
	}
	if _, err = s.Create(context.Background(), Input{Target: "@target"}); apperr.From(err).Code != apperr.CodeStoreConstraint {
		t.Fatal(err)
	}
	audit, err := st.ListAudit(context.Background(), 10, 0)
	if err != nil || len(audit) != 1 || audit[0].Action != "recovery.create" {
		t.Fatalf("audit %+v %v", audit, err)
	}
}
func TestCacheOutcomeSafety(t *testing.T) {
	for _, tc := range []struct {
		name   string
		ids    []int
		err    error
		status string
		source bool
	}{{"copy", []int{90, 91}, nil, "succeeded", false}, {"partial", []int{90}, nil, "uncertain", false}, {"network", nil, context.DeadlineExceeded, "uncertain", false}, {"partial error", []int{90}, fmt.Errorf("%w: source inaccessible", tgbot.ErrorBadRequest), "uncertain", false}, {"definite rejection", nil, fmt.Errorf("%w: missing source", tgbot.ErrorBadRequest), "succeeded", true}, {"unknown string", nil, errors.New("bad request: looks definite but is not structured"), "uncertain", false}} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, f, _ := testService(t, true)
			f.copy = func(context.Context, int64, int64, []int) ([]int, error) { return tc.ids, tc.err }
			j := createFixture(t, s)
			var source atomic.Int64
			rt := Runtime{Source: func(c context.Context, i store.RecoveryItem, to int64, sender delivery.Sender) (queue.RecoveryResult, error) {
				source.Add(1)
				stored := itemOf(t, s, j)
				if stored.Status != "processing" {
					t.Fatalf("sent before claim %+v", stored)
				}
				return queue.RecoveryResult{SentIDs: []int{90, 91}, Began: true}, nil
			}}
			if err := s.runOne(context.Background(), rt, j); err != nil {
				t.Fatal(err)
			}
			it := itemOf(t, s, j)
			if it.Status != tc.status || (source.Load() > 0) != tc.source {
				t.Fatalf("outcome %+v source %d", it, source.Load())
			}
			if len(tc.ids) > 0 && it.SentIDs[0] != tc.ids[0] {
				t.Fatal("lost known IDs")
			}
		})
	}
}
func TestSourceOutcomeSafety(t *testing.T) {
	for _, tc := range []struct {
		name   string
		r      queue.RecoveryResult
		err    error
		status string
	}{{"source unknown", queue.RecoveryResult{Began: true}, context.DeadlineExceeded, "uncertain"}, {"source inaccessible", queue.RecoveryResult{}, apperr.New(apperr.CodeChannelInaccessible, ""), "unrecoverable"}, {"source partial", queue.RecoveryResult{Began: true, SentIDs: []int{2}}, context.Canceled, "uncertain"}, {"source full", queue.RecoveryResult{Began: true, SentIDs: []int{2}}, nil, "succeeded"}} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _, _ := testService(t, false)
			j := createFixture(t, s)
			if err := s.runOne(context.Background(), Runtime{Source: func(context.Context, store.RecoveryItem, int64, delivery.Sender) (queue.RecoveryResult, error) {
				return tc.r, tc.err
			}}, j); err != nil {
				t.Fatal(err)
			}
			if it := itemOf(t, s, j); it.Status != tc.status {
				t.Fatalf("outcome %+v", it)
			}
		})
	}
}
func TestLostTargetPausesPendingAndResumeRechecks(t *testing.T) {
	s, _, f, p := testService(t, true)
	attachTest(s)
	j := createFixture(t, s)
	p.mu.Lock()
	p.admin = false
	p.mu.Unlock()
	if err := s.runOne(context.Background(), Runtime{Source: noSource}, j); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(context.Background(), j.ID)
	if err != nil || got.Status != "paused" || got.Pending != 1 || f.calls.Load() != 0 {
		t.Fatalf("lost target %+v %v", got, err)
	}
	if _, err = s.Control(context.Background(), j.ID, "resume"); apperr.From(err).Code != apperr.CodeChannelNotPostable {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.admin = true
	p.mu.Unlock()
	if _, err = s.Control(context.Background(), j.ID, "resume"); err != nil {
		t.Fatal(err)
	}
}
func waitFor(t *testing.T, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if predicate() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timeout")
}
func TestControlWaitsForInflightPersistence(t *testing.T) {
	for _, action := range []string{"pause", "cancel"} {
		t.Run(action, func(t *testing.T) {
			s, _, f, _ := testService(t, true)
			entered := make(chan struct{})
			cancelled := make(chan struct{})
			release := make(chan struct{})
			f.copy = func(ctx context.Context, from, to int64, ids []int) ([]int, error) {
				close(entered)
				<-ctx.Done()
				close(cancelled)
				<-release
				return []int{900}, ctx.Err()
			}
			j := createFixture(t, s)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- s.Run(ctx, Runtime{Source: noSource}) }()
			<-entered
			controlled := make(chan error, 1)
			go func() { _, err := s.Control(context.Background(), j.ID, action); controlled <- err }()
			<-cancelled
			select {
			case err := <-controlled:
				t.Fatalf("control returned before persistence %v", err)
			default:
			}
			close(release)
			if err := <-controlled; err != nil {
				t.Fatal(err)
			}
			it := itemOf(t, s, j)
			if it.Status != "uncertain" || len(it.SentIDs) != 1 {
				t.Fatalf("not persisted %+v", it)
			}
			got, _ := s.Get(context.Background(), j.ID)
			want := "paused"
			if action == "cancel" {
				want = "cancelled"
			}
			if got.Status != want {
				t.Fatal(got)
			}
			cancel()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestControlCallerDisconnectStillConverges(t *testing.T) {
	for _, action := range []string{"pause", "cancel"} {
		t.Run(action, func(t *testing.T) {
			s, _, f, _ := testService(t, true)
			entered := make(chan struct{})
			cancelled := make(chan struct{})
			release := make(chan struct{})
			f.copy = func(ctx context.Context, from, to int64, ids []int) ([]int, error) {
				close(entered)
				<-ctx.Done()
				close(cancelled)
				<-release
				return []int{900}, ctx.Err()
			}
			j := createFixture(t, s)
			runCtx, stopRun := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- s.Run(runCtx, Runtime{Source: noSource}) }()
			<-entered
			callCtx, disconnect := context.WithCancel(context.Background())
			controlled := make(chan error, 1)
			go func() { _, err := s.Control(callCtx, j.ID, action); controlled <- err }()
			<-cancelled
			// HTTP 调用方在等待在途结果落库时断开：控制必须用独立有界
			// 上下文完成状态收敛，不能留下 running+blocked。
			disconnect()
			close(release)
			select {
			case err := <-controlled:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("control did not converge after caller disconnect")
			}
			it := itemOf(t, s, j)
			if it.Status != "uncertain" || len(it.SentIDs) != 1 {
				t.Fatalf("not persisted %+v", it)
			}
			got, _ := s.Get(context.Background(), j.ID)
			want := "paused"
			if action == "cancel" {
				want = "cancelled"
			}
			if got.Status != want {
				t.Fatal(got)
			}
			stopRun()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestRuntimeOfflineJoinsAndNeverAutosendsOnReconnect(t *testing.T) {
	s, _, f, _ := testService(t, false)
	j := createFixture(t, s)
	entered := make(chan struct{})
	exited := make(chan struct{})
	rt := Runtime{Source: func(ctx context.Context, i store.RecoveryItem, to int64, sender delivery.Sender) (queue.RecoveryResult, error) {
		close(entered)
		<-ctx.Done()
		close(exited)
		return queue.RecoveryResult{Began: true}, ctx.Err()
	}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, rt) }()
	<-entered
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	select {
	case <-exited:
	default:
		t.Fatal("source not joined")
	}
	got, _ := s.Get(context.Background(), j.ID)
	if got.Status != "paused" || got.Uncertain != 1 {
		t.Fatal(got)
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	done2 := make(chan error, 1)
	go func() { done2 <- s.Run(ctx2, Runtime{Source: noSource}) }()
	time.Sleep(20 * time.Millisecond)
	if f.calls.Load() != 0 {
		t.Fatal("reconnect auto send")
	}
	cancel2()
	if err := <-done2; err != nil {
		t.Fatal(err)
	}
	if _, err := s.Preview(context.Background(), Input{Target: "@target"}); apperr.From(err).Code != apperr.CodeNetworkError {
		t.Fatal(err)
	}
}
func TestDBFailureHaltsAfterSend(t *testing.T) {
	s, st, f, _ := testService(t, true)
	j := createFixture(t, s)
	f.copy = func(context.Context, int64, int64, []int) ([]int, error) {
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
		return []int{9, 10}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := s.Run(ctx, Runtime{Source: noSource})
	if apperr.From(err).Code != apperr.CodeStoreUnavailable || f.calls.Load() != 1 {
		t.Fatalf("not halted %v count %d job %d", err, f.calls.Load(), j.ID)
	}
	if _, err = s.Preview(context.Background(), Input{Target: "@target"}); apperr.From(err).Code != apperr.CodeStoreUnavailable {
		t.Fatal(err)
	}
}
func TestSourceWaitsForOrdinaryQueueIdle(t *testing.T) {
	s, _, _, _ := testService(t, false)
	j := createFixture(t, s)
	q := queue.New(1)
	if err := q.Enqueue(queue.Job{ID: "ordinary"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	called := atomic.Bool{}
	done := make(chan error, 1)
	go func() {
		done <- s.runOne(ctx, Runtime{Queue: q, Source: func(context.Context, store.RecoveryItem, int64, delivery.Sender) (queue.RecoveryResult, error) {
			called.Store(true)
			return queue.RecoveryResult{}, nil
		}}, j)
	}()
	waitFor(t, func() bool { return itemOf(t, s, j).Status == "processing" })
	time.Sleep(20 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if called.Load() {
		t.Fatal("source competed with normal queue")
	}
	if it := itemOf(t, s, j); it.Status != "pending" {
		t.Fatal(it)
	}
}
