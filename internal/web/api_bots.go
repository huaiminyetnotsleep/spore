package web

// 机器人管理 API：GET /api/v1/bots、POST /api/v1/bots、
// POST /api/v1/bots/{id}/delete。
// 列表合并 env（BOT_TOKEN/BOT_TOKENS，只读）与 bots.json（可增删）来源，
// 并按 bot id 合并运行时身份（getMe 快照 + 长轮询在线状态 + MTProto 会话）。
// 数据范围红线：token 只进不出——请求体接收后即落 0600 文件，任何响应、
// 日志与审计不回显 token；身份一律以脱敏字段表达。

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/huaiminyetnotsleep/spore/internal/botlist"
	"github.com/huaiminyetnotsleep/spore/internal/config"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// apiBotRow 是机器人列表条目。
type apiBotRow struct {
	BotID          int64  `json:"bot_id"` // 0 = 尚未接入（等待重启或 getMe 未回填）
	Username       string `json:"username,omitempty"`
	Name           string `json:"name,omitempty"`
	Primary        bool   `json:"primary"`  // 主 bot（env 首项）
	Online         bool   `json:"online"`   // Bot API 长轮询在线
	Conflict       bool   `json:"conflict"` // 消息拉取冲突（token 被其他服务占用；收不到新消息）
	Paused         bool   `json:"paused"`   // 已暂停（停止接收新消息；在途任务正常完成）
	Disabled       bool   `json:"disabled"` // 停用（token 失效：被封禁或撤销；发送路由跳过）
	MTProtoState   string `json:"mtproto_state,omitempty"`
	Source         string `json:"source"`          // env | file（botlist.Source）
	RestartPending bool   `json:"restart_pending"` // 已配置但当前进程未接入（等待重启）
}

// botsView 是 GET /api/v1/bots 的响应。
type botsView struct {
	Bots      []apiBotRow `json:"bots"`
	MaxBots   int         `json:"max_bots"`
	NeedApply bool        `json:"need_apply"` // 有条目等待重启生效
}

// handleAPIBotsGet 返回机器人列表（env ∪ 文件，合并运行时身份）。
func (s *Server) handleAPIBotsGet(w http.ResponseWriter, r *http.Request, _ session) {
	if !s.apiRequireAccess(w, r, "api.bots.get") {
		return
	}
	if s.botList == nil {
		writeAPIError(w, http.StatusServiceUnavailable, apiCodeUnavailable,
			"机器人列表管理未接入本实例。")
		return
	}
	writeAPISingle(w, s.botsView())
}

// botsView 组装机器人列表视图（查询与变更后的回显共用）。
func (s *Server) botsView() botsView {
	identities := map[int64]BotIdentityEntry{}
	if s.botIdentity != nil {
		for _, e := range s.botIdentity.BotIdentities() {
			identities[e.ID] = e
		}
	}
	mtpStates := map[int64]string{}
	if s.botMTPs != nil {
		for _, e := range s.botMTPs.BotMTProtoEntries() {
			mtpStates[e.BotID] = e.Snapshot.State
		}
	}
	paused := LoadPausedBots(context.Background(), s.st)

	appendRow := func(token, source string, primary bool) apiBotRow {
		id := botlist.BotID(token)
		row := apiBotRow{Primary: primary, Source: source}
		if ident, ok := identities[id]; ok {
			row.BotID = ident.ID
			row.Username = ident.Username
			row.Name = ident.Name
			row.Online = ident.Online
			row.Conflict = ident.Conflict
			row.Disabled = ident.Disabled
		} else {
			// 进程未接入该 token（等待重启）：id 由 token 前缀已知，
			// 标记待重启；不回显 token 本身
			row.BotID = id
			row.RestartPending = true
		}
		row.Paused = paused[row.BotID]
		row.MTProtoState = mtpStates[row.BotID]
		return row
	}

	view := botsView{Bots: []apiBotRow{}, MaxBots: config.MaxBots}
	envCount := len(s.cfg.BotTokens)
	for i, tok := range s.cfg.BotTokens {
		view.Bots = append(view.Bots, appendRow(tok, string(botlist.SourceEnv), i == 0))
	}
	for _, tok := range s.botList.List() {
		view.Bots = append(view.Bots, appendRow(tok, string(botlist.SourceFile), envCount == 0 && len(view.Bots) == 0))
	}
	for _, b := range view.Bots {
		if b.RestartPending {
			view.NeedApply = true
			break
		}
	}
	return view
}

// handleAPIBotsAdd 新增文件来源 bot：token 校验/去重/写盘（0600）在
// botlist.Manager 内完成；变更写审计，重启后生效。
func (s *Server) handleAPIBotsAdd(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.bots.add"
	if !s.apiRequireAccess(w, r, op) {
		return
	}
	if s.botList == nil {
		writeAPIError(w, http.StatusServiceUnavailable, apiCodeUnavailable,
			"机器人列表管理未接入本实例。")
		return
	}
	var in struct {
		Token string `json:"token"`
	}
	if !s.apiReadJSON(w, r, op, &in) {
		return
	}
	// 数量上限按有效列表口径校验（env ∪ 文件），超出直接拒绝
	if len(s.cfg.BotTokens)+len(s.botList.List()) >= config.MaxBots {
		s.apiBadRequest(w, r, op, "机器人数量已达上限")
		return
	}
	token := strings.TrimSpace(in.Token)
	for _, existing := range s.cfg.BotTokens {
		if existing == token {
			s.apiBadRequest(w, r, op, "该机器人已存在（环境变量来源）")
			return
		}
	}
	if err := s.botList.Add(token); err != nil {
		s.apiBadRequest(w, r, op, err.Error())
		return
	}
	// 审计只带 bot id（token 数字前缀），绝不带 token 本身
	s.audit(r.Context(), "bots.add", "bot:"+strconv.FormatInt(botlist.BotID(token), 10),
		map[string]any{"effect": "重启后生效"})
	writeAPIJSON(w, http.StatusOK, struct {
		apiWriteOK
		botsView
		RestartHint string `json:"restart_hint"`
	}{apiWriteOK{OK: true}, s.botsView(), "机器人已添加，重启进程后生效（可在系统运维页受控重启）。"})
}

