package cloudarchive

import (
	"archive/zip"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

const (
	BackupFormatVersion     = 1
	MinBackupPasswordLength = 8
	MaxBackupSize           = 4 << 20
	MaxBackupEntrySize      = 2 << 20

	backupManifestName = "manifest.json"
	backupPayloadName  = "cloud-drive.json.enc"
	backupCipherName   = "aes-256-gcm"
	backupKDFName      = "argon2id"

	argonTime    = uint32(2)
	argonMemory  = uint32(32 * 1024)
	argonThreads = uint8(2)
	argonKeyLen  = uint32(32)
)

var (
	ErrInvalidBackupPassword = errors.New("备份密码错误或加密载荷已损坏")
	ErrBackupTooLarge        = errors.New("备份包超过 4 MiB 大小上限")
)

// BackupManifest 是加密云盘配置备份 v1 的公开清单。清单只包含算法参数、
// 密文摘要及目的地名称，不包含 options 或其他凭据值。
type BackupManifest struct {
	FormatVersion    int             `json:"format_version"`
	CreatedAt        string          `json:"created_at"`
	AppVersion       string          `json:"app_version"`
	Cipher           ManifestCipher  `json:"cipher"`
	KDF              ManifestKDF     `json:"kdf"`
	Payload          ManifestPayload `json:"payload"`
	DestinationNames []string        `json:"destination_names"`
}

type ManifestCipher struct {
	Name  string `json:"name"`
	Nonce string `json:"nonce"`
}

type ManifestKDF struct {
	Name    string `json:"name"`
	Salt    string `json:"salt"`
	Time    uint32 `json:"time"`
	Memory  uint32 `json:"memory"`
	Threads uint8  `json:"threads"`
	KeyLen  uint32 `json:"key_length"`
}

type ManifestPayload struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// BackupMetadata 是 API 与审计可安全使用的备份摘要。
type BackupMetadata struct {
	FormatVersion    int      `json:"format_version"`
	CreatedAt        string   `json:"created_at"`
	AppVersion       string   `json:"app_version"`
	PayloadSHA256    string   `json:"sha256"`
	PayloadSize      int64    `json:"size"`
	PackageSize      int64    `json:"package_size"`
	DestinationNames []string `json:"destination_names"`
}

// ExportBackup 将完整 Config 加密为严格的 ZIP v1 包。
func ExportBackup(cfg Config, password, appVersion string, now time.Time) ([]byte, BackupMetadata, error) {
	if len([]rune(password)) < MinBackupPasswordLength {
		return nil, BackupMetadata{}, errors.New("备份密码至少需要 8 个字符")
	}
	if err := cfg.Validate(); err != nil {
		return nil, BackupMetadata{}, err
	}
	plain, err := json.Marshal(cfg)
	if err != nil {
		return nil, BackupMetadata{}, fmt.Errorf("编码云盘配置失败: %w", err)
	}
	if len(plain) > MaxBackupEntrySize {
		return nil, BackupMetadata{}, errors.New("云盘配置超过 2 MiB 大小上限")
	}

	salt := make([]byte, 16)
	nonce := make([]byte, 12)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, BackupMetadata{}, fmt.Errorf("生成备份随机盐失败: %w", err)
	}
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, BackupMetadata{}, fmt.Errorf("生成备份随机数失败: %w", err)
	}
	names := destinationNames(cfg)
	manifest := BackupManifest{
		FormatVersion: BackupFormatVersion,
		CreatedAt:     now.UTC().Format(time.RFC3339Nano),
		AppVersion:    appVersion,
		Cipher: ManifestCipher{
			Name: backupCipherName, Nonce: base64.StdEncoding.EncodeToString(nonce),
		},
		KDF: ManifestKDF{
			Name: backupKDFName, Salt: base64.StdEncoding.EncodeToString(salt),
			Time: argonTime, Memory: argonMemory, Threads: argonThreads, KeyLen: argonKeyLen,
		},
		Payload:          ManifestPayload{Name: backupPayloadName},
		DestinationNames: names,
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	defer clear(key)
	aead, err := newBackupAEAD(key)
	if err != nil {
		return nil, BackupMetadata{}, err
	}
	aad, err := manifestAAD(manifest)
	if err != nil {
		return nil, BackupMetadata{}, err
	}
	payload := aead.Seal(nil, nonce, plain, aad)
	sum := sha256.Sum256(payload)
	manifest.Payload.Size = int64(len(payload))
	manifest.Payload.SHA256 = hex.EncodeToString(sum[:])
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return nil, BackupMetadata{}, fmt.Errorf("编码备份清单失败: %w", err)
	}

	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	if err := writeZipEntry(zw, backupManifestName, manifestJSON); err != nil {
		return nil, BackupMetadata{}, err
	}
	if err := writeZipEntry(zw, backupPayloadName, payload); err != nil {
		return nil, BackupMetadata{}, err
	}
	if err := zw.Close(); err != nil {
		return nil, BackupMetadata{}, fmt.Errorf("完成备份包失败: %w", err)
	}
	if out.Len() > MaxBackupSize {
		return nil, BackupMetadata{}, ErrBackupTooLarge
	}
	meta := metadataFromManifest(manifest, int64(out.Len()))
	return out.Bytes(), meta, nil
}

