// redirectInvoker：连接池之上的 DC 迁移感知层。
//
// 为什么需要它：gotd 直连主连接的 invoker（telegram.invokeDirect）内建了
// *_MIGRATE 错误处理——FILE_MIGRATE_X 表示"这个文件在 DC X"，会经
// invokeSub 把授权迁移过去并在目标 DC 的子连接上重发该 RPC；而
// client.Pool() 返回的池 invoker 只会把 303 原样抛回调用方，跨 DC 文件
// 的下载/上传（真机 2026-09-07：1.4GB 视频在 DC5，主 DC 连接首片即
// FILE_MIGRATE）因此失败。本层把该语义补回池化路径：
//
//   - FILE_MIGRATE / STATS_MIGRATE：惰性创建目标 DC 的 N 连接池（经
//     client.DC 自动 export/import 授权）并缓存；后续分片仍先经主 DC
//     获得重定向，但复用目标池，不重复握手/授权迁移；目标 DC 内并行度
//     比直连路径 invokeSub 的单连接子池更高；
//   - 其余 _MIGRATE（PHONE/NETWORK/USER，会话级迁移）：交回主连接
//     invoker 全量处理（它知道如何迁移主会话）；
//   - 非 MIGRATE 错误原样透传（FLOOD_WAIT 由外层 floodwait 处理）。
//
// 每片跨 DC 分片付出一次"主 DC 探测 + 重定向"的额外 RTT——与直连路径
// invokeDirect 每片先打主连接再 invokeSub 的行为等价，不是回归。
package mtproto

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"sync"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

// redirectInvoker 实现 tg.Invoker，可与 floodwait 任意叠层。
type redirectInvoker struct {
	home      telegram.CloseInvoker // 主 DC 连接池
	createSub func(ctx context.Context, dc int) (telegram.CloseInvoker, error)
	fallback  tg.Invoker // 其余 _MIGRATE 的全量处理链（主连接 invoker）
	log       *slog.Logger

	mu      sync.Mutex
	subs    map[int]telegram.CloseInvoker // 目标 DC → 连接池（惰性创建、缓存）
	pending map[int]*subPoolPending
	closed  bool
	routes  map[routeKey]routeEntry
	seq     uint64
}

type routeKey struct {
	kind routeKeyKind
	loc  interface{}
	id   int64
}

type routeKeyKind uint8

const (
	routeFileLocation routeKeyKind = iota + 1
	routeWebLocation
	routeFileID
)

type routeEntry struct{ dc, used uint64 }

type subPoolPending struct {
	ready chan struct{}
	pool  telegram.CloseInvoker
	err   error
	done  bool
}

const (
	maxFileRedirectHops = 4
	maxStickyRoutes     = 4096
)

var errRedirectClosed = errors.New("mtproto redirect invoker is closed")

func (r *redirectInvoker) Invoke(ctx context.Context, input bin.Encoder, output bin.Decoder) error {
	key, sticky := fileRouteKey(input)
	if !sticky {
		return r.invokeHome(ctx, input, output)
	}

	// A route hit skips the home DC entirely. A missing route starts with home,
	// which is needed to discover FILE_MIGRATE on the first part.
	current := r.home
	currentDC := -1
	if dc, ok := r.routeDC(key); ok {
		currentDC = dc
		var err error
		current, err = r.subPool(ctx, dc)
		if err != nil {
			return err
		}
	}
	visited := map[int]struct{}{}
	var lastErr error
	for hop := 0; hop < maxFileRedirectHops; hop++ {
		err := current.Invoke(ctx, input, output)
		lastErr = err
		if err == nil {
			return nil
		}
		rpcErr, ok := tgerr.As(err)
		if !ok || !strings.HasSuffix(rpcErr.Type, "_MIGRATE") {
			return err
		}
		if rpcErr.IsOneOf("STATS_MIGRATE") {
			sub, serr := r.subPool(ctx, rpcErr.Argument)
			if serr != nil {
				return err
			}
			return sub.Invoke(ctx, input, output)
		}
		if !rpcErr.IsOneOf("FILE_MIGRATE") {
			return r.fallback.Invoke(ctx, input, output)
		}
		target := rpcErr.Argument
		if target == currentDC {
			return err
		}
		if _, ok := visited[target]; ok {
			return err
		}
		visited[target] = struct{}{}
		sub, serr := r.subPool(ctx, target)
		if serr != nil {
			if r.log != nil {
				r.log.Warn("跨 DC 子连接池创建失败", "dc", target, "error", serr.Error())
			}
			return err
		}
		r.setRoute(key, target)
		current, currentDC = sub, target
	}
	return lastErr
}

