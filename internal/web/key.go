package web

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"

	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// settings 表中由本包持有的键（value_json 为 JSON 编码值，与 access 包约定一致）。
const (
	settingKeyAccessKeyHash = "access_key_hash" // 访问密钥的 SHA-256 十六进制哈希（JSON 字符串）
	settingKeyGitHubBinding = "github_binding"  // 已绑定的 GitHub 账号（JSON 对象，见 GitHubBinding）
)

// accessKeyBytes 访问密钥的随机字节数：32 字节 = 256 位熵（下限）。
const accessKeyBytes = 32

// hashValue 把任意凭证（密钥/会话 ID）归一为 SHA-256 十六进制哈希。
// 库内与内存中一律只保留哈希形式，明文不落任何持久层。
func hashValue(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

// randomToken 生成 nBytes 字节熵的随机 token，base64url 无填充编码。
func randomToken(nBytes int) (string, error) {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("读取系统随机源失败: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// EnsureAccessKey 确保 settings 中存在访问密钥哈希：缺失（首次部署）时生成
// ≥256 位随机密钥，明文仅向 out（生产为 stderr）输出一次并提示保存，库内只写
// SHA-256 哈希。created 表示本次是否新生成。已存在时什么都不做、不打印。
func EnsureAccessKey(ctx context.Context, st *store.Store, out io.Writer, log *slog.Logger) (created bool, err error) {
	if _, ok, err := st.GetSetting(ctx, settingKeyAccessKeyHash); err != nil {
		return false, err
	} else if ok {
		return false, nil
	}
	key, err := randomToken(accessKeyBytes)
	if err != nil {
		return false, err
	}
	if err := storeAccessKeyHash(ctx, st, key); err != nil {
		return false, err
	}
	printAccessKey(out, key, false)
	log.Info("已生成管理端访问密钥（仅显示一次）")
	return true, nil
}

// ResetAccessKey 重新生成访问密钥（spore admin reset-key）：
// 更新哈希、失效全部既有会话并写审计；明文仅向 out 输出一次。
// 旧密钥随哈希覆盖立即失效，已登录会话随 DeleteAllWebSessions 全部失效。
func ResetAccessKey(ctx context.Context, st *store.Store, out io.Writer, log *slog.Logger) error {
	key, err := randomToken(accessKeyBytes)
	if err != nil {
		return err
	}
	if err := storeAccessKeyHash(ctx, st, key); err != nil {
		return err
	}
	if err := st.DeleteAllWebSessions(ctx); err != nil {
		return fmt.Errorf("失效全部会话失败: %w", err)
	}
	if err := st.AppendAudit(ctx, store.AuditEntry{
		Actor:  "admin",
		Action: "access_key.reset",
		Target: "web",
	}); err != nil {
		return err
	}
	printAccessKey(out, key, true)
	log.Info("访问密钥已重置，旧密钥与全部会话已失效")
	return nil
}

// storeAccessKeyHash 把密钥哈希以 JSON 字符串形式写入 settings。
func storeAccessKeyHash(ctx context.Context, st *store.Store, key string) error {
	raw, err := json.Marshal(hashValue(key))
	if err != nil {
		return err
	}
	return st.SetSetting(ctx, settingKeyAccessKeyHash, string(raw))
}

// loadAccessKeyHash 读出库内密钥哈希；未初始化时 ok 为 false。
func loadAccessKeyHash(ctx context.Context, st *store.Store) (hash string, ok bool, err error) {
	v, ok, err := st.GetSetting(ctx, settingKeyAccessKeyHash)
	if err != nil || !ok {
		return "", false, err
	}
	var h string
	if err := json.Unmarshal([]byte(v), &h); err != nil {
		return "", false, fmt.Errorf("访问密钥哈希损坏: %w", err)
	}
	return h, h != "", nil
}

// printAccessKey 把明文密钥输出一次，附保存与丢失后的重置指引。
// reset 为 true 时提示这是重置后的新密钥。
func printAccessKey(out io.Writer, key string, reset bool) {
	title := "首次生成"
	if reset {
		title = "已重置"
	}
	fmt.Fprintf(out, `==== Spore 管理端访问密钥（%s，仅此一次显示）====
%s
用途：粘贴到 Web 管理端登录页完成密钥登录。
请立即妥善保存；丢失或泄露后在服务器上运行：
  docker compose exec bot ./spore admin reset-key
（重置后旧密钥与全部已登录会话立即失效）
======================================================
`, title, key)
}

// GitHubBinding 是 settings.github_binding 的值模型：
// 管理员在已登录会话中绑定的唯一 GitHub 账号（GitHub 数字 ID 为准）。
type GitHubBinding struct {
	ID      int64  `json:"id"` // GitHub 数字 ID（稳定标识，登录比对依据）
	Login   string `json:"login,omitempty"`
	BoundAt int64  `json:"bound_at"` // Unix 毫秒
}

// loadGitHubBinding 读出当前绑定；未绑定时 ok 为 false。
func loadGitHubBinding(ctx context.Context, st *store.Store) (b GitHubBinding, ok bool, err error) {
	v, ok, err := st.GetSetting(ctx, settingKeyGitHubBinding)
	if err != nil || !ok {
		return GitHubBinding{}, false, err
	}
	if err := json.Unmarshal([]byte(v), &b); err != nil {
		return GitHubBinding{}, false, fmt.Errorf("GitHub 绑定数据损坏: %w", err)
	}
	return b, b.ID != 0, nil
}

// saveGitHubBinding 写入绑定。
func saveGitHubBinding(ctx context.Context, st *store.Store, b GitHubBinding) error {
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	return st.SetSetting(ctx, settingKeyGitHubBinding, string(raw))
}
