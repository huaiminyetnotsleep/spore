package media

// 下载错误细分测试：downloadErrorCode 把底层错误拆为可定位的错误码——
// 引用失效（worker 据此刷新重试）、网络传输故障、其余 MEDIA_DOWNLOAD_FAILED 兜底。

import (
	"errors"
	"io"
	"net"
	"syscall"
	"testing"

	"github.com/gotd/td/tgerr"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
)

func TestDownloadErrorCode(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want apperr.Code
	}{
		{"引用过期", tgerr.New(400, "FILE_REFERENCE_EXPIRED"), apperr.CodeFileReferenceInvalid},
		{"引用彻底失效", tgerr.New(400, "PERSISTENT_FILE_REFERENCE_INVALID"), apperr.CodeFileReferenceInvalid},
		{"网络连接故障", &net.OpError{Op: "read", Err: syscall.ECONNRESET}, apperr.CodeNetworkError},
		{"传输中断", io.ErrUnexpectedEOF, apperr.CodeNetworkError},
		{"服务端错误保持下载兜底", tgerr.New(500, "INTERNAL_SERVER_ERROR"), apperr.CodeMediaDownloadFailed},
		{"普通错误保持下载兜底", errors.New("boom"), apperr.CodeMediaDownloadFailed},
	}
	for _, tc := range cases {
		if got := downloadErrorCode(tc.err); got != tc.want {
			t.Errorf("%s: 应归类 %s，得到 %s", tc.name, tc.want, got)
		}
	}
}
