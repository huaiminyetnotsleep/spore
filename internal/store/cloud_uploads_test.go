package store

import (
	"context"
	"testing"
)

func TestCloudUploadLifecycle(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	u := mustUser(t, s, 1)
	r, err := s.CreateRequest(ctx, Request{
		UserID: u.ID, ChannelKey: "example", MessageID: 7,
		DeliveryMode: DeliveryModeCloud, // 云盘请求占位
	})
	if err != nil {
		t.Fatalf("创建云盘请求失败: %v", err)
	}
	if r.DeliveryMode != DeliveryModeCloud {
		t.Fatalf("delivery_mode 应保留 cloud 占位: %+v", r)
	}

	// 上传前落 uploading 行
	up1, err := s.InsertCloudUpload(ctx, CloudUpload{
		RequestID: r.ID, Destination: "mega-1",
		RemotePath: "spore/example/2026-09-09/7/01_a.jpg", FileName: "01_a.jpg",
	})
	if err != nil {
		t.Fatalf("创建上传记录失败: %v", err)
	}
	if up1.Status != CloudUploadUploading || up1.CreatedAt == 0 || up1.FinishedAt != 0 {
		t.Fatalf("初始行应为 uploading 且只带创建时间: %+v", up1)
	}
	up2, err := s.InsertCloudUpload(ctx, CloudUpload{
		RequestID: r.ID, Destination: "mega-1",
		RemotePath: "spore/example/2026-09-09/7/caption.txt", FileName: "caption.txt",
	})
	if err != nil {
		t.Fatalf("创建第二条上传记录失败: %v", err)
	}

	// 终态：一条成功、一条失败
	if err := s.FinishCloudUpload(ctx, up1.ID, CloudUploadSucceeded, "", 1024, 2000); err != nil {
		t.Fatalf("落成功终态失败: %v", err)
	}
	if err := s.FinishCloudUpload(ctx, up2.ID, CloudUploadFailed, "CLOUD_QUOTA", 0, 2100); err != nil {
		t.Fatalf("落失败终态失败: %v", err)
	}
	ups, err := s.CloudUploadsByRequest(ctx, r.ID)
	if err != nil {
		t.Fatalf("查询上传记录失败: %v", err)
	}
	if len(ups) != 2 {
		t.Fatalf("应有两条记录，得到 %d", len(ups))
	}
	if ups[0].ID != up1.ID || ups[0].Status != CloudUploadSucceeded ||
		ups[0].Bytes != 1024 || ups[0].FinishedAt != 2000 || ups[0].ErrorCode != "" {
		t.Fatalf("成功记录往返不符: %+v", ups[0])
	}
	if ups[1].ID != up2.ID || ups[1].Status != CloudUploadFailed ||
		ups[1].ErrorCode != "CLOUD_QUOTA" || ups[1].Bytes != 0 {
		t.Fatalf("失败记录往返不符: %+v", ups[1])
	}

	// 不存在的行为空；不存在的终态更新报 ErrNotFound
	if empty, err := s.CloudUploadsByRequest(ctx, 9999); err != nil || len(empty) != 0 {
		t.Fatalf("无记录请求应返回空切片: %+v %v", empty, err)
	}
	if err := s.FinishCloudUpload(ctx, 9999, CloudUploadFailed, "X", 0, 0); err != ErrNotFound {
		t.Fatalf("不存在的上传记录应返回 ErrNotFound，得到 %v", err)
	}
	if err := s.FinishCloudUpload(ctx, up1.ID, "weird", "", 0, 0); err == nil {
		t.Fatal("非法终态应被拒绝")
	}
}

// 补存请求行：parent_request_id 往返一致；普通请求保持 0。
func TestCreateRequestParent(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	u := mustUser(t, s, 1)

	orig, err := s.CreateRequest(ctx, Request{UserID: u.ID, ChannelKey: "example", MessageID: 7})
	if err != nil {
		t.Fatalf("创建原请求失败: %v", err)
	}
	if got, _ := s.GetRequest(ctx, orig.ID); got.ParentRequestID != 0 {
		t.Fatalf("普通请求 parent 应为 0: %+v", got)
	}

	retry, err := s.CreateRequest(ctx, Request{
		UserID: u.ID, ChannelKey: "example", MessageID: 7,
		DeliveryMode: DeliveryModeCloud, ParentRequestID: orig.ID,
	})
	if err != nil {
		t.Fatalf("创建补存请求失败: %v", err)
	}
	got, err := s.GetRequest(ctx, retry.ID)
	if err != nil {
		t.Fatalf("读取补存请求失败: %v", err)
	}
	if got.ParentRequestID != orig.ID || got.DeliveryMode != DeliveryModeCloud {
		t.Fatalf("补存请求应带父请求与 cloud 标记: %+v", got)
	}
}

