package web

// 兼容性测试：资料刷新失败保留旧资料、媒体设置保存 +
// 受控重启、OAuth Secret 加密落库与旧环境变量停用/恢复。

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/config"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

type fakeProfileLookup struct {
	profile store.UserProfile
	err     error
}

func (f *fakeProfileLookup) LookupUserProfile(context.Context, int64) (store.UserProfile, error) {
	return f.profile, f.err
}

func TestUserProfileRefreshKeepsOldOnFailure(t *testing.T) {
	lookup := &fakeProfileLookup{profile: store.UserProfile{ID: 7, Username: "new_name", DisplayName: "New Name"}}
	e := newTestEnvOpts(t, func(_ *config.Config, opt *Options) { opt.Profile = lookup })
	seedUser(t, e, 7, store.UserEnabled)
	if err := e.st.UpdateUserProfile(context.Background(), 7, "old_name", "Old Name"); err != nil {
		t.Fatal(err)
	}
	j := e.login(t)
	csrf := apiCSRFToken(t, e, j)
	resp := e.apiPost(j, "/api/v1/users/7/refresh-profile", csrf, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("刷新应成功，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
	}
	u, _ := e.st.GetUser(context.Background(), 7)
	if u.Username != "new_name" || u.DisplayName != "New Name" {
		t.Fatalf("成功刷新应覆盖资料: %+v", u)
	}
	lookup.err = errors.New("offline")
	csrf = apiCSRFToken(t, e, j)
	resp = e.apiPost(j, "/api/v1/users/7/refresh-profile", csrf, "")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("刷新失败应返回受控 503，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
	}
	u, _ = e.st.GetUser(context.Background(), 7)
	if u.Username != "new_name" || u.DisplayName != "New Name" {
		t.Fatalf("刷新失败不应覆盖旧资料: %+v", u)
	}
	if !e.containsAction("user.profile_refresh_failed") {
		t.Fatal("刷新失败应写脱敏审计")
	}
}

func TestMediaSettingsAndRestart(t *testing.T) {
	called := make(chan struct{}, 1)
	e := newTestEnvOpts(t, func(c *config.Config, opt *Options) {
		c.MaxFileSize = 50 << 20
		c.StreamLimit = 20 << 20
		opt.RestartFunc = func() error { called <- struct{}{}; return nil }
	})
	j := e.login(t)
	csrf := apiCSRFToken(t, e, j)
	resp := e.apiPost(j, "/api/v1/settings", csrf,
		`{"max_file_size":"40","max_file_unit":"MB","stream_limit":"10","stream_limit_unit":"MB"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("媒体设置应保存，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
	}
	if raw, ok, _ := e.st.GetSetting(context.Background(), settingKeyMaxFileSize); !ok || raw != "41943040" {
		t.Fatalf("文件大小应按字节保存: %q %v", raw, ok)
	}
	csrf = apiCSRFToken(t, e, j)
	resp = e.apiPost(j, "/api/v1/restart", csrf, `{"confirm":"restart"}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("重启应返回 202，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
	}
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("重启函数未被调用")
	}
	if !e.containsAction("admin.restart") {
		t.Fatal("重启应写审计")
	}
}

func TestOAuthSecretIsEncrypted(t *testing.T) {
	e := newTestEnvOpts(t, func(c *config.Config, _ *Options) {
		c.OAuthEncryptionKey = []byte(strings.Repeat("k", 32))
	})
	j := e.login(t)
	csrf := apiCSRFToken(t, e, j)
	resp := e.apiPost(j, "/api/v1/oauth/settings", csrf,
		`{"action":"save","client_id":"client-id","client_secret":"plain-secret","enabled":true}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("OAuth 配置应保存，得到 %d body=%s", resp.StatusCode, bodyOf(t, resp))
	}
	raw, ok, err := e.st.GetSetting(context.Background(), settingKeyGitHubOAuthConfig)
	if err != nil || !ok {
		t.Fatalf("应写入 OAuth 配置: %v %v", ok, err)
	}
	if strings.Contains(raw, "plain-secret") {
		t.Fatalf("数据库不得出现明文 Secret: %q", raw)
	}
	if _, _, enabled := e.srv.oauthCredentials(context.Background()); !enabled {
		t.Fatal("保存并启用后 OAuth 应立即可用")
	}
}

func TestLegacyEnvOAuthCanBeDisabledWithoutEncryptionKey(t *testing.T) {
	e := newTestEnv(t, func(c *config.Config) {
		c.GitHubClientID = "legacy-client"
		c.GitHubClientSecret = "legacy-secret"
	})
	j := e.login(t)
	csrf := apiCSRFToken(t, e, j)
	resp := e.apiPost(j, "/api/v1/oauth/settings", csrf, `{"action":"disable"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("旧环境 OAuth 应可在无主密钥时停用，得到 %d body=%s", resp.StatusCode, bodyOf(t, resp))
	}
	if _, _, enabled := e.srv.oauthCredentials(context.Background()); enabled {
		t.Fatal("停用后的旧环境 OAuth 不应继续生效")
	}

	csrf = apiCSRFToken(t, e, j)
	resp = e.apiPost(j, "/api/v1/oauth/settings", csrf, `{"action":"enable"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("旧环境 OAuth 应可恢复启用，得到 %d body=%s", resp.StatusCode, bodyOf(t, resp))
	}
	if _, _, enabled := e.srv.oauthCredentials(context.Background()); !enabled {
		t.Fatal("恢复启用后旧环境 OAuth 应生效")
	}
}

func TestValidateMediaInput(t *testing.T) {
	if _, err := parseMediaInput("1", "TB"); err == nil {
		t.Fatal("未知媒体单位应拒绝")
	}
	if err := config.ValidateMediaLimits(2<<20, 3<<20, 5<<30); err == nil {
		t.Fatal("流式阈值大于上限应拒绝")
	}
}
