package queue

// worker 落库 requests.delivery_mode 的端到端测试：
// 引用直发移除后生产侧只产 upload / text 两种标记
//（reference/mixed 仍是历史行的合法展示取值，store 常量与标签保留）。

import (
	"context"
	"testing"

	"github.com/gotd/td/tg"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/message"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// deliveryModeOf 读取任务终态行的投递方式标记。
func deliveryModeOf(t *testing.T, s *store.Store, requestID int64) string {
	t.Helper()
	r, err := s.GetRequest(context.Background(), requestID)
	if err != nil {
		t.Fatalf("读取请求失败: %v", err)
	}
	if r.Status != store.RequestSucceeded {
		t.Fatalf("任务应成功，得到 %s（错误码 %s）", r.Status, r.ErrorCode)
	}
	return r.DeliveryMode
}

func TestWorkerDeliveryModeSingleMedia(t *testing.T) {
	s := openStore(t)
	job, _ := newJobWithRequest(t, s, 0)
	sender := &fakeSender{}
	d := uploadDeps(t, s, nil, sender)

	runOneMedia(t, d, job, docMsg(7, 1101))

	if got := deliveryModeOf(t, s, job.RequestID); got != store.DeliveryModeUpload {
		t.Errorf("媒体送达应落 upload，得到 %q", got)
	}
}

func TestWorkerDeliveryModeText(t *testing.T) {
	s := openStore(t)
	job, _ := newJobWithRequest(t, s, 0)
	deps := Deps{
		Fetcher: &fakeFetcher{msgs: []*tg.Message{{ID: 7, Message: "hello"}}},
		Sender:  &fakeSender{},
		Store:   s,
		Log:     testLog(),
	}

	Process(deps)(context.Background(), job)

	if got := deliveryModeOf(t, s, job.RequestID); got != store.DeliveryModeText {
		t.Errorf("纯文本请求应落 text，得到 %q", got)
	}
}

// 早失败（取数失败，消息未转换）无可归并信息：终态回落列默认 upload。
func TestWorkerDeliveryModeEarlyFailureDefaultsToUpload(t *testing.T) {
	s := openStore(t)
	job, r := newJobWithRequest(t, s, 0)
	deps := Deps{
		Fetcher: &fakeFetcher{err: apperr.New(apperr.CodeChannelInaccessible, "no access")},
		Sender:  &fakeSender{},
		Store:   s,
		Log:     testLog(),
	}

	Process(deps)(context.Background(), job)

	got, err := s.GetRequest(context.Background(), r.ID)
	if err != nil {
		t.Fatalf("读取请求失败: %v", err)
	}
	if got.Status != store.RequestFailed || got.DeliveryMode != store.DeliveryModeUpload {
		t.Fatalf("早失败应落 failed 且 delivery_mode=upload: %+v", got)
	}
}

func TestWorkerDeliveryModeAlbum(t *testing.T) {
	s := openStore(t)
	job, _ := newJobWithRequest(t, s, 0)
	msgs := []*tg.Message{docMsg(7, 1201), docMsg(8, 1202)}
	msgs[0].SetGroupedID(42)
	msgs[1].SetGroupedID(42)
	sender := &fakeSender{groupable: func(m message.Media) bool { return true }}
	d := uploadDeps(t, s, fetcherWith(errInvoker{}, msgs...), sender)

	runJobSync(t, d, job)

	if got := deliveryModeOf(t, s, job.RequestID); got != store.DeliveryModeUpload {
		t.Errorf("整组上传成功应落 upload，得到 %q", got)
	}
}
