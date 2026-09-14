package store

import (
	"context"
	"testing"
)

func TestSystemMetricSamplesCRUDAndBuckets(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	i64 := func(v int64) *int64 { return &v }
	f64 := func(v float64) *float64 { return &v }

	samples := []SystemMetricSample{
		{SampledAt: 60_000, RSSBytes: i64(100), DownloadBytesPerSecond: f64(10)},
		{SampledAt: 90_000, RSSBytes: i64(300), TempDirBytes: i64(50), DownloadBytesPerSecond: f64(0), UploadBytesPerSecond: f64(20)},
		{SampledAt: 180_000, RSSBytes: i64(500), TempDirBytes: i64(70), DownloadBytesPerSecond: f64(30), UploadBytesPerSecond: f64(0)},
	}
	for _, sample := range samples {
		if err := s.UpsertSystemMetricSample(ctx, sample); err != nil {
			t.Fatalf("写采样失败: %v", err)
		}
	}
	// 同时间点应覆盖且保留 NULL。
	if err := s.UpsertSystemMetricSample(ctx, SystemMetricSample{SampledAt: 180_000, RSSBytes: i64(600)}); err != nil {
		t.Fatalf("覆盖采样失败: %v", err)
	}

	listed, err := s.ListSystemMetricSamples(ctx, 60_000, 180_000)
	if err != nil {
		t.Fatalf("查询采样失败: %v", err)
	}
	if len(listed) != 2 || listed[0].SampledAt != 60_000 || listed[1].SampledAt != 90_000 {
		t.Fatalf("范围或顺序不符: %+v", listed)
	}
	if listed[0].TempDirBytes != nil || listed[0].UploadBytesPerSecond != nil {
		t.Fatalf("NULL 字段应保持 nil: %+v", listed[0])
	}

	buckets, err := s.ListSystemMetricBuckets(ctx, 0, 240_000, 120_000)
	if err != nil {
		t.Fatalf("分桶失败: %v", err)
	}
	if len(buckets) != 2 || buckets[0].SampledAt != 0 || buckets[1].SampledAt != 120_000 {
		t.Fatalf("分桶顺序不符: %+v", buckets)
	}
	if buckets[0].RSSBytes == nil || *buckets[0].RSSBytes != 200 {
		t.Fatalf("RSS 平均值不符: %+v", buckets[0])
	}
	if buckets[0].DownloadBytesPerSecond == nil || *buckets[0].DownloadBytesPerSecond != 5 {
		t.Fatalf("下载平均值不符: %+v", buckets[0])
	}
	if buckets[0].TempDirBytes == nil || *buckets[0].TempDirBytes != 50 {
		t.Fatalf("NULL 不应参与平均值: %+v", buckets[0])
	}
	if buckets[1].TempDirBytes != nil || buckets[1].DownloadBytesPerSecond != nil {
		t.Fatalf("覆盖后的 NULL 应保持 nil: %+v", buckets[1])
	}

	deleted, err := s.DeleteSystemMetricSamplesBefore(ctx, 180_000)
	if err != nil || deleted != 2 {
		t.Fatalf("清理结果不符: deleted=%d err=%v", deleted, err)
	}
	remaining, err := s.ListSystemMetricSamples(ctx, 0, 240_000)
	if err != nil || len(remaining) != 1 || remaining[0].SampledAt != 180_000 {
		t.Fatalf("清理后数据不符: samples=%+v err=%v", remaining, err)
	}
}

func TestSystemMetricBucketsRejectInvalidWidth(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.ListSystemMetricBuckets(context.Background(), 0, 1, 0); err == nil {
		t.Fatal("零分桶宽度应返回错误")
	}
}
