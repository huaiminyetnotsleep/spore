package queue

// delivery_mode 观测与归并的纯逻辑测试：
//   - mergeDeliveryMode 表驱动覆盖全部归并分支（含空观测回落空串）；
//   - deliveryTrack 的送达计数。
// 引用直发移除后生产侧只产 upload/text；reference/mixed 仍是历史行的
// 合法展示取值（store 常量与标签保留），此处不再产生。

import (
	"testing"

	"github.com/huaiminyetnotsleep/spore/internal/store"
)

func TestMergeDeliveryMode(t *testing.T) {
	cases := []struct {
		name  string
		track deliveryTrack
		want  string
	}{
		{
			name:  "消息未及转换：返回空串由落库层回落默认",
			track: deliveryTrack{},
			want:  "",
		},
		{
			name:  "纯文本请求：text",
			track: deliveryTrack{converted: true},
			want:  store.DeliveryModeText,
		},
		{
			name:  "媒体请求送达：upload",
			track: deliveryTrack{converted: true, hasMedia: true, uploadOK: 3},
			want:  store.DeliveryModeUpload,
		},
		{
			name:  "媒体请求零送达（发送失败）：upload，错误码另行记录",
			track: deliveryTrack{converted: true, hasMedia: true},
			want:  store.DeliveryModeUpload,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mergeDeliveryMode(tc.track); got != tc.want {
				t.Errorf("mergeDeliveryMode(%+v) 应为 %q，得到 %q", tc.track, tc.want, got)
			}
		})
	}
}

func TestDeliveryTrackCounting(t *testing.T) {
	var tr deliveryTrack
	tr.delivered()
	tr.delivered()
	tr.delivered()
	if tr.uploadOK != 3 {
		t.Errorf("送达计数应为 3，得到 %d", tr.uploadOK)
	}
}
