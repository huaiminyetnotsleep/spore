package web

// 云盘下载 API：
//   - GET/PUT /api/v1/cloud-drive：配置视图与全量校验保存（cloudarchive.Manager
//     校验 + 0600 原子写，凭据只落 data/cloud-drive.json 不进数据库，options
//     原样回显供管理台编辑）；
//   - POST /api/v1/cloud-drive/test：逐目的地连通性测试（Sink.Ping 只读探测）；
//   - POST /api/v1/requests/{id}/cloud-archive 与 /api/v1/requests/cloud-archive-batch：
//     单条/批量补存，公共逻辑见 cloudArchiveBackfill——请求级资格
//     与特权入队在 internal/access.CloudBackfill（绕过用户配额/频率/去重），
//     云盘全局状态（开关/rclone/目的地）在本层判定。
// 全部走 apiAuth/apiCSRF 与统一 JSON 信封；错误文案受控，options 值不进
// 任何日志、错误信息与审计。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/access"
	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/cloudarchive"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// cloudPingTimeout 是连通性测试的单次时间窗（按需手动点击，不做自动化
// 轮询——高频会触发封禁）。MEGA 登录失败的单周期可达数十秒（go-mega
// 内部退避重试），窗口过短会把确定性凭据错误吞成超时，管理台拿不到
// 明确答案。
const cloudPingTimeout = 60 * time.Second

const cloudSecretMask = "********"

var cloudSensitiveOptionPattern = regexp.MustCompile(`(?i)(pass|2fa|secret|token|key)`)

// apiCloudDestinationView 是目的地行 DTO（GET 视图与 PUT 请求体同构）。
// 响应中的敏感 options 只返回固定掩码；PUT 收到该掩码时沿用服务端已有值。
type apiCloudDestinationView struct {
	Name       string            `json:"name"`
	Type       string            `json:"type"`
	PathPrefix string            `json:"path_prefix"`
	Enabled    bool              `json:"enabled"`
	Options    map[string]string `json:"options"`
}

// apiCloudDriveView 是 GET /api/v1/cloud-drive 的响应 DTO。
type apiCloudDriveView struct {
	Enabled            bool                      `json:"enabled"`
	DefaultDestination string                    `json:"default_destination"`
	RcloneAvailable    bool                      `json:"rclone_available"`
	Destinations       []apiCloudDestinationView `json:"destinations"`
}

// apiRequireCloudCfg 校验云盘配置管理器已注入；未注入时输出 503 JSON 并
// 返回 false（与 apiRequireAccess 同款受控不可用）。
func (s *Server) apiRequireCloudCfg(w http.ResponseWriter, r *http.Request, op string) bool {
	if s.cloudCfg != nil {
		return true
	}
	s.log.Warn("API 云盘配置未接入", "op", op, "path", r.URL.Path)
	writeAPIError(w, http.StatusServiceUnavailable, apiCodeUnavailable,
		apiUserMessage(apiCodeUnavailable))
	return false
}

// cloudDriveView 由配置快照组装响应视图（rclone_available 每次实时探测）。
// 凭据类 option 只返回掩码，避免通过管理端 API 泄露到浏览器网络响应或 JS 状态。
func cloudDriveView(cfg cloudarchive.Config) apiCloudDriveView {
	_, binErr := cloudarchive.BinPath()
	dests := make([]apiCloudDestinationView, 0, len(cfg.Destinations))
	for _, d := range cfg.Destinations {
		dests = append(dests, apiCloudDestinationView{
			Name: d.Name, Type: d.Type, PathPrefix: d.PathPrefix,
			Enabled: d.Enabled, Options: maskedCloudOptions(d.Options),
		})
	}
	return apiCloudDriveView{
		Enabled:            cfg.Enabled,
		DefaultDestination: cfg.DefaultDestination,
		RcloneAvailable:    binErr == nil,
		Destinations:       dests,
	}
}

func isCloudSensitiveOption(key string) bool {
	return cloudSensitiveOptionPattern.MatchString(key)
}

func maskedCloudOptions(options map[string]string) map[string]string {
	masked := make(map[string]string, len(options))
	for key, value := range options {
		if isCloudSensitiveOption(key) && value != "" {
			masked[key] = cloudSecretMask
			continue
		}
		masked[key] = value
	}
	return masked
}

