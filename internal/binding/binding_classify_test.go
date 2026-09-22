package binding

// ClassifyVerifyError 细分断言：绑定/监听校验（GetChat/GetChatMember）的
// 网络故障与限流透传真实错误码（避免误导用户去调权限），其余保持调用方
// 给定的频道错误码。classifyPinError 的置顶分类在此一并覆盖。

import (
	"errors"
	"net"
	"syscall"
	"testing"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
)

func TestClassifyVerifyError(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		fallback apperr.Code
		want     apperr.Code
	}{
		{"网络故障透传", &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED},
			apperr.CodeChannelNotPostable, apperr.CodeNetworkError},
		{"聊天不存在保持频道码", errors.New(`BadRequest: chat not found`),
			apperr.CodeChannelNotPostable, apperr.CodeChannelNotPostable},
		{"权限不足保持频道码", errors.New(`Forbidden: not enough rights to manage pinned messages in the chat`),
			apperr.CodeChannelNotPinnable, apperr.CodeChannelNotPinnable},
		{"未识别错误保持频道码", errors.New("boom"),
			apperr.CodeChannelNotPostable, apperr.CodeChannelNotPostable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ae := apperr.From(ClassifyVerifyError(tc.err, tc.fallback))
			if ae.Code != tc.want {
				t.Fatalf("应归类 %s，得到 %s", tc.want, ae.Code)
			}
		})
	}
}

func TestClassifyPinError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want apperr.Code
	}{
		{"置顶目标被删", errors.New(`BadRequest: message to pin not found`), apperr.CodeMessageNotFound},
		{"被移出/权限收回", errors.New(`Forbidden: not enough rights to manage pinned messages in the chat`), apperr.CodeSendTargetInvalid},
		{"聊天不存在", errors.New(`BadRequest: chat not found`), apperr.CodeSendTargetInvalid},
		{"网络故障", &net.OpError{Op: "dial", Err: syscall.ECONNRESET}, apperr.CodeNetworkError},
		{"未识别兜底", errors.New("boom"), apperr.CodeSendFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyPinError(tc.err); got != tc.want {
				t.Fatalf("应归类 %s，得到 %s", tc.want, got)
			}
		})
	}
}
