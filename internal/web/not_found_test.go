package web

import (
	"net/http"
	"strings"
	"testing"
)

func TestNeutralNotFoundForUnknownOrdinaryPath(t *testing.T) {
	e := newTestEnv(t, nil)
	j := newJar(t)
	path := "/missing/page?probe=/admin/%2Fapi%2Fsecret"

	resp := e.do(j, http.MethodGet, path, "", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("未匹配普通路径应返回 404，得到 %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Fatalf("普通 404 Content-Type 应为 UTF-8 纯文本，得到 %q", got)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("普通 404 Cache-Control 应为 no-store，得到 %q", got)
	}

	body := bodyOf(t, resp)
	if want := neutralNotFoundMessage + "\n"; body != want {
		t.Fatalf("普通 404 应返回固定中性文案，得到 %q", body)
	}
	for _, leaked := range []string{
		"404 page not found",
		"/admin",
		"/api",
		"Spore",
		"spore",
		"missing/page",
		"secret",
		"http://",
		"127.0.0.1",
		"500",
	} {
		if strings.Contains(body, leaked) {
			t.Errorf("普通 404 不应包含泄露信息 %q：%q", leaked, body)
		}
	}
}

func TestUnknownAssetsRemainResource404(t *testing.T) {
	e := newTestEnv(t, nil)
	j := newJar(t)

	resp := e.do(j, http.MethodGet, "/assets/missing.js", "", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("未知静态资源应返回 404，得到 %d", resp.StatusCode)
	}
	body := bodyOf(t, resp)
	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "html") ||
		strings.Contains(strings.ToLower(body), "<html") {
		t.Fatalf("未知静态资源不应返回 SPA 壳，Content-Type=%q body=%q",
			resp.Header.Get("Content-Type"), body)
	}
}

func TestAdminUnknownPathStillServesSPAShell(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)

	resp := e.do(j, http.MethodGet, "/admin/unknown-page", "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("已认证管理端未知页面应返回 SPA 壳 200，得到 %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Fatalf("管理端未知页面应返回 HTML SPA 壳，得到 %q", got)
	}
	body := bodyOf(t, resp)
	if !strings.Contains(body, `id="root"`) {
		t.Fatalf("管理端未知页面应包含 SPA root 节点，得到 %q", body)
	}
}