// preserveCloudSecrets restores values returned as the API mask from the current
// in-memory snapshot. A masked value for a new destination is rejected rather than
// persisted as a fake credential.
func preserveCloudSecrets(previous cloudarchive.Config, next *cloudarchive.Config) error {
	oldByName := make(map[string]cloudarchive.Destination, len(previous.Destinations))
	for _, destination := range previous.Destinations {
		oldByName[destination.Name] = destination
	}
	for i := range next.Destinations {
		destination := &next.Destinations[i]
		old, exists := oldByName[destination.Name]
		for key, value := range destination.Options {
			if value != cloudSecretMask || !isCloudSensitiveOption(key) {
				continue
			}
			if !exists || old.Options[key] == "" {
				return errors.New("目的地 " + destination.Name + " 的敏感配置项 " + key + " 未提供有效值")
			}
			destination.Options[key] = old.Options[key]
		}
	}
	return nil
}

// obscureCloudSecrets 只处理管理 API 新提交的 MEGA 密码。管理台回传的
// 掩码已经由 preserveCloudSecrets 恢复为旧值，因此不会被重复 obscure；
// 其他后端的 pass 可能要求明文，不能按 MEGA 规则一概处理。
func obscureCloudSecrets(ctx context.Context, cfg *cloudarchive.Config) error {
	for i := range cfg.Destinations {
		destination := &cfg.Destinations[i]
		if !strings.EqualFold(destination.Type, "mega") {
			continue
		}
		for key, value := range destination.Options {
			if !strings.EqualFold(key, "pass") || value == "" || value == cloudSecretMask {
				continue
			}
			obscured, err := cloudarchive.Obscure(ctx, value)
			if err != nil {
				return fmt.Errorf("目的地 %s 的密码处理失败", destination.Name)
			}
			destination.Options[key] = obscured
		}
	}
	return nil
}

// handleAPICloudDriveGet 返回云盘下载配置与运行状态（认证）。
func (s *Server) handleAPICloudDriveGet(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.cloud_drive.get"
	if !s.apiRequireCloudCfg(w, r, op) {
		return
	}
	writeAPISingle(w, cloudDriveView(s.cloudCfg.Snapshot()))
}

