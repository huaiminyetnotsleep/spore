package web

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
)

const settingKeyGitHubOAuthConfig = "github_oauth_config"

type githubOAuthConfig struct {
	Provider          string `json:"provider"`
	Enabled           bool   `json:"enabled"`
	ClientID          string `json:"client_id"`
	SecretCipher      string `json:"secret_cipher"`
	SecretNonce       string `json:"secret_nonce"`
	CipherVersion     int    `json:"cipher_version"`
	UpdatedAt         int64  `json:"updated_at"`
	LegacyEnvDisabled bool   `json:"legacy_env_disabled,omitempty"`
}

func (s *Server) oauthCredentials(ctx context.Context) (clientID, clientSecret string, enabled bool) {
	raw, ok, err := s.st.GetSetting(ctx, settingKeyGitHubOAuthConfig)
	if err != nil {
		return "", "", false
	}
	if !ok {
		return s.cfg.GitHubClientID, s.cfg.GitHubClientSecret,
			s.cfg.GitHubClientID != "" && s.cfg.GitHubClientSecret != ""
	}
	var cfg githubOAuthConfig
	if json.Unmarshal([]byte(raw), &cfg) != nil || cfg.Provider != "github" || !cfg.Enabled || cfg.ClientID == "" {
		return "", "", false
	}
	secret, err := decryptOAuthSecret(s.cfg.OAuthEncryptionKey, cfg.SecretCipher, cfg.SecretNonce)
	if err != nil {
		return "", "", false
	}
	return cfg.ClientID, secret, secret != ""
}

func (s *Server) oauthConfigured() bool {
	_, _, enabled := s.oauthCredentials(context.Background())
	return enabled
}

// oauthSettingsData 是 OAuth 配置的数据快照：SPA API DTO（api_oauth.go）
// 的唯一读取来源。
type oauthSettingsData struct {
	Configured bool   // Client ID 与 Secret 已完整配置（数据库或环境变量）
	Enabled    bool   // 通道启用状态（未配置时随环境变量派生）
	ClientID   string // 当前生效的 Client ID（未配置为空串；非敏感）
	SecretSet  bool   // Secret 是否已设置（只报布尔，永不回显内容）

	DBConfigured bool              // 数据库中已保存配置（区别于旧环境变量）
	DBConfig     githubOAuthConfig // 数据库配置原文（含密文，仅供内部判断）

	Binding    GitHubBinding // 绑定账号；Bound 为 false 时无意义
	Bound      bool
	ConfigErr  error // 配置读取失败（存储不可用/数据损坏）
	BindingErr error // 绑定读取失败（存储不可用）
}

// loadOAuthSettingsData 读取 OAuth 配置与绑定状态。
func (s *Server) loadOAuthSettingsData(ctx context.Context) oauthSettingsData {
	var d oauthSettingsData
	cfg, configured, err := s.loadGitHubOAuthConfig(ctx)
	if err != nil {
		d.ConfigErr = err
	} else if configured {
		d.Configured = true
		d.DBConfigured = true
		d.DBConfig = cfg
		d.Enabled = cfg.Enabled
		d.ClientID = cfg.ClientID
		d.SecretSet = cfg.SecretCipher != "" && cfg.SecretNonce != ""
	} else {
		d.ClientID = s.cfg.GitHubClientID
		d.SecretSet = s.cfg.GitHubClientSecret != ""
		d.Configured = s.cfg.GitHubClientID != "" && s.cfg.GitHubClientSecret != ""
		d.Enabled = d.Configured
	}
	binding, bound, err := loadGitHubBinding(ctx, s.st)
	if err != nil {
		d.BindingErr = err
	} else if bound {
		d.Bound = true
		d.Binding = binding
	}
	return d
}

func encryptOAuthSecret(key []byte, secret string) (cipherText, nonce string, err error) {
	if len(key) != 32 {
		return "", "", errors.New("未配置有效的 WEB_OAUTH_ENCRYPTION_KEY")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", "", err
	}
	n := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, n); err != nil {
		return "", "", err
	}
	c := gcm.Seal(nil, n, []byte(secret), nil)
	return base64.RawURLEncoding.EncodeToString(c), base64.RawURLEncoding.EncodeToString(n), nil
}

func decryptOAuthSecret(key []byte, cipherText, nonce string) (string, error) {
	if len(key) != 32 || cipherText == "" || nonce == "" {
		return "", errors.New("OAuth Secret 无法解密")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	c, err := base64.RawURLEncoding.DecodeString(cipherText)
	if err != nil {
		return "", err
	}
	n, err := base64.RawURLEncoding.DecodeString(nonce)
	if err != nil {
		return "", err
	}
	plain, err := gcm.Open(nil, n, c, nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func (s *Server) loadGitHubOAuthConfig(ctx context.Context) (githubOAuthConfig, bool, error) {
	raw, ok, err := s.st.GetSetting(ctx, settingKeyGitHubOAuthConfig)
	if err != nil {
		return githubOAuthConfig{}, false, apperr.Wrap(apperr.CodeStoreUnavailable, err)
	}
	if !ok {
		return githubOAuthConfig{}, false, nil
	}
	var cfg githubOAuthConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return githubOAuthConfig{}, true, errors.New("GitHub OAuth 配置格式无效")
	}
	return cfg, true, nil
}

func (s *Server) saveGitHubOAuthConfig(ctx context.Context, cfg githubOAuthConfig) error {
	cfg.Provider = "github"
	cfg.CipherVersion = 1
	cfg.UpdatedAt = time.Now().UnixMilli()
	data, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := s.st.SetSetting(ctx, settingKeyGitHubOAuthConfig, string(data)); err != nil {
		return apperr.Wrap(apperr.CodeStoreUnavailable, err)
	}
	return nil
}

func oauthConfigError() string {
	return "修改 GitHub 登录凭据需要配置有效的 WEB_OAUTH_ENCRYPTION_KEY（32 字节主密钥）。"
}
