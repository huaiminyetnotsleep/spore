// Package r2backup 提供定时备份的 Cloudflare R2 异地上传：连接配置
// （data/r2-backup.json，0600 原子写，凭据只落该文件、不进数据库）、
// S3 兼容客户端封装（minio-go，仅此一家 provider）、全量 ZIP 上传与
// 远端按份数轮转。配置刻意不进 settings 表：R2 Secret 若随 DB 快照进入
// 备份件，拿到一份备份即拿到 bucket 钥匙（自带钥匙死胡同）。
package r2backup

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
)

// FileName 是 R2 上传配置在数据目录中的固定文件名（与 cloud-drive.json
// 同级同权限语义，gitignored；打包上传时必须排除，凭据不出机器）。
const FileName = "r2-backup.json"

// accountIDPattern：Cloudflare Account ID 为 32 位十六进制串。
var accountIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)

// bucketPattern：S3 兼容桶名（3–63 位小写字母/数字/连字符，首尾字母数字）。
var bucketPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)

// Config 是 R2 上传配置与运行状态。LastUploadAt / LastUploadError 是
// 最近一次上传结果（供备份页展示），由上传流程回写、不参与校验。
type Config struct {
	Enabled         bool   `json:"enabled"`
	AccountID       string `json:"account_id"`
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	Bucket          string `json:"bucket"`
	LastUploadAt    int64  `json:"last_upload_at"`    // Unix 毫秒；0 = 从未上传
	LastUploadError string `json:"last_upload_error"` // 受控中文场景，非原始错误
}

// Path 返回 dataDir 下配置文件的固定路径。
func Path(dataDir string) string {
	return dataDir + string(os.PathSeparator) + FileName
}

// Endpoint 由 Account ID 拼出 S3 兼容端点（region 恒为 auto，无需配置）。
func Endpoint(accountID string) string {
	return "https://" + strings.ToLower(accountID) + ".r2.cloudflarestorage.com"
}

// Normalize 去除各字段首尾空白（Account ID/桶名顺带转小写）。
func (c *Config) Normalize() {
	c.AccountID = strings.ToLower(strings.TrimSpace(c.AccountID))
	c.AccessKeyID = strings.TrimSpace(c.AccessKeyID)
	c.SecretAccessKey = strings.TrimSpace(c.SecretAccessKey)
	c.Bucket = strings.ToLower(strings.TrimSpace(c.Bucket))
}

// Complete 报告连接四要素（Account ID、Access Key、Secret、Bucket）是否
// 已填齐——与 Enabled 无关：关闭状态允许保存不完整草稿，开启前必须齐备。
// 内部自行归一化，未 Normalize 的输入同样可用。
func (c Config) Complete() bool {
	id := strings.ToLower(strings.TrimSpace(c.AccountID))
	bucket := strings.ToLower(strings.TrimSpace(c.Bucket))
	return accountIDPattern.MatchString(id) &&
		len(strings.TrimSpace(c.AccessKeyID)) >= 8 &&
		len(strings.TrimSpace(c.SecretAccessKey)) >= 8 &&
		bucketPattern.MatchString(bucket)
}

// Validate 校验已填字段并返回受控中文错误；Enabled=true 时追加完整性
// 门禁（与 cloud-drive 配置同款"草稿可存、开启须全"语义）。
func (c *Config) Validate() error {
	c.Normalize()
	if c.AccountID != "" && !accountIDPattern.MatchString(c.AccountID) {
		return errors.New("Account ID 必须是 32 位十六进制字符串（Cloudflare 控制台右侧栏可复制）")
	}
	if c.Bucket != "" && !bucketPattern.MatchString(c.Bucket) {
		return errors.New("Bucket 名称不合法（3–63 位小写字母、数字与连字符，字母数字开头结尾）")
	}
	if c.Enabled && !c.Complete() {
		return errors.New("开启 R2 上传前需要完整填写 Account ID、Access Key ID、Secret Access Key 与 Bucket")
	}
	return nil
}

// fileMu 保护读-改-写整个文件的状态回写与配置保存之间不互相覆盖
// （调度协程写状态、管理端写配置是仅有的两个写方）。
var fileMu sync.Mutex

// Load 从 dataDir 读取配置。文件不存在视为"从未配置"，返回零值且无错误
// （功能默认关闭）；文件存在但读取或解析失败返回错误（调用方据此告警，
// 不静默当作关闭——配置坏了应当修，而不是悄悄停掉异地备份）。
func Load(dataDir string) (Config, error) {
	fileMu.Lock()
	defer fileMu.Unlock()
	return loadLocked(Path(dataDir))
}

func loadLocked(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Config{}, nil
		}
		return Config{}, fmt.Errorf("读取 R2 备份配置失败: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("R2 备份配置格式无效: %w", err)
	}
	cfg.Normalize()
	return cfg, nil
}

// Save 校验后以临时文件 + rename 原子写配置（权限 0600，含 Secret，
// 与 session.json 同级同权限语义；写失败保持原文件不变）。调用方持有
// 合并语义：空 Secret 等字段由调用方先行并入旧值再整体保存。
func Save(dataDir string, cfg Config) error {
	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		return err
	}
	fileMu.Lock()
	defer fileMu.Unlock()
	return saveLocked(Path(dataDir), cfg)
}

func saveLocked(path string, cfg Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("编码 R2 备份配置失败: %w", err)
	}
	dir := fileDirOf(path)
	tmp, err := os.CreateTemp(dir, ".r2-backup-*.tmp")
	if err != nil {
		return fmt.Errorf("创建 R2 配置临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // rename 成功后指向不存在的路径，无害 no-op
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("设置 R2 配置权限失败: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("写入 R2 配置失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭 R2 配置临时文件失败: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("替换 R2 配置失败: %w", err)
	}
	return nil
}

// UpdateStatus 在持有文件锁的前提下回写最近上传结果（成功清空错误、
// 失败记录受控场景）；配置字段保持原值，保存失败返回错误由调用方记日志。
func UpdateStatus(dataDir string, atUnixMilli int64, errMsg string) error {
	fileMu.Lock()
	defer fileMu.Unlock()
	cfg, err := loadLocked(Path(dataDir))
	if err != nil {
		return err
	}
	cfg.LastUploadAt = atUnixMilli
	cfg.LastUploadError = errMsg
	return saveLocked(Path(dataDir), cfg)
}

// fileDirOf 返回 path 所在目录（相对路径回退 "."，与 CreateTemp 对齐）。
func fileDirOf(path string) string {
	idx := strings.LastIndexByte(path, os.PathSeparator)
	switch {
	case idx < 0:
		return "."
	case idx == 0:
		return string(os.PathSeparator)
	default:
		return path[:idx]
	}
}
