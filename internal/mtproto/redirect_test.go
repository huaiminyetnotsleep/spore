package mtproto

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

type fakeCloseInvoker struct {
	invoke func(context.Context, bin.Encoder, bin.Decoder) error
	closed atomic.Int32
}

func (f *fakeCloseInvoker) Invoke(ctx context.Context, in bin.Encoder, out bin.Decoder) error {
	return f.invoke(ctx, in, out)
}
func (f *fakeCloseInvoker) Close() error {
	f.closed.Add(1)
	return nil
}

func migrateError(kind string, dc int) error {
	return tgerr.New(303, fmt.Sprintf("%s_%d", kind, dc))
}

func testRedirect(home telegram.CloseInvoker, create func(context.Context, int) (telegram.CloseInvoker, error), fallback tg.Invoker) *redirectInvoker {
	return &redirectInvoker{
		home:      home,
		createSub: create,
		fallback:  fallback,
		log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		subs:      map[int]telegram.CloseInvoker{},
	}
}

func TestRedirectInvokerFileMigrate(t *testing.T) {
	var homeCalls, subCalls atomic.Int32
	home := &fakeCloseInvoker{invoke: func(context.Context, bin.Encoder, bin.Decoder) error {
		homeCalls.Add(1)
		return migrateError("FILE_MIGRATE", 5)
	}}
	sub := &fakeCloseInvoker{invoke: func(context.Context, bin.Encoder, bin.Decoder) error {
		subCalls.Add(1)
		return nil
	}}
	var creates atomic.Int32
	r := testRedirect(home, func(_ context.Context, dc int) (telegram.CloseInvoker, error) {
		if dc != 5 {
			t.Fatalf("目标 DC 应为 5，得到 %d", dc)
		}
		creates.Add(1)
		return sub, nil
	}, tg.Invoker(home))

	for range 2 {
		if err := r.Invoke(context.Background(), &tg.HelpGetConfigRequest{}, &tg.Config{}); err != nil {
			t.Fatalf("重定向调用失败：%v", err)
		}
	}
	if homeCalls.Load() != 2 || subCalls.Load() != 2 || creates.Load() != 1 {
		t.Fatalf("调用次数不符：home=%d sub=%d create=%d", homeCalls.Load(), subCalls.Load(), creates.Load())
	}

	r.closeAll()
	if home.closed.Load() != 1 || sub.closed.Load() != 1 {
		t.Fatalf("连接池应各关闭一次：home=%d sub=%d", home.closed.Load(), sub.closed.Load())
	}
}

func TestRedirectInvokerConcurrentSubPoolCreation(t *testing.T) {
	home := &fakeCloseInvoker{invoke: func(context.Context, bin.Encoder, bin.Decoder) error {
		return migrateError("FILE_MIGRATE", 5)
	}}
	sub := &fakeCloseInvoker{invoke: func(context.Context, bin.Encoder, bin.Decoder) error { return nil }}
	var creates atomic.Int32
	r := testRedirect(home, func(context.Context, int) (telegram.CloseInvoker, error) {
		creates.Add(1)
		return sub, nil
	}, tg.Invoker(home))

	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := r.Invoke(context.Background(), &tg.HelpGetConfigRequest{}, &tg.Config{}); err != nil {
				t.Errorf("并发重定向失败：%v", err)
			}
		}()
	}
	wg.Wait()
	if creates.Load() != 1 {
		t.Fatalf("并发首片应只创建一个子池，得到 %d", creates.Load())
	}
}

func TestRedirectInvokerOtherMigrateUsesFallback(t *testing.T) {
	home := &fakeCloseInvoker{invoke: func(context.Context, bin.Encoder, bin.Decoder) error {
		return migrateError("PHONE_MIGRATE", 2)
	}}
	var fallbackCalls atomic.Int32
	fallback := &fakeCloseInvoker{invoke: func(context.Context, bin.Encoder, bin.Decoder) error {
		fallbackCalls.Add(1)
		return nil
	}}
	r := testRedirect(home, func(context.Context, int) (telegram.CloseInvoker, error) {
		t.Fatal("会话级迁移不应创建文件子池")
		return nil, nil
	}, fallback)

	if err := r.Invoke(context.Background(), &tg.HelpGetConfigRequest{}, &tg.Config{}); err != nil {
		t.Fatalf("fallback 调用失败：%v", err)
	}
	if fallbackCalls.Load() != 1 {
		t.Fatalf("fallback 应调用一次，得到 %d", fallbackCalls.Load())
	}
}

func TestRedirectInvokerPassesThroughOrdinaryError(t *testing.T) {
	want := errors.New("ordinary failure")
	home := &fakeCloseInvoker{invoke: func(context.Context, bin.Encoder, bin.Decoder) error { return want }}
	r := testRedirect(home, func(context.Context, int) (telegram.CloseInvoker, error) {
		t.Fatal("普通错误不应创建子池")
		return nil, nil
	}, home)

	if got := r.Invoke(context.Background(), &tg.HelpGetConfigRequest{}, &tg.Config{}); !errors.Is(got, want) {
		t.Fatalf("应原样透传错误，得到 %v", got)
	}
}

func TestRedirectInvokerSubPoolCreationFailureKeepsMigrateError(t *testing.T) {
	migration := migrateError("FILE_MIGRATE", 5)
	home := &fakeCloseInvoker{invoke: func(context.Context, bin.Encoder, bin.Decoder) error { return migration }}
	r := testRedirect(home, func(context.Context, int) (telegram.CloseInvoker, error) {
		return nil, errors.New("dial failed")
	}, home)

	got := r.Invoke(context.Background(), &tg.HelpGetConfigRequest{}, &tg.Config{})
	rpcErr, ok := tgerr.As(got)
	if !ok || rpcErr.Type != "FILE_MIGRATE" || rpcErr.Argument != 5 {
		t.Fatalf("应保留原始 FILE_MIGRATE，得到 %v", got)
	}
}

