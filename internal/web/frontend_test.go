package web

// 当前 Go 侧唯一 HTML 是 SPA 应用壳：/admin/login 公开、其余 /admin/* 受
// 会话保护（302 到登录壳）；SPA 壳不含业务数据，认证与数据均走 /api/v1。

import (
	"context"
	"io/fs"
	"net/http"
	"strings"
	"testing"

	"github.com/huaiminyetnotsleep/spore/internal/syscfg"
)

func TestAdminSPARequiresAuthentication(t *testing.T) {
	e := newTestEnv(t, nil)
	j := newJar(t)

	for _, path := range []string{"/admin", "/admin/users"} {
		resp := e.do(j, http.MethodGet, path, "", "")
		if resp.StatusCode != http.StatusFound {
			t.Errorf("未认证 GET %s 应返回 302，得到 %d", path, resp.StatusCode)
		}
		if got := resp.Header.Get("Location"); got != "/admin/login" {
			t.Errorf("未认证 GET %s 应重定向到 /admin/login，得到 %q", path, got)
		}
		bodyOf(t, resp)
	}
}

func TestAdminLoginShellIsPublic(t *testing.T) {
	e := newTestEnv(t, nil)
	j := newJar(t)

	resp := e.do(j, http.MethodGet, "/admin/login", "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("未认证 GET /admin/login 应返回公开 SPA 壳 200，得到 %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("SPA 壳 Content-Type 应为 HTML，得到 %q", got)
	}
	body := bodyOf(t, resp)
	if !strings.Contains(body, `id="root"`) {
		t.Errorf("SPA 壳缺少 root 节点，得到 %s", body)
	}
}

// 免认证面只限于精确路径 /admin/login：带尾斜杠的子路径与大小写变体都
// 落入受会话保护的 /admin/{path...} 通配（未认证 302 到登录壳），
// 不能借道绕过登录，也不能未认证进入登录壳以外的任何壳内容。
func TestAdminLoginShellExactPathOnly(t *testing.T) {
	e := newTestEnv(t, nil)
	j := newJar(t)

	for _, path := range []string{"/admin/login/", "/admin/Login", "/admin/LOGIN", "/admin/login/x"} {
		resp := e.do(j, http.MethodGet, path, "", "")
		if resp.StatusCode != http.StatusFound {
			t.Errorf("未认证 GET %s 应落入会话保护并 302，得到 %d", path, resp.StatusCode)
		}
		if loc := resp.Header.Get("Location"); loc != "/admin/login" {
			t.Errorf("未认证 GET %s 应重定向到 /admin/login，得到 %q", path, loc)
		}
		bodyOf(t, resp)
	}
}

func TestAdminSPAEntryAndFallbackBoundaries(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)

	resp := e.do(j, http.MethodGet, "/admin/users", "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("SPA 页面应返回 200，得到 %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("SPA 页面 Content-Type 应为 HTML，得到 %q", got)
	}
	body := bodyOf(t, resp)
	if !strings.Contains(body, `id="root"`) {
		t.Errorf("SPA 页面缺少 root 节点，得到 %s", body)
	}

	for _, path := range []string{"/api/not-found", "/assets/not-found.js"} {
		resp = e.do(j, http.MethodGet, path, "", "")
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("不存在的 %s 不应回退到 SPA，得到 %d", path, resp.StatusCode)
		}
		bodyOf(t, resp)
	}
}

// SSR 遗留清理：/static/admin.js 与旧 SSR 登录页均已删除（404）。
func TestAdminJSRemoved(t *testing.T) {
	e := newTestEnv(t, nil)

	jAnon := newJar(t)
	for _, path := range []string{"/static/admin.js"} {
		resp := e.do(jAnon, http.MethodGet, path, "", "")
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s 已删除，应 404，得到 %d", path, resp.StatusCode)
		}
		bodyOf(t, resp)
	}
}

// SPA 壳注入可配置的系统名称（<meta name="app-name">）：未配置时为缺省值，
// 登录壳（未认证）同样注入；修改后下次壳响应生效。
func TestAdminShellInjectsAppName(t *testing.T) {
	e := newTestEnv(t, nil)
	j := newJar(t)

	// 构建产物由 Vite 产出（自闭合格式），断言只看 name/content 键值对本身
	hasAppName := func(body, want string) bool {
		for _, line := range strings.Split(body, "\n") {
			if strings.Contains(line, `name="app-name"`) && strings.Contains(line, `content="`+want+`"`) {
				return true
			}
		}
		return false
	}

	body := bodyOf(t, e.do(j, http.MethodGet, "/admin/login", "", ""))
	if !hasAppName(body, "Spore") {
		t.Errorf("未配置时壳应注入缺省名称，得到 %s", body)
	}

	ctx := context.Background()
	if err := syscfg.SetName(ctx, e.st, "我的提取站"); err != nil {
		t.Fatalf("写入系统名称失败: %v", err)
	}
	body = bodyOf(t, e.do(j, http.MethodGet, "/admin/login", "", ""))
	if !hasAppName(body, "我的提取站") {
		t.Errorf("修改后壳应注入新名称，得到 %s", body)
	}
	if strings.Contains(body, appNamePlaceholder) {
		t.Error("壳不应残留系统名称占位符")
	}
}

func TestFrontendRootBrandAssets(t *testing.T) {
	if _, err := fs.Stat(frontendFiles, "frontend-dist/icon.svg"); err != nil {
		t.Skip("frontend build output is not present")
	}

	e := newTestEnv(t, nil)
	j := newJar(t)
	for _, path := range []string{"/favicon.svg", "/icon.svg"} {
		resp := e.do(j, http.MethodGet, path, "", "")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("品牌资源 %s 应返回 200，得到 %d", path, resp.StatusCode)
		}
		if got := resp.Header.Get("Content-Type"); got != "image/svg+xml" {
			t.Errorf("品牌资源 %s Content-Type 应为 image/svg+xml，得到 %q", path, got)
		}
		if body := bodyOf(t, resp); !strings.HasPrefix(body, "<svg") {
			t.Errorf("品牌资源 %s 应返回 SVG，得到 %q", path, body[:min(len(body), 40)])
		}
	}
}
