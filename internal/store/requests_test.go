package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"slices"
	"testing"
)

func TestRequestLifecycle(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	u := mustUser(t, s, 1)

	r, err := s.CreateRequest(ctx, Request{
		UserID:     u.ID,
		SourceKind: SourcePublic,
		ChannelKey: "example",
		MessageID:  123,
	})
	if err != nil {
		t.Fatalf("创建请求失败: %v", err)
	}
	if r.Status != RequestQueued {
		t.Errorf("新请求应为 queued，得到 %s", r.Status)
	}
	if r.Attempt != 1 {
		t.Errorf("初始 attempt 应为 1，得到 %d", r.Attempt)
	}
	if r.RequestedAt == 0 || r.QueuedAt == 0 {
		t.Error("时间戳应回填")
	}

	// 阶段更新：开始处理
	if err := s.MarkRequestStarted(ctx, r.ID, 2000); err != nil {
		t.Fatalf("标记开始失败: %v", err)
	}
	got, _ := s.GetRequest(ctx, r.ID)
	if got.Status != RequestProcessing || got.StartedAt != 2000 {
		t.Fatalf("开始处理后的状态/时间不对: %+v", got)
	}

	// 阶段更新：成功结束 + 元数据
	if err := s.FinishRequest(ctx, r.ID, RequestResult{
		Status:           RequestSucceeded,
		MediaType:        "album",
		MediaTypes:       []string{"photo", "video"},
		FileSize:         1024,
		FileName:         "clip.mp4",
		SourceMediaDCIDs: []int{2, 4},
		At:               3500,
	}); err != nil {
		t.Fatalf("落库成功终态失败: %v", err)
	}
	got, _ = s.GetRequest(ctx, r.ID)
	if got.Status != RequestSucceeded {
		t.Fatalf("终态应为 succeeded，得到 %s", got.Status)
	}
	if got.DurationMs != 1500 {
		t.Errorf("duration_ms 应自 started_at 起算 = 1500，得到 %d", got.DurationMs)
	}
	if got.MediaType != "album" || got.FileSize != 1024 || got.FileName != "clip.mp4" {
		t.Errorf("结果元数据应往返一致: %+v", got)
	}
	if !slices.Equal(got.MediaTypes, []string{"photo", "video"}) {
		t.Errorf("媒体成员类型应往返一致: %+v", got.MediaTypes)
	}
	if !slices.Equal(got.SourceMediaDCIDs, []int{2, 4}) {
		t.Errorf("媒体 DC 应往返一致: %+v", got.SourceMediaDCIDs)
	}
	if got.ErrorCode != "" {
		t.Errorf("成功请求不应有 error_code，得到 %q", got.ErrorCode)
	}
}

func TestRequestFailedAndRetry(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	u := mustUser(t, s, 1)
	r, _ := s.CreateRequest(ctx, Request{UserID: u.ID, ChannelKey: "example", MessageID: 1})
	_ = s.MarkRequestStarted(ctx, r.ID, 100)

	if err := s.FinishRequest(ctx, r.ID, RequestResult{
		Status:    RequestFailed,
		ErrorCode: "CHANNEL_NOT_ACCESSIBLE",
		At:        900,
	}); err != nil {
		t.Fatalf("落库失败终态失败: %v", err)
	}
	got, _ := s.GetRequest(ctx, r.ID)
	if got.Status != RequestFailed || got.ErrorCode != "CHANNEL_NOT_ACCESSIBLE" {
		t.Fatalf("失败终态应带错误码: %+v", got)
	}

	// 未及开始即失败：duration 回退到 queued_at 起算
	r2, _ := s.CreateRequest(ctx, Request{UserID: u.ID, ChannelKey: "example", MessageID: 2, QueuedAt: 500})
	if err := s.FinishRequest(ctx, r2.ID, RequestResult{Status: RequestFailed, ErrorCode: "INTERRUPTED", At: 800}); err != nil {
		t.Fatalf("落库终态失败: %v", err)
	}
	got2, _ := s.GetRequest(ctx, r2.ID)
	if got2.DurationMs != 300 {
		t.Errorf("无 started_at 时 duration 应回退 queued_at 起算 = 300，得到 %d", got2.DurationMs)
	}

	// 重试：状态回 queued、attempt+1、清空错误与阶段时间
	if err := s.RetryRequest(ctx, r.ID, 5000); err != nil {
		t.Fatalf("重试失败: %v", err)
	}
	got, _ = s.GetRequest(ctx, r.ID)
	if got.Status != RequestQueued || got.Attempt != 2 {
		t.Fatalf("重试后应 queued/attempt=2: %+v", got)
	}
	if got.ErrorCode != "" || got.StartedAt != 0 || got.FinishedAt != 0 || got.DurationMs != 0 {
		t.Errorf("重试应清空错误与阶段时间: %+v", got)
	}
	if got.QueuedAt != 5000 {
		t.Errorf("重试应刷新 queued_at = 5000，得到 %d", got.QueuedAt)
	}
}

