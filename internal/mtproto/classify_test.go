package mtproto

// Telegram RPC 错误分类测试：classifyTgError 的错误码映射（可定位原因
// 优先，未知错误兜底 INTERNAL_ERROR），以及 IsFileReferenceExpired 对
// file reference 失效的识别——worker 的刷新重试以它为唯一判据。

import (
	"errors"
	"io"
	"net"
	"syscall"
	"testing"

	"github.com/gotd/td/tgerr"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
)

func TestClassifyTgError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want apperr.Code
	}{
		{"频道不可访问", tgerr.New(400, "CHANNEL_PRIVATE"), apperr.CodeChannelInaccessible},
		{"公开群不存在", tgerr.New(400, "CHANNEL_PUBLIC_GROUP_NA"), apperr.CodeChannelInaccessible},
		{"需要管理员权限", tgerr.New(400, "CHAT_ADMIN_REQUIRED"), apperr.CodeChannelInaccessible},
		{"聊天不存在", tgerr.New(400, "CHAT_NOT_FOUND"), apperr.CodeChannelInaccessible},
		{"用户名未注册", tgerr.New(400, "USERNAME_NOT_OCCUPIED"), apperr.CodeInvalidURL},
		{"用户名非法", tgerr.New(400, "USERNAME_INVALID"), apperr.CodeInvalidURL},
		{"消息 ID 非法", tgerr.New(400, "MESSAGE_ID_INVALID"), apperr.CodeMessageNotFound},
		{"消息为空", tgerr.New(400, "MESSAGE_EMPTY"), apperr.CodeMessageNotFound},
		{"引用过期", tgerr.New(400, "FILE_REFERENCE_EXPIRED"), apperr.CodeFileReferenceInvalid},
		{"引用彻底失效", tgerr.New(400, "PERSISTENT_FILE_REFERENCE_INVALID"), apperr.CodeFileReferenceInvalid},
		{"限流", tgerr.New(420, "FLOOD_WAIT_X"), apperr.CodeRateLimited},
		{"premium 限流", tgerr.New(420, "FLOOD_PREMIUM_WAIT_X"), apperr.CodeRateLimited},
		{"服务端故障", tgerr.New(500, "INTERNAL_SERVER_ERROR"), apperr.CodeTelegramServer},
		{"服务端超时", tgerr.New(500, "TIMEOUT"), apperr.CodeTelegramServer},
		{"网络连接故障", &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}, apperr.CodeNetworkError},
		{"传输中断", io.ErrUnexpectedEOF, apperr.CodeNetworkError},
		{"未识别错误兜底", tgerr.New(400, "SOME_UNKNOWN_ERROR"), apperr.CodeInternal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ae := classifyTgError(tc.err)
			if ae.Code != tc.want {
				t.Fatalf("应归类为 %s，得到 %s（%v）", tc.want, ae.Code, tc.err)
			}
		})
	}
}

func TestIsFileReferenceExpired(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"引用过期", apperr.Wrap(apperr.CodeFileReferenceInvalid, tgerr.New(400, "FILE_REFERENCE_EXPIRED")), true},
		{"其他 Telegram 错误", apperr.Wrap(apperr.CodeMediaDownloadFailed, tgerr.New(400, "MEDIA_EMPTY")), false},
		{"非 Telegram 包装错误", apperr.Wrap(apperr.CodeNetworkError, errors.New("boom")), false},
		{"无 Cause 的 AppError", apperr.New(apperr.CodeInternal, "boom"), false},
		{"裸错误", errors.New("boom"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsFileReferenceExpired(tc.err); got != tc.want {
				t.Fatalf("IsFileReferenceExpired 应为 %v，得到 %v", tc.want, got)
			}
		})
	}
}