// ImportBackup 严格校验并解密 ZIP v1 包。任何失败均不修改当前配置。
func ImportBackup(data []byte, password string) (Config, BackupMetadata, error) {
	if len(data) > MaxBackupSize {
		return Config{}, BackupMetadata{}, ErrBackupTooLarge
	}
	if len([]rune(password)) < MinBackupPasswordLength {
		return Config{}, BackupMetadata{}, errors.New("备份密码至少需要 8 个字符")
	}
	entries, err := readBackupEntries(data)
	if err != nil {
		return Config{}, BackupMetadata{}, err
	}
	var manifest BackupManifest
	if err := decodeStrictJSON(entries[backupManifestName], &manifest); err != nil {
		return Config{}, BackupMetadata{}, errors.New("备份清单格式无效")
	}
	if err := validateManifest(manifest); err != nil {
		return Config{}, BackupMetadata{}, err
	}
	payload := entries[backupPayloadName]
	if int64(len(payload)) != manifest.Payload.Size {
		return Config{}, BackupMetadata{}, errors.New("加密载荷大小与清单不一致")
	}
	sum := sha256.Sum256(payload)
	if !strings.EqualFold(hex.EncodeToString(sum[:]), manifest.Payload.SHA256) {
		return Config{}, BackupMetadata{}, errors.New("加密载荷摘要校验失败")
	}
	salt, _ := base64.StdEncoding.DecodeString(manifest.KDF.Salt)
	nonce, _ := base64.StdEncoding.DecodeString(manifest.Cipher.Nonce)
	key := argon2.IDKey([]byte(password), salt, manifest.KDF.Time, manifest.KDF.Memory, manifest.KDF.Threads, manifest.KDF.KeyLen)
	defer clear(key)
	aead, err := newBackupAEAD(key)
	if err != nil {
		return Config{}, BackupMetadata{}, err
	}
	aad, err := manifestAAD(manifest)
	if err != nil {
		return Config{}, BackupMetadata{}, err
	}
	plain, err := aead.Open(nil, nonce, payload, aad)
	if err != nil {
		return Config{}, BackupMetadata{}, ErrInvalidBackupPassword
	}
	if len(plain) > MaxBackupEntrySize {
		return Config{}, BackupMetadata{}, errors.New("解密后的配置超过 2 MiB 大小上限")
	}
	var cfg Config
	if err := decodeStrictJSON(plain, &cfg); err != nil {
		return Config{}, BackupMetadata{}, errors.New("备份中的云盘配置格式无效")
	}
	if cfg.Destinations == nil {
		cfg.Destinations = []Destination{}
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, BackupMetadata{}, errors.New("备份中的云盘配置校验失败")
	}
	if !equalStrings(destinationNames(cfg), manifest.DestinationNames) {
		return Config{}, BackupMetadata{}, errors.New("目的地名称与备份清单不一致")
	}
	return cfg, metadataFromManifest(manifest, int64(len(data))), nil
}

func readBackupEntries(data []byte) (map[string][]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, errors.New("备份包不是有效的 ZIP 文件")
	}
	if len(zr.File) != 2 {
		return nil, errors.New("备份包必须恰好包含两个文件")
	}
	out := make(map[string][]byte, 2)
	for _, f := range zr.File {
		name := f.Name
		clean := path.Clean(name)
		if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") || clean != name || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
			return nil, errors.New("备份包包含不安全的文件路径")
		}
		if !f.Mode().IsRegular() {
			return nil, errors.New("备份包不得包含目录、符号链接或其他特殊文件")
		}
		if name != backupManifestName && name != backupPayloadName {
			return nil, errors.New("备份包包含未知文件")
		}
		if _, exists := out[name]; exists {
			return nil, errors.New("备份包包含重复文件")
		}
		if f.UncompressedSize64 > MaxBackupEntrySize {
			return nil, errors.New("备份包内文件超过 2 MiB 大小上限")
		}
		if f.CompressedSize64 > 0 && f.UncompressedSize64 > f.CompressedSize64*100+1024 {
			return nil, errors.New("备份包内文件压缩比异常")
		}
		rc, err := f.Open()
		if err != nil {
			return nil, errors.New("无法读取备份包内文件")
		}
		b, readErr := io.ReadAll(io.LimitReader(rc, MaxBackupEntrySize+1))
		closeErr := rc.Close()
		if readErr != nil || closeErr != nil {
			return nil, errors.New("读取备份包内文件失败")
		}
		if len(b) > MaxBackupEntrySize {
			return nil, errors.New("备份包内文件超过 2 MiB 大小上限")
		}
		out[name] = b
	}
	if out[backupManifestName] == nil || out[backupPayloadName] == nil {
		return nil, errors.New("备份包缺少必要文件")
	}
	return out, nil
}