// handleAPIBotsDelete 移除文件来源 bot（按 bot id 定位）；env 来源的
// 机器人不在此通道（需修改环境变量后重启）。变更写审计，重启后生效。
func (s *Server) handleAPIBotsDelete(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.bots.delete"
	if !s.apiRequireAccess(w, r, op) {
		return
	}
	if s.botList == nil {
		writeAPIError(w, http.StatusServiceUnavailable, apiCodeUnavailable,
			"机器人列表管理未接入本实例。")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		s.apiBadRequest(w, r, op, "bot id 无效")
		return
	}
	var token string
	for _, tok := range s.botList.List() {
		if botlist.BotID(tok) == id {
			token = tok
			break
		}
	}
	if token == "" {
		s.apiBadRequest(w, r, op, "该机器人不在文件配置中（env 来源请在环境变量中移除）")
		return
	}
	if err := s.botList.Remove(token); err != nil {
		s.apiBadRequest(w, r, op, err.Error())
		return
	}
	s.audit(r.Context(), "bots.remove", "bot:"+strconv.FormatInt(id, 10),
		map[string]any{"effect": "重启后生效"})
	writeAPIJSON(w, http.StatusOK, struct {
		apiWriteOK
		botsView
		RestartHint string `json:"restart_hint"`
	}{apiWriteOK{OK: true}, s.botsView(), "机器人已移除，重启进程后生效（可在系统运维页受控重启）。"})
}

// ---- 暂停/恢复（运行时状态） ----

// settingKeyBotsPaused 存放被暂停的 bot ID 集合（JSON 数组，元素为 bot 数字
// ID）。暂停是运营动作不是凭据：入库的只有 bot ID，token 仍在 env 与
// bots.json（数据范围红线不变）。
const settingKeyBotsPaused = "bots_paused"

// LoadPausedBots 读取当前暂停的 bot 集合（装配层构建轮询监督循环与管理端
// 展示共用）；键缺失或非法时返回空集合并忽略错误（尽力而为）。
func LoadPausedBots(ctx context.Context, st *store.Store) map[int64]bool {
	paused := map[int64]bool{}
	if st == nil {
		return paused
	}
	v, ok, err := st.GetSetting(ctx, settingKeyBotsPaused)
	if err != nil || !ok {
		return paused
	}
	var ids []int64
	if json.Unmarshal([]byte(v), &ids) != nil {
		return paused
	}
	for _, id := range ids {
		if id > 0 {
			paused[id] = true
		}
	}
	return paused
}

// SetBotPaused 更新单个 bot 的暂停态（读改写整个集合）。仅存储层错误返回
// error；目标 ID 不在集合中时幂等。
func SetBotPaused(ctx context.Context, st *store.Store, botID int64, paused bool) error {
	set := LoadPausedBots(ctx, st)
	if paused {
		set[botID] = true
	} else {
		delete(set, botID)
	}
	ids := make([]int64, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	buf, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	return st.SetSetting(ctx, settingKeyBotsPaused, string(buf))
}

// handleAPIBotRuntime 暂停/恢复指定 bot（认证 + CSRF）：先落 settings（重启
// 保持），再经运行时控制即时生效（池未就绪时仅持久化，待 MTProto 就绪生命
// 周期应用）。暂停语义：停止接收该 bot 的新消息；已受理任务由原 bot 正常完成。
func (s *Server) handleAPIBotRuntime(paused bool) sessionHandler {
	return func(w http.ResponseWriter, r *http.Request, sess session) {
		op := "api.bots.pause"
		action := "bot.pause"
		effect := "已停止接收该机器人的新消息（在途任务正常完成）；重启后保持暂停"
		if !paused {
			op = "api.bots.resume"
			action = "bot.resume"
			effect = "已恢复接收该机器人的新消息；重启后保持恢复"
		}
		if !s.apiRequireAccess(w, r, op) {
			return
		}
		if s.botList == nil {
			writeAPIError(w, http.StatusServiceUnavailable, apiCodeUnavailable,
				"机器人列表管理未接入本实例。")
			return
		}
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			s.apiBadRequest(w, r, op, "bot id 无效")
			return
		}
		if err := SetBotPaused(r.Context(), s.st, id, paused); err != nil {
			s.writeAPIAppErr(w, r, op, err)
			return
		}
		if s.botRuntime != nil {
			if paused {
				err = s.botRuntime.PauseBot(r.Context(), id)
			} else {
				err = s.botRuntime.ResumeBot(r.Context(), id)
			}
			if err != nil {
				s.writeAPIAppErr(w, r, op, err)
				return
			}
		}
		s.audit(r.Context(), action, "bot:"+strconv.FormatInt(id, 10),
			map[string]any{"effect": effect})
		writeAPIJSON(w, http.StatusOK, struct {
			apiWriteOK
			botsView
			Message string `json:"message"`
		}{apiWriteOK{OK: true}, s.botsView(), effect + "。"})
	}
}
