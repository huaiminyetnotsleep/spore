package apperr

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestUserText(t *testing.T) {
	for _, code := range []Code{
		CodeInvalidURL, CodeMessageNotFound, CodeChannelInaccessible,
		CodeServiceMessage, CodeMediaUnsupported, CodeFileTooLarge,
		CodeMediaDownloadFailed, CodeRateLimited,
		CodeSendFailed, CodeLargeChannelUnavailable, CodeInternal,
		CodeNetworkError, CodeTelegramServer, CodeFileReferenceInvalid,
		CodeSendTargetInvalid,

		CodeStoreUnavailable, CodeStoreMigration, CodeStoreConstraint,
		CodeUserNotAuthorized, CodeUserPending, CodeUserDisabled,
		CodeDuplicateLink, CodeSubmitRateLimited, CodeQuotaExceeded,
		CodeConcurrentLimited, CodeQueueFull,
		CodeInterrupted, CodeRetryExhausted,

		CodeCloudAuthFailed, CodeCloudQuota, CodeCloudNetwork,
		CodeCloudUploadFailed, CodeCloudUploadTimeout, CodeCloudTextOnly,
		CodeCloudVerifyFailed,
	} {
		if UserText(code) == UserText(CodeInternal) && code != CodeInternal {
			t.Errorf("错误码 %s 缺少专属用户文案", code)
		}
	}
	if got := UserText(CodeLargeChannelUnavailable); got != "大文件发送通道暂不可用，请稍后重试。" {
		t.Errorf("大文件通道不可用文案不符合契约: %q", got)
	}
	if got := UserText(CodeCloudAuthFailed); got != "网盘账号验证失败，请联系管理员。" {
		t.Errorf("云盘凭据错误文案不符合契约: %q", got)
	}
	if got := UserText(CodeCloudQuota); got != "网盘空间不足。" {
		t.Errorf("云盘配额文案不符合契约: %q", got)
	}
	if got := UserText(CodeCloudNetwork); got != "网盘网络异常，请稍后重试。" {
		t.Errorf("云盘网络文案不符合契约: %q", got)
	}
	if got := UserText(CodeCloudUploadFailed); got != "网盘上传失败，请稍后重试。" {
		t.Errorf("云盘上传兜底文案不符合契约: %q", got)
	}
	if got := UserText(CodeCloudUploadTimeout); got != "网盘上传超时，请稍后重试。" {
		t.Errorf("云盘上传超时文案不符合契约: %q", got)
	}
	if got := UserText(CodeCloudTextOnly); got != "纯文本消息不支持网盘下载。" {
		t.Errorf("纯文本拒绝文案不符合契约: %q", got)
	}
	if got := UserText(CodeCloudVerifyFailed); got != "云盘文件核验暂时不可用，请稍后重试。" {
		t.Errorf("云盘核验不可用文案不符合契约: %q", got)
	}
	if got := UserText(CodeNetworkError); got != "网络连接失败或超时，请稍后重试。" {
		t.Errorf("网络故障文案不符合契约: %q", got)
	}
	if got := UserText(CodeTelegramServer); got != "Telegram 服务暂时故障，请稍后重试。" {
		t.Errorf("服务端故障文案不符合契约: %q", got)
	}
	if got := UserText(CodeFileReferenceInvalid); got != "源消息的媒体引用已失效且无法刷新，内容可能已被删除或更换，请确认后重试。" {
		t.Errorf("引用失效文案不符合契约: %q", got)
	}
	if got := UserText(CodeSendTargetInvalid); got != "消息发送目标不可用：机器人可能已离开你的绑定频道或缺少发言权限，请重新绑定频道或联系管理员。" {
		t.Errorf("发送目标不可用文案不符合契约: %q", got)
	}
	if UserText("NOT_A_CODE") != UserText(CodeInternal) {
		t.Error("未知错误码应回落 INTERNAL_ERROR 文案")
	}
}

func TestWrapKeepsCause(t *testing.T) {
	root := fmt.Errorf("root cause")
	err := Wrap(CodeChannelInaccessible, root)
	if err.Unwrap() != root {
		t.Fatal("Wrap 应保留原始错误")
	}
	var ae *AppError
	if !errors.As(fmt.Errorf("outer: %w", err), &ae) || ae.Code != CodeChannelInaccessible {
		t.Fatal("errors.As 应能穿透取回 AppError 及其错误码")
	}
	// 未显式给 Message 时，日志文本应回落到用户文案
	if got := err.Error(); got[:len(CodeChannelInaccessible)] != string(CodeChannelInaccessible) {
		t.Errorf("Error() 应以错误码开头，得到 %q", got)
	}
}

func TestFrom(t *testing.T) {
	if got := From(errors.New("boom")); got.Code != CodeInternal {
		t.Errorf("未知错误应归类 INTERNAL_ERROR，得到 %s", got.Code)
	}
	wrapped := Wrap(CodeMessageNotFound, errors.New("MSG_ID_INVALID"))
	if From(wrapped).Code != CodeMessageNotFound {
		t.Error("已是 AppError 时 From 不应重新分类")
	}
	timeout := Wrap(CodeCloudUploadTimeout, context.DeadlineExceeded)
	if From(timeout).Code != CodeCloudUploadTimeout {
		t.Error("云盘上传超时错误不应重新分类")
	}
	if !errors.Is(timeout, context.DeadlineExceeded) {
		t.Error("云盘上传超时应保留 context deadline 原因")
	}
}
