// Package botlist 管理多机器人池的 token 列表：环境变量（BOT_TOKEN +
// BOT_TOKENS，经 config 解析为 cfg.BotTokens）提供基础列表，Web 管理端可向
// data/bots.json 追加/移除额外 bot（重启生效）。token 只在内存与 0600 文件
// 中流转，绝不入数据库、绝不入日志（数据范围红线，与云盘凭据同级）。
package botlist

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/huaiminyetnotsleep/spore/internal/config"
)

// FileName 是机器人 token 列表在数据目录中的固定文件名（与 session.json
// 同级，gitignored；凭据只落该文件，不进数据库）。
const FileName = "bots.json"

// Source 标记 token 的配置来源；env 来源在管理端只读（以环境变量为准）。
type Source string

const (
	SourceEnv  Source = "env"
	SourceFile Source = "file"
)

// Bot 是有效机器人列表的单个条目：token 与来源。任何日志/错误/接口响应
// 不得包含 Token 值。
type Bot struct {
	Token  string
	Source Source
}

// FileConfig 是 bots.json 的持久化结构。
type FileConfig struct {
	Tokens []string `json:"tokens"`
}

// Manager 持有 bots.json 的内存副本并提供增删（Web 管理端调用，原子写盘）。
// 加载失败（文件损坏/含非法 token）时保持空列表不阻断启动——错误经
// LoadError 暴露给事件中心，有效列表退化为 env 来源。
type Manager struct {
	mu      sync.Mutex
	path    string
	log     *slog.Logger
	file    FileConfig
	loadErr error
}

// NewManager 读取 bots.json（不存在视为空列表）；损坏或含非法 token 的条目
// 整体跳过并记 Warn，不阻断启动。
func NewManager(dataDir string, log *slog.Logger) *Manager {
	m := &Manager{
		path: filepath.Join(dataDir, FileName),
		log:  log,
		file: FileConfig{Tokens: []string{}},
	}
	fc, err := LoadFile(m.path)
	if err != nil {
		m.loadErr = err
		log.Warn("机器人列表文件加载失败，忽略文件条目（可修复后在管理端重新保存）",
			"path", m.path, "error", err.Error())
		return m
	}
	m.file = fc
	return m
}

// LoadError 返回启动加载错误；nil 表示文件正常（含不存在）。
func (m *Manager) LoadError() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.loadErr
}

// List 返回文件条目快照（不含 env 来源）。
func (m *Manager) List() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.file.Tokens))
	copy(out, m.file.Tokens)
	return out
}

// Add 校验并追加 token（格式校验、文件内去重），原子写盘并更新内存副本。
func (m *Manager) Add(token string) error {
	token = strings.TrimSpace(token)
	if !config.IsValidBotToken(token) {
		return errors.New("token 格式无效（应为数字:字母数字组合的 Bot API token）")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.file.Tokens {
		if existing == token {
			return errors.New("该机器人已存在")
		}
	}
	fc := FileConfig{Tokens: append([]string{}, m.file.Tokens...)}
	fc.Tokens = append(fc.Tokens, token)
	if err := SaveFile(m.path, fc); err != nil {
		return err
	}
	m.file = fc
	return nil
}

// Remove 移除 token 并原子写盘；不存在时返回受控错误。
func (m *Manager) Remove(token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	idx := -1
	for i, existing := range m.file.Tokens {
		if existing == token {
			idx = i
			break
		}
	}
	if idx < 0 {
		return errors.New("该机器人不在文件配置中（env 来源的机器人请在环境变量中移除）")
	}
	fc := FileConfig{Tokens: append([]string{}, m.file.Tokens...)}
	fc.Tokens = append(fc.Tokens[:idx], fc.Tokens[idx+1:]...)
	if err := SaveFile(m.path, fc); err != nil {
		return err
	}
	m.file = fc
	return nil
}

// LoadFile 读 bots.json；不存在返回空列表（无错误）。解析失败返回受控错误，
// 不暴露文件内容。
func LoadFile(path string) (FileConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return FileConfig{Tokens: []string{}}, nil
		}
		return FileConfig{}, fmt.Errorf("读取机器人列表失败: %w", err)
	}
	var fc FileConfig
	if err := json.Unmarshal(data, &fc); err != nil {
		return FileConfig{}, fmt.Errorf("机器人列表格式无效: %w", err)
	}
	if fc.Tokens == nil {
		fc.Tokens = []string{}
	}
	for _, tok := range fc.Tokens {
		if !config.IsValidBotToken(strings.TrimSpace(tok)) {
			return FileConfig{}, errors.New("机器人列表含格式非法的 token，整体拒绝加载")
		}
	}
	return fc, nil
}