// cloud_destination 往返：云盘请求创建时写入、读取与列表一致；普通请求为空串，
// 且重试（RetryRequest）不清空该列——重试入队据此恢复 Job.CloudDest。
func TestCreateRequestCloudDestination(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	u := mustUser(t, s, 1)

	r, err := s.CreateRequest(ctx, Request{
		UserID: u.ID, ChannelKey: "example", MessageID: 7,
		DeliveryMode: DeliveryModeCloud, CloudDestination: "mega-1",
	})
	if err != nil {
		t.Fatalf("创建云盘请求失败: %v", err)
	}
	got, err := s.GetRequest(ctx, r.ID)
	if err != nil {
		t.Fatalf("读取云盘请求失败: %v", err)
	}
	if got.CloudDestination != "mega-1" || got.DeliveryMode != DeliveryModeCloud {
		t.Fatalf("云盘请求应带目的地名称与 cloud 标记: %+v", got)
	}
	rows, err := s.ListRequests(ctx, RequestFilter{UserID: u.ID})
	if err != nil || len(rows) != 1 {
		t.Fatalf("列表应返回该云盘请求: %v %d", err, len(rows))
	}
	if rows[0].CloudDestination != "mega-1" {
		t.Fatalf("列表行目的地应往返一致: %+v", rows[0])
	}

	plain, err := s.CreateRequest(ctx, Request{UserID: u.ID, ChannelKey: "example", MessageID: 8})
	if err != nil {
		t.Fatalf("创建普通请求失败: %v", err)
	}
	if got, _ := s.GetRequest(ctx, plain.ID); got.CloudDestination != "" {
		t.Fatalf("普通请求目的地应为空串: %+v", got)
	}

	// 失败后重试：cloud_destination 保留（RetryRequest 不触碰该列）
	_ = s.FinishRequest(ctx, r.ID, RequestResult{Status: RequestFailed, ErrorCode: "CLOUD_QUOTA"})
	if err := s.RetryRequest(ctx, r.ID, 5000); err != nil {
		t.Fatalf("重试失败: %v", err)
	}
	got, _ = s.GetRequest(ctx, r.ID)
	if got.Status != RequestQueued || got.CloudDestination != "mega-1" {
		t.Fatalf("重试后应保持 queued 且目的地保留: %+v", got)
	}
}

// delivery_mode 列表筛选：请求列表（与 CSV 导出同源筛选）可按投递方式过滤，
// cloud 行用于管理台「网盘」筛选（零值字段不参与过滤）。
func TestListRequestsFilterDeliveryMode(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	u := mustUser(t, s, 1)

	mustCreate := func(id int, mode string) {
		t.Helper()
		if _, err := s.CreateRequest(ctx, Request{
			UserID: u.ID, ChannelKey: "example", MessageID: id, DeliveryMode: mode,
		}); err != nil {
			t.Fatalf("创建请求失败: %v", err)
		}
	}
	mustCreate(1, DeliveryModeCloud)
	mustCreate(2, DeliveryModeUpload)
	mustCreate(3, DeliveryModeCloud)

	cloud, err := s.ListRequests(ctx, RequestFilter{DeliveryMode: DeliveryModeCloud})
	if err != nil || len(cloud) != 2 {
		t.Fatalf("按 cloud 筛选应命中 2 条: %v %d", err, len(cloud))
	}
	for _, r := range cloud {
		if r.DeliveryMode != DeliveryModeCloud {
			t.Fatalf("筛选结果应全为 cloud: %+v", r)
		}
	}
	upload, err := s.ListRequests(ctx, RequestFilter{DeliveryMode: DeliveryModeUpload})
	if err != nil || len(upload) != 1 {
		t.Fatalf("按 upload 筛选应命中 1 条: %v %d", err, len(upload))
	}
	all, err := s.CountRequests(ctx, RequestFilter{})
	if err != nil || all != 3 {
		t.Fatalf("零值筛选应命中全部 3 条: %v %d", err, all)
	}
}
