package queue

// 投递方式标记（requests.delivery_mode）的观测与归并。
// worker 在任务过程中经 deliveryTrack 记录成功投递媒体的计数，任务结束时
// 由 mergeDeliveryMode 归并为单一标记落库；中文标签映射在 internal/web 与
// 前端 format.ts。历史行可能含 reference/mixed（媒体引用直发时代的旧数据），
// 展示侧保留标签，生产侧不再产生。

import "github.com/huaiminyetnotsleep/spore/internal/store"

// deliveryTrack 累计任务内投递观测：是否完成转换、是否含媒体条目与
// 成功送达计数。以指针在单次任务的各发送函数间传递（单 worker 内串行使用）。
type deliveryTrack struct {
	converted bool // 消息已完成转换（区分转换前失败与纯文本请求）
	hasMedia  bool // 条目中含媒体（区分纯文本与未送达的媒体请求）
	uploadOK  int  // 成功送达的媒体计数（相册整组记 1）
}

// delivered 记录一次成功送达。
func (t *deliveryTrack) delivered() {
	t.uploadOK++
}

// mergeDeliveryMode 把观测归并为 requests.delivery_mode 的最终取值。
// 返回空串表示无可归并的信息（消息未及转换即失败），由 FinishRequest 回落
// 列默认 upload：
//
//   - 未转换（取数失败等）→ ""；
//   - 纯文本请求（无媒体条目）→ text；
//   - 媒体请求（无论送达与否）→ upload（统一"下载+上传"路径，
//     发送失败时错误码另行记录失败原因）。
func mergeDeliveryMode(t deliveryTrack) string {
	if !t.converted {
		return ""
	}
	if !t.hasMedia {
		return store.DeliveryModeText
	}
	return store.DeliveryModeUpload
}

// deliveryMode 是 mediaMeta 的投递方式归并入口，供终态落库取值。
// 复用路径（copyMessages 直拷，见 reuse.go）不经 Track 观测，以 Reused
// 标记直接归并为 reuse。
func (m mediaMeta) deliveryMode() string {
	if m.Reused {
		return store.DeliveryModeReuse
	}
	return mergeDeliveryMode(m.Track)
}