// SaveFile 以临时文件 + rename 原子写配置，权限 0600（含 bot 凭据，与
// session.json 同级同权限语义；写失败保持原文件不变）。
func SaveFile(path string, fc FileConfig) error {
	data, err := json.MarshalIndent(fc, "", "  ")
	if err != nil {
		return fmt.Errorf("编码机器人列表失败: %w", err)
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".bots-*.tmp")
	if err != nil {
		return fmt.Errorf("创建机器人列表临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // rename 成功后为不存在的路径，是无害 no-op
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("设置机器人列表权限失败: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("写入机器人列表失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭机器人列表临时文件失败: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("替换机器人列表失败: %w", err)
	}
	return nil
}

// Resolve 合并 env 与文件 token 为有效机器人列表：env 在前（主 bot 恒为
// 首项），按 token 去重（env 优先占位），总数上限 config.MaxBots（超出
// 截断并记 Warn——配置错误不应阻断启动，管理员可在管理端修正后重启）。
func Resolve(envTokens []string, m *Manager, log *slog.Logger) []Bot {
	bots := make([]Bot, 0, len(envTokens)+4)
	seen := make(map[string]struct{}, len(envTokens)+4)
	for _, tok := range envTokens {
		if _, dup := seen[tok]; dup {
			continue
		}
		seen[tok] = struct{}{}
		bots = append(bots, Bot{Token: tok, Source: SourceEnv})
	}
	if m != nil {
		for _, tok := range m.List() {
			if _, dup := seen[tok]; dup {
				continue
			}
			seen[tok] = struct{}{}
			bots = append(bots, Bot{Token: tok, Source: SourceFile})
		}
	}
	if len(bots) > config.MaxBots {
		log.Warn("机器人数量超过上限，多余条目被忽略（重启前请修正配置）",
			"limit", config.MaxBots, "total", len(bots))
		bots = bots[:config.MaxBots]
	}
	return bots
}

// SessionPath 返回指定 bot 的 MTProto 会话文件路径：主 bot（env 首项）沿用
// 历史 bot-session.json（已有会话免重登），其余按 bot id 独立文件（bot id
// 即 token 的数字前缀，无需等 getMe）。会话文件按 bot 隔离，互不覆盖。
func SessionPath(dataDir string, token string, primary bool) string {
	if primary {
		return filepath.Join(dataDir, "bot-session.json")
	}
	return filepath.Join(dataDir, "bot-session-"+tokenPrefix(token)+".json")
}

// BotID 返回 token 的数字前缀（即该 bot 的 Telegram 账号 ID）：getMe 之前
// 即可确定 bot 身份主键（会话文件命名、池成员归备用）。
func BotID(token string) int64 {
	prefix := tokenPrefix(token)
	id, _ := strconv.ParseInt(prefix, 10, 64)
	return id
}

// tokenPrefix 返回 token 的数字前缀（即 bot id 的十进制表示）。
func tokenPrefix(token string) string {
	if idx := strings.IndexByte(token, ':'); idx > 0 {
		return token[:idx]
	}
	// 理论不可达：token 均经格式校验；兜底用整个 token 的话太长，改为固定名
	return "unknown"
}