func TestListRequestsFilterAndPaging(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	u1 := mustUser(t, s, 1)
	u2 := mustUser(t, s, 2)

	for i, tc := range []struct {
		user   int64
		key    string
		status string
		at     int64
	}{
		{1, "alpha", RequestSucceeded, 100},
		{1, "alpha", RequestFailed, 200},
		{1, "beta", RequestQueued, 300},
		{2, "alpha", RequestSucceeded, 400},
		{2, "beta", RequestProcessing, 500},
	} {
		r, err := s.CreateRequest(ctx, Request{
			UserID: tc.user, ChannelKey: tc.key, MessageID: i + 1, RequestedAt: tc.at,
		})
		if err != nil {
			t.Fatalf("创建请求失败: %v", err)
		}
		if tc.status != RequestQueued {
			if tc.status == RequestProcessing {
				_ = s.MarkRequestStarted(ctx, r.ID, tc.at+10)
			} else {
				_ = s.FinishRequest(ctx, r.ID, RequestResult{Status: tc.status, At: tc.at + 20})
			}
		}
	}

	// 按用户
	got, err := s.ListRequests(ctx, RequestFilter{UserID: u1.ID})
	if err != nil {
		t.Fatalf("按用户筛选失败: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("用户 1 应有 3 条，得到 %d", len(got))
	}
	// 时间倒序
	if got[0].RequestedAt < got[len(got)-1].RequestedAt {
		t.Error("列表应按请求时间倒序")
	}

	// 按状态
	got, _ = s.ListRequests(ctx, RequestFilter{Status: RequestSucceeded})
	if len(got) != 2 {
		t.Errorf("succeeded 应有 2 条，得到 %d", len(got))
	}

	// 组合：用户 + 频道
	got, _ = s.ListRequests(ctx, RequestFilter{UserID: u2.ID, ChannelKey: "alpha"})
	if len(got) != 1 || got[0].ChannelKey != "alpha" {
		t.Errorf("用户 2 + alpha 应恰 1 条，得到 %d", len(got))
	}

	// 时间窗
	got, _ = s.ListRequests(ctx, RequestFilter{Since: 200, Until: 450})
	if len(got) != 3 {
		t.Errorf("[200,450) 应命中 at=200/300/400 共 3 条，得到 %d", len(got))
	}

	// 分页
	got, _ = s.ListRequests(ctx, RequestFilter{Limit: 2, Offset: 0})
	if len(got) != 2 || got[0].RequestedAt != 500 {
		t.Errorf("第一页应 2 条且最新在前，得到 %d 条，首条 at=%d", len(got), got[0].RequestedAt)
	}
	got, _ = s.ListRequests(ctx, RequestFilter{Limit: 2, Offset: 2})
	if len(got) != 2 || got[0].RequestedAt != 300 {
		t.Errorf("第二页首条应为 at=300，得到 %d", got[0].RequestedAt)
	}
	got, _ = s.ListRequests(ctx, RequestFilter{Limit: 2, Offset: 4})
	if len(got) != 1 {
		t.Errorf("末页应剩 1 条，得到 %d", len(got))
	}
}

func TestCountUnfinishedByUser(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	u := mustUser(t, s, 1)

	if n, err := s.CountUnfinishedByUser(ctx, u.ID); err != nil || n != 0 {
		t.Fatalf("无请求时应为 0，得到 %d err=%v", n, err)
	}

	r1, _ := s.CreateRequest(ctx, Request{UserID: u.ID, ChannelKey: "a", MessageID: 1})
	r2, _ := s.CreateRequest(ctx, Request{UserID: u.ID, ChannelKey: "a", MessageID: 2})
	r3, _ := s.CreateRequest(ctx, Request{UserID: u.ID, ChannelKey: "a", MessageID: 3})
	_ = s.MarkRequestStarted(ctx, r1.ID, 1)                                  // processing
	_ = s.FinishRequest(ctx, r2.ID, RequestResult{Status: RequestSucceeded}) // succeeded
	_ = s.FinishRequest(ctx, r3.ID, RequestResult{Status: RequestFailed})    // failed

	n, err := s.CountUnfinishedByUser(ctx, u.ID)
	if err != nil {
		t.Fatalf("统计失败: %v", err)
	}
	if n != 1 {
		t.Errorf("queued+processing 应只有 r1 共 1 条，得到 %d", n)
	}
}

func TestListUnfinishedByUserAndLink(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	u := mustUser(t, s, 1)
	other := mustUser(t, s, 2)

	// 命中：自己的 queued + processing
	r1, _ := s.CreateRequest(ctx, Request{UserID: u.ID, ChannelKey: "example", MessageID: 7})
	r2, _ := s.CreateRequest(ctx, Request{UserID: u.ID, ChannelKey: "example", MessageID: 7})
	_ = s.MarkRequestStarted(ctx, r2.ID, 1)
	// 不命中：他人的同链接在途、自己的已结束、自己的不同链接
	rOther, _ := s.CreateRequest(ctx, Request{UserID: other.ID, ChannelKey: "example", MessageID: 7})
	rDone, _ := s.CreateRequest(ctx, Request{UserID: u.ID, ChannelKey: "example", MessageID: 7})
	_ = s.FinishRequest(ctx, rDone.ID, RequestResult{Status: RequestSucceeded})
	_, _ = s.CreateRequest(ctx, Request{UserID: u.ID, ChannelKey: "example", MessageID: 8})

	got, err := s.ListUnfinishedByUserAndLink(ctx, u.ID, "example", 7)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("应只命中自己的 queued+processing 共 2 条，得到 %d: %+v", len(got), got)
	}
	ids := map[int64]bool{got[0].ID: true, got[1].ID: true}
	if !ids[r1.ID] || !ids[r2.ID] {
		t.Errorf("命中结果不符: want {%d %d}, got %+v", r1.ID, r2.ID, got)
	}
	for _, r := range got {
		if r.Status != RequestQueued && r.Status != RequestProcessing {
			t.Errorf("命中请求应为在途状态，得到 %s", r.Status)
		}
	}

	if ids[rOther.ID] {
		t.Errorf("他人的在途请求不应命中: %d", rOther.ID)
	}

	// 无匹配：空列表而非错误
	empty, err := s.ListUnfinishedByUserAndLink(ctx, u.ID, "missing", 1)
	if err != nil || len(empty) != 0 {
		t.Fatalf("无匹配应为空列表，得到 %d err=%v", len(empty), err)
	}
}

