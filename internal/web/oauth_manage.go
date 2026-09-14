package web

// GitHub OAuth 配置变更核心：SPA API（POST /api/v1/oauth/settings）的业务
// 规则、加密、写库与审计唯一来源；审计只记
// enabled/client_id_changed/secret_changed 布尔状态，Secret 本身
// 不进入审计、日志或任何响应。

import (
	"context"
	"strings"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
)

// oauthSettingsUpdate 是一次 GitHub OAuth 配置变更的原始载荷
// （SPA API JSON 映射到本结构，再交 updateOAuthSettings 执行）。
type oauthSettingsUpdate struct {
	ClientID     string
	ClientSecret string // 仅本次提交携带新 Secret 时非空；任何输出都不回显
	Enabled      bool
}

// oauthSettingsParamError 表示 OAuth 配置载荷或本地加密配置不合法；
// 它与存储失败分离，使 API 能保留正确的 400/5xx 语义。
type oauthSettingsParamError struct {
	msg string
}

func (e *oauthSettingsParamError) Error() string { return e.msg }

func oauthParamError(msg string) error { return &oauthSettingsParamError{msg: msg} }

// updateOAuthSettings 应用 GitHub OAuth 配置变更（action：save / enable /
// disable / clear）。数据库配置优先于环境变量；保存后新 OAuth 请求立即读取
// 最新配置。参数或配置拒绝返回受控中文文案错误；
// 存储失败透传底层错误（调用方统一走 apperr 错误链路）。
func (s *Server) updateOAuthSettings(ctx context.Context, action string, in oauthSettingsUpdate) error {
	cfg, configured, err := s.loadGitHubOAuthConfig(ctx)
	if err != nil {
		return err
	}
	if !configured {
		cfg = githubOAuthConfig{Provider: "github", ClientID: s.cfg.GitHubClientID}
	}
	if action == "clear" {
		cfg.ClientID, cfg.SecretCipher, cfg.SecretNonce = "", "", ""
		cfg.Enabled = false
		cfg.LegacyEnvDisabled = false
		if err := s.saveGitHubOAuthConfig(ctx, cfg); err != nil {
			return err
		}
		s.audit(ctx, "oauth.credentials.clear", "github", map[string]any{"effect": "immediate"})
		return nil
	}
	if action == "save" {
		clientID := strings.TrimSpace(in.ClientID)
		if clientID == "" {
			return oauthParamError("Client ID 不能为空。")
		}
		cfg.ClientID = clientID
		secret := strings.TrimSpace(in.ClientSecret)
		if secret != "" {
			cipherText, nonce, err := encryptOAuthSecret(s.cfg.OAuthEncryptionKey, secret)
			if err != nil {
				return oauthParamError(oauthConfigError())
			}
			cfg.SecretCipher, cfg.SecretNonce = cipherText, nonce
		} else if cfg.SecretCipher == "" || cfg.SecretNonce == "" {
			if !configured && s.cfg.GitHubClientSecret != "" {
				cipherText, nonce, err := encryptOAuthSecret(s.cfg.OAuthEncryptionKey, s.cfg.GitHubClientSecret)
				if err != nil {
					return oauthParamError(oauthConfigError())
				}
				cfg.SecretCipher, cfg.SecretNonce = cipherText, nonce
			} else {
				return oauthParamError("Client Secret 不能为空；页面不会回显已保存的 Secret。")
			}
		}
		cfg.LegacyEnvDisabled = false
		cfg.Enabled = in.Enabled
	}
	if action == "enable" {
		if !configured && s.cfg.GitHubClientID != "" && s.cfg.GitHubClientSecret != "" {
			// 旧环境变量配置本来已经启用；不要为了“启用”而强制把 Secret
			// 持久化到数据库，缺少加密主密钥时也应可正常操作。
			s.audit(ctx, "oauth.config", "github", map[string]any{
				"enabled": true, "client_id_changed": false, "secret_changed": false,
				"source": "environment"})
			return nil
		}
		if configured && cfg.LegacyEnvDisabled && cfg.ClientID == s.cfg.GitHubClientID &&
			s.cfg.GitHubClientSecret != "" {
			if err := s.st.DeleteSetting(ctx, settingKeyGitHubOAuthConfig); err != nil {
				return apperr.Wrap(apperr.CodeStoreUnavailable, err)
			}
			s.audit(ctx, "oauth.config", "github", map[string]any{
				"enabled": true, "client_id_changed": false, "secret_changed": false,
				"source": "environment"})
			return nil
		}
		if cfg.ClientID == "" || cfg.SecretCipher == "" || cfg.SecretNonce == "" {
			return oauthParamError("请先完整配置 Client ID 与 Client Secret。")
		}
		if _, err := decryptOAuthSecret(s.cfg.OAuthEncryptionKey, cfg.SecretCipher, cfg.SecretNonce); err != nil {
			return oauthParamError(oauthConfigError())
		}
		cfg.Enabled = true
	}
	if action == "disable" {
		cfg.Enabled = false
		cfg.LegacyEnvDisabled = !configured && s.cfg.GitHubClientID != "" && s.cfg.GitHubClientSecret != ""
		if cfg.LegacyEnvDisabled && len(s.cfg.OAuthEncryptionKey) == 32 {
			if cipherText, nonce, encErr := encryptOAuthSecret(s.cfg.OAuthEncryptionKey, s.cfg.GitHubClientSecret); encErr == nil {
				cfg.SecretCipher, cfg.SecretNonce = cipherText, nonce
				cfg.LegacyEnvDisabled = false
			}
		}
	}
	if err := s.saveGitHubOAuthConfig(ctx, cfg); err != nil {
		return err
	}
	detail := map[string]any{"enabled": cfg.Enabled, "client_id_changed": action == "save", "secret_changed": action == "save" && in.ClientSecret != ""}
	s.audit(ctx, "oauth.config", "github", detail)
	return nil
}
