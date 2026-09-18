package apperr

// 传输层故障识别：网络连接失败、超时、对端中断等与业务无关的底层错误。
// 各分类点（取数 classifyTgError / 下载 handle.go / 发送 classifyBotError、
// classifySendError）用它把大筐兜底码细分为 NETWORK_ERROR——操作员据此
// 区分"本机网络问题"与真实业务失败。只依赖标准库，维持本包零第三方依赖。

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"syscall"
)

// IsTransportFailure 判断错误链中是否含传输层故障。
// 不含 context.Canceled：任务取消/中断由 worker 统一改判 INTERRUPTED，
// 不属于网络问题。net.Error、常见连接类 errno 与 io.ErrUnexpectedEOF
// 覆盖 Go HTTP 客户端与 gotd MTProto 连接的主流断链形态。
func IsTransportFailure(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNABORTED) ||
		errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.EHOSTUNREACH) ||
		errors.Is(err, syscall.ENETUNREACH) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var sysErr *os.SyscallError
	if errors.As(err, &sysErr) {
		return true
	}
	return false
}
