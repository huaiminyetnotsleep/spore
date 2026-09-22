package mtproto

import (
	"context"
	"errors"
	"net"
	"testing"
)

func TestClassifyMTProtoError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"封禁", errors.New("RPC响应失败: USER_DEACTIVATED_BAN"), ErrorKindBanned},
		{"会话撤销", errors.New("SESSION_REVOKED"), ErrorKindRevoked},
		{"会话未注册", errors.New("AUTH_KEY_UNREGISTERED (401)"), ErrorKindRevoked},
		{"会话过期", errors.New("SESSION_EXPIRED"), ErrorKindRevoked},
		{"包裹的撤销", context.Canceled, ErrorKindUnknown},
		{"网络超时", context.DeadlineExceeded, ErrorKindNetwork},
		{"连接失败", errors.New("dial tcp: connection refused"), ErrorKindNetwork},
		{"net错误", &net.DNSError{Err: "no such host"}, ErrorKindNetwork},
		{"未知", errors.New("PHONE_NUMBER_INVALID"), ErrorKindUnknown},
		{"空", nil, ErrorKindUnknown},
	}
	for _, tc := range cases {
		if got := ClassifyMTProtoError(tc.err); got != tc.want {
			t.Errorf("%s: got %s want %s", tc.name, got, tc.want)
		}
	}
}

// TestSessionErrorKindLifecycle 覆盖快照字段生命周期：offline 带分类、
// 重新进入登录/就绪后清空。
func TestSessionErrorKindLifecycle(t *testing.T) {
	s := newSession(nil)
	if snap := s.Status(); snap.ErrorKind != "" {
		t.Fatalf("初始无分类: %+v", snap)
	}
	s.setOffline(errors.New("USER_DEACTIVATED_BAN"))
	if snap := s.Status(); snap.ErrorKind != ErrorKindBanned {
		t.Fatalf("offline 应带封禁分类: %+v", snap)
	}
	s.setLoginPending(true)
	if snap := s.Status(); snap.ErrorKind != "" {
		t.Fatalf("登录中应清空分类: %+v", snap)
	}
	s.setOffline(errors.New("connection reset"))
	if snap := s.Status(); snap.ErrorKind != ErrorKindNetwork {
		t.Fatalf("网络错误分类: %+v", snap)
	}
	s.setReady()
	if snap := s.Status(); snap.ErrorKind != "" {
		t.Fatalf("ready 应清空分类: %+v", snap)
	}
}
