package web

// MTProto Web 测试：旧页面入口删除、二维码渲染、重连触发与降级（未注入）。

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/huaiminyetnotsleep/spore/internal/config"
	"github.com/huaiminyetnotsleep/spore/internal/mtproto"
)

// fakeMTProto 是 MTProtoRelogin 的假实现：模拟登录会话状态机
// （offline → login_pending+QR → ready / 失败回 offline）。
type fakeMTProto struct {
	mu       sync.Mutex
	snap     mtproto.StatusSnapshot
	triggers int
	failTrig bool
}

func (f *fakeMTProto) Status() mtproto.StatusSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snap
}

func (f *fakeMTProto) ClearSessionFiles() error { return nil }

func (f *fakeMTProto) TriggerRelogin() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failTrig {
		return errFakeTrigger
	}
	if f.snap.State != mtproto.StateOffline {
		return mtproto.ErrNotOffline
	}
	f.triggers++
	// 模拟进入登录流程并推出首个扫码 URL
	f.snap = mtproto.StatusSnapshot{
		State: mtproto.StateLoginPending,
		QRURL: "tg://login?token=faketoken",
	}
	return nil
}

var errFakeTrigger = &fakeErr{}

type fakeErr struct{}

func (*fakeErr) Error() string { return "模拟触发失败" }

func (f *fakeMTProto) set(snap mtproto.StatusSnapshot) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snap = snap
}

func (f *fakeMTProto) triggerCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.triggers
}

// newMTTestEnv 构造注入假登录会话的测试环境。
func newMTTestEnv(t *testing.T) (*testEnv, *fakeMTProto) {
	t.Helper()
	fake := &fakeMTProto{snap: mtproto.StatusSnapshot{State: mtproto.StateOffline}}
	e := newTestEnvOpts(t, func(_ *config.Config, opt *Options) { opt.MTProto = fake })
	return e, fake
}

func TestMTProtoPagesUnavailable(t *testing.T) {
	// 旧页面入口已删除，MTProto 状态与重连只通过 /api/v1 提供。
	e, _ := newMTTestEnv(t)
	j := e.login(t)

	for _, p := range []string{"/mtproto/login", "/mtproto/status"} {
		resp := e.do(j, "GET", p, "", "")
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("旧 %s 应返回 404，得到 %d", p, resp.StatusCode)
		}
		bodyOf(t, resp)
	}
}

func TestMTProtoReloginFlow(t *testing.T) {
	e, fake := newMTTestEnv(t)
	j := e.login(t)
	csrf := apiCSRFToken(t, e, j)

	// CSRF 缺失 → 403（API 层）
	resp := e.apiPost(j, "/api/v1/mtproto/relogin", "", "")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("缺 CSRF 应 403，得到 %d", resp.StatusCode)
	}
	bodyOf(t, resp)

	// 离线触发成功：假会话进入 login_pending 并带二维码
	resp = e.apiPost(j, "/api/v1/mtproto/relogin", csrf, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("触发重连应 200，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
	}
	if fake.triggerCount() != 1 {
		t.Fatalf("应触发一次重连，得到 %d", fake.triggerCount())
	}

	// 二维码渲染：PNG 魔数（SPA 直接引用的保留端点）
	resp = e.do(j, "GET", "/mtproto/qr.png", "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("二维码应 200，得到 %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/png" {
		t.Fatalf("二维码应为 PNG，得到 %q", ct)
	}
	body := bodyOf(t, resp)
	if !strings.HasPrefix(body, "\x89PNG") {
		t.Fatalf("二维码应为 PNG 魔数，前缀 %q", body[:8])
	}

	// 就绪后：再触发被拒（非离线，409），二维码 404
	fake.set(mtproto.StatusSnapshot{State: mtproto.StateReady})
	resp = e.apiPost(j, "/api/v1/mtproto/relogin", csrf, "")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("就绪后触发应 409，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
	}
	resp = e.do(j, "GET", "/mtproto/qr.png", "", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("无二维码时应 404，得到 %d", resp.StatusCode)
	}
	bodyOf(t, resp)
}

func TestMTProtoNotAttached(t *testing.T) {
	// 未注入登录会话：SPA 经 /api/v1/mtproto/status 读到 unknown，重连 503
	e := newTestEnv(t, nil)
	j := e.login(t)

	var st map[string]any
	resp := e.do(j, "GET", "/api/v1/mtproto/status", "", "")
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil || st["state"] != "unknown" {
		t.Fatalf("未接入状态应为 unknown：%v err=%v", st, err)
	}

	csrf := apiCSRFToken(t, e, j)
	resp = e.apiPost(j, "/api/v1/mtproto/relogin", csrf, "")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("未接入重连应 503，得到 %d", resp.StatusCode)
	}
	bodyOf(t, resp)
}
