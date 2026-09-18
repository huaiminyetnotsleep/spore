package apperr

// 传输层故障识别测试：net.Error、连接类 errno、io.ErrUnexpectedEOF 与
// 超时哨兵应命中；context.Canceled（任务取消，由 worker 改判 INTERRUPTED）
// 与普通业务错误不得误判。

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"syscall"
	"testing"
)

type fakeNetError struct {
	timeout   bool
	temporary bool
}

func (e *fakeNetError) Error() string   { return "fake net error" }
func (e *fakeNetError) Timeout() bool   { return e.timeout }
func (e *fakeNetError) Temporary() bool { return e.temporary }

func TestIsTransportFailure(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"net 超时错误", &fakeNetError{timeout: true}, true},
		{"net 非超时错误", &fakeNetError{}, true},
		{"连接重置", &net.OpError{Op: "read", Err: syscall.ECONNRESET}, true},
		{"连接拒绝", &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}, true},
		{"裸 errno", syscall.EPIPE, true},
		{"syscall 包装", os.NewSyscallError("read", syscall.ECONNRESET), true},
		{"意外 EOF", io.ErrUnexpectedEOF, true},
		{"超时哨兵", context.DeadlineExceeded, true},
		{"包装在业务错误内", errors.Join(errors.New("下载失败"), io.ErrUnexpectedEOF), true},
		{"任务取消", context.Canceled, false},
		{"普通业务错误", errors.New("MEDIA_EMPTY"), false},
	}
	for _, tc := range cases {
		if got := IsTransportFailure(tc.err); got != tc.want {
			t.Errorf("%s: IsTransportFailure 应为 %v，得到 %v", tc.name, tc.want, got)
		}
	}
}
