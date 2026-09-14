package access

import (
	"context"
	"testing"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/queue"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// newTerminalRequest 经完整提交路径构造一条终态请求（补存资格的种子）。
func newTerminalRequest(t *testing.T, s *Service, st *store.Store, msgID int, mode string) store.Request {
	t.Helper()
	d := mustSubmit(t, s, Submission{UserID: 1, ChatID: 1, Ref: pubRef(msgID)})
	r, err := st.GetRequest(context.Background(), d.RequestID)
	if err != nil {
		t.Fatalf("读取请求失败: %v", err)
	}
	if err := st.FinishRequest(context.Background(), r.ID, store.RequestResult{
		Status: store.RequestSucceeded, DeliveryMode: mode,
	}); err != nil {
		t.Fatalf("落库终态失败: %v", err)
	}
	r.Status = store.RequestSucceeded
	r.DeliveryMode = mode
	return r
}

// TestCloudBackfillSuccess：资格通过 → 新建 cloud 行（沿用原 user/ref、
// parent 指向原行、目的地落列）→ 特权入队（Job 携带 CloudDest 与新行 ID，
// 不占用户额度、不写占位消息）→ 审计留痕。
func TestCloudBackfillSuccess(t *testing.T) {
	svc, st, q := newTestService(t, 8, newClock(baseTime).Now)
	jobs := startWorkers(t, q)
	mustEnabledUser(t, st, 1)
	src := newTerminalRequest(t, svc, st, 7, store.DeliveryModeReference)
	waitJob(t, jobs) // 排空提交路径遗留的原任务，后续 waitJob 即补存任务

	// 前置：原请求已按提交路径扣过一次额度
	usage, _ := st.GetUsage(context.Background(), 1, baseTime.In(defaultLoc).Format(dayFormat))
	if usage.Used != 1 {
		t.Fatalf("前置：当日已用应为 1，得到 %d", usage.Used)
	}

	out, err := svc.CloudBackfill(context.Background(), "admin", src.ID, "mega-1")
	if err != nil {
		t.Fatalf("补存不应失败: %v", err)
	}
	if out.SkipReason != "" || out.QueueFull || out.CreatedRequestID == 0 {
		t.Fatalf("补存应建行并入队: %+v", out)
	}

	row, err := st.GetRequest(context.Background(), out.CreatedRequestID)
	if err != nil {
		t.Fatalf("读取新行失败: %v", err)
	}
	if row.Status != store.RequestQueued || row.DeliveryMode != store.DeliveryModeCloud ||
		row.ParentRequestID != src.ID || row.CloudDestination != "mega-1" {
		t.Errorf("新行字段不符: %+v", row)
	}
	if row.UserID != src.UserID || row.ChannelKey != src.ChannelKey || row.MessageID != src.MessageID {
		t.Errorf("新行应沿用原 user/ref: %+v", row)
	}

	job := waitJob(t, jobs)
	if job.RequestID != out.CreatedRequestID || job.CloudDest != "mega-1" ||
		job.UserID != src.UserID || job.ChatID != src.UserID || job.StatusMsgID != 0 {
		t.Errorf("补存任务字段不符: %+v", job)
	}
	if job.Ref.Username != src.ChannelKey || job.Ref.MessageID != src.MessageID {
		t.Errorf("补存任务应重建来源链接: %+v", job.Ref)
	}

	// 特权：不扣用户额度
	usage, _ = st.GetUsage(context.Background(), 1, baseTime.In(defaultLoc).Format(dayFormat))
	if usage.Used != 1 {
		t.Errorf("补存不应扣减额度，已用应为 1，得到 %d", usage.Used)
	}
	if !auditContains(t, st, "request.cloud_archive") {
		t.Error("补存应写审计")
	}
}

func auditContains(t *testing.T, st *store.Store, action string) bool {
	t.Helper()
	entries, err := st.ListAudit(context.Background(), 10, 0)
	if err != nil {
		t.Fatalf("读取审计失败: %v", err)
	}
	for _, e := range entries {
		if e.Action == action {
			return true
		}
	}
	return false
}

// TestCloudBackfillSkipMatrix：四类请求级跳过原因（云盘全局状态由 Web 层判定）。
func TestCloudBackfillSkipMatrix(t *testing.T) {
	clock := newClock(baseTime)
	svc, st, _ := newTestService(t, 8, clock.Now)
	mustEnabledUser(t, st, 1)
	// 同一用户的多次提交之间推进时钟，避开提交间隔限制
	submit := func(msgID int) Decision {
		t.Helper()
		clock.Advance(time.Minute)
		return mustSubmit(t, svc, Submission{UserID: 1, ChatID: 1, Ref: pubRef(msgID)})
	}

	// not_found
	out, err := svc.CloudBackfill(context.Background(), "admin", 404, "mega-1")
	if err != nil || out.SkipReason != CloudBackfillSkipNotFound {
		t.Fatalf("不存在请求应 skip not_found: %+v err=%v", out, err)
	}

	// not_finished：排队中的请求
	d := submit(11)
	out, err = svc.CloudBackfill(context.Background(), "admin", d.RequestID, "mega-1")
	if err != nil || out.SkipReason != CloudBackfillSkipNotFinished {
		t.Fatalf("未终态应 skip not_finished: %+v err=%v", out, err)
	}

	// text_only：终态纯文本请求
	clock.Advance(time.Minute)
	text := newTerminalRequest(t, svc, st, 12, store.DeliveryModeText)
	out, err = svc.CloudBackfill(context.Background(), "admin", text.ID, "mega-1")
	if err != nil || out.SkipReason != CloudBackfillSkipTextOnly {
		t.Fatalf("纯文本应 skip text_only: %+v err=%v", out, err)
	}

	// already_archiving：同 parent 已有在途补存行
	clock.Advance(time.Minute)
	src := newTerminalRequest(t, svc, st, 13, store.DeliveryModeUpload)
	if _, err := svc.CloudBackfill(context.Background(), "admin", src.ID, "mega-1"); err != nil {
		t.Fatalf("首次补存不应失败: %v", err)
	}
	out, err = svc.CloudBackfill(context.Background(), "admin", src.ID, "mega-1")
	if err != nil || out.SkipReason != CloudBackfillSkipAlreadyArchiving {
		t.Fatalf("在途补存应 skip already_archiving: %+v err=%v", out, err)
	}
	if out.CreatedRequestID != 0 {
		t.Errorf("跳过时不应建行: %+v", out)
	}
}

// TestCloudBackfillEnqueueFailureMarksQueueFull：提交后入队失败的竞态收尾，
// 新行标记 failed(QUEUE_FULL) 且保持 cloud 投递标记（可经现有重试入口重试）。
func TestCloudBackfillEnqueueFailureMarksQueueFull(t *testing.T) {
	svc, st, _ := newTestService(t, 8, newClock(baseTime).Now)
	mustEnabledUser(t, st, 1)
	src := newTerminalRequest(t, svc, st, 21, store.DeliveryModeReference)

	svc.queue = &halfFullQueue{delegate: queue.New(8), fail: true}

	out, err := svc.CloudBackfill(context.Background(), "admin", src.ID, "mega-1")
	if err != nil {
		t.Fatalf("队列满不是存储错误: %v", err)
	}
	if !out.QueueFull || out.CreatedRequestID == 0 {
		t.Fatalf("应建行并标记队列满: %+v", out)
	}
	row, err := st.GetRequest(context.Background(), out.CreatedRequestID)
	if err != nil {
		t.Fatalf("读取新行失败: %v", err)
	}
	if row.Status != store.RequestFailed || row.ErrorCode != "QUEUE_FULL" ||
		row.DeliveryMode != store.DeliveryModeCloud {
		t.Fatalf("竞态收尾应落 failed(QUEUE_FULL)/cloud: %+v", row)
	}
}
