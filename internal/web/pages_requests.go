package web

// 请求记录的共享辅助：筛选参数组装、来源链接规范化与重试拒绝文案。
// 当前 SSR 记录/频道页面已删除，本文件只保留 /api/v1 与 CSV 导出
// 仍在使用的底层 helper。

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/access"
	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/store"
	"github.com/huaiminyetnotsleep/spore/internal/tmeurl"
)

// requestFilterForm 是筛选表单的当前值（buildRequestFilter 返回值之一；
// SPA API 与 CSV 只使用筛选条件，回显字段保留以维持单一组装入口）。
type requestFilterForm struct {
	UserID       string
	BotID        string
	Status       string
	Channel      string
	MediaType    string
	DeliveryMode string
	ErrorCode    string
	Since        string
	Until        string
}

// buildRequestFilter 从查询参数组装 DAO 筛选条件；非法参数返回错误（→400）。
func buildRequestFilter(r *http.Request, loc *time.Location) (store.RequestFilter, requestFilterForm, timeRange, error) {
	form := requestFilterForm{
		UserID:       strings.TrimSpace(r.URL.Query().Get("user_id")),
		BotID:        strings.TrimSpace(r.URL.Query().Get("bot_id")),
		Status:       r.URL.Query().Get("status"),
		Channel:      strings.TrimSpace(r.URL.Query().Get("channel")),
		MediaType:    r.URL.Query().Get("media_type"),
		DeliveryMode: r.URL.Query().Get("delivery_mode"),
		ErrorCode:    r.URL.Query().Get("error_code"),
	}
	tr, err := parseTimeRange(r, loc)
	if err != nil {
		return store.RequestFilter{}, form, tr, err
	}
	form.Since, form.Until = tr.SinceDay, tr.UntilDay

	f := store.RequestFilter{Status: form.Status, ChannelKey: form.Channel,
		MediaType: form.MediaType, DeliveryMode: form.DeliveryMode,
		ErrorCode: form.ErrorCode, Since: tr.Since, Until: tr.Until}
	if form.UserID != "" {
		id, err := strconv.ParseInt(form.UserID, 10, 64)
		if err != nil || id <= 0 {
			return store.RequestFilter{}, form, tr, errors.New("用户 ID 必须为正整数")
		}
		f.UserID = id
	}
	if form.BotID != "" {
		id, err := strconv.ParseInt(form.BotID, 10, 64)
		if err != nil || id <= 0 {
			return store.RequestFilter{}, form, tr, errors.New("机器人 ID 必须为正整数")
		}
		f.BotID = id
	}
	return f, form, tr, nil
}

// requestLink 把请求行渲染为规范化来源 URL：
// 公开频道 https://t.me/<username>/<id>；私有频道 https://t.me/c/<内部ID>/<id>
// （channel_key 的 -100 前缀去除即链接内部 ID）。结构化字段异常时返回空值。
func requestLink(rq store.Request) string {
	ref := tmeurl.SourceRef{MessageID: rq.MessageID}
	switch rq.SourceKind {
	case store.SourcePublic:
		ref.Kind = tmeurl.PeerUsername
		ref.Username = rq.ChannelKey
	case store.SourcePrivate:
		if !strings.HasPrefix(rq.ChannelKey, "-100") || len(rq.ChannelKey) <= 4 {
			return ""
		}
		channelID, err := strconv.ParseInt(strings.TrimPrefix(rq.ChannelKey, "-100"), 10, 64)
		if err != nil {
			return ""
		}
		ref.Kind = tmeurl.PeerChannelID
		ref.ChannelID = channelID
	default:
		return ""
	}
	link, _ := ref.URL()
	return link
}

// requestLinkText 生成链接显示文本（频道标识 # 消息 ID）。
func requestLinkText(rq store.Request) string {
	if rq.SourceKind == store.SourcePrivate {
		return "私有 " + strings.TrimPrefix(rq.ChannelKey, "-100") + "#" + strconv.Itoa(rq.MessageID)
	}
	return rq.ChannelKey + "#" + strconv.Itoa(rq.MessageID)
}

// retryErrText 把重试拒绝码转为中文提示。
func retryErrText(ae *apperr.AppError) string {
	switch ae.Code {
	case apperr.CodeRetryExhausted:
		return "该请求已达最大尝试次数（累计含首次最多 " +
			strconv.Itoa(access.MaxRequestAttempts) + " 次）"
	case apperr.CodeUserDisabled:
		return "请求所属用户未启用，无法重试"
	case apperr.CodeQueueFull:
		return "内存队列已满，请稍后重试"
	case apperr.CodeStoreConstraint:
		return "仅失败状态的请求可重试"
	default:
		return "操作未能完成，请稍后重试"
	}
}

// utcOffsetSec 计算时区相对 UTC 的当前偏移秒（趋势分桶对齐用，
// 与 store 统计 DAO 的 ListRequestTrend 约定一致）。
func utcOffsetSec(loc *time.Location, now time.Time) int {
	_, off := now.In(loc).Zone()
	return off
}
