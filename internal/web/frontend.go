package web

import (
	"bytes"
	"embed"
	"errors"
	"io/fs"
	"net/http"
	"strings"

	"github.com/huaiminyetnotsleep/spore/internal/branding"
	"github.com/huaiminyetnotsleep/spore/internal/syscfg"
)

// frontendFiles 包含前端构建产物。开发和未构建时仅保留 .gitkeep，
// handleFrontendApp 会返回内置的最小占位入口；生产构建会把 dist 覆盖到此目录。
//
//go:embed frontend-dist
var frontendFiles embed.FS

const cspNoncePlaceholder = branding.CSPNoncePlaceholder

// appNamePlaceholder 是 SPA 壳中系统名称的占位符：serveSPAShell 每次响应
// 以 settings 中可配置的名称替换（前端经 <meta name="app-name"> 读取，
// 登录页未认证也可取到；旧构建产物缺占位时在 </head> 前补入）。
const appNamePlaceholder = branding.AppNamePlaceholder

const fallbackFrontendIndex = `<!doctype html>
<html lang="zh-CN">
<head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><meta name="csp-nonce" content="` + cspNoncePlaceholder + `"><meta name="app-name" content="` + appNamePlaceholder + `"><title>Spore</title></head>
<body><div id="root" data-spa-shell="fallback">Spore 管理端 SPA 基线</div></body>
</html>
`

// handleAdminApp 返回受会话保护的 SPA 入口。页面本身不包含业务数据，业务数据
// 由 /api/v1 同源接口提供。每次响应都生成新的 CSP nonce，让 Ant Design
// CSS-in-JS 只能向当前文档注入带授权的 style 元素。
func (s *Server) handleAdminApp(w http.ResponseWriter, r *http.Request, _ session) {
	s.serveSPAShell(w, r)
}

// handleAdminLoginApp 是免认证的公开 SPA 登录壳（/admin/login）：
// 壳本身不含业务数据，登录动作只经公开的 /api/v1/login* 端点完成；
// 其余 /admin/* 路径仍由 requireAuth 保护（302 到本页）。
func (s *Server) handleAdminLoginApp(w http.ResponseWriter, r *http.Request) {
	s.serveSPAShell(w, r)
}

// serveSPAShell 输出 SPA 应用壳（读 frontend-dist 入口、注入本次响应专属的
// CSP nonce 与可配置系统名称）。/admin（requireAuth 版）与 /admin/login
// （公开登录壳）共用同一实现；壳不含任何业务数据，业务数据只能经
// 认证后的 /api/v1 获取。
func (s *Server) serveSPAShell(w http.ResponseWriter, r *http.Request) {
	index, err := fs.ReadFile(frontendFiles, "frontend-dist/index.html")
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			http.Error(w, "服务器内部错误", http.StatusInternalServerError)
			return
		}
		index = []byte(fallbackFrontendIndex)
	}

	nonceFunc := s.nonceFunc
	if nonceFunc == nil {
		nonceFunc = randomToken
	}
	nonce, err := nonceFunc(16)
	if err != nil || !validCSPNonce(nonce) {
		s.log.Error("生成 SPA CSP nonce 失败")
		http.Error(w, "服务器内部错误", http.StatusInternalServerError)
		return
	}
	index, err = injectCSPNonce(index, nonce)
	if err != nil {
		s.log.Error("注入 SPA CSP nonce 失败", "error", err.Error())
		http.Error(w, "服务器内部错误", http.StatusInternalServerError)
		return
	}
	// 名称读取失败不阻断壳响应：syscfg.Name 出错时回退缺省值
	index = injectAppName(index, syscfg.Name(r.Context(), s.st))

	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy",
		"default-src 'self'; style-src 'self' 'nonce-"+nonce+"'; style-src-elem 'self' 'nonce-"+nonce+"'; "+
			"style-src-attr 'unsafe-inline'; img-src 'self' data:; form-action 'self'; frame-ancestors 'none'; base-uri 'self'")
	_, _ = w.Write(index)
}

// escapeHTMLAttr 做 HTML 属性值转义（& < > " '）。nonce 经 validCSPNonce
// 限定为 token-safe 字符，转义只是纵深防御；当前 Go 侧不再引入任何 HTML
// 模板渲染，转义由本 helper 单点维护。
func escapeHTMLAttr(v string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&#39;",
	)
	return r.Replace(v)
}

// injectCSPNonce 将 nonce 放入构建入口的元数据。优先替换稳定占位符；面对旧构建产物
// 时在 </head> 前补入元数据，避免静态资源缓存导致入口响应缺少 nonce。
func injectCSPNonce(index []byte, nonce string) ([]byte, error) {
	escaped := escapeHTMLAttr(nonce)
	placeholder := []byte(cspNoncePlaceholder)
	if bytes.Contains(index, placeholder) {
		return bytes.ReplaceAll(index, placeholder, []byte(escaped)), nil
	}

	lower := bytes.ToLower(index)
	closeHead := []byte("</head>")
	at := bytes.Index(lower, closeHead)
	if at < 0 {
		return nil, errors.New("SPA 入口缺少 head 结束标签")
	}
	meta := []byte(`<meta name="csp-nonce" content="` + escaped + `">`)
	result := make([]byte, 0, len(index)+len(meta))
	result = append(result, index[:at]...)
	result = append(result, meta...)
	result = append(result, index[at:]...)
	return result, nil
}

// injectAppName 将可配置的系统名称放入构建入口的元数据（前端经
// <meta name="app-name"> 读取并渲染品牌栏/标题）。优先替换稳定占位符；
// 面对旧构建产物时在 </head> 前补入元数据。
func injectAppName(index []byte, name string) []byte {
	escaped := escapeHTMLAttr(name)
	placeholder := []byte(appNamePlaceholder)
	if bytes.Contains(index, placeholder) {
		return bytes.ReplaceAll(index, placeholder, []byte(escaped))
	}

	lower := bytes.ToLower(index)
	closeHead := []byte("</head>")
	at := bytes.Index(lower, closeHead)
	if at < 0 {
		return index // 无法定位 head：保留占位符，前端按缺省名兜底
	}
	meta := []byte(`<meta name="app-name" content="` + escaped + `">`)
	result := make([]byte, 0, len(index)+len(meta))
	result = append(result, index[:at]...)
	result = append(result, meta...)
	result = append(result, index[at:]...)
	return result
}

// validCSPNonce 限制 nonce 为 token-safe 的 base64url 字符串，防止错误注入器把响应头
// 或 HTML 属性变成可控内容。默认 randomToken 生成的值天然满足此约束。
func validCSPNonce(nonce string) bool {
	if nonce == "" {
		return false
	}
	for _, r := range nonce {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}

// handleFrontendAssets 提供 Vite 产出的同源静态资源。构建产物不存在时返回 404，
// 不把资源请求错误地回退成 SPA HTML。
func handleFrontendAssets() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assets, err := fs.Sub(frontendFiles, "frontend-dist/assets")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		http.StripPrefix("/assets/", http.FileServer(http.FS(assets))).ServeHTTP(w, r)
	})
}

// handleFrontendRootAsset 提供构建产物根目录中的品牌资源。只允许由路由显式注册的文件名，
// 避免根级资源请求意外访问其他构建文件或回退成 SPA HTML。
func handleFrontendRootAsset(name string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := fs.ReadFile(frontendFiles, "frontend-dist/"+name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Header().Set("Content-Type", "image/svg+xml")
		_, _ = w.Write(data)
	})
}
