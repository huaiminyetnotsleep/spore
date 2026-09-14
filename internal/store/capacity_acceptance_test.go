package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
)

// TestCapacityAcceptance 验证容量目标下的请求统计、趋势聚合和 SQLite 快照。
// 数据库只创建在 t.TempDir()，不接触仓库或生产 data/ 目录。
func TestCapacityAcceptance(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "capacity.db"), testLogger())
	if err != nil {
		t.Fatalf("打开容量测试数据库失败: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Fatalf("关闭容量测试数据库失败: %v", err)
		}
	})

	const (
		userCount    = 100
		requestCount = 5000
		channelCount = 26
	)
	for i := 1; i <= userCount; i++ {
		mustUser(t, s, int64(i))
	}
	for i := 0; i < requestCount; i++ {
		status := RequestSucceeded
		media := "video"
		errCode := ""
		if i%2 == 1 {
			status = RequestFailed
			media = ""
			errCode = "CHANNEL_NOT_ACCESSIBLE"
		}
		seedRequest(t, s, seedReq{
			user:    int64(i%userCount + 1),
			channel: fmt.Sprintf("channel-%02d", i%channelCount),
			msgID:   i + 1,
			status:  status,
			at:      int64(i + 1),
			media:   media,
			errCode: errCode,
		})
	}

	if got, err := s.CountRequests(ctx, RequestFilter{}); err != nil {
		t.Fatalf("统计请求数失败: %v", err)
	} else if got != requestCount {
		t.Fatalf("请求数应为 %d，得到 %d", requestCount, got)
	}

	channels, err := s.ListChannelStats(ctx, StatsFilter{Limit: channelCount})
	if err != nil {
		t.Fatalf("频道聚合失败: %v", err)
	}
	if len(channels) != channelCount {
		t.Fatalf("频道数应为 %d，得到 %d", channelCount, len(channels))
	}

	trend, err := s.ListRequestTrend(ctx, StatsFilter{}, 8*60*60)
	if err != nil {
		t.Fatalf("请求趋势失败: %v", err)
	}
	if len(trend) == 0 {
		t.Fatal("请求趋势不应为空")
	}

	snapshot := filepath.Join(t.TempDir(), "snapshot.db")
	if err := s.BackupTo(ctx, snapshot); err != nil {
		t.Fatalf("容量测试快照失败: %v", err)
	}
	copy, err := Open(ctx, snapshot, testLogger())
	if err != nil {
		t.Fatalf("打开容量测试快照失败: %v", err)
	}
	defer copy.Close()
	if got, err := copy.CountRequests(ctx, RequestFilter{}); err != nil {
		t.Fatalf("读取容量测试快照失败: %v", err)
	} else if got != requestCount {
		t.Fatalf("快照请求数应为 %d，得到 %d", requestCount, got)
	}
}
