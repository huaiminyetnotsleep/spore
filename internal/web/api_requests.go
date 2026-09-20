package web

// GET /api/v1/requests、GET /api/v1/requests/{id}：消息记录列表与详情查询 API。
// 筛选参数与 SSR /requests 页面一一对应（user_id/status/channel/media_type/
// error_code/since/until），筛选组装复用 buildRequestFilter（与 SSR、CSV 导出
// 同一来源）；分页总数经 CountRequests DAO 取得，不在 handler 拼 SQL。
// message_url 复用 requestLink 单一来源，只出现在受保护页面响应体中，
// 不进入任何日志字段。

import (
	"net/http"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/store"
	"github.com/huaiminyetnotsleep/spore/internal/syscfg"
)

// apiRequestProgress 是处理中请求的实时传输进度（字节，内存态不落库）。
// 仅 status == processing 且 worker 已在注册表中登记的记录非 null；
// 下载与上传是重叠的两条独立管线，分别计数（total 为任务全部媒体之和）。
type apiRequestProgress struct {
	TotalBytes      int64 `json:"total_bytes"`
	DownloadedBytes int64 `json:"downloaded_bytes"`
	UploadedBytes   int64 `json:"uploaded_bytes"`
}

// apiRequestRow 是消息记录列表行 DTO。MessageURL 由 requestLink 生成，
// 让 SPA 列表与 SSR/CSV 同源展示完整原始消息链接；只在受保护页面
// 响应体中出现，不进入任何日志字段。
type apiRequestRow struct {
	ID               int64    `json:"id"`
	UserID           int64    `json:"user_id"`
	Username         string   `json:"username"`
	DisplayName      string   `json:"display_name"`
	SourceKind       string   `json:"source_kind"` // public | private
	ChannelKey       string   `json:"channel_key"`
	MessageID        int      `json:"message_id"`
	MessageURL       string   `json:"message_url"`
	Status           string   `json:"status"`
	Attempt          int      `json:"attempt"`
	ErrorCode        string   `json:"error_code"`
	MediaType        string   `json:"media_type"` // 空串 = 未记录（失败于消息转换前）
	MediaTypes       []string `json:"media_types"`
	SourceMediaDCIDs []int    `json:"source_media_dc_ids"`
	DeliveryMode     string   `json:"delivery_mode"`
	// 受理 bot（多机器人池归属）；0 = 存量行/非 Bot 通道创建，前端显示"—"
	BotID       int64               `json:"bot_id"`
	BotUsername string              `json:"bot_username,omitempty"`
	RequestedAt int64               `json:"requested_at"`
	DurationMs  int64               `json:"duration_ms"`
	Progress    *apiRequestProgress `json:"progress,omitempty"`
}

// apiCloudUploadRow 是请求详情的云盘上传记录行 DTO（字段与 store.CloudUpload
// 对齐，见 docs/reference/api.md「请求详情」；不含行 ID——展示不依赖它）。
type apiCloudUploadRow struct {
	Destination string `json:"destination"`
	RemotePath  string `json:"remote_path"`
	FileName    string `json:"file_name"`
	Status      string `json:"status"`
	ErrorCode   string `json:"error_code"`
	Bytes       int64  `json:"bytes"`
	CreatedAt   int64  `json:"created_at"`
	FinishedAt  int64  `json:"finished_at"`
}

// apiRequestDetail 是请求详情 DTO：覆盖 SSR 请求详情页展示的业务字段
// （各阶段时间、尝试上限、错误文案与媒体诊断元数据）。
// message_url 由 requestLink 生成；结构化字段异常时为空串（与 SSR"链接不可用"对应）。
type apiRequestDetail struct {
	apiRequestRow
	ChannelLinkText string `json:"channel_link_text"` // 频道标识#消息ID（与 SSR LinkText 同源）
	MessageURL      string `json:"message_url"`
	Username        string `json:"username"`     // 所属用户名（可为空）
	DisplayName     string `json:"display_name"` // 所属显示名（可为空）
	ErrorText       string `json:"error_text"`
	AttemptMax      int    `json:"attempt_max"`
	FileName        string `json:"file_name"`
	FileSize        int64  `json:"file_size"`
	QueuedAt        int64  `json:"queued_at"`
	StartedAt       int64  `json:"started_at"`
	FinishedAt      int64  `json:"finished_at"`
	// ParentRequestID：云盘补存新建的行指向原请求；普通请求为 0。
	ParentRequestID int64               `json:"parent_request_id"`
	CloudUploads    []apiCloudUploadRow `json:"cloud_uploads"` // 无记录时为 []
}