// handleAPICloudDrivePut 保存云盘下载配置（认证 + CSRF）。请求体与 GET 视图
// 同构（rclone_available 为服务端探测值，不接受提交，解码时自然忽略）；
// 服务端全量校验（cloudarchive.Config.Validate，受控中文错误）后经
// Manager.Save 原子写回并替换内存快照。
func (s *Server) handleAPICloudDrivePut(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.cloud_drive.update"
	if !s.apiRequireCloudCfg(w, r, op) {
		return
	}
	var in struct {
		Enabled            bool                      `json:"enabled"`
		DefaultDestination string                    `json:"default_destination"`
		Destinations       []apiCloudDestinationView `json:"destinations"`
	}
	if !s.apiReadJSON(w, r, op, &in) {
		return
	}
	previous := s.cloudCfg.Snapshot()
	cfg := cloudarchive.Config{
		Enabled:            in.Enabled,
		DefaultDestination: in.DefaultDestination,
	}
	for _, d := range in.Destinations {
		cfg.Destinations = append(cfg.Destinations, cloudarchive.Destination{
			Name: d.Name, Type: d.Type, PathPrefix: d.PathPrefix,
			Enabled: d.Enabled, Options: d.Options,
		})
	}
	if err := preserveCloudSecrets(previous, &cfg); err != nil {
		s.apiBadRequest(w, r, op, err.Error())
		return
	}
	if err := obscureCloudSecrets(r.Context(), &cfg); err != nil {
		s.log.Warn("云盘配置凭据处理失败", "op", op, "error", err.Error())
		writeAPIError(w, http.StatusServiceUnavailable, apiCodeUnavailable,
			"rclone 不可用或无法处理网盘凭据，请稍后重试。")
		return
	}
	// 校验错误是 cloudarchive 的受控中文文案（只含名称与配置键，不含值）
	if err := cfg.Validate(); err != nil {
		s.apiBadRequest(w, r, op, err.Error())
		return
	}
	if err := s.cloudCfg.Save(cfg); err != nil {
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	s.auditCloudDriveUpdate(r.Context(), previous, cfg)
	writeAPIJSON(w, http.StatusOK, struct {
		apiWriteOK
		Config apiCloudDriveView `json:"config"`
	}{apiWriteOK{OK: true}, cloudDriveView(s.cloudCfg.Snapshot())})
}

// auditCloudDriveUpdate 为配置保存留审计（best-effort）：快照只含开关、
// 默认目的地与目的地名称列表——options 值（网盘凭据）绝不入审计。
func (s *Server) auditCloudDriveUpdate(ctx context.Context, before, after cloudarchive.Config) {
	snap := func(c cloudarchive.Config) map[string]any {
		names := make([]string, 0, len(c.Destinations))
		for _, d := range c.Destinations {
			names = append(names, d.Name)
		}
		return map[string]any{
			"enabled": c.Enabled, "default_destination": c.DefaultDestination,
			"destinations": names,
		}
	}
	b, errB := json.Marshal(snap(before))
	a, errA := json.Marshal(snap(after))
	if errB != nil || errA != nil {
		s.log.Warn("云盘配置审计序列化失败", "error", errors.Join(errB, errA).Error())
		return
	}
	if err := s.st.AppendAudit(ctx, store.AuditEntry{
		Actor: "admin", Action: "cloud_drive.update", Target: "cloud_drive",
		BeforeJSON: string(b), AfterJSON: string(a),
	}); err != nil {
		s.log.Warn("写云盘配置审计失败", "error", err.Error())
	}
}

// handleAPICloudDriveTest 测试目的地连通性（认证 + CSRF）：对当前配置中的
// 目的地执行只读探测。测试失败不是错误信封，而是 200 {ok:false, 分类中文原因}。
func (s *Server) handleAPICloudDriveTest(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.cloud_drive.test"
	if !s.apiRequireCloudCfg(w, r, op) {
		return
	}
	if s.cloudSink == nil {
		s.log.Warn("API 云盘上传通道未接入", "op", op, "path", r.URL.Path)
		writeAPIError(w, http.StatusServiceUnavailable, apiCodeUnavailable,
			apiUserMessage(apiCodeUnavailable))
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if !s.apiReadJSON(w, r, op, &in) {
		return
	}
	if in.Name == "" {
		s.apiBadRequest(w, r, op, "目的地名称不能为空。")
		return
	}
	cfg := s.cloudCfg.Snapshot()
	var dest cloudarchive.Destination
	found := false
	for _, d := range cfg.Destinations {
		if d.Name == in.Name { // 草稿目的地（未启用）也允许测试
			dest, found = d, true
			break
		}
	}
	if !found {
		s.apiBadRequest(w, r, op, "目的地不存在："+in.Name)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), cloudPingTimeout)
	defer cancel()
	if err := s.cloudSink.Ping(ctx, dest); err != nil {
		s.log.Warn("云盘连通性测试失败", "op", op, "destination", dest.Name,
			"code", apperr.From(err).Code)
		writeAPIJSON(w, http.StatusOK, struct {
			OK      bool   `json:"ok"`
			Message string `json:"message"`
		}{false, cloudTestFailureText(err)})
		return
	}
	writeAPIJSON(w, http.StatusOK, struct {
		OK      bool   `json:"ok"`
		Message string `json:"message"`
	}{true, "目的地连通正常。"})
}

// cloudTestFailureText 把 Ping 失败映射为分类中文原因：CLOUD_* 走 apperr
// 用户文案；探测超时归网络异常；其余给兜底受控文案（不含底层细节）。
func cloudTestFailureText(err error) string {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return apperr.UserText(apperr.CodeCloudNetwork)
	}
	code := apperr.From(err).Code
	switch code {
	case apperr.CodeCloudAuthFailed, apperr.CodeCloudQuota,
		apperr.CodeCloudNetwork, apperr.CodeCloudUploadFailed:
		return apperr.UserText(code)
	}
	return "网盘连通性测试失败，请稍后重试。"
}

// ---- 补存（单条 + 批量共用逻辑） ----

// 云盘全局状态类跳过原因（与 docs/reference/api.md skip_reason 枚举对齐；
// 请求级四类见 access.CloudBackfillSkip*）。
const (
	cloudSkipDisabled    = "cloud_disabled"
	cloudSkipRcloneGone  = "rclone_unavailable"
	maxCloudArchiveBatch = 100
)

