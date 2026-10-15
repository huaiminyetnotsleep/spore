// plan.go — 监听源投递计划：按源实时解析缓存频道之外的额外转发目标。
//
// 快路径（listener）与受保护内容回退路径（queue DumpMirror）共用本解析，
// 保证专属目标、全局镜像、循环过滤与去重两条链路一致：
//
//   - 专属目标（user_binding）：源申请人选择的一条频道绑定，投递时复查
//     active/归属/路由（绑定被软解绑、物理删除、接管或换绑后即停止专属
//     投递，不回退全局）；按 watch_sources.bot_id 精确路由受理 Bot。
//   - 全局镜像（global_mirror）：仅当源行 global_mirror_enabled（管理员
//     逐源开关；管理员源默认开保持历史行为，用户源默认关）时加入全局
//     watch_forward_channels。
//
// 循环不变量（运行期兜底）：任何监听源永远不能向任何监听源转发——解析
// 时逐目标复查 watch_sources，命中即丢弃并记结构化日志，防御配置期校验
// 之外的中间态、并发操作与历史脏数据。目标按数字 ID 去重，专属目标优先。
package watch

import (
	"context"
	"errors"
	"strconv"

	"github.com/huaiminyetnotsleep/spore/internal/store"
	"github.com/huaiminyetnotsleep/spore/internal/syscfg"
)

// ForwardTargetKind 是额外转发目标的来源类型（日志/错误上下文与事件归属）。
type ForwardTargetKind string

const (
	ForwardTargetUser   ForwardTargetKind = "user_binding"  // 源申请人选择的频道绑定
	ForwardTargetGlobal ForwardTargetKind = "global_mirror" // 全局监听转发频道镜像
)

// ForwardTarget 是一个缓存频道之外的额外转发目标。
type ForwardTarget struct {
	ChannelID int64             `json:"channel_id"`
	Title     string            `json:"title"`
	Kind      ForwardTargetKind `json:"kind"`
	// BotID 是投递该目标必须使用的精确 Bot（专属目标 = 源的受理 Bot，
	// 绝不回退主 Bot；全局镜像 = 0，沿用主 Bot 既有语义）。
	BotID int64 `json:"bot_id"`
}

// PlanResolver 是按源解析投递计划的函数签名（cmd/bot 装配把
// Service.ForwardTargets 注入 listener 与回退镜像；两路径共用同一实现）。
type PlanResolver = func(ctx context.Context, src store.WatchSource, cacheChannels []int64) []ForwardTarget

// ForwardTargets 解析源的额外转发目标：有效专属目标（若有）+ 条件全局镜像，
// 已做运行时循环过滤与缓存频道/目标间去重。缓存频道本身仍由调用方按既有
// 逻辑预热（本方法只负责缓存之外的增量）。任何查询失败只影响对应目标
// （记日志跳过），不拖垮整份计划。
func (s *Service) ForwardTargets(ctx context.Context, src store.WatchSource, cacheChannels []int64) []ForwardTarget {
	seen := make(map[int64]bool, len(cacheChannels)+2)
	for _, c := range cacheChannels {
		seen[c] = true
	}
	out := make([]ForwardTarget, 0, 4)

	// 专属目标：运行时复查绑定有效性（active + 归属 + 路由）。失效只停止
	// 专属副本，缓存预热与全局镜像（若启用）不受影响。
	if src.ForwardBindingChannelID != 0 {
		if t, ok := s.validUserTarget(ctx, src); ok {
			if !seen[t.ChannelID] && s.notWatchSource(ctx, t.ChannelID, src.ChannelID) {
				seen[t.ChannelID] = true
				out = append(out, t)
			}
		}
	}
	// 全局镜像：仅源行开关启用时加入（读实时配置，逐源开关即时生效）。
	if src.GlobalMirrorEnabled {
		for _, f := range syscfg.LoadWatchForwardChannels(ctx, s.st) {
			if seen[f.ChannelID] || !s.notWatchSource(ctx, f.ChannelID, src.ChannelID) {
				continue
			}
			seen[f.ChannelID] = true
			out = append(out, ForwardTarget{
				ChannelID: f.ChannelID, Title: f.Title,
				Kind: ForwardTargetGlobal, BotID: 0,
			})
		}
	}
	return out
}

// validUserTarget 复查专属目标绑定并构造目标；任何不满足（不存在/软解绑/
// 归属变更/路由不匹配/源无受理 Bot）返回 false 并记原因日志（判定与
// userBindingStatus 同源，投递/展示口径一致）。
func (s *Service) validUserTarget(ctx context.Context, src store.WatchSource) (ForwardTarget, bool) {
	b, reason, ok := s.userBindingStatus(ctx, src)
	if !ok {
		if src.ForwardBindingChannelID != 0 {
			s.log.Info("监听源专属目标失效，跳过专属投递（仅停止专属副本）",
				"source_channel_id", src.ChannelID,
				"target_channel_id", src.ForwardBindingChannelID, "reason", reason)
		}
		return ForwardTarget{}, false
	}
	title := b.Title
	if title == "" {
		title = strconv.FormatInt(b.ChannelID, 10)
	}
	return ForwardTarget{
		ChannelID: b.ChannelID, Title: title,
		Kind: ForwardTargetUser, BotID: src.BotID,
	}, true
}

// notWatchSource 运行期循环兜底：目标是任何监听源（含源自身）即不可投递。
// 查询失败按可疑处理（宁可少投不可成环）。
func (s *Service) notWatchSource(ctx context.Context, targetID, sourceID int64) bool {
	if targetID == sourceID {
		s.log.Warn("循环转发防护：转发目标与源相同，已运行时过滤",
			"source_channel_id", sourceID, "target_channel_id", targetID)
		return false
	}
	if _, err := s.st.GetWatchSource(ctx, targetID); err == nil {
		s.log.Warn("循环转发防护：转发目标是监听源，已运行时过滤",
			"source_channel_id", sourceID, "target_channel_id", targetID)
		return false
	} else if !errors.Is(err, store.ErrNotFound) {
		// 存储故障：无法确认目标安全，保守跳过
		s.log.Warn("循环转发防护：目标监听源查询失败，保守跳过",
			"source_channel_id", sourceID, "target_channel_id", targetID,
			"error", err.Error())
		return false
	}
	return true
}
