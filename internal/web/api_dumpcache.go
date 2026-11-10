package web

// 缓存频道迁移工具 API：POST /api/v1/dumpcache/migrate（发起迁移）与
// GET /api/v1/dumpcache/migrate（进度查询，兼容旧形状）；v29 起新增运行
// 查询与控制端点（runs / runs/{id} / runs/{id}/items / pause / resume /
// cancel）。业务核心在 internal/dumpcache（migrate.go）：旧缓存频道可读时
// 整批复制副本免重新提取；源频道不可读时中止并提示由复用自愈重建。迁移
// 后台执行、幂等可续（重启续跑）。运行/明细持久化在 dump_migrate_runs/
// items（store 层状态机）：控制端点即写 DB 状态，执行循环轮询感知——
// 跨 MTProto 生命周期安全；暂停/取消的受控状态转换由 store 保证。

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// apiRequireDumpCache 是迁移端点的可选依赖守卫（MTProto 未就绪时持有器
// 为空，返回受控不可用而非 500）。
func (s *Server) apiRequireDumpCache(w http.ResponseWriter, r *http.Request, op string) bool {
	if s.dumpCache != nil {
		return true
	}
	s.log.Warn("API 缓存迁移服务未接入", "op", op, "path", r.URL.Path)
	writeAPIError(w, http.StatusServiceUnavailable, apiCodeUnavailable,
		apiUserMessage(apiCodeUnavailable))
	return false
}

// settingKeyDumpChannelLegacy 是"升级前存量条目所属频道"的 settings 键
// （v24 不回填，首次查询时按当时生效频道记录一次，供前端默认填充源频道）。
const settingKeyDumpChannelLegacy = "dump_channel_legacy_id"

// dumpMigrateProgressView 是 GET /api/v1/dumpcache/migrate 的 progress
// 字段形状：兼容旧 MigrateProgress（running/from/to/total/done/failed/
// skipped/started_at/finished_at/last_error），新增 paused 与 run_id。
// 由最新一次持久化运行 + 明细聚合合成；从未发起时为零视图。
type dumpMigrateProgressView struct {
	Running    bool    `json:"running"`
	Paused     bool    `json:"paused"`
	RunID      int64   `json:"run_id"`
	From       int64   `json:"from"`
	To         []int64 `json:"to"`
	Total      int64   `json:"total"`
	Done       int64   `json:"done"`
	Failed     int64   `json:"failed"`
	Skipped    int64   `json:"skipped"`
	StartedAt  int64   `json:"started_at"`
	FinishedAt int64   `json:"finished_at,omitempty"`
	LastError  string  `json:"last_error,omitempty"`
}

// dumpMigrateProgressOf 合成最新运行的进度视图；无运行返回零视图。
func (s *Server) dumpMigrateProgressOf(r *http.Request) dumpMigrateProgressView {
	run, err := s.st.LatestDumpMigrateRun(r.Context())
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			// 轮询端点不因瞬时 DB 故障 500：记日志并回零视图（下次轮询自愈）
			s.log.Warn("读取迁移进度失败", "error", err.Error())
		}
		return dumpMigrateProgressView{To: []int64{}}
	}
	return dumpMigrateProgressView{
		Running: run.Status == store.DumpMigrateRunning,
		Paused:  run.Status == store.DumpMigratePaused,
		RunID:   run.ID, From: run.FromChannelID, To: run.To, Total: run.Total,
		Done: run.Done, Failed: run.Failed, Skipped: run.Skipped,
		StartedAt: run.CreatedAt, FinishedAt: run.FinishedAt, LastError: run.LastError,
	}
}

// handleAPIDumpCacheMigrateGet 返回迁移进度与建议源频道（legacy 键存在、
// 或存在 dump_channel_id=0 存量条目时给出）。channel_id 为首个启用缓存
// 频道（多缓存频道部署下 configured 表示存在任一启用频道）。
func (s *Server) handleAPIDumpCacheMigrateGet(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.dumpcache.migrate.get"
	if !s.apiRequireDumpCache(w, r, op) {
		return
	}
	channels, _ := s.dumpCache.Channels()
	channel := int64(0)
	if len(channels) > 0 {
		channel = channels[0]
	}
	resp := map[string]any{
		"configured":   channel != 0,
		"channel_id":   channel,
		"progress":     s.dumpMigrateProgressOf(r),
		"suggest_from": s.suggestMigrateSource(r, channel),
	}
	writeAPIJSON(w, http.StatusOK, resp)
}