func TestHasRecentSucceeded(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	u := mustUser(t, s, 1)

	r, _ := s.CreateRequest(ctx, Request{
		UserID: u.ID, ChannelKey: "example", MessageID: 42, RequestedAt: 10_000,
	})
	_ = s.FinishRequest(ctx, r.ID, RequestResult{Status: RequestSucceeded})

	cases := []struct {
		name  string
		user  int64
		key   string
		msgID int
		since int64
		want  bool
	}{
		{"窗口内同链接", u.ID, "example", 42, 5_000, true},
		{"窗口外", u.ID, "example", 42, 20_000, false},
		{"不同消息", u.ID, "example", 43, 5_000, false},
		{"不同频道", u.ID, "other", 42, 5_000, false},
		{"不同用户", 999, "example", 42, 5_000, false},
	}
	for _, tc := range cases {
		got, err := s.HasRecentSucceeded(ctx, tc.user, tc.key, tc.msgID, tc.since)
		if err != nil {
			t.Fatalf("%s: 查询失败: %v", tc.name, err)
		}
		if got != tc.want {
			t.Errorf("%s: 应 %v，得到 %v", tc.name, tc.want, got)
		}
	}

	// 失败记录不算成功
	r2, _ := s.CreateRequest(ctx, Request{UserID: u.ID, ChannelKey: "example", MessageID: 50, RequestedAt: 10_000})
	_ = s.FinishRequest(ctx, r2.ID, RequestResult{Status: RequestFailed})
	if ok, _ := s.HasRecentSucceeded(ctx, u.ID, "example", 50, 0); ok {
		t.Error("失败记录不应命中重复检测")
	}
}

