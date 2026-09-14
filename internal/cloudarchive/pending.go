package cloudarchive

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/crypto/hkdf"
)

const (
	PendingFileName = "pending-cloud-drive.json"
	pendingVersion  = 1
	pendingKeyInfo  = "spore/cloud-drive-backup-candidate/v1"
)

var ErrPendingNotFound = errors.New("没有待确认的云盘配置备份")

// PendingStatus 是待确认候选的非敏感摘要。
type PendingStatus struct {
	Pending          bool     `json:"pending"`
	FormatVersion    int      `json:"format_version,omitempty"`
	CreatedAt        string   `json:"created_at,omitempty"`
	UploadedAt       int64    `json:"uploaded_at,omitempty"`
	AppVersion       string   `json:"app_version,omitempty"`
	SHA256           string   `json:"sha256,omitempty"`
	Size             int64    `json:"size,omitempty"`
	DestinationNames []string `json:"destination_names,omitempty"`
}

type pendingFile struct {
	Version          int      `json:"version"`
	FormatVersion    int      `json:"format_version"`
	CreatedAt        string   `json:"created_at"`
	UploadedAt       int64    `json:"uploaded_at"`
	AppVersion       string   `json:"app_version"`
	SHA256           string   `json:"sha256"`
	Size             int64    `json:"size"`
	DestinationNames []string `json:"destination_names"`
	Nonce            string   `json:"nonce"`
	Ciphertext       string   `json:"candidate"`
}

// PendingStore 使用 Web OAuth 主密钥经 HKDF 域分离后的专用 key 保护候选 Config。
// 用户备份密码不会写入文件，也不会跨请求保存。
type PendingStore struct {
	path string
	key  []byte
	mu   sync.Mutex
}

func NewPendingStore(dir string, rootKey []byte) (*PendingStore, error) {
	if len(rootKey) != 32 {
		return nil, errors.New("云盘备份服务端加密密钥不可用")
	}
	r := hkdf.New(sha256.New, rootKey, nil, []byte(pendingKeyInfo))
	key := make([]byte, 32)
	if _, err := io.ReadFull(r, key); err != nil {
		return nil, fmt.Errorf("派生云盘备份候选密钥失败: %w", err)
	}
	return &PendingStore{path: filepath.Join(dir, PendingFileName), key: key}, nil
}

func (s *PendingStore) Path() string { return s.path }

// Save 加密并原子保存已验证候选；文件权限固定为 0600。
func (s *PendingStore) Save(cfg Config, meta BackupMetadata, uploadedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked(cfg, meta, uploadedAt)
}

func (s *PendingStore) saveLocked(cfg Config, meta BackupMetadata, uploadedAt time.Time) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	plain, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("编码云盘配置候选失败: %w", err)
	}
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return fmt.Errorf("初始化候选加密失败: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return fmt.Errorf("初始化候选认证加密失败: %w", err)
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return fmt.Errorf("生成候选随机数失败: %w", err)
	}
	marker := pendingFile{
		Version: pendingVersion, FormatVersion: meta.FormatVersion, CreatedAt: meta.CreatedAt,
		UploadedAt: uploadedAt.UTC().UnixMilli(), AppVersion: meta.AppVersion,
		SHA256: meta.PayloadSHA256, Size: meta.PackageSize,
		DestinationNames: append([]string(nil), meta.DestinationNames...),
		Nonce:            base64.StdEncoding.EncodeToString(nonce),
	}
	aad, err := pendingAAD(marker)
	if err != nil {
		return err
	}
	marker.Ciphertext = base64.StdEncoding.EncodeToString(aead.Seal(nil, nonce, plain, aad))
	data, err := json.Marshal(marker)
	if err != nil {
		return fmt.Errorf("编码候选标记失败: %w", err)
	}
	return writePrivateAtomic(s.path, ".pending-cloud-drive-*.tmp", data)
}

// Status 返回候选的非敏感元数据；不存在时 Pending=false。
func (s *PendingStore) Status() (PendingStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	marker, err := s.readMarkerLocked()
	if errors.Is(err, ErrPendingNotFound) {
		return PendingStatus{}, nil
	}
	if err != nil {
		return PendingStatus{}, err
	}
	return statusFromMarker(marker), nil
}

