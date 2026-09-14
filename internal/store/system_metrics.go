package store

import (
	"context"
	"database/sql"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
)

// SystemMetricSample 是系统资源与传输速率的历史采样。
// 除 SampledAt 外的指针字段为 nil 时表示对应探针不可用。
type SystemMetricSample struct {
	SampledAt              int64
	RSSBytes               *int64
	TempDirBytes           *int64
	DownloadBytesPerSecond *float64
	UploadBytesPerSecond   *float64
	CPUPercent             *float64 // 进程 CPU 占用率（0–100，占全部核心）
}

// UpsertSystemMetricSample 按 sampled_at 插入或覆盖一个聚合采样点。
func (s *Store) UpsertSystemMetricSample(ctx context.Context, sample SystemMetricSample) error {
	_, err := s.ex.ExecContext(ctx, `INSERT INTO system_metric_samples (
			sampled_at, rss_bytes, temp_dir_bytes, download_bytes_per_second, upload_bytes_per_second, cpu_percent
		) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(sampled_at) DO UPDATE SET
			rss_bytes = excluded.rss_bytes,
			temp_dir_bytes = excluded.temp_dir_bytes,
			download_bytes_per_second = excluded.download_bytes_per_second,
			upload_bytes_per_second = excluded.upload_bytes_per_second,
			cpu_percent = excluded.cpu_percent`,
		sample.SampledAt, nullableInt64(sample.RSSBytes), nullableInt64(sample.TempDirBytes),
		nullableFloat64(sample.DownloadBytesPerSecond), nullableFloat64(sample.UploadBytesPerSecond),
		nullableFloat64(sample.CPUPercent))
	return wrapDB("写系统指标采样", err)
}

// ListSystemMetricSamples 返回 [since, until) 范围内的原始采样，按时间升序。
func (s *Store) ListSystemMetricSamples(ctx context.Context, since, until int64) ([]SystemMetricSample, error) {
	rows, err := s.ex.QueryContext(ctx, `SELECT sampled_at, rss_bytes, temp_dir_bytes,
			download_bytes_per_second, upload_bytes_per_second, cpu_percent
		FROM system_metric_samples
		WHERE sampled_at >= ? AND sampled_at < ?
		ORDER BY sampled_at`, since, until)
	if err != nil {
		return nil, wrapDB("查询系统指标采样", err)
	}
	defer rows.Close()
	return scanSystemMetricSamples(rows)
}

// ListSystemMetricBuckets 返回 [since, until) 内按 bucketMillis 对齐的平均值，
// 桶起点按 Unix epoch 对齐并升序返回；每个指标的 NULL 值不参与其平均值。
func (s *Store) ListSystemMetricBuckets(ctx context.Context, since, until, bucketMillis int64) ([]SystemMetricSample, error) {
	if bucketMillis <= 0 {
		return nil, apperr.New(apperr.CodeInternal, "系统指标分桶宽度必须大于零")
	}
	rows, err := s.ex.QueryContext(ctx, `SELECT (sampled_at / ?) * ? AS bucket_at,
			CAST(AVG(rss_bytes) AS INTEGER), CAST(AVG(temp_dir_bytes) AS INTEGER),
			AVG(download_bytes_per_second), AVG(upload_bytes_per_second), AVG(cpu_percent)
		FROM system_metric_samples
		WHERE sampled_at >= ? AND sampled_at < ?
		GROUP BY bucket_at
		ORDER BY bucket_at`, bucketMillis, bucketMillis, since, until)
	if err != nil {
		return nil, wrapDB("聚合系统指标采样", err)
	}
	defer rows.Close()
	return scanSystemMetricSamples(rows)
}

// DeleteSystemMetricSamplesBefore 删除 sampled_at < before 的历史采样，
// 返回实际删除行数；没有命中不是错误。
func (s *Store) DeleteSystemMetricSamplesBefore(ctx context.Context, before int64) (int64, error) {
	res, err := s.ex.ExecContext(ctx, "DELETE FROM system_metric_samples WHERE sampled_at < ?", before)
	if err != nil {
		return 0, wrapDB("清理系统指标采样", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, wrapDB("统计清理系统指标采样", err)
	}
	return n, nil
}

func scanSystemMetricSamples(rows *sql.Rows) ([]SystemMetricSample, error) {
	var out []SystemMetricSample
	for rows.Next() {
		var sample SystemMetricSample
		var rss, temp sql.NullInt64
		var download, upload, cpu sql.NullFloat64
		if err := rows.Scan(&sample.SampledAt, &rss, &temp, &download, &upload, &cpu); err != nil {
			return nil, wrapDB("扫描系统指标采样行", err)
		}
		sample.RSSBytes = int64Pointer(rss)
		sample.TempDirBytes = int64Pointer(temp)
		sample.DownloadBytesPerSecond = float64Pointer(download)
		sample.UploadBytesPerSecond = float64Pointer(upload)
		sample.CPUPercent = float64Pointer(cpu)
		out = append(out, sample)
	}
	return out, wrapDB("遍历系统指标采样行", rows.Err())
}

func nullableInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableFloat64(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}

func int64Pointer(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	v := value.Int64
	return &v
}

func float64Pointer(value sql.NullFloat64) *float64 {
	if !value.Valid {
		return nil
	}
	v := value.Float64
	return &v
}