// seedCloudRequest 建一条云盘请求并按参数落终态（finalStatus 空串保持 queued）。
func seedCloudRequest(t *testing.T, s *Store, userID int64, msgID int, dest, finalStatus string) Request {
	t.Helper()
	r, err := s.CreateRequest(context.Background(), Request{
		UserID: userID, ChannelKey: "example", MessageID: msgID,
		DeliveryMode: DeliveryModeCloud, CloudDestination: dest,
	})
	if err != nil {
		t.Fatalf("创建云盘请求失败: %v", err)
	}
	if finalStatus == "" {
		return r
	}
	_ = s.MarkRequestStarted(context.Background(), r.ID, 1)
	if err := s.FinishRequest(context.Background(), r.ID, RequestResult{
		Status: finalStatus, DeliveryMode: DeliveryModeCloud,
	}); err != nil {
		t.Fatalf("落库云盘终态失败: %v", err)
	}
	return r
}

func TestLatestSucceededCloudRequest(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	u := mustUser(t, s, 1)

	seedCloudRequest(t, s, u.ID, 7, "mega-1", RequestSucceeded) // 较早（同毫秒时按 id 靠后命中）
	latest := seedCloudRequest(t, s, u.ID, 7, "mega-1", RequestSucceeded)

	got, err := s.LatestSucceededCloudRequest(ctx, u.ID, "example", 7, "mega-1")
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if got.ID != latest.ID || got.DeliveryMode != DeliveryModeCloud || got.CloudDestination != "mega-1" {
		t.Fatalf("应命中最新成功云盘请求 %d，得到 %+v", latest.ID, got)
	}

	cases := []struct {
		name  string
		user  int64
		key   string
		msgID int
		dest  string
		setUp func()
	}{
		{
			name: "不同目的地不命中", user: u.ID, key: "example", msgID: 7, dest: "mega-2",
			setUp: func() { seedCloudRequest(t, s, u.ID, 7, "mega-1", RequestSucceeded) },
		},
		{
			name: "失败云盘请求不命中", user: u.ID, key: "example", msgID: 8, dest: "mega-1",
			setUp: func() { seedCloudRequest(t, s, u.ID, 8, "mega-1", RequestFailed) },
		},
		{
			name: "在途云盘请求不命中", user: u.ID, key: "example", msgID: 9, dest: "mega-1",
			setUp: func() { seedCloudRequest(t, s, u.ID, 9, "mega-1", "") },
		},
		{
			name: "普通 TG 成功不命中", user: u.ID, key: "example", msgID: 10, dest: "mega-1",
			setUp: func() {
				r, err := s.CreateRequest(ctx, Request{UserID: u.ID, ChannelKey: "example", MessageID: 10})
				if err != nil {
					t.Fatalf("创建请求失败: %v", err)
				}
				_ = s.MarkRequestStarted(ctx, r.ID, 1)
				if err := s.FinishRequest(ctx, r.ID, RequestResult{
					Status: RequestSucceeded, DeliveryMode: DeliveryModeReference,
				}); err != nil {
					t.Fatalf("落库终态失败: %v", err)
				}
			},
		},
	}
	for _, tc := range cases {
		tc.setUp()
		if _, err := s.LatestSucceededCloudRequest(ctx, tc.user, tc.key, tc.msgID, tc.dest); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: 应返回 ErrNotFound，得到 %v", tc.name, err)
		}
	}

	// 不同用户/频道/消息互不影响
	if _, err := s.LatestSucceededCloudRequest(ctx, 999, "example", 7, "mega-1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("其他用户不应命中: %v", err)
	}
	if _, err := s.LatestSucceededCloudRequest(ctx, u.ID, "other", 7, "mega-1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("其他频道不应命中: %v", err)
	}
	if _, err := s.LatestSucceededCloudRequest(ctx, u.ID, "example", 11, "mega-1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("其他消息不应命中: %v", err)
	}
}

