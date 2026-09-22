package web

// GET /api/v1/overview 总览快照 API：只携带实时快照——服务状态（版本/启动
// 时间/监听地址/GitHub 通道）、接入机器人身份（getMe 快照）、MTProto/Bot API/
// 数据库/临时目录健康、队列与 Worker、用户状态计数与 requests 行状态计数，
// 以及频道加入（join）全时段统计（申请状态计数、在加入/已退出频道、来源分布
// 与数量上限配置）。
// 时间范围类请求指标与图表数据由 GET /api/v1/stats 承接（api_stats.go）。
// DTO 只携带原始值（raw 状态码、Unix 毫秒、字节计数），中文标签与格式化
// 由前端共享 util 处理；错误链路经统一 writeAPIAppErr 映射。

import (
	"net/http"
	"os"

	"github.com/huaiminyetnotsleep/spore/internal/store"
	"github.com/huaiminyetnotsleep/spore/internal/syscfg"
)

// apiQueueView 是内存队列指标；queue 未注入时整个字段省略。
type apiQueueView struct {
	Len int `json:"len"`
	Cap int `json:"cap"`
}

// apiUserTallyView 是用户按状态的计数。
type apiUserTallyView struct {
	Total    int `json:"total"`
	Enabled  int `json:"enabled"`
	Pending  int `json:"pending"`
	Disabled int `json:"disabled"`
	Archived int `json:"archived"`
}

// apiOverviewHealth 是服务健康快照（Bot API 状态随 MTProto 会话启停，
// 文案由前端按 mtproto_state 推导）。
type apiOverviewHealth struct {
	StoreOK          bool   `json:"store_ok"`
	MTProtoState     string `json:"mtproto_state"` // ready|login_pending|offline|unknown（raw）
	MTProtoError     string `json:"mtproto_error,omitempty"`
	BotMTProtoState  string `json:"bot_mtproto_state,omitempty"` // ready|offline；空串表示未接入
	BotMTProtoDCID   int    `json:"bot_mtproto_dc_id,omitempty"`
	DBSizeBytes      int64  `json:"db_size_bytes"`
	DBPath           string `json:"db_path"`
	TempDirBytes     int64  `json:"temp_dir_bytes"`
	TempDir          string `json:"temp_dir"`
	GitHubConfigured bool   `json:"github_configured"`
}

// apiOverviewRequests 是请求行的当前状态计数（全时段快照，无范围筛选）。
type apiOverviewRequests struct {
	QueuedRows     int `json:"queued_rows"`
	ProcessingRows int `json:"processing_rows"`
}

// apiJoinTallyView 是频道加入的全时段快照统计（join 功能关闭时历史数据
// 仍有意义，照常下发）。SourceDist 固定五来源各一行（含 0），Key 为
// joined_via raw 值，中文标签由前端处理。
type apiJoinTallyView struct {
	Pending        int          `json:"pending"`
	Approved       int          `json:"approved"`
	Rejected       int          `json:"rejected"`
	Failed         int          `json:"failed"`
	ActiveJoined   int          `json:"active_joined"` // 当前加入中的频道总数（全部来源合计）
	ExternalActive int          `json:"external_active"`
	LeftTotal      int          `json:"left_total"`
	MaxChannels    int          `json:"max_channels"` // 加入数量上限（syscfg；0 = 不限）
	SourceDist     []apiDistRow `json:"source_dist"`
}

// apiOverviewView 是 GET /api/v1/overview 的只读快照 DTO（独立于 SSR view struct）。
type apiOverviewView struct {
	Version   string            `json:"version"`
	StartedAt int64             `json:"started_at"` // Unix 毫秒
	Addr      string            `json:"addr"`
	Workers   int               `json:"workers"`
	Health    apiOverviewHealth `json:"health"`
	Queue     *apiQueueView     `json:"queue,omitempty"`
	// Bot 是接入的 Bot API 机器人身份（getMe 快照，主 bot）；Bot 未就绪或
	// 身份查询未成功时整体省略，前端显示"未接入"。
	// Bots 是多机器人池全部成员（装配顺序，主 bot 在前）；单 bot 部署长度
	// 为 1。空池（尚无 bot 接入）时省略。
	Bot      *BotIdentity        `json:"bot,omitempty"`
	Bots     []BotIdentityEntry  `json:"bots,omitempty"`
	Requests apiOverviewRequests `json:"requests"`
	Users    apiUserTallyView    `json:"users"`
	Join     apiJoinTallyView    `json:"join"`
}