// cloudArchiveOutcome 是单条补存的公共执行结果：SkipReason 非空表示未建行
// （单条端点转错误信封、批量端点转 skip_reason）；QueueFull 表示已建行但
// 入队失败，行标记 failed(QUEUE_FULL)，可经现有重试入口重试。
type cloudArchiveOutcome struct {
	CreatedRequestID int64
	SkipReason       string
	QueueFull        bool
}

// cloudArchiveBackfill 是单条与批量端点的公共补存逻辑。检查顺序与
// skip_reason 语义一致：请求存在 → 终态 → 非纯文本 → 云盘开启 → rclone
// 可用 → 无在途补存（后三项是全局状态，逐条判定保证混合批次各有准确原因）。
// 返回 error 仅表示存储故障（批量端点此时中止并返回 503，已建行不回滚）。
func (s *Server) cloudArchiveBackfill(ctx context.Context, requestID int64, dest string) (cloudArchiveOutcome, error) {
	skip, err := s.access.CloudBackfillEligibility(ctx, requestID)
	if err != nil {
		return cloudArchiveOutcome{}, err
	}
	if skip != "" {
		return cloudArchiveOutcome{SkipReason: skip}, nil
	}
	if !s.cloudCfg.Enabled() {
		return cloudArchiveOutcome{SkipReason: cloudSkipDisabled}, nil
	}
	if _, err := cloudarchive.BinPath(); err != nil {
		return cloudArchiveOutcome{SkipReason: cloudSkipRcloneGone}, nil
	}
	out, err := s.access.CloudBackfill(ctx, "admin", requestID, dest)
	if err != nil {
		return cloudArchiveOutcome{}, err
	}
	// 事务内复核的竞态跳过（如并发补存命中 already_archiving）同样透传
	return cloudArchiveOutcome{
		CreatedRequestID: out.CreatedRequestID,
		SkipReason:       out.SkipReason,
		QueueFull:        out.QueueFull,
	}, nil
}

// resolveCloudArchiveDest 解析补存目的地：显式指定优先，缺省用
// default_destination；解析结果必须是当前已启用的目的地，否则 400。
func (s *Server) resolveCloudArchiveDest(w http.ResponseWriter, r *http.Request, op, requested string) (string, bool) {
	cfg := s.cloudCfg.Snapshot()
	name := requested
	if name == "" {
		name = cfg.DefaultDestination
	}
	if name == "" {
		s.apiBadRequest(w, r, op, "未指定目的地，且当前配置没有默认目的地。")
		return "", false
	}
	if _, ok := cfg.EnabledDestination(name); !ok {
		s.apiBadRequest(w, r, op, "目的地不存在或未启用："+name)
		return "", false
	}
	return name, true
}

// writeCloudArchiveSkipErr 把单条补存的跳过原因映射为错误信封
// （状态码语义见 docs/reference/api.md「云盘下载」）。
func (s *Server) writeCloudArchiveSkipErr(w http.ResponseWriter, r *http.Request, op, skip string) {
	s.log.Warn("补存请求被拒绝", "op", op, "path", r.URL.Path, "skip_reason", skip)
	switch skip {
	case access.CloudBackfillSkipNotFound:
		s.writeAPIAppErr(w, r, op, store.ErrNotFound)
	case access.CloudBackfillSkipNotFinished:
		writeAPIError(w, http.StatusConflict, string(apperr.CodeStoreConstraint),
			"该请求尚未结束，仅终态请求可补存。")
	case access.CloudBackfillSkipTextOnly:
		writeAPIError(w, http.StatusConflict, string(apperr.CodeStoreConstraint),
			"纯文本请求没有可保存的媒体。")
	case access.CloudBackfillSkipAlreadyArchiving:
		writeAPIError(w, http.StatusConflict, string(apperr.CodeStoreConstraint),
			"该请求已有在途的云盘补存任务。")
	case cloudSkipDisabled:
		writeAPIError(w, http.StatusServiceUnavailable, apiCodeUnavailable,
			"云盘下载功能未开启。")
	case cloudSkipRcloneGone:
		writeAPIError(w, http.StatusServiceUnavailable, apiCodeUnavailable,
			"rclone 不可用，云盘下载功能暂不可用。")
	default:
		s.writeAPIAppErr(w, r, op, apperr.New(apperr.CodeInternal, "未知补存跳过原因: "+skip))
	}
}

