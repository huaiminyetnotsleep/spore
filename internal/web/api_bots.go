package web

// 机器人管理 API：GET /api/v1/bots、POST /api/v1/bots、
// POST /api/v1/bots/{id}/delete。
// 列表合并 env（BOT_TOKEN/BOT_TOKENS，只读）与 bots.json（可增删）来源，
// 并按 bot id 合并运行时身份（getMe 快照 + 长轮询在线状态 + MTProto 会话）。
// 数据范围红线：token 只进不出——请求体接收后即落 0600 文件，任何响应、
// 日志与审计不回显 token；身份一律以脱敏字段表达。

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/huaiminyetnotsleep/spore/internal/botlist"
	"github.com/huaiminyetnotsleep/spore/internal/config"
)

// apiBotRow 是机器人列表条目。
type apiBotRow struct {
	BotID          int64  `json:"bot_id"` // 0 = 尚未接入（等待重启或 getMe 未回填）
	Username       string `json:"username,omitempty"`
	Name           string `json:"name,omitempty"`
	Primary        bool   `json:"primary"` // 主 bot（env 首项）
	Online         bool   `json:"online"`  // Bot API 长轮询在线
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

	appendRow := func(token, source string, primary bool) apiBotRow {
		id := botlist.BotID(token)
		row := apiBotRow{Primary: primary, Source: source}
		if ident, ok := identities[id]; ok {
			row.BotID = ident.ID
			row.Username = ident.Username
			row.Name = ident.Name
			row.Online = ident.Online
		} else {
			// 进程未接入该 token（等待重启）：id 由 token 前缀已知，
			// 标记待重启；不回显 token 本身
			row.BotID = id
			row.RestartPending = true
		}
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
