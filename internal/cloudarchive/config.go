package cloudarchive

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// FileName 是云盘下载配置在数据目录中的固定文件名（与 session.json 同级，
// gitignored；凭据只落该文件，不进数据库）。
const FileName = "cloud-drive.json"

// namePattern 目的地名称规则：小写字母开头，仅 [a-z0-9-]，至多 32 字符。
// 禁止下划线——env 映射把 `-` 转 `_` 后会与原生下划线名撞车。
var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// Config 是云盘下载的全局配置（多目的地列表 + 总开关）。
type Config struct {
	Enabled            bool          `json:"enabled"`
	DefaultDestination string        `json:"default_destination"`
	Destinations       []Destination `json:"destinations"`
}

// Destination 是单个网盘目的地。Type 对应 rclone 后端类型（如 mega）；
// Options 是该后端的配置键值对（值可能为 rclone obscure 后的凭据，
// 任何日志与错误信息不得包含其值）。
type Destination struct {
	Name       string            `json:"name"`
	Type       string            `json:"type"`
	PathPrefix string            `json:"path_prefix"`
	Enabled    bool              `json:"enabled"`
	Options    map[string]string `json:"options"`
}

// EnabledDestination 按名称查找已启用的目的地；不存在或未启用返回 false。
// 消费方（queue 云盘任务、web 连通性测试）统一经此入口取目的地快照。
func (c Config) EnabledDestination(name string) (Destination, bool) {
	for _, d := range c.Destinations {
		if d.Name == name && d.Enabled {
			return d, true
		}
	}
	return Destination{}, false
}

// providerHomeSites 是已知 rclone 后端类型到服务商官网的内置映射（上传成功
// 确认文案展示用）。地址是代码内常量：不经配置注入，避免管理员可控的任意外链。
var providerHomeSites = map[string]string{
	"mega": "https://mega.nz/",
}

// ProviderHomeSite 返回 providerType（Destination.Type，大小写不敏感）对应的
// 网盘服务商官网地址；未知类型返回空串（调用方省略官网展示）。
func ProviderHomeSite(providerType string) string {
	return providerHomeSites[strings.ToLower(strings.TrimSpace(providerType))]
}

// Validate 校验配置结构并返回受控中文错误。
// 规则：
//   - 结构校验（任何时候执行）：目的地 name 匹配 namePattern 且唯一、
//     type 非空、options 键值均为非空字符串；
//   - 门禁校验（enabled=true 时追加全部成立）：default_destination 指向
//     已启用的目的地、目的地列表非空且默认目的地已配置；
//   - enabled=false 允许不完整草稿：默认目的地悬空、目的地未启用等
//     门禁类问题不报错，便于管理台分步编辑。
func validatePathPrefix(prefix string) error {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return nil
	}
	if strings.HasPrefix(prefix, "/") || strings.HasPrefix(prefix, "\\") {
		return errors.New("不能是绝对路径")
	}
	for _, r := range prefix {
		if r < 0x20 || r == 0x7f {
			return errors.New("不能包含控制字符")
		}
	}
	for _, part := range strings.FieldsFunc(prefix, func(r rune) bool { return r == '/' || r == '\\' }) {
		if part == ".." {
			return errors.New("不能包含 .. 路径段")
		}
	}
	return nil
}

func (c Config) Validate() error {
	seen := make(map[string]struct{}, len(c.Destinations))
	var msgs []string
	for _, d := range c.Destinations {
		if !namePattern.MatchString(d.Name) {
			msgs = append(msgs, "目的地名称不合法（需以小写字母开头，仅含小写字母、数字与连字符，长度 1–32）")
			continue
		}
		if _, dup := seen[d.Name]; dup {
			msgs = append(msgs, "目的地名称重复："+d.Name)
			continue
		}
		seen[d.Name] = struct{}{}
		if strings.TrimSpace(d.Type) == "" {
			msgs = append(msgs, "目的地 "+d.Name+" 缺少类型（type）")
		}
		if err := validatePathPrefix(d.PathPrefix); err != nil {
			msgs = append(msgs, "目的地 "+d.Name+" 的路径前缀"+err.Error())
		}
		for k, v := range d.Options {
			if strings.TrimSpace(k) == "" {
				msgs = append(msgs, "目的地 "+d.Name+" 存在空配置键")
				break
			}
			if v == "" {
				msgs = append(msgs, "目的地 "+d.Name+" 的配置项 "+k+" 值为空")
			}
		}
	}
	if c.Enabled {
		if len(c.Destinations) == 0 {
			msgs = append(msgs, "开启云盘下载前至少需要配置一个目的地")
		}
		if c.DefaultDestination == "" {
			msgs = append(msgs, "开启云盘下载前需要指定默认目的地")
		} else if _, ok := c.EnabledDestination(c.DefaultDestination); !ok {
			msgs = append(msgs, "默认目的地 "+c.DefaultDestination+" 不存在或未启用")
		}
	}
	if len(msgs) > 0 {
		return errors.New(strings.Join(msgs, "；"))
	}
	return nil
}

// LoadFile 从 path 读取配置。文件不存在视为"从未配置"，返回零值配置且无错误
// （功能默认关闭）；文件存在但读取或 JSON 解析失败时返回错误（调用方据此
// 上报 cloud.config_invalid，维持内存中的旧快照）。
func LoadFile(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Config{}, nil
		}
		return Config{}, fmt.Errorf("读取云盘配置失败: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("云盘配置格式无效: %w", err)
	}
	if cfg.Destinations == nil {
		cfg.Destinations = []Destination{}
	}
	return cfg, nil
}

// SaveFile 校验 cfg 后以临时文件 + rename 原子写配置，权限 0600
// （含网盘凭据，与 session.json 同级同权限语义；写失败保持原文件不变）。
// 校验前置保证磁盘上永远不会出现未通过校验的配置。
func SaveFile(path string, cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("编码云盘配置失败: %w", err)
	}
	dir := fileDir(path)
	tmp, err := os.CreateTemp(dir, ".cloud-drive-*.tmp")
	if err != nil {
		return fmt.Errorf("创建云盘配置临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // rename 成功后为不存在的路径，是无害 no-op
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("设置云盘配置权限失败: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("写入云盘配置失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭云盘配置临时文件失败: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("替换云盘配置失败: %w", err)
	}
	return nil
}

// fileDir 返回 path 所在目录（相对路径时用 "."，与 CreateTemp 语义对齐）。
func fileDir(path string) string {
	dir := strings.LastIndexByte(path, os.PathSeparator)
	if dir < 0 {
		return "."
	}
	if dir == 0 {
		return string(os.PathSeparator)
	}
	return path[:dir]
}
