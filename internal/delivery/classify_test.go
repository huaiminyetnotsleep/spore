package delivery

// ClassifyBotError 细分断言：发送目标不可用按 Bot API 响应描述特征识别
// （重试无效，需用户/管理员处理目标），网络传输故障归 NETWORK_ERROR，
// 其余保持 BOT_SEND_FAILED 兜底。

import (
	"errors"
	"net"
	"syscall"
	"testing"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
)

func TestClassifyBotError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want apperr.Code
	}{
		{"聊天不存在", errors.New(`BadRequest: chat not found`), apperr.CodeSendTargetInvalid},
		{"被用户拉黑", errors.New(`Forbidden: bot was blocked by the user`), apperr.CodeSendTargetInvalid},
		{"被踢出群组", errors.New(`Forbidden: bot was kicked from the channel chat`), apperr.CodeSendTargetInvalid},
		{"非频道成员", errors.New(`Forbidden: bot is not a member of the channel chat`), apperr.CodeSendTargetInvalid},
		{"缺管理员权限", errors.New(`BadRequest: need administrator rights in the channel chat`), apperr.CodeSendTargetInvalid},
		{"权限不足", errors.New(`BadRequest: not enough rights to send text messages to the chat`), apperr.CodeSendTargetInvalid},
		{"无权限变体", errors.New(`Forbidden: have no rights to send a message`), apperr.CodeSendTargetInvalid},
		{"群升级迁移", errors.New(`BadRequest: group chat was upgraded to a supergroup chat`), apperr.CodeSendTargetInvalid},
		{"用户注销", errors.New(`Forbidden: user is deactivated`), apperr.CodeSendTargetInvalid},
		{"网络故障", &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}, apperr.CodeNetworkError},
		{"其他 400 兜底", errors.New(`BadRequest: photo invalid`), apperr.CodeSendFailed},
	}
	for _, tc := range cases {
		ae, ok := ClassifyBotError(tc.err).(*apperr.AppError)
		if !ok || ae.Code != tc.want {
			t.Errorf("%s: 应归类 %s，得到 %v", tc.name, tc.want, ClassifyBotError(tc.err))
		}
	}
	if ClassifyBotError(nil) != nil {
		t.Error("nil 应透传为 nil")
	}
}
