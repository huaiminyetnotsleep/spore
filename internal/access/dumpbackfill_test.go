package access

import (
	"context"
	"testing"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/queue"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// TestDumpBackfillSuccess：资格通过 → 新建 dump 行（沿用原 user/ref、
// parent 指向原行）→ 特权入队（Job 携带 DumpOnly 与新行 ID，不占用户
// 额度、不写占位消息）→ 审计留痕。
func TestDumpBackfillSuccess(t *testing.T) {
	svc, st, q := newTestService(t, 8, newClock(baseTime).Now)
	jobs := startWorkers(t, q)
	mustEnabledUser(t, st, 1)
	src := newTerminalRequest(t, svc, st, 7, store.DeliveryModeReference)
	waitJob(t, jobs) // 排空提交路径遗留的原任务，后续 waitJob 即补写任务

	out, err := svc.DumpBackfill(context.Background(), "admin", src.ID)
	if err != nil {
		t.Fatalf("缓存补写不应失败: %v", err)
	}
	if out.SkipReason != "" || out.QueueFull || out.CreatedRequestID == 0 {
		t.Fatalf("补写应建行并入队: %+v", out)
	}

	row, err := st.GetRequest(context.Background(), out.CreatedRequestID)
	if err != nil {
		t.Fatalf("读取新行失败: %v", err)
	}
	if row.Status != store.RequestQueued || row.DeliveryMode != store.DeliveryModeDump ||
		row.ParentRequestID != src.ID || row.CloudDestination != "" {
		t.Errorf("新行字段不符: %+v", row)
	}
	if row.UserID != src.UserID || row.ChannelKey != src.ChannelKey || row.MessageID != src.MessageID {
		t.Errorf("新行应沿用原 user/ref: %+v", row)
	}

	job := waitJob(t, jobs)
	if job.RequestID != out.CreatedRequestID || !job.DumpOnly || job.CloudDest != "" ||
		job.UserID != src.UserID || job.ChatID != src.UserID || job.StatusMsgID != 0 {
		t.Errorf("补写任务字段不符: %+v", job)
	}
	if job.Ref.Username != src.ChannelKey || job.Ref.MessageID != src.MessageID {
		t.Errorf("补写任务应重建来源链接: %+v", job.Ref)
	}

	// 特权：不扣用户额度（前置：原请求已按提交路径扣过一次）
	usage, _ := st.GetUsage(context.Background(), 1, baseTime.In(defaultLoc).Format(dayFormat))
	if usage.Used != 1 {
		t.Errorf("补写不应扣减额度，已用应为 1，得到 %d", usage.Used)
	}
	if !auditContains(t, st, "request.dump_backfill") {
		t.Error("补写应写审计")
	}
}

// TestDumpBackfillSkipMatrix：三类请求级跳过原因（缓存频道全局配置由 Web
// 层判定）。纯文本终态请求允许补写（与云盘补存的 text_only 跳过不同）。
func TestDumpBackfillSkipMatrix(t *testing.T) {
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
	out, err := svc.DumpBackfill(context.Background(), "admin", 404)
	if err != nil || out.SkipReason != DumpBackfillSkipNotFound {
		t.Fatalf("不存在请求应 skip not_found: %+v err=%v", out, err)
	}

	// not_finished：排队中的请求
	d := submit(11)
	out, err = svc.DumpBackfill(context.Background(), "admin", d.RequestID)
	if err != nil || out.SkipReason != DumpBackfillSkipNotFinished {
		t.Fatalf("未终态应 skip not_finished: %+v err=%v", out, err)
	}

	// 纯文本终态请求可补写（资格通过，建行并入队）
	clock.Advance(time.Minute)
	text := newTerminalRequest(t, svc, st, 12, store.DeliveryModeText)
	out, err = svc.DumpBackfill(context.Background(), "admin", text.ID)
	if err != nil || out.SkipReason != "" || out.CreatedRequestID == 0 {
		t.Fatalf("纯文本终态应可补写: %+v err=%v", out, err)
	}
	// 补写行保持 queued 会占用用户并发额度，落终态避免影响后续提交
	if err := st.FinishRequest(context.Background(), out.CreatedRequestID, store.RequestResult{
		Status: store.RequestSucceeded, DeliveryMode: store.DeliveryModeDump,
	}); err != nil {
		t.Fatalf("落库补写行终态失败: %v", err)
	}

	// already_dumped：缓存频道已有同链接条目
	clock.Advance(time.Minute)
	src := newTerminalRequest(t, svc, st, 13, store.DeliveryModeUpload)
	if _, err := st.InsertDumpEntry(context.Background(), store.DumpEntry{
		ChannelKey: src.ChannelKey, MessageID: src.MessageID, DumpIDs: []int{501},
	}); err != nil {
		t.Fatalf("落缓存条目失败: %v", err)
	}
	out, err = svc.DumpBackfill(context.Background(), "admin", src.ID)
	if err != nil || out.SkipReason != DumpBackfillSkipAlreadyDumped {
		t.Fatalf("已有副本应 skip already_dumped: %+v err=%v", out, err)
	}
	if out.CreatedRequestID != 0 {
		t.Errorf("跳过时不应建行: %+v", out)
	}
}

// TestDumpBackfillEnqueueFailureMarksQueueFull：提交后入队失败的竞态收尾，
// 新行标记 failed(QUEUE_FULL) 且保持 dump 投递标记（可经现有重试入口重试）。
func TestDumpBackfillEnqueueFailureMarksQueueFull(t *testing.T) {
	svc, st, _ := newTestService(t, 8, newClock(baseTime).Now)
	mustEnabledUser(t, st, 1)
	src := newTerminalRequest(t, svc, st, 21, store.DeliveryModeReference)

	svc.queue = &halfFullQueue{delegate: queue.New(8), fail: true}

	out, err := svc.DumpBackfill(context.Background(), "admin", src.ID)
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
		row.DeliveryMode != store.DeliveryModeDump {
		t.Fatalf("竞态收尾应落 failed(QUEUE_FULL)/dump: %+v", row)
	}
}

// TestRetryDumpBackfillKeepsDumpOnly：dump 行（如队列满收尾的 failed 行）
// 经现有重试入口重试时必须保留 DumpOnly 路由——否则会误走普通投递路径
// 向用户重发消息。
func TestRetryDumpBackfillKeepsDumpOnly(t *testing.T) {
	svc, st, q := newTestService(t, 8, newClock(baseTime).Now)
	jobs := startWorkers(t, q)
	mustEnabledUser(t, st, 1)
	src := newTerminalRequest(t, svc, st, 31, store.DeliveryModeUpload)
	waitJob(t, jobs)

	// 构造一条 failed 的 dump 补写行（模拟队列满收尾结果）
	clock := newClock(baseTime)
	_ = clock
	out, err := svc.DumpBackfill(context.Background(), "admin", src.ID)
	if err != nil || out.CreatedRequestID == 0 {
		t.Fatalf("补写建行失败: %+v err=%v", out, err)
	}
	waitJob(t, jobs) // 排空补写任务
	if err := st.FinishRequest(context.Background(), out.CreatedRequestID, store.RequestResult{
		Status:       store.RequestFailed,
		ErrorCode:    "QUEUE_FULL",
		DeliveryMode: store.DeliveryModeDump,
	}); err != nil {
		t.Fatalf("落库 failed 终态失败: %v", err)
	}

	if err := svc.Retry(context.Background(), "admin", out.CreatedRequestID); err != nil {
		t.Fatalf("重试不应失败: %v", err)
	}
	job := waitJob(t, jobs)
	if !job.DumpOnly {
		t.Errorf("dump 行重试应保留 DumpOnly 路由: %+v", job)
	}
	if job.CloudDest != "" {
		t.Errorf("dump 行重试不应携带云盘目的地: %+v", job)
	}
}

// TestDumpBackfillSkipMatrixLiveEntry：副本失效自愈——条目存在但消息已被
// 删除（管理员在缓存频道客户端删除）时放行补写（试探复制判定）；校验通道
// 未注入时保守维持 already_dumped 跳过。
func TestDumpBackfillSkipMatrixLiveEntry(t *testing.T) {
	clock := newClock(baseTime)
	svc, st, _ := newTestService(t, 8, clock.Now)
	mustEnabledUser(t, st, 1)
	clock.Advance(time.Minute)
	src := newTerminalRequest(t, svc, st, 41, store.DeliveryModeUpload)
	if _, err := st.InsertDumpEntry(context.Background(), store.DumpEntry{
		ChannelKey: src.ChannelKey, MessageID: src.MessageID, DumpIDs: []int{501},
	}); err != nil {
		t.Fatalf("落缓存条目失败: %v", err)
	}

	t.Run("校验通道未注入时保守跳过", func(t *testing.T) {
		out, err := svc.DumpBackfill(context.Background(), "admin", src.ID)
		if err != nil || out.SkipReason != DumpBackfillSkipAlreadyDumped {
			t.Fatalf("应跳过 already_dumped: %+v err=%v", out, err)
		}
	})

	t.Run("副本失效放行补写", func(t *testing.T) {
		svc.SetDumpLive(func(context.Context, string, int) bool { return false })
		out, err := svc.DumpBackfill(context.Background(), "admin", src.ID)
		if err != nil || out.SkipReason != "" || out.CreatedRequestID == 0 {
			t.Fatalf("失效副本应放行建行: %+v err=%v", out, err)
		}
	})

	t.Run("副本仍有效维持跳过", func(t *testing.T) {
		svc.SetDumpLive(func(context.Context, string, int) bool { return true })
		out, err := svc.DumpBackfill(context.Background(), "admin", src.ID)
		if err != nil || out.SkipReason != DumpBackfillSkipAlreadyDumped {
			t.Fatalf("有效副本应跳过: %+v err=%v", out, err)
		}
	})

	t.Run("试探故障同样放行（判定失效，避免卡死补写）", func(t *testing.T) {
		svc.SetDumpLive(func(context.Context, string, int) bool { return false })
		out, err := svc.DumpBackfill(context.Background(), "admin", src.ID)
		if err != nil || out.SkipReason != "" || out.CreatedRequestID == 0 {
			t.Fatalf("试探故障判定失效应放行建行: %+v err=%v", out, err)
		}
	})
}

// TestDumpBackfillProbeOutsideTx：试探复制（Bot API 网络调用）必须发生在
// store 事务之外——store 是单连接（SQLite 单写者），事务持连接期间的网络
// IO 会拖住全站 DB 操作（线上事故：转存触发全站卡死直至重启）。
// 哨兵：dumpLive 假件内用独立 2s 超时连接查询——若在事务内被调用，
// 连接被事务占用，查询必然超时失败。
func TestDumpBackfillProbeOutsideTx(t *testing.T) {
	clock := newClock(baseTime)
	svc, st, _ := newTestService(t, 8, clock.Now)
	mustEnabledUser(t, st, 1)
	clock.Advance(time.Minute)
	src := newTerminalRequest(t, svc, st, 51, store.DeliveryModeUpload)
	if _, err := st.InsertDumpEntry(context.Background(), store.DumpEntry{
		ChannelKey: src.ChannelKey, MessageID: src.MessageID, DumpIDs: []int{501},
	}); err != nil {
		t.Fatalf("落缓存条目失败: %v", err)
	}

	inTx := false
	svc.SetDumpLive(func(ctx context.Context, _ string, _ int) bool {
		qctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if _, err := st.GetRequest(qctx, src.ID); err != nil {
			inTx = true // 连接被事务占用：试探在事务内执行了
		}
		return false // 判定副本失效，放行补写
	})

	out, err := svc.DumpBackfill(context.Background(), "admin", src.ID)
	if err != nil || out.SkipReason != "" || out.CreatedRequestID == 0 {
		t.Fatalf("失效副本应放行建行: %+v err=%v", out, err)
	}
	if inTx {
		t.Fatal("试探复制在 store 事务内被调用（会拖住全站 DB 操作）")
	}
}