// handleAPIOverview 返回总览快照。主要计数（行状态/用户/加入统计）失败经
// 统一 apperr 映射输出 JSON 错误，不让整页缺数。
func (s *Server) handleAPIOverview(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.overview"
	ctx := r.Context()

	view := apiOverviewView{
		Version:   s.version,
		StartedAt: s.started.UnixMilli(),
		Addr:      s.cfg.WebAddr,
		Workers:   s.cfg.WorkerCount,
		Health: apiOverviewHealth{
			DBPath:           s.dbPath,
			TempDir:          s.cfg.TempDir,
			GitHubConfigured: s.oauthConfigured(),
		},
	}

	// MTProto / Bot API 状态（raw 交由前端推导中文标签）
	if s.mtp == nil {
		view.Health.MTProtoState = "unknown"
	} else {
		snap := s.mtp.Status()
		view.Health.MTProtoState = snap.State
		view.Health.MTProtoError = snap.LastError
	}

	// Bot MTProto 会话状态与当前主 DC（BotClient 离线时已清空 DC，不会残留旧值；
	// provider 未接入时字段整体省略，前端显示"未接入"）
	if s.botMTP != nil {
		bot := s.botMTP.Status()
		view.Health.BotMTProtoState = bot.State
		view.Health.BotMTProtoDCID = bot.DCID
	}

	// 数据库连通性（一次 settings 读）与文件指标
	if _, _, err := s.st.GetSetting(ctx, "overview_probe"); err != nil {
		s.log.Warn("总览数据库探测失败", "op", op, "error", err.Error())
	} else {
		view.Health.StoreOK = true
	}
	if fi, err := os.Stat(s.dbPath); err == nil {
		view.Health.DBSizeBytes = fi.Size()
	}
	view.Health.TempDirBytes = dirSize(s.cfg.TempDir)

	// 队列指标（内存队列 + requests 行状态）
	if s.queue != nil {
		view.Queue = &apiQueueView{Len: s.queue.Len(), Cap: s.queue.Cap()}
	}

	// 接入的 Bot API 机器人身份（Bot 客户端晚于 Web 服务创建，未就绪时省略）
	if s.botIdentity != nil {
		if ident, ok := s.botIdentity.BotIdentity(); ok {
			view.Bot = &ident
		}
		if bots := s.botIdentity.BotIdentities(); len(bots) > 0 {
			// 暂停态存于 settings（运营动作）：尽力而为合并，读取失败不缺页
			pausedSet := LoadPausedBots(ctx, s.st)
			for i := range bots {
				bots[i].Paused = pausedSet[bots[i].ID]
			}
			view.Bots = bots
		}
	}

	queued, processing, err := s.st.CountRequestsByStatus(ctx)
	if err != nil {
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	view.Requests.QueuedRows, view.Requests.ProcessingRows = queued, processing

	// 用户概览
	users, err := s.st.ListUsers(ctx)
	if err != nil {
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	view.Users.Total = len(users)
	for _, u := range users {
		switch u.Status {
		case store.UserEnabled:
			view.Users.Enabled++
		case store.UserPending:
			view.Users.Pending++
		case store.UserDisabled:
			view.Users.Disabled++
		case store.UserArchived:
			view.Users.Archived++
		}
	}

	// 频道加入全时段快照（主计数语义：失败即整页错误）
	reqTally, err := s.st.TallyJoinRequests(ctx)
	if err != nil {
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	chTally, err := s.st.TallyJoinedChannels(ctx)
	if err != nil {
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	joinCfg := syscfg.LoadJoinConfig(ctx, s.st)
	view.Join = apiJoinTallyView{
		Pending:        reqTally.Pending,
		Approved:       reqTally.Approved,
		Rejected:       reqTally.Rejected,
		Failed:         reqTally.Failed,
		ActiveJoined:   chTally.Active(),
		ExternalActive: chTally.ExternalActive,
		LeftTotal:      chTally.Left(),
		MaxChannels:    joinCfg.MaxChannels,
		SourceDist: []apiDistRow{
			{Key: store.JoinedViaCommand, Count: chTally.CommandActive},
			{Key: store.JoinedViaApproved, Count: chTally.ApprovedActive},
			{Key: store.JoinedViaExternal, Count: chTally.ExternalActive},
			{Key: store.JoinedViaWatchSource, Count: chTally.WatchSourceActive},
			{Key: store.JoinedViaBindResolve, Count: chTally.BindResolveActive},
		},
	}

	w.Header().Set("Cache-Control", "no-store")
	writeAPIJSON(w, http.StatusOK, view)
}