// suggestMigrateSource 解析建议源频道：legacy 键已记录直接用；否则若存在
// dump_channel_id=0 的存量条目（v24 升级前写入），把当前生效频道记录为
// legacy 并返回（只记一次；之后 Web 端改频道不再改写该键）。无法判定
// 返回 0（前端让管理员手填）。
func (s *Server) suggestMigrateSource(r *http.Request, current int64) int64 {
	ctx := r.Context()
	if raw, ok, err := s.st.GetSetting(ctx, settingKeyDumpChannelLegacy); err == nil && ok {
		var v int64
		if json.Unmarshal([]byte(raw), &v) == nil && v != 0 {
			return v
		}
		return 0
	}
	legacyCount, err := s.st.CountDumpEntriesByChannel(ctx, 0)
	if err != nil || legacyCount == 0 || current == 0 {
		return 0
	}
	_ = s.st.SetSetting(ctx, settingKeyDumpChannelLegacy, strconv.FormatInt(current, 10))
	return current
}

// handleAPIDumpCacheMigrateStart 发起迁移：body {"from_channel_id": -100…}。
// 拒绝条件由 dumpcache.StartMigrate 受控返回（未配置缓存频道/源即目标/
// 已在进行中）；创建审计由 store 层与运行行同事务写入。
func (s *Server) handleAPIDumpCacheMigrateStart(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.dumpcache.migrate.start"
	if !s.apiRequireDumpCache(w, r, op) {
		return
	}
	var in struct {
		FromChannelID int64 `json:"from_channel_id"`
	}
	if !s.apiReadJSON(w, r, op, &in) {
		return
	}
	if in.FromChannelID == 0 {
		s.apiBadRequest(w, r, op, "请提供源缓存频道 ID（形如 -100…）。")
		return
	}
	if err := s.dumpCache.StartMigrate(r.Context(), in.FromChannelID); err != nil {
		// 拒绝条件均为受控中文文案（未配置频道/源即目标/已在进行中），
		// 保持旧契约：400 + 文案直出
		s.apiBadRequest(w, r, op, err.Error())
		return
	}
	// 兼容旧响应：200 + {ok, progress}（旧端点即此形状，不随新端点改 201）
	writeAPIJSON(w, http.StatusOK, struct {
		apiWriteOK
		Progress dumpMigrateProgressView `json:"progress"`
	}{apiWriteOK{OK: true}, s.dumpMigrateProgressOf(r)})
}

