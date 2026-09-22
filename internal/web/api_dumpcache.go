package web

// 缓存频道迁移工具 API：POST /api/v1/dumpcache/migrate（发起迁移）与
// GET /api/v1/dumpcache/migrate（进度查询）。业务核心在 internal/dumpcache
//（migrate.go）：旧缓存频道可读时整批复制副本免重新提取；源频道不可读时
// 中止并提示由复用自愈重建。迁移后台执行、幂等可续（重启续跑）。

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/huaiminyetnotsleep/spore/internal/dumpcache"
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

// handleAPIDumpCacheMigrateGet 返回迁移进度与建议源频道（legacy 键存在、
// 或存在 dump_channel_id=0 存量条目时给出）。
func (s *Server) handleAPIDumpCacheMigrateGet(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.dumpcache.migrate.get"
	if !s.apiRequireDumpCache(w, r, op) {
		return
	}
	channel, _ := s.dumpCache.Channel()
	resp := map[string]any{
		"configured":   channel != 0,
		"channel_id":   channel,
		"progress":     s.dumpCache.MigrateProgress(),
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
// 已在进行中）。发起与完成各写一条审计。
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
		s.apiBadRequest(w, r, op, err.Error())
		return
	}
	s.audit(r.Context(), "dumpcache.migrate", "dump_entries",
		map[string]any{"from_channel_id": in.FromChannelID, "action": "start"})
	writeAPIJSON(w, http.StatusOK, struct {
		apiWriteOK
		Progress dumpcache.MigrateProgress `json:"progress"`
	}{apiWriteOK{OK: true}, s.dumpCache.MigrateProgress()})
}