// handleAPIRequestCloudArchive 单条补存（认证 + CSRF）。
func (s *Server) handleAPIRequestCloudArchive(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.requests.cloud_archive"
	if !s.apiRequireAccess(w, r, op) || !s.apiRequireCloudCfg(w, r, op) {
		return
	}
	id, ok := s.apiPathID(w, r, op)
	if !ok {
		return
	}
	var in struct {
		Destination string `json:"destination"`
	}
	if !s.apiReadJSON(w, r, op, &in) {
		return
	}
	dest, ok := s.resolveCloudArchiveDest(w, r, op, in.Destination)
	if !ok {
		return
	}
	out, err := s.cloudArchiveBackfill(r.Context(), id, dest)
	if err != nil {
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	if out.SkipReason != "" {
		s.writeCloudArchiveSkipErr(w, r, op, out.SkipReason)
		return
	}
	if out.QueueFull {
		// 行已创建并标记 failed(QUEUE_FULL)，可经现有重试入口重试
		s.log.Warn("补存入队时队列已满", "op", op, "request_id", id,
			"created_request_id", out.CreatedRequestID)
		writeAPIError(w, http.StatusServiceUnavailable, string(apperr.CodeQueueFull),
			apiUserMessage(string(apperr.CodeQueueFull)))
		return
	}
	writeAPIJSON(w, http.StatusOK, struct {
		apiWriteOK
		CreatedRequestID int64 `json:"created_request_id"`
	}{apiWriteOK{OK: true}, out.CreatedRequestID})
}

// apiCloudArchiveBatchItem 是批量补存的逐条摘要：
// 创建成功带 created_request_id；资格不满足带 skip_reason；
// 入队失败（队列饱和）带 created_request_id 且 queue_full=true。
type apiCloudArchiveBatchItem struct {
	RequestID        int64  `json:"request_id"`
	CreatedRequestID int64  `json:"created_request_id,omitempty"`
	SkipReason       string `json:"skip_reason,omitempty"`
	QueueFull        bool   `json:"queue_full,omitempty"`
}

// handleAPIRequestsCloudArchiveBatch 批量补存（认证 + CSRF）：与单条端点
// 相同的资格校验与建行入队逻辑，逐条独立执行、互不回滚；复用队列容量控制
// （入队失败逐条标记 QUEUE_FULL）。存储故障中止整个响应（已建行保持有效）。
func (s *Server) handleAPIRequestsCloudArchiveBatch(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.requests.cloud_archive_batch"
	if !s.apiRequireAccess(w, r, op) || !s.apiRequireCloudCfg(w, r, op) {
		return
	}
	var in struct {
		RequestIDs  []int64 `json:"request_ids"`
		Destination string  `json:"destination"`
	}
	if !s.apiReadJSON(w, r, op, &in) {
		return
	}
	if len(in.RequestIDs) == 0 || len(in.RequestIDs) > maxCloudArchiveBatch {
		s.apiBadRequest(w, r, op, "请选择 1 到 100 条请求记录。")
		return
	}
	for _, id := range in.RequestIDs {
		if id <= 0 {
			s.apiBadRequest(w, r, op, "请求 ID 必须为正整数。")
			return
		}
	}
	dest, ok := s.resolveCloudArchiveDest(w, r, op, in.Destination)
	if !ok {
		return
	}
	ctx := r.Context()
	results := make([]apiCloudArchiveBatchItem, 0, len(in.RequestIDs))
	for _, id := range in.RequestIDs {
		item := apiCloudArchiveBatchItem{RequestID: id}
		out, err := s.cloudArchiveBackfill(ctx, id, dest)
		if err != nil {
			// 存储故障：中止响应（503），此前已建行的补存任务保持有效
			s.writeAPIAppErr(w, r, op, err)
			return
		}
		item.SkipReason = out.SkipReason
		item.CreatedRequestID = out.CreatedRequestID
		item.QueueFull = out.QueueFull
		results = append(results, item)
	}
	writeAPIJSON(w, http.StatusOK, struct {
		apiWriteOK
		Results []apiCloudArchiveBatchItem `json:"results"`
	}{apiWriteOK{OK: true}, results})
}