func TestRedirectInvokerStickyLocationAcrossFileMethods(t *testing.T) {
	location := &tg.InputDocumentFileLocation{ID: 42}
	var homeCalls, targetCalls atomic.Int32
	home := &fakeCloseInvoker{invoke: func(_ context.Context, input bin.Encoder, _ bin.Decoder) error {
		homeCalls.Add(1)
		if _, ok := input.(*tg.UploadGetFileRequest); !ok {
			t.Fatalf("首次探测应为 getFile，得到 %T", input)
		}
		return migrateError("FILE_MIGRATE", 5)
	}}
	target := &fakeCloseInvoker{invoke: func(_ context.Context, input bin.Encoder, _ bin.Decoder) error {
		targetCalls.Add(1)
		switch req := input.(type) {
		case *tg.UploadGetFileRequest:
			if req.Location != location {
				t.Fatalf("getFile location 被改变")
			}
		case *tg.UploadGetFileHashesRequest:
			if req.Location != location {
				t.Fatalf("getFileHashes 未复用 location")
			}
		default:
			t.Fatalf("目标池收到未知请求 %T", input)
		}
		return nil
	}}
	r := testRedirect(home, func(_ context.Context, dc int) (telegram.CloseInvoker, error) {
		if dc != 5 {
			t.Fatalf("目标 DC 应为 5，得到 %d", dc)
		}
		return target, nil
	}, home)

	if err := r.Invoke(context.Background(), &tg.UploadGetFileRequest{Location: location}, &tg.UploadFile{}); err != nil {
		t.Fatalf("首次下载失败：%v", err)
	}
	if err := r.Invoke(context.Background(), &tg.UploadGetFileHashesRequest{Location: location}, nil); err != nil {
		t.Fatalf("hash 请求失败：%v", err)
	}
	if homeCalls.Load() != 1 || targetCalls.Load() != 2 {
		t.Fatalf("粘性路由未生效：home=%d target=%d", homeCalls.Load(), targetCalls.Load())
	}
}

func TestRedirectInvokerStickyFileIDAndLocationIsolation(t *testing.T) {
	var homeCalls atomic.Int32
	home := &fakeCloseInvoker{invoke: func(context.Context, bin.Encoder, bin.Decoder) error {
		homeCalls.Add(1)
		return migrateError("FILE_MIGRATE", 4)
	}}
	var targetCalls atomic.Int32
	target := &fakeCloseInvoker{invoke: func(context.Context, bin.Encoder, bin.Decoder) error {
		targetCalls.Add(1)
		return nil
	}}
	r := testRedirect(home, func(context.Context, int) (telegram.CloseInvoker, error) { return target, nil }, home)

	loc1 := &tg.InputPhotoFileLocation{ID: 1, AccessHash: 2, FileReference: []byte{1}}
	loc2 := &tg.InputPhotoFileLocation{ID: 1, AccessHash: 2, FileReference: []byte{2}}
	if err := r.Invoke(context.Background(), &tg.UploadGetFileRequest{Location: loc1}, &tg.UploadFile{}); err != nil {
		t.Fatal(err)
	}
	if err := r.Invoke(context.Background(), &tg.UploadGetFileRequest{Location: loc1}, &tg.UploadFile{}); err != nil {
		t.Fatal(err)
	}
	if err := r.Invoke(context.Background(), &tg.UploadGetFileRequest{Location: loc2}, &tg.UploadFile{}); err != nil {
		t.Fatal(err)
	}
	if err := r.Invoke(context.Background(), &tg.UploadSaveFilePartRequest{FileID: 99}, nil); err != nil {
		t.Fatal(err)
	}
	if err := r.Invoke(context.Background(), &tg.UploadSaveBigFilePartRequest{FileID: 99}, nil); err != nil {
		t.Fatal(err)
	}
	if homeCalls.Load() != 3 || targetCalls.Load() != 5 {
		t.Fatalf("路由隔离或 FileID 共享失败：home=%d target=%d", homeCalls.Load(), targetCalls.Load())
	}
}

func TestRedirectInvokerStickyRouteUpdatesWithBoundedHops(t *testing.T) {
	location := &tg.InputDocumentFileLocation{ID: 7}
	var homeCalls atomic.Int32
	home := &fakeCloseInvoker{invoke: func(context.Context, bin.Encoder, bin.Decoder) error {
		homeCalls.Add(1)
		return migrateError("FILE_MIGRATE", 5)
	}}
	pools := map[int]*fakeCloseInvoker{}
	for _, dc := range []int{4, 5} {
		dc := dc
		pools[dc] = &fakeCloseInvoker{invoke: func(context.Context, bin.Encoder, bin.Decoder) error {
			if dc == 5 {
				return migrateError("FILE_MIGRATE", 4)
			}
			return nil
		}}
	}
	r := testRedirect(home, func(_ context.Context, dc int) (telegram.CloseInvoker, error) {
		return pools[dc], nil
	}, home)
	if err := r.Invoke(context.Background(), &tg.UploadGetFileRequest{Location: location}, &tg.UploadFile{}); err != nil {
		t.Fatalf("多级迁移失败：%v", err)
	}
	if err := r.Invoke(context.Background(), &tg.UploadGetFileRequest{Location: location}, &tg.UploadFile{}); err != nil {
		t.Fatalf("更新后的直达失败：%v", err)
	}
	if homeCalls.Load() != 1 || pools[4].closed.Load() != 0 {
		t.Fatalf("多级迁移未更新到 DC4：home=%d", homeCalls.Load())
	}
}