// handleAPIRequestsList 返回消息记录分页数据。
func (s *Server) handleAPIRequestsList(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.requests.list"
	ctx := r.Context()
	filter, _, _, err := buildRequestFilter(r, s.tz(ctx))
	if err != nil {
		// buildRequestFilter 的错误文案是本包固定受控文案（用户 ID/日期格式）
		s.apiBadRequest(w, r, op, err.Error())
		return
	}
	page, err := parseAPIPageParams(r)
	if err != nil {
		s.apiBadRequest(w, r, op, err.Error())
		return
	}
	total, err := s.st.CountRequests(ctx, filter)
	if err != nil {
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	filter.Limit, filter.Offset = page.PageSize, page.Offset
	rows, err := s.st.ListRequestsWithUser(ctx, filter)
	if err != nil {
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	items := make([]apiRequestRow, 0, len(rows))
	for _, rq := range rows {
		row := requestRowDTO(rq.Request)
		row.Username = rq.UserUsername
		row.DisplayName = rq.UserDisplayName
		row.Progress = s.requestProgress(rq.Request)
		items = append(items, row)
	}
	writeAPIList(w, newAPIListEnvelope(items, page, total))
}

// handleAPIRequestDetail 返回单条请求详情；不存在输出 404 JSON。
func (s *Server) handleAPIRequestDetail(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.requests.detail"
	ctx := r.Context()
	id, ok := s.apiPathID(w, r, op)
	if !ok {
		return
	}
	rq, err := s.st.GetRequest(ctx, id)
	if err != nil {
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	u, err := s.st.GetUser(ctx, rq.UserID)
	if err != nil {
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	uploads, err := s.st.CloudUploadsByRequest(ctx, id)
	if err != nil {
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	detail := apiRequestDetail{
		apiRequestRow:   requestRowDTO(rq),
		ChannelLinkText: requestLinkText(rq),
		MessageURL:      requestLink(rq),
		Username:        u.Username,
		DisplayName:     u.DisplayName,
		AttemptMax:      syscfg.LoadMaxRequestAttempts(ctx, s.st),
		FileName:        rq.FileName,
		FileSize:        rq.FileSize,
		QueuedAt:        rq.QueuedAt,
		StartedAt:       rq.StartedAt,
		FinishedAt:      rq.FinishedAt,
		ParentRequestID: rq.ParentRequestID,
		CloudUploads:    make([]apiCloudUploadRow, 0, len(uploads)),
	}
	for _, up := range uploads {
		detail.CloudUploads = append(detail.CloudUploads, apiCloudUploadRow{
			Destination: up.Destination,
			RemotePath:  up.RemotePath,
			FileName:    up.FileName,
			Status:      up.Status,
			ErrorCode:   up.ErrorCode,
			Bytes:       up.Bytes,
			CreatedAt:   up.CreatedAt,
			FinishedAt:  up.FinishedAt,
		})
	}
	detail.Progress = s.requestProgress(rq)
	if rq.ErrorCode != "" {
		detail.ErrorText = apperr.UserText(apperr.Code(rq.ErrorCode))
	}
	writeAPISingle(w, detail)
}

// requestRowDTO 把 store 请求行转换为列表/详情共用的行 DTO；
// MessageURL 复用 requestLink 单一来源，异常字段时为空串。
func requestRowDTO(rq store.Request) apiRequestRow {
	mediaTypes := rq.MediaTypes
	if mediaTypes == nil {
		mediaTypes = []string{}
	}
	dcIDs := rq.SourceMediaDCIDs
	if dcIDs == nil {
		dcIDs = []int{}
	}
	return apiRequestRow{
		ID: rq.ID, UserID: rq.UserID,
		SourceKind: rq.SourceKind, ChannelKey: rq.ChannelKey, MessageID: rq.MessageID,
		MessageURL: requestLink(rq),
		Status:     rq.Status, Attempt: rq.Attempt,
		ErrorCode: rq.ErrorCode, MediaType: rq.MediaType,
		MediaTypes:       mediaTypes,
		SourceMediaDCIDs: dcIDs,
		DeliveryMode:     rq.DeliveryMode,
		BotID:            rq.BotID, BotUsername: rq.BotUsername,
		RequestedAt: rq.RequestedAt, DurationMs: rq.DurationMs,
	}
}

// requestProgress 读取处理中请求的实时传输进度快照：仅 processing 状态且
// worker 已注册（任务进行中）的记录返回非 nil；未接线（nil 注册表）、排队、
// 文本任务与终态记录均返回 nil，响应体省略 progress 字段。
func (s *Server) requestProgress(rq store.Request) *apiRequestProgress {
	if rq.Status != store.RequestProcessing {
		return nil
	}
	snap := s.progress.Snapshot(rq.ID)
	if snap == nil {
		return nil
	}
	return &apiRequestProgress{
		TotalBytes:      snap.TotalBytes,
		DownloadedBytes: snap.DownloadedBytes,
		UploadedBytes:   snap.UploadedBytes,
	}
}