func TestHasUnfinishedCloudRequest(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	u := mustUser(t, s, 1)

	// 命中：同链接同目的地的 queued 云盘请求
	seedCloudRequest(t, s, u.ID, 7, "mega-1", "")
	ok, err := s.HasUnfinishedCloudRequest(ctx, u.ID, "example", 7, "mega-1")
	if err != nil || !ok {
		t.Fatalf("queued 云盘请求应命中在途，得到 %v err=%v", ok, err)
	}

	// 命中：processing
	r := seedCloudRequest(t, s, u.ID, 8, "mega-1", "")
	_ = s.MarkRequestStarted(ctx, r.ID, 1)
	if ok, _ := s.HasUnfinishedCloudRequest(ctx, u.ID, "example", 8, "mega-1"); !ok {
		t.Error("processing 云盘请求应命中在途")
	}

	// 不命中：同链接不同目的地
	seedCloudRequest(t, s, u.ID, 9, "mega-2", "")
	if ok, _ := s.HasUnfinishedCloudRequest(ctx, u.ID, "example", 9, "mega-1"); ok {
		t.Error("不同目的地的在途请求不应命中")
	}

	// 不命中：已终态
	seedCloudRequest(t, s, u.ID, 10, "mega-1", RequestSucceeded)
	seedCloudRequest(t, s, u.ID, 11, "mega-1", RequestFailed)
	if ok, _ := s.HasUnfinishedCloudRequest(ctx, u.ID, "example", 10, "mega-1"); ok {
		t.Error("已成功请求不应命中在途")
	}
	if ok, _ := s.HasUnfinishedCloudRequest(ctx, u.ID, "example", 11, "mega-1"); ok {
		t.Error("已失败请求不应命中在途")
	}

	// 不命中：普通 TG 在途（无目的地）
	if _, err := s.CreateRequest(ctx, Request{UserID: u.ID, ChannelKey: "example", MessageID: 12}); err != nil {
		t.Fatalf("创建请求失败: %v", err)
	}
	if ok, _ := s.HasUnfinishedCloudRequest(ctx, u.ID, "example", 12, "mega-1"); ok {
		t.Error("普通 TG 在途请求不应命中")
	}

	// 不命中：不同用户
	if ok, _ := s.HasUnfinishedCloudRequest(ctx, 999, "example", 7, "mega-1"); ok {
		t.Error("其他用户不应命中")
	}
}

// TestRequestsDeliveryMode 迁移旧行默认值与终态落库往返。
func TestRequestsDeliveryMode(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	u := mustUser(t, s, 1)

	// 新建请求显式写列并占位历史默认值；空串终态回落默认 upload
	r, err := s.CreateRequest(ctx, Request{UserID: u.ID, ChannelKey: "example", MessageID: 1})
	if err != nil {
		t.Fatalf("创建请求失败: %v", err)
	}
	if got, _ := s.GetRequest(ctx, r.ID); got.DeliveryMode != DeliveryModeUpload {
		t.Errorf("新请求应占位默认 upload，得到 %q", got.DeliveryMode)
	}
	for _, tc := range []struct {
		mode string
		want string
	}{
		{DeliveryModeReference, DeliveryModeReference},
		{DeliveryModeMixed, DeliveryModeMixed},
		{DeliveryModeText, DeliveryModeText},
		{"", DeliveryModeUpload}, // 空串（无投递信息）回落列默认
	} {
		id := r.ID
		if err := s.FinishRequest(ctx, id, RequestResult{
			Status: RequestSucceeded, DeliveryMode: tc.mode,
		}); err != nil {
			t.Fatalf("落库终态失败: %v", err)
		}
		got, _ := s.GetRequest(ctx, id)
		if got.DeliveryMode != tc.want {
			t.Errorf("DeliveryMode %q 应落库为 %q，得到 %q", tc.mode, tc.want, got.DeliveryMode)
		}
		// 还原为 queued 以便下一轮覆盖
		if err := s.RetryRequest(ctx, id, 0); err != nil {
			t.Fatalf("重置请求失败: %v", err)
		}
	}
}

