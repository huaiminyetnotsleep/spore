package r2backup

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"sort"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/branding"
)

// keyPrefix / keySuffix 定义远端对象的命名契约：时间戳字典序即时间序，
// 轮转只依赖名字排序，无需任何元数据（与本地 backups 目录同款思路）。
const (
	keyPrefix = branding.StoragePrefix + "-full-backup-"
	keySuffix = ".zip"
)

// ErrNotConfigured 是配置不完整时的受控错误（测试连接/上传前置校验）。
var ErrNotConfigured = errors.New("r2backup: R2 配置不完整")

// ObjectStore 是远端对象存储的最小面：Put 上传本地文件、List 列出桶内
// 指定前缀的全部对象名、Delete 删除对象。接口先行便于伪造测试与未来
// 接入其他 S3 兼容 provider。
type ObjectStore interface {
	Put(ctx context.Context, key, path, contentType string, size int64) error
	List(ctx context.Context, prefix string) ([]string, error)
	Delete(ctx context.Context, key string) error
}

// NewStore 按配置构造 minio 客户端封装。region 恒为 auto（R2 语义），
// 端点由 Account ID 拼出，TLS 强制开启。
func NewStore(cfg Config) (ObjectStore, error) {
	if !cfg.Complete() {
		return nil, apperr.New(apperr.CodeInternal, "r2backup: 配置不完整，无法构造客户端")
	}
	cli, err := minio.New(Endpoint(cfg.AccountID), &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		Secure: true,
		Region: "auto",
	})
	if err != nil {
		return nil, apperr.Wrap(apperr.CodeInternal, err)
	}
	return &minioStore{cli: cli, bucket: cfg.Bucket}, nil
}

type minioStore struct {
	cli    *minio.Client
	bucket string
}

func (m *minioStore) Put(ctx context.Context, key, path, contentType string, _ int64) error {
	_, err := m.cli.FPutObject(ctx, m.bucket, key, path, minio.PutObjectOptions{
		ContentType: contentType,
		PartSize:    64 << 20, // 64MB 分片：几十 MB 的备份 ZIP 单请求完成
	})
	return err
}

func (m *minioStore) List(ctx context.Context, prefix string) ([]string, error) {
	var names []string
	for object := range m.cli.ListObjects(ctx, m.bucket, minio.ListObjectsOptions{
		Prefix:    prefix,
		Recursive: true,
	}) {
		if object.Err != nil {
			return names, object.Err
		}
		names = append(names, object.Key)
	}
	return names, nil
}

func (m *minioStore) Delete(ctx context.Context, key string) error {
	return m.cli.RemoveObject(ctx, m.bucket, key, minio.RemoveObjectOptions{})
}

// Result 描述一次成功的上传与轮转。
type Result struct {
	Key       string // 远端对象名
	SizeBytes int64
	Removed   int // 轮转删除的旧对象数
}

// BackupKey 生成 now 时刻的远端对象名（与本地全量导出 ZIP 同构）。
func BackupKey(now time.Time) string {
	return keyPrefix + now.Format("20060102-150405") + keySuffix
}

// UploadAndRotate 上传 zipPath 到远端并按 keep 份轮转：上传成功后列出
// 前缀下全部对象，按名字升序（= 时间从旧到新）保留最新 keep 份、删除
// 更旧者。轮转失败时上传本身已成功：返回已得结果与错误，调用方按告警
// 处理，下一轮上传后会再次收敛。
func UploadAndRotate(ctx context.Context, store ObjectStore, zipPath string, keep int, now time.Time) (Result, error) {
	if keep < 1 {
		keep = 1
	}
	var size int64
	if fi, err := os.Stat(zipPath); err == nil {
		size = fi.Size()
	}
	key := BackupKey(now)
	if err := store.Put(ctx, key, zipPath, "application/zip", size); err != nil {
		return Result{}, err
	}
	res := Result{Key: key, SizeBytes: size}

	names, err := store.List(ctx, keyPrefix)
	if err != nil {
		return res, err
	}
	sort.Strings(names)
	if excess := len(names) - keep; excess > 0 {
		for _, old := range names[:excess] {
			if err := store.Delete(ctx, old); err != nil {
				return res, err
			}
			res.Removed++
		}
	}
	return res, nil
}

// TestConnection 用已保存配置做一次最小代价连通性验证（ListObjects，
// 只读不写）：成功说明四项配置与网络链路全部有效。
func TestConnection(ctx context.Context, cfg Config) error {
	if !cfg.Complete() {
		return ErrNotConfigured
	}
	store, err := NewStore(cfg)
	if err != nil {
		return err
	}
	_, err = store.List(ctx, keyPrefix)
	return err
}

// ClassifyError 把底层错误映射为受控中文场景（进入事件与配置文件的
// last_upload_error；原始错误只进服务日志，端点/签名细节不下发管理端）。
func ClassifyError(err error) string {
	var resp minio.ErrorResponse
	if errors.As(err, &resp) {
		switch resp.Code {
		case "InvalidAccessKeyId", "SignatureDoesNotMatch", "AccessDenied", "Unauthorized":
			return "R2 密钥无效或无权限（请核对 Access Key ID / Secret，或重新生成 API Token）"
		case "NoSuchBucket":
			return "R2 存储桶不存在（请核对 Bucket 名称）"
		case "EntityTooLarge":
			return "R2 单对象超限（备份包过大）"
		case "SlowDownWrite", "SlowDown":
			return "R2 限流，本次上传未完成"
		}
		return "R2 上传失败（" + resp.Code + "）"
	}
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) ||
		errors.As(err, &netErr) || errors.Is(err, io.ErrUnexpectedEOF) {
		return "R2 上传超时或网络中断"
	}
	if errors.Is(err, ErrNotConfigured) {
		return "R2 配置不完整"
	}
	return "R2 上传失败"
}