// handleAPIDumpCacheMigrateRuns 分页返回迁移运行历史（新到旧）。
func (s *Server) handleAPIDumpCacheMigrateRuns(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.dumpcache.migrate.runs"
	if !s.apiRequireDumpCache(w, r, op) {
		return
	}
	p, err := parseAPIPageParams(r)
	if err != nil {
		s.apiBadRequest(w, r, op, "分页参数无效。")
		return
	}
	runs, total, err := s.st.ListDumpMigrateRuns(r.Context(), p.Page, p.PageSize)
	if err != nil {
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	writeAPIList(w, newAPIListEnvelope(runs, p, total))
}

// handleAPIDumpCacheMigrateRun 返回单次运行详情（含明细聚合计数）。
func (s *Server) handleAPIDumpCacheMigrateRun(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.dumpcache.migrate.run"
	if !s.apiRequireDumpCache(w, r, op) {
		return
	}
	id, ok := s.apiPathID(w, r, op)
	if !ok {
		return
	}
	run, err := s.st.GetDumpMigrateRun(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeAPIError(w, http.StatusNotFound, apiCodeNotFound, apiUserMessage(apiCodeNotFound))
		return
	}
	if err != nil {
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	writeAPISingle(w, run)
}

// handleAPIDumpCacheMigrateItems 分页返回单次运行的明细，status 可选
// （migrated/skipped/failed 白名单）。
func (s *Server) handleAPIDumpCacheMigrateItems(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.dumpcache.migrate.items"
	if !s.apiRequireDumpCache(w, r, op) {
		return
	}
	id, ok := s.apiPathID(w, r, op)
	if !ok {
		return
	}
	p, err := parseAPIPageParams(r)
	if err != nil {
		s.apiBadRequest(w, r, op, "分页参数无效。")
		return
	}
	status := r.URL.Query().Get("status")
	switch status {
	case "", "migrated", "skipped", "failed":
	default:
		s.apiBadRequest(w, r, op, "迁移明细状态无效。")
		return
	}
	items, total, err := s.st.ListDumpMigrateItems(r.Context(), id, status, p.Page, p.PageSize)
	if err != nil {
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	writeAPIList(w, newAPIListEnvelope(items, p, total))
}

// dumpMigrateControlReq 是控制动作对当前运行状态的要求与目标态。
var dumpMigrateControls = map[string]struct {
	want   []string // 允许的当前状态
	target string   // 转换目标状态
	label  string   // 中文动作名（错误提示/审计用）
}{
	"pause":  {[]string{store.DumpMigrateRunning}, store.DumpMigratePaused, "暂停"},
	"resume": {[]string{store.DumpMigratePaused}, store.DumpMigrateRunning, "继续"},
	"cancel": {[]string{store.DumpMigrateRunning, store.DumpMigratePaused}, store.DumpMigrateCancelled, "取消"},
}

// handleAPIDumpCacheMigrateControl 暂停/继续/取消当前活跃运行（无 id：
// 同时至多一个活跃运行，避免陈旧 id 误操作）。状态即写 DB，执行循环
// 批间感知；各动作写一条审计。前置状态不符（无活跃运行、终态后再控制）
// 返回受控 409（CONFLICT，与 MTProto 重连/重启等状态冲突端点同约定）。
func (s *Server) handleAPIDumpCacheMigrateControl(action string) sessionHandler {
	return func(w http.ResponseWriter, r *http.Request, _ session) {
		op := "api.dumpcache.migrate." + action
		if !s.apiRequireDumpCache(w, r, op) {
			return
		}
		ctl, ok := dumpMigrateControls[action]
		if !ok {
			s.apiBadRequest(w, r, op, "迁移控制动作无效。")
			return
		}
		ctx := r.Context()
		run, err := s.st.LatestDumpMigrateRun(ctx)
		if errors.Is(err, store.ErrNotFound) {
			writeAPIError(w, http.StatusConflict, apiCodeConflict, "当前没有可"+ctl.label+"的迁移运行。")
			return
		}
		if err != nil {
			s.writeAPIAppErr(w, r, op, err)
			return
		}
		allowed := false
		for _, st := range ctl.want {
			if run.Status == st {
				allowed = true
				break
			}
		}
		if !allowed {
			writeAPIError(w, http.StatusConflict, apiCodeConflict, "当前迁移状态不允许"+ctl.label+"（状态："+run.Status+"）。")
			return
		}
		lastErr := ""
		if ctl.target == store.DumpMigrateCancelled {
			lastErr = "管理员取消"
		}
		if err := s.st.SetDumpMigrateRunStatus(ctx, run.ID, ctl.target, lastErr); err != nil {
			s.writeAPIAppErr(w, r, op, err)
			return
		}
		s.audit(ctx, "dumpcache.migrate", "dump_migrate_run:"+strconv.FormatInt(run.ID, 10),
			map[string]any{"action": action, "from_channel_id": run.FromChannelID})
		run, err = s.st.GetDumpMigrateRun(ctx, run.ID)
		if err != nil {
			s.writeAPIAppErr(w, r, op, err)
			return
		}
		writeAPISingle(w, run)
	}
}

// registerAPIDumpCacheRoutes 注册迁移工具全部端点。基础路径 GET 有真实
// 语义（进度查询），不走 mountAPIWrite（其会给同路径挂 GET 405）。
func (s *Server) registerAPIDumpCacheRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/v1/dumpcache/migrate", s.apiAuth(s.handleAPIDumpCacheMigrateGet))
	mux.Handle("POST /api/v1/dumpcache/migrate", s.apiAuth(s.apiCSRF(s.handleAPIDumpCacheMigrateStart)))
	for _, action := range []string{"pause", "resume", "cancel"} {
		s.mountAPIWrite(mux, "/api/v1/dumpcache/migrate/"+action, s.handleAPIDumpCacheMigrateControl(action))
	}
	mux.Handle("GET /api/v1/dumpcache/migrate/runs", s.apiAuth(s.handleAPIDumpCacheMigrateRuns))
	mux.Handle("GET /api/v1/dumpcache/migrate/runs/{id}", s.apiAuth(s.handleAPIDumpCacheMigrateRun))
	mux.Handle("GET /api/v1/dumpcache/migrate/runs/{id}/items", s.apiAuth(s.handleAPIDumpCacheMigrateItems))
}
