package queue

// errlog.go — worker 侧错误日志接入辅助：统一的任务上下文构造、错误码→
// 环节推断与站点速记。全部经 Deps.ErrLog（*errlog.Service，nil 安全）落
// error_logs 表，尽力而为不影响任务结果（观测面不是业务面）。

import (
	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/errlog"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// errorStage 把终态错误码映射为管线环节名（error_logs.stage）：取数/下载/
// 切段/发送/云盘上传/入队各自有代表性错误码；无明确归属返回空串（未标注，
// 管理端按码筛选仍可定位）。INTERRUPTED 等收尾码不标注环节。
func errorStage(code apperr.Code) string {
	switch code {
	case apperr.CodeMessageNotFound, apperr.CodeChannelInaccessible,
		apperr.CodeServiceMessage, apperr.CodeInvalidURL:
		return "fetch"
	case apperr.CodeMediaDownloadFailed, apperr.CodeTempDirFull, apperr.CodeFileTooLarge,
		apperr.CodeFileReferenceInvalid:
		return "download"
	case apperr.CodeSplitUnavailable:
		return "split"
	case apperr.CodeSendFailed, apperr.CodeRateLimited, apperr.CodePeerFlood,
		apperr.CodeNetworkError, apperr.CodeTelegramServer,
		apperr.CodeSendTargetInvalid, apperr.CodeLargeChannelUnavailable:
		return "send"
	case apperr.CodeCloudAuthFailed, apperr.CodeCloudQuota, apperr.CodeCloudNetwork,
		apperr.CodeCloudUploadFailed, apperr.CodeCloudUploadTimeout,
		apperr.CodeCloudTextOnly, apperr.CodeCloudVerifyFailed:
		return "upload"
	case apperr.CodeQueueFull:
		return "enqueue"
	default:
		return ""
	}
}

// jobLogContext 构造任务级参数快照（纯 ID/键类值，数据范围红线）。
func jobLogContext(j Job) map[string]any {
	ctx := map[string]any{"job_id": j.ID}
	if j.BotID != 0 {
		ctx["bot_id"] = j.BotID
	}
	if j.UserID != 0 {
		ctx["user_id"] = j.UserID
	}
	if key := refChannelKey(j.Ref); key != "" {
		ctx["channel_key"] = key
	}
	if j.Ref.MessageID != 0 {
		ctx["message_id"] = j.Ref.MessageID
	}
	if j.CloudDest != "" {
		ctx["cloud_dest"] = j.CloudDest
	}
	return ctx
}

// taskErrorLog 构造一次失败尝试的错误日志（请求管线终态记录：每次失败
// 尝试一行，中间尝试的根因不再被 requests 终态覆盖丢失）。媒体诊断元数据
// （转换阶段之后）一并进上下文。
func taskErrorLog(j Job, ae *apperr.AppError, meta mediaMeta) errlog.Record {
	rec := errlog.Record{
		Source:    store.ErrorSourceRequest,
		Code:      string(ae.Code),
		Stage:     errorStage(ae.Code),
		Severity:  store.ErrorSeverityError,
		Message:   "任务失败",
		Detail:    errorDetailText(ae),
		Context:   jobLogContext(j),
		RequestID: j.RequestID,
	}
	if meta.MediaType != "" {
		rec.Context["media_type"] = meta.MediaType
	}
	if meta.FileSize > 0 {
		rec.Context["file_size"] = meta.FileSize
	}
	if meta.FileName != "" {
		rec.Context["file_name"] = meta.FileName
	}
	return rec
}

// warnErrorLog 构造尽力而为操作失败的 warn 级记录（不影响任务结果的
// 收尾/附属操作：占位补发、失败提示、确认文案、坐标落库等）。
func warnErrorLog(j Job, source, stage, message string, err error) errlog.Record {
	rec := errlog.FromError(source, stage, message, err, jobLogContext(j))
	rec.Severity = store.ErrorSeverityWarn
	rec.RequestID = j.RequestID
	return rec
}