// Apply 在持有候选锁时解密候选并执行 fn。候选先移出可见状态，fn 失败时
// 原子写回；这样确认成功后不会留下可重复应用的 marker，确认与取消也不会竞态。
func (s *PendingStore) Apply(fn func(Config, PendingStatus) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	marker, err := s.readMarkerLocked()
	if err != nil {
		return err
	}
	cfg, err := s.decryptLocked(marker)
	if err != nil {
		return err
	}
	data, err := json.Marshal(marker)
	if err != nil {
		return fmt.Errorf("编码待确认云盘配置失败: %w", err)
	}
	if err := os.Remove(s.path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrPendingNotFound
		}
		return fmt.Errorf("锁定待确认云盘配置失败: %w", err)
	}
	status := statusFromMarker(marker)
	if err := fn(cfg, status); err != nil {
		if restoreErr := writePrivateAtomic(s.path, ".pending-cloud-drive-*.tmp", data); restoreErr != nil {
			return fmt.Errorf("%w；恢复待确认候选失败", err)
		}
		return err
	}
	return nil
}

// Delete 取消并删除待确认候选；不存在返回 ErrPendingNotFound。
func (s *PendingStore) Delete() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Remove(s.path); errors.Is(err, os.ErrNotExist) {
		return ErrPendingNotFound
	} else if err != nil {
		return fmt.Errorf("删除待确认云盘配置失败: %w", err)
	}
	return nil
}

func (s *PendingStore) readMarkerLocked() (pendingFile, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return pendingFile{}, ErrPendingNotFound
	}
	if err != nil {
		return pendingFile{}, fmt.Errorf("读取待确认云盘配置失败: %w", err)
	}
	var marker pendingFile
	if err := decodeStrictJSON(data, &marker); err != nil || marker.Version != pendingVersion {
		return pendingFile{}, errors.New("待确认云盘配置标记无效")
	}
	return marker, nil
}

func (s *PendingStore) decryptLocked(marker pendingFile) (Config, error) {
	nonce, err := base64.StdEncoding.DecodeString(marker.Nonce)
	if err != nil {
		return Config{}, errors.New("待确认云盘配置标记无效")
	}
	ciphertext, err := base64.StdEncoding.DecodeString(marker.Ciphertext)
	if err != nil {
		return Config{}, errors.New("待确认云盘配置标记无效")
	}
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return Config{}, errors.New("无法解密待确认云盘配置")
	}
	aead, err := cipher.NewGCM(block)
	if err != nil || len(nonce) != aead.NonceSize() {
		return Config{}, errors.New("待确认云盘配置标记无效")
	}
	aad, err := pendingAAD(marker)
	if err != nil {
		return Config{}, err
	}
	plain, err := aead.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return Config{}, errors.New("无法解密待确认云盘配置")
	}
	var cfg Config
	if err := decodeStrictJSON(plain, &cfg); err != nil || cfg.Validate() != nil {
		return Config{}, errors.New("待确认云盘配置无效")
	}
	return cfg, nil
}

func pendingAAD(marker pendingFile) ([]byte, error) {
	stable := struct {
		Version          int      `json:"version"`
		FormatVersion    int      `json:"format_version"`
		CreatedAt        string   `json:"created_at"`
		UploadedAt       int64    `json:"uploaded_at"`
		AppVersion       string   `json:"app_version"`
		SHA256           string   `json:"sha256"`
		Size             int64    `json:"size"`
		DestinationNames []string `json:"destination_names"`
	}{marker.Version, marker.FormatVersion, marker.CreatedAt, marker.UploadedAt,
		marker.AppVersion, marker.SHA256, marker.Size, marker.DestinationNames}
	data, err := json.Marshal(stable)
	if err != nil {
		return nil, fmt.Errorf("编码候选认证数据失败: %w", err)
	}
	return data, nil
}

func statusFromMarker(marker pendingFile) PendingStatus {
	return PendingStatus{
		Pending: true, FormatVersion: marker.FormatVersion, CreatedAt: marker.CreatedAt,
		UploadedAt: marker.UploadedAt, AppVersion: marker.AppVersion, SHA256: marker.SHA256,
		Size: marker.Size, DestinationNames: append([]string(nil), marker.DestinationNames...),
	}
}

func writePrivateAtomic(path, pattern string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), pattern)
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	return nil
}