func validateManifest(m BackupManifest) error {
	if m.FormatVersion != BackupFormatVersion {
		return errors.New("不支持的云盘备份格式版本")
	}
	if m.Cipher.Name != backupCipherName || m.KDF.Name != backupKDFName {
		return errors.New("不支持的云盘备份加密算法")
	}
	if m.KDF.Time != argonTime || m.KDF.Memory != argonMemory || m.KDF.Threads != argonThreads || m.KDF.KeyLen != argonKeyLen {
		return errors.New("不支持的云盘备份密钥派生参数")
	}
	if m.Payload.Name != backupPayloadName || m.Payload.Size <= 0 || m.Payload.Size > MaxBackupEntrySize {
		return errors.New("备份清单中的加密载荷信息无效")
	}
	if len(m.Payload.SHA256) != sha256.Size*2 {
		return errors.New("备份清单中的摘要无效")
	}
	if _, err := hex.DecodeString(m.Payload.SHA256); err != nil {
		return errors.New("备份清单中的摘要无效")
	}
	salt, err := base64.StdEncoding.DecodeString(m.KDF.Salt)
	if err != nil || len(salt) != 16 {
		return errors.New("备份清单中的随机盐无效")
	}
	nonce, err := base64.StdEncoding.DecodeString(m.Cipher.Nonce)
	if err != nil || len(nonce) != 12 {
		return errors.New("备份清单中的随机数无效")
	}
	if _, err := time.Parse(time.RFC3339Nano, m.CreatedAt); err != nil {
		return errors.New("备份清单中的创建时间无效")
	}
	if len(m.DestinationNames) > 1024 {
		return errors.New("备份清单中的目的地数量异常")
	}
	return nil
}

func manifestAAD(m BackupManifest) ([]byte, error) {
	stable := struct {
		FormatVersion    int            `json:"format_version"`
		CreatedAt        string         `json:"created_at"`
		AppVersion       string         `json:"app_version"`
		Cipher           ManifestCipher `json:"cipher"`
		KDF              ManifestKDF    `json:"kdf"`
		PayloadName      string         `json:"payload_name"`
		DestinationNames []string       `json:"destination_names"`
	}{m.FormatVersion, m.CreatedAt, m.AppVersion, m.Cipher, m.KDF, m.Payload.Name, m.DestinationNames}
	b, err := json.Marshal(stable)
	if err != nil {
		return nil, fmt.Errorf("编码备份认证数据失败: %w", err)
	}
	return b, nil
}

func newBackupAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("初始化备份加密失败: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("初始化备份认证加密失败: %w", err)
	}
	return aead, nil
}

func writeZipEntry(zw *zip.Writer, name string, data []byte) error {
	h := &zip.FileHeader{Name: name, Method: zip.Store}
	h.SetMode(0o600)
	w, err := zw.CreateHeader(h)
	if err != nil {
		return fmt.Errorf("创建备份包文件失败: %w", err)
	}
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("写入备份包文件失败: %w", err)
	}
	return nil
}

func decodeStrictJSON(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.Decode(&struct{}{}) != io.EOF {
		return errors.New("存在多余 JSON 内容")
	}
	return nil
}

func destinationNames(cfg Config) []string {
	names := make([]string, 0, len(cfg.Destinations))
	for _, d := range cfg.Destinations {
		names = append(names, d.Name)
	}
	sort.Strings(names)
	return names
}

func metadataFromManifest(m BackupManifest, packageSize int64) BackupMetadata {
	return BackupMetadata{
		FormatVersion: m.FormatVersion, CreatedAt: m.CreatedAt, AppVersion: m.AppVersion,
		PayloadSHA256: m.Payload.SHA256, PayloadSize: m.Payload.Size, PackageSize: packageSize,
		DestinationNames: append([]string(nil), m.DestinationNames...),
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
