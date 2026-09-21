package access

import (
	"context"
	"testing"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// pinFlagOf 读取指定请求行的置顶标记。
func pinFlagOf(t *testing.T, st *store.Store, requestID int64) store.Request {
	t.Helper()
	r, err := st.GetRequest(context.Background(), requestID)
	if err != nil {
		t.Fatalf("读取请求 %d 失败: %v", requestID, err)
	}
	return r
}

// TestSubmitPinMarksRequestRow：/pin <链接>（Submission.Pin）落库 requests.pin。
func TestSubmitPinMarksRequestRow(t *testing.T) {
	clock := newClock(baseTime)
	svc, st, _ := newTestService(t, 4, clock.Now)
	ctx := context.Background()
	if _, err := st.CreateUser(ctx, store.User{ID: 1, Status: store.UserEnabled}); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}

	d := mustSubmit(t, svc, Submission{UserID: 1, ChatID: 1, Ref: pubRef(42), Pin: true})

	r := pinFlagOf(t, st, d.RequestID)
	if !r.Pin || r.PinOK != 0 || r.PinTotal != 0 {
		t.Fatalf("/pin 提交应标记置顶且结果为零值: %+v", r)
	}
}

// TestSubmitAutoPinPreferenceOnlyForPlainDelivery：用户 auto_pin 偏好仅对
// 普通投递生效——普通提交默认置顶，云盘提交（CloudDest 非空）不置顶；
// 未开启偏好的普通提交保持不置顶。
func TestSubmitAutoPinPreferenceOnlyForPlainDelivery(t *testing.T) {
	clock := newClock(baseTime)
	svc, st, _ := newTestService(t, 8, clock.Now)
	ctx := context.Background()
	if _, err := st.CreateUser(ctx, store.User{ID: 1, Status: store.UserEnabled}); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	if err := st.UpdateUserAutoPin(ctx, 1, true); err != nil {
		t.Fatalf("开启 auto_pin 失败: %v", err)
	}
	// 云盘提交需用户级下载权限（显式允许）
	if err := st.UpdateUserCloudDownload(ctx, 1, store.CloudDownloadAllow); err != nil {
		t.Fatalf("设置云盘下载权限失败: %v", err)
	}

	// 普通提交：偏好生效
	d := mustSubmit(t, svc, Submission{UserID: 1, ChatID: 1, Ref: pubRef(41)})
	if r := pinFlagOf(t, st, d.RequestID); !r.Pin {
		t.Fatalf("开启偏好后普通提交应默认置顶: %+v", r)
	}

	// 云盘提交：偏好不生效（推进时钟越过提交间隔）
	clock.Advance(60 * time.Second)
	dc := mustSubmit(t, svc, Submission{UserID: 1, ChatID: 1, Ref: pubRef(43), CloudDest: "mega-1"})
	if r := pinFlagOf(t, st, dc.RequestID); r.Pin {
		t.Fatalf("云盘提交不应被偏好置顶: %+v", r)
	}

	// 未开启偏好的用户：普通提交不置顶
	clock.Advance(60 * time.Second)
	if _, err := st.CreateUser(ctx, store.User{ID: 2, Status: store.UserEnabled}); err != nil {
		t.Fatalf("创建用户 2 失败: %v", err)
	}
	d2 := mustSubmit(t, svc, Submission{UserID: 2, ChatID: 2, Ref: pubRef(44)})
	if r := pinFlagOf(t, st, d2.RequestID); r.Pin {
		t.Fatalf("未开启偏好的普通提交不应置顶: %+v", r)
	}
}
