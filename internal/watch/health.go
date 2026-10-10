// 监听源探活：监听源把受理 bot 移出/封禁（或源整体被封禁）后，bot 只是
// 收不到新 channel_post，没有任何主动错误信号——监听静默失效，唯一被动
// 线索是管理端"最近预热时间"停更。本文件提供周期探活：逐个对生效源调
// Bot API GetChat，连续失败达到阈值判定源不可用，集合非空上报管理员事件
// （watch.source_unavailable），全部恢复可达后自动解决事件。
package watch

import (
	"context"
	"strconv"
	"strings"
	"time"

	tgbot "github.com/go-telegram/bot"

	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// healthCheckInterval 是监听源探活周期（对齐装配层其他周期对账的节奏）；
// 检测延迟 = 周期 × 防抖阈值，对"源把 bot 踢出"这类无副作用失效足够。
const healthCheckInterval = 30 * time.Minute

// healthFailThreshold 是判定源不可用所需的连续失败轮数：单次网络抖动或
// Bot API 瞬时故障不告警。
const healthFailThreshold = 2

// Events 是监听源探活的事件上报出口（*notify.Hub 结构性满足；nil 表示
// 未装配，跳过上报）。定义在包内以保持依赖边界——watch 不直接依赖 notify。
type Events interface {
	// WatchSourcesUnavailable 上报当前不可用源（titles 为展示名列表）。
	// 集合持续非空期间重复上报按事件 key 合并，推送展示最新列表。
	WatchSourcesUnavailable(ctx context.Context, titles []string)
	// WatchSourcesRecovered 在全部源恢复可达后自动解决对应事件
	//（事件从未发生时为静默 no-op）。
	WatchSourcesRecovered(ctx context.Context)
}

// SetEvents 注入探活事件出口（装配层在 RunHealthCheck 之前调用；探活只在
// 该 goroutine 内读写 events，无需加锁）。nil 表示不装配。
func (s *Service) SetEvents(events Events) {
	s.healthMu.Lock()
	defer s.healthMu.Unlock()
	s.events = events
}

// RunHealthCheck 启动周期探活（阻塞直至 ctx 取消；装配层以独立 goroutine
// 运行，生命周期与 MTProto ready 会话一致）。
func (s *Service) RunHealthCheck(ctx context.Context) {
	ticker := time.NewTicker(healthCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.CheckSources(ctx)
		}
	}
}

// CheckSources 探测全部生效源一轮：GetChat 成功即健康（清零失败计数并
// 移出不可用集合）；连续 healthFailThreshold 轮失败判定不可用。集合由空
// 变非空时上报事件、恢复为空时自动解决——事件表示"有监听源不可用"的
// 整体状态，payload 携带最新不可用源列表。Bot 未就绪时整轮跳过（保留
// 既有判定）；源被移除/停用后同步移出集合，避免已消失的源卡住事件。
func (s *Service) CheckSources(ctx context.Context) {
	sources, err := s.st.ListWatchSources(ctx)
	if err != nil {
		s.log.Warn("监听源探活读取源列表失败（下轮重试）", "error", err.Error())
		return
	}
	clients := s.currentBotClients()
	if len(clients) == 0 {
		return
	}

	active := make(map[int64]store.WatchSource, len(sources))
	for _, src := range sources {
		if src.Status == store.WatchApproved && src.Enabled {
			active[src.ChannelID] = src
		}
	}

	s.healthMu.Lock()
	hadUnavailable := len(s.unavailable) > 0
	for id := range s.unavailable {
		if _, ok := active[id]; !ok {
			delete(s.unavailable, id)
			delete(s.healthFails, id)
		}
	}
	events := s.events
	s.healthMu.Unlock()

	for _, src := range active {
		if ctx.Err() != nil {
			return
		}
		if err := s.probeSource(ctx, clients, src); err != nil {
			s.recordSourceFailure(src)
			continue
		}
		s.recordSourceSuccess(src.ChannelID)
	}

	s.healthMu.Lock()
	hasUnavailable := len(s.unavailable) > 0
	var titles []string
	if hasUnavailable {
		titles = s.unavailableTitlesLocked(active)
	}
	s.healthMu.Unlock()

	switch {
	case hasUnavailable && events != nil:
		events.WatchSourcesUnavailable(ctx, titles)
	case !hasUnavailable && hadUnavailable && events != nil:
		events.WatchSourcesRecovered(ctx)
	}
}

// probeSource 对单个源做一次 GetChat 探测：bot 被移出/源被封禁时该调用
// 以明确错误失败。受理 bot 优先（它才是应该在源里的 bot），Web 添加的
// 源（BotID=0）或受理 bot 已下线时用主 bot。
func (s *Service) probeSource(ctx context.Context, clients []botClient, src store.WatchSource) error {
	client := clients[0]
	for _, c := range clients {
		if c.ID() == src.BotID {
			client = c
			break
		}
	}
	pctx, cancel := context.WithTimeout(ctx, verifyTimeout)
	defer cancel()
	_, err := client.GetChat(pctx, &tgbot.GetChatParams{ChatID: src.ChannelID})
	return err
}

// recordSourceFailure 累计一轮探测失败；达到阈值加入不可用集合。
func (s *Service) recordSourceFailure(src store.WatchSource) {
	s.healthMu.Lock()
	defer s.healthMu.Unlock()
	s.healthFails[src.ChannelID]++
	if s.healthFails[src.ChannelID] >= healthFailThreshold {
		s.unavailable[src.ChannelID] = true
	}
}

// recordSourceSuccess 清零失败计数并移出不可用集合（健康即恢复）。
func (s *Service) recordSourceSuccess(channelID int64) {
	s.healthMu.Lock()
	defer s.healthMu.Unlock()
	delete(s.healthFails, channelID)
	delete(s.unavailable, channelID)
}

// unavailableTitlesLocked 汇总当前不可用源的展示名（调用方须已持
// healthMu）；标题优先，回退 @用户名，再回退数字 ID。
func (s *Service) unavailableTitlesLocked(active map[int64]store.WatchSource) []string {
	titles := make([]string, 0, len(s.unavailable))
	for id := range s.unavailable {
		titles = append(titles, sourceDisplayName(id, active[id]))
	}
	return titles
}

// sourceDisplayName 监听源的展示名（事件推送用）：标题优先，回退
// @用户名，最后回退 -100 数字 ID（私有源无标题快照时的兜底）。
func sourceDisplayName(channelID int64, src store.WatchSource) string {
	switch {
	case strings.TrimSpace(src.Title) != "":
		return strings.TrimSpace(src.Title)
	case strings.TrimSpace(src.Username) != "":
		return "@" + strings.TrimSpace(src.Username)
	default:
		return strconv.FormatInt(channelID, 10)
	}
}
