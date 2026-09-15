// 检查更新：查询上游最新发布（GitHub Releases）并与当前服务版本比较。
// 上游查询经 TTL 缓存收敛（GitHub API 未认证限流 60 次/小时/出口 IP），
// 版本比较只认纯数字点分格式（可选 v 前缀），dev 构建等无法解析的版本
// 交由 status=unknown 表达，不猜测结论。
package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/branding"
)

// releaseCheckTTL 是上游查询结果的缓存窗口：检查更新由管理员手动触发，
// 频度天然低，1 小时足够规避未认证限流；发布滞后至多一个窗口可接受。
const releaseCheckTTL = time.Hour

// ReleaseInfo 是上游最新发布的查询结果。
type ReleaseInfo struct {
	Version   string    // 最新发布 tag（如 v1.2.0）；空串表示本次查询失败
	URL       string    // 发布页链接
	CheckedAt time.Time // 查询时间（缓存命中时为原始查询时间）
}

// ReleaseChecker 查询上游最新发布版本（总览页"检查更新"的数据源）。
// 实现应自带缓存与超时，不要求调用方控制频度。
type ReleaseChecker interface {
	// LatestRelease 返回上游最新发布；force 为 true 时跳过缓存直接查询
	// 上游（总览页手动刷新，管理员预期"点了就要最新结果"）。
	LatestRelease(ctx context.Context, force bool) ReleaseInfo
}

// NewGitHubReleaseChecker 创建查询本项目上游仓库最新 Release 的检查器
// （仓库地址派生自 branding.ModulePath，发布 tag 形如 v1.2.0）。
func NewGitHubReleaseChecker() *GitHubReleaseChecker {
	apiURL := "https://api.github.com/repos/" +
		strings.TrimPrefix(branding.ModulePath, "github.com/") + "/releases/latest"
	return newGitHubReleaseChecker(apiURL, &http.Client{Timeout: 10 * time.Second}, releaseCheckTTL, time.Now)
}

// GitHubReleaseChecker 经 GitHub Releases API 查询最新发布，成功结果缓存
// releaseCheckTTL；失败不缓存（下次点击重试）。零值不可用，经构造器创建。
type GitHubReleaseChecker struct {
	apiURL string
	client *http.Client
	ttl    time.Duration
	now    func() time.Time

	mu     sync.Mutex
	cached ReleaseInfo
}

// newGitHubReleaseChecker 是测试可注入的底层构造器。
func newGitHubReleaseChecker(apiURL string, client *http.Client, ttl time.Duration, now func() time.Time) *GitHubReleaseChecker {
	return &GitHubReleaseChecker{apiURL: apiURL, client: client, ttl: ttl, now: now}
}

// LatestRelease 返回上游最新发布；Version 为空串表示查询失败。缺省命中
// TTL 缓存；force=true 跳过缓存读（结果成功时仍回写缓存，惠及后续自动检查）。
func (g *GitHubReleaseChecker) LatestRelease(ctx context.Context, force bool) ReleaseInfo {
	if !force {
		g.mu.Lock()
		if g.cached.Version != "" && g.now().Sub(g.cached.CheckedAt) < g.ttl {
			cached := g.cached
			g.mu.Unlock()
			return cached
		}
		g.mu.Unlock()
	}

	info := g.fetch(ctx)
	g.mu.Lock()
	defer g.mu.Unlock()
	if info.Version != "" {
		g.cached = info
	}
	return info
}

// fetch 执行一次上游查询。GitHub 的 /releases/latest 只返回最新正式发布
// （草稿与预发布除外），与"有新版本可升级"的语义一致；body 按 1MB 截断，
// 响应字段只取 tag_name 与 html_url。
func (g *GitHubReleaseChecker) fetch(ctx context.Context) ReleaseInfo {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.apiURL, nil)
	if err != nil {
		return ReleaseInfo{CheckedAt: g.now()}
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := g.client.Do(req)
	if err != nil {
		return ReleaseInfo{CheckedAt: g.now()}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ReleaseInfo{CheckedAt: g.now()}
	}
	var payload struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil || payload.TagName == "" {
		return ReleaseInfo{CheckedAt: g.now()}
	}
	return ReleaseInfo{Version: payload.TagName, URL: payload.HTMLURL, CheckedAt: g.now()}
}

// compareSemver 比较两个可选 v 前缀的点分版本号：返回 -1/0/1（a 相对 b）；
// 任一版本无法解析为纯数字分段（如 dev、0.2.0-web-pages、v1.3.0-rc.1）时
// ok=false。分段数可不同，缺失段按 0 补齐（1.2 == 1.2.0）。
func compareSemver(a, b string) (cmp int, ok bool) {
	as, okA := parseSemverSegments(a)
	bs, okB := parseSemverSegments(b)
	if !okA || !okB {
		return 0, false
	}
	n := max(len(as), len(bs))
	for i := range n {
		x, y := semverSegment(as, i), semverSegment(bs, i)
		if x != y {
			if x < y {
				return -1, true
			}
			return 1, true
		}
	}
	return 0, true
}

func parseSemverSegments(v string) ([]int, bool) {
	v = strings.TrimPrefix(strings.TrimPrefix(v, "v"), "V")
	if v == "" {
		return nil, false
	}
	parts := strings.Split(v, ".")
	segs := make([]int, 0, len(parts))
	for _, p := range parts {
		if p == "" {
			return nil, false
		}
		for _, r := range p {
			if r < '0' || r > '9' {
				return nil, false
			}
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, false
		}
		segs = append(segs, n)
	}
	return segs, true
}

func semverSegment(segs []int, i int) int {
	if i < len(segs) {
		return segs[i]
	}
	return 0
}