// TestMigrateOldRowsDeliveryModeDefault 模拟 v1 旧库（无 delivery_mode 列）
// 经 v2 迁移后历史行自动取默认 upload。
func TestMigrateOldRowsDeliveryModeDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "old.db")
	ctx := context.Background()

	// 直接以 v1 脚本建库并写入一条历史行（不含 delivery_mode 列），
	// 同时把 user_version 抬到 1，模拟旧版本程序留下的数据库
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("打开旧库失败: %v", err)
	}
	if _, err := db.Exec(migrations[0]); err != nil {
		t.Fatalf("执行 v1 迁移失败: %v", err)
	}
	if _, err := db.Exec("PRAGMA user_version = 1"); err != nil {
		t.Fatalf("写入 v1 版本号失败: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO requests
		(user_id, channel_key, message_id, status, attempt, requested_at, queued_at)
		VALUES (1, 'example', 7, 'succeeded', 1, 100, 100)`); err != nil {
		t.Fatalf("写入历史行失败: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("关闭旧库失败: %v", err)
	}

	s, err := Open(ctx, path, testLogger())
	if err != nil {
		t.Fatalf("迁移旧库失败: %v", err)
	}
	defer s.Close()

	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("读取 user_version 失败: %v", err)
	}
	if version != len(migrations) {
		t.Fatalf("迁移后 user_version 应为 %d，得到 %d", len(migrations), version)
	}
	r, err := s.GetRequest(ctx, 1)
	if err != nil {
		t.Fatalf("读取历史行失败: %v", err)
	}
	if r.DeliveryMode != DeliveryModeUpload {
		t.Errorf("旧行应默认 delivery_mode=upload，得到 %q", r.DeliveryMode)
	}
}

func TestGetRequestNotFound(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.GetRequest(context.Background(), 404); !errors.Is(err, ErrNotFound) {
		t.Fatalf("应返回 ErrNotFound，得到 %v", err)
	}
	if err := s.MarkRequestStarted(context.Background(), 404, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("更新不存在的请求应返回 ErrNotFound，得到 %v", err)
	}
}

func TestListRequestsMediaAndErrorFilter(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	u := mustUser(t, s, 1)

	for i, tc := range []struct {
		status  string
		media   string
		errCode string
	}{
		{RequestSucceeded, "video", ""},
		{RequestSucceeded, "text", ""},
		{RequestFailed, "video", "FILE_TOO_LARGE"},
		{RequestFailed, "", "CHANNEL_NOT_ACCESSIBLE"}, // 早失败：无媒体类型
	} {
		seedRequest(t, s, seedReq{
			user: u.ID, channel: "example", msgID: i + 1,
			status: tc.status, at: 100, media: tc.media, errCode: tc.errCode,
		})
	}

	got, err := s.ListRequests(ctx, RequestFilter{MediaType: "video"})
	if err != nil {
		t.Fatalf("按媒体类型筛选失败: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("video 应有 2 条，得到 %d", len(got))
	}

	got, _ = s.ListRequests(ctx, RequestFilter{ErrorCode: "FILE_TOO_LARGE"})
	if len(got) != 1 || got[0].ErrorCode != "FILE_TOO_LARGE" {
		t.Errorf("按错误码筛选应命中 1 条，得到 %d", len(got))
	}

	// 组合：状态 + 媒体类型
	got, _ = s.ListRequests(ctx, RequestFilter{Status: RequestFailed, MediaType: "video"})
	if len(got) != 1 || got[0].ErrorCode != "FILE_TOO_LARGE" {
		t.Errorf("失败 + video 应命中 1 条，得到 %d", len(got))
	}
}

func TestFailInterruptedRequests(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	u := mustUser(t, s, 1)

	queued, _ := s.CreateRequest(ctx, Request{UserID: u.ID, ChannelKey: "a", MessageID: 1, RequestedAt: 100, QueuedAt: 100})
	processing, _ := s.CreateRequest(ctx, Request{UserID: u.ID, ChannelKey: "a", MessageID: 2, RequestedAt: 100, QueuedAt: 150})
	_ = s.MarkRequestStarted(ctx, processing.ID, 200) // started_at = 200
	succeeded, _ := s.CreateRequest(ctx, Request{UserID: u.ID, ChannelKey: "a", MessageID: 3, RequestedAt: 100})
	_ = s.FinishRequest(ctx, succeeded.ID, RequestResult{Status: RequestSucceeded})
	failed, _ := s.CreateRequest(ctx, Request{UserID: u.ID, ChannelKey: "a", MessageID: 4, RequestedAt: 100})
	_ = s.FinishRequest(ctx, failed.ID, RequestResult{Status: RequestFailed, ErrorCode: "MESSAGE_NOT_FOUND"})

	n, err := s.FailInterruptedRequests(ctx, 1000)
	if err != nil {
		t.Fatalf("启动恢复失败: %v", err)
	}
	if n != 2 {
		t.Fatalf("应恢复 2 条（queued+processing），得到 %d", n)
	}

	got, _ := s.GetRequest(ctx, queued.ID)
	if got.Status != RequestFailed || got.ErrorCode != "INTERRUPTED" || got.FinishedAt != 1000 {
		t.Fatalf("遗留 queued 行应置为 failed(INTERRUPTED): %+v", got)
	}
	if got.DurationMs != 900 { // 无 started_at：回退 queued_at(100) 起算
		t.Errorf("queued 行 duration 应为 900，得到 %d", got.DurationMs)
	}
	got, _ = s.GetRequest(ctx, processing.ID)
	if got.Status != RequestFailed || got.ErrorCode != "INTERRUPTED" {
		t.Fatalf("遗留 processing 行应置为 failed(INTERRUPTED): %+v", got)
	}
	if got.DurationMs != 800 { // 自 started_at(200) 起算
		t.Errorf("processing 行 duration 应为 800，得到 %d", got.DurationMs)
	}

	// 已终态的行不受影响
	got, _ = s.GetRequest(ctx, succeeded.ID)
	if got.Status != RequestSucceeded {
		t.Errorf("succeeded 行不应被改动: %+v", got)
	}
	got, _ = s.GetRequest(ctx, failed.ID)
	if got.Status != RequestFailed || got.ErrorCode != "MESSAGE_NOT_FOUND" {
		t.Errorf("已失败行不应被覆盖错误码: %+v", got)
	}

	// 幂等：无遗留时返回 0 且不报错
	if n, err := s.FailInterruptedRequests(ctx, 2000); err != nil || n != 0 {
		t.Fatalf("无遗留时应返回 0，得到 n=%d err=%v", n, err)
	}

	// 空库同样安全
	s2 := openTestStore(t)
	if n, err := s2.FailInterruptedRequests(ctx, 0); err != nil || n != 0 {
		t.Fatalf("空库应返回 0，得到 n=%d err=%v", n, err)
	}
}

// seedSucceededTGRequest 创建一条 TG 请求并落终态（meta 还原查询测试用）。
func seedSucceededTGRequest(t *testing.T, s *Store, userID int64, msgID int, finalStatus string) Request {
	t.Helper()
	r, err := s.CreateRequest(context.Background(), Request{
		UserID: userID, ChannelKey: "example", MessageID: msgID,
	})
	if err != nil {
		t.Fatalf("创建请求失败: %v", err)
	}
	if finalStatus == "" {
		return r
	}
	_ = s.MarkRequestStarted(context.Background(), r.ID, 1)
	if err := s.FinishRequest(context.Background(), r.ID, RequestResult{
		Status: finalStatus, MediaType: "video",
	}); err != nil {
		t.Fatalf("落库终态失败: %v", err)
	}
	return r
}

// TestRequestLegacySentCoordinatesReadOnly v12 复用坐标列自转存频道方案起
// 停止写入：新终态保持零值，历史行（直写列模拟）仍可读。
func TestRequestLegacySentCoordinatesReadOnly(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	u := mustUser(t, s, 1)

	r := seedSucceededTGRequest(t, s, u.ID, 1, RequestSucceeded)
	got, err := s.GetRequest(ctx, r.ID)
	if err != nil {
		t.Fatalf("读取请求失败: %v", err)
	}
	if got.SentChatID != 0 || got.SentMessageIDs != nil {
		t.Errorf("新终态坐标应为零值，得到 chat=%d ids=%v", got.SentChatID, got.SentMessageIDs)
	}

	// 历史行（v12 时代写入）直写列后仍可读
	legacy := seedSucceededTGRequest(t, s, u.ID, 2, RequestSucceeded)
	if _, err := s.ex.ExecContext(ctx,
		"UPDATE requests SET sent_chat_id = ?, sent_message_ids_json = ? WHERE id = ?",
		int64(777), "[11,12]", legacy.ID); err != nil {
		t.Fatalf("模拟历史行失败: %v", err)
	}
	got, _ = s.GetRequest(ctx, legacy.ID)
	if got.SentChatID != 777 || len(got.SentMessageIDs) != 2 || got.SentMessageIDs[1] != 12 {
		t.Fatalf("历史行坐标应可读: chat=%d ids=%v", got.SentChatID, got.SentMessageIDs)
	}
}

// TestLatestSucceededTGRequest meta 还原查询：最新成功优先、failed/queued/
// 云盘排除、跨用户命中。
func TestLatestSucceededTGRequest(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	u1 := mustUser(t, s, 1)
	u2 := mustUser(t, s, 2)

	seedSucceededTGRequest(t, s, u1.ID, 7, RequestSucceeded)           // 较早
	latest := seedSucceededTGRequest(t, s, u2.ID, 7, RequestSucceeded) // 最新（跨用户）

	got, err := s.LatestSucceededTGRequest(ctx, "example", 7)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if got.ID != latest.ID || got.MediaType != "video" {
		t.Fatalf("应命中最新成功行 %d，得到 %+v", latest.ID, got)
	}

	// 排除：failed / queued / 云盘
	seedSucceededTGRequest(t, s, u1.ID, 8, RequestFailed)
	seedSucceededTGRequest(t, s, u1.ID, 9, "")
	seedCloudRequest(t, s, u1.ID, 10, "mega-1", RequestSucceeded)
	for _, msgID := range []int{8, 9, 10} {
		if _, err := s.LatestSucceededTGRequest(ctx, "example", msgID); !errors.Is(err, ErrNotFound) {
			t.Errorf("消息 %d 不应命中，得到 %v", msgID, err)
		}
	}
	if _, err := s.LatestSucceededTGRequest(ctx, "other", 7); !errors.Is(err, ErrNotFound) {
		t.Errorf("其他频道不应命中: %v", err)
	}
}

// TestDumpEntries 缓存频道条目：写入回读、最新优先、ErrNotFound。
func TestDumpEntries(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if _, err := s.InsertDumpEntry(ctx, DumpEntry{ChannelKey: "example", MessageID: 1}); err == nil {
		t.Error("空 DumpIDs 应返回防御错误")
	}

	older, err := s.InsertDumpEntry(ctx, DumpEntry{
		ChannelKey: "example", MessageID: 7, DumpIDs: []int{11}})
	if err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if older.ID == 0 || older.CreatedAt == 0 {
		t.Fatalf("应回填 ID 与时间: %+v", older)
	}
	latest, err := s.InsertDumpEntry(ctx, DumpEntry{
		ChannelKey: "example", MessageID: 7, DumpIDs: []int{21, 22, 23}})
	if err != nil {
		t.Fatalf("写入失败: %v", err)
	}

	got, err := s.LatestDumpEntry(ctx, "example", 7)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if got.ID != latest.ID || len(got.DumpIDs) != 3 || got.DumpIDs[2] != 23 {
		t.Fatalf("应命中最新条目并保持顺序: %+v", got)
	}
	if _, err := s.LatestDumpEntry(ctx, "example", 8); !errors.Is(err, ErrNotFound) {
		t.Errorf("无条目应 ErrNotFound，得到 %v", err)
	}
	if _, err := s.LatestDumpEntry(ctx, "other", 7); !errors.Is(err, ErrNotFound) {
		t.Errorf("其他频道不应命中: %v", err)
	}
}