func (r *redirectInvoker) invokeHome(ctx context.Context, input bin.Encoder, output bin.Decoder) error {
	err := r.home.Invoke(ctx, input, output)
	if err == nil {
		return nil
	}
	rpcErr, ok := tgerr.As(err)
	if !ok || !strings.HasSuffix(rpcErr.Type, "_MIGRATE") {
		return err
	}
	if rpcErr.IsOneOf("FILE_MIGRATE", "STATS_MIGRATE") {
		sub, serr := r.subPool(ctx, rpcErr.Argument)
		if serr != nil {
			if r.log != nil {
				r.log.Warn("跨 DC 子连接池创建失败", "dc", rpcErr.Argument, "error", serr.Error())
			}
			return err
		}
		return sub.Invoke(ctx, input, output)
	}
	return r.fallback.Invoke(ctx, input, output)
}

func (r *redirectInvoker) routeDC(key routeKey) (int, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.routes[key]
	if !ok {
		return 0, false
	}
	r.seq++
	entry.used = r.seq
	r.routes[key] = entry
	return int(entry.dc), true
}

func (r *redirectInvoker) setRoute(key routeKey, dc int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	if r.routes == nil {
		r.routes = make(map[routeKey]routeEntry)
	}
	r.seq++
	r.routes[key] = routeEntry{dc: uint64(dc), used: r.seq}
	if len(r.routes) <= maxStickyRoutes {
		return
	}
	var oldest routeKey
	var oldestUse uint64
	for k, entry := range r.routes {
		if oldestUse == 0 || entry.used < oldestUse {
			oldest, oldestUse = k, entry.used
		}
	}
	delete(r.routes, oldest)
}

// subPool uses a per-DC promise. Network/auth setup runs outside the mutex;
// callers for another DC can therefore proceed in parallel.
func (r *redirectInvoker) subPool(ctx context.Context, dc int) (telegram.CloseInvoker, error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, errRedirectClosed
	}
	if r.subs == nil {
		r.subs = make(map[int]telegram.CloseInvoker)
	}
	if p, ok := r.subs[dc]; ok {
		r.mu.Unlock()
		return p, nil
	}
	if r.pending == nil {
		r.pending = make(map[int]*subPoolPending)
	}
	if pending, ok := r.pending[dc]; ok {
		r.mu.Unlock()
		select {
		case <-pending.ready:
			return pending.pool, pending.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	pending := &subPoolPending{ready: make(chan struct{})}
	r.pending[dc] = pending
	r.mu.Unlock()

	pool, err := r.createSub(ctx, dc)
	r.mu.Lock()
	delete(r.pending, dc)
	if pending.done {
		r.mu.Unlock()
		if pool != nil {
			_ = pool.Close()
		}
		return nil, pending.err
	}
	pending.pool, pending.err, pending.done = pool, err, true
	if err == nil {
		r.subs[dc] = pool
	}
	close(pending.ready)
	r.mu.Unlock()
	return pool, err
}

// closeAll prevents new pools, wakes pending waiters, then closes resources
// outside the lock. A pool finishing creation after close is closed by its
// creator instead of being published.
func (r *redirectInvoker) closeAll() {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	home := r.home
	subs := make([]telegram.CloseInvoker, 0, len(r.subs)+1)
	if home != nil {
		subs = append(subs, home)
	}
	for dc, p := range r.subs {
		subs = append(subs, p)
		delete(r.subs, dc)
	}
	r.routes = nil
	for _, pending := range r.pending {
		pending.err, pending.done = errRedirectClosed, true
		close(pending.ready)
	}
	r.pending = nil
	r.mu.Unlock()
	for _, pool := range subs {
		if err := pool.Close(); err != nil && r.log != nil {
			r.log.Warn("跨 DC 子连接池关闭失败", "error", err.Error())
		}
	}
}

func fileRouteKey(input bin.Encoder) (routeKey, bool) {
	switch req := input.(type) {
	case *tg.UploadGetFileRequest:
		return locationRouteKey(routeFileLocation, req.Location)
	case *tg.UploadGetFileHashesRequest:
		return locationRouteKey(routeFileLocation, req.Location)
	case *tg.UploadGetWebFileRequest:
		return locationRouteKey(routeWebLocation, req.Location)
	case *tg.UploadSaveFilePartRequest:
		return routeKey{kind: routeFileID, id: req.FileID}, true
	case *tg.UploadSaveBigFilePartRequest:
		return routeKey{kind: routeFileID, id: req.FileID}, true
	default:
		return routeKey{}, false
	}
}

func locationRouteKey(kind routeKeyKind, location interface{}) (routeKey, bool) {
	if location == nil {
		return routeKey{}, false
	}
	value := reflect.ValueOf(location)
	if value.Kind() == reflect.Pointer && value.IsNil() {
		return routeKey{}, false
	}
	return routeKey{kind: kind, loc: location}, true
}
