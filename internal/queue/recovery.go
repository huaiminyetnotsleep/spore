package queue

import (
	"context"
	"io"
	"log/slog"
	"sort"
	"strconv"
	"time"

	"github.com/gotd/td/tg"
	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/delivery"
	"github.com/huaiminyetnotsleep/spore/internal/message"
	"github.com/huaiminyetnotsleep/spore/internal/store"
	"github.com/huaiminyetnotsleep/spore/internal/tmeurl"
)

// RecoveryResult 记录任何已知输出；Began 表示已进入发送边界。
type RecoveryResult struct {
	SentIDs []int
	Began   bool
}

// RecoverySource 复用转换发送，仅发指定目标，不执行Process副作用。
func RecoverySource(ctx context.Context, d Deps, item store.RecoveryItem, target int64, sender delivery.Sender) (RecoveryResult, error) {
	result := RecoveryResult{SentIDs: []int{}}
	if sender == nil || d.Fetcher == nil {
		return result, apperr.New(apperr.CodeNetworkError, "恢复发送运行时未就绪")
	}
	ref := tmeurl.SourceRef{Kind: tmeurl.PeerUsername, Username: item.ChannelKey, MessageID: item.MessageID}
	if id, e := strconv.ParseInt(item.ChannelKey, 10, 64); e == nil {
		ref.Kind = tmeurl.PeerChannelID
		ref.ChannelID = id
		ref.Username = ""
	}
	// Fetch may expand albums. Keep only confirmed historical members; never infer
	// the full source album from a legacy singleton or omit a known missing member.
	want := map[int]bool{}
	for _, id := range item.MemberIDs {
		want[id] = true
	}
	got := map[int]*tg.Message{}
	fctx, cancel := context.WithTimeout(ctx, processTimeout)
	defer cancel()
	for _, id := range item.MemberIDs {
		if got[id] != nil {
			continue
		}
		r := ref
		r.MessageID = id
		msgs, err := d.Fetcher.Fetch(fctx, r)
		if err != nil {
			return result, err
		}
		for _, m := range msgs {
			if m != nil && want[m.ID] {
				got[m.ID] = m
			}
		}
	}
	msgs := make([]*tg.Message, 0, len(want))
	for _, id := range item.MemberIDs {
		if got[id] == nil {
			return result, apperr.New(apperr.CodeMessageNotFound, "已知相册成员缺失")
		}
		msgs = append(msgs, got[id])
	}
	sort.Slice(msgs, func(i, j int) bool { return msgs[i].ID < msgs[j].ID })
	if len(message.Convert(msgs)) != len(item.MemberIDs) {
		return result, apperr.New(apperr.CodeServiceMessage, "已知成员包含不可转换消息")
	}
	if d.Transfer != nil {
		d.Media.DownloadThreads = d.Transfer.Snapshot().DownloadThreads
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	tracker := &recoverySender{Sender: sender, result: &result}
	// Only media configuration, reference refresh, and logging survive. No Store,
	// Progress, Copier, Dump, Cloud, Events, Channels, or fallback SenderFor.
	clean := Deps{Fetcher: d.Fetcher, Sender: tracker, Media: d.Media, Log: d.Log}
	j := NewJob(0, target, ref, 0, 0)
	j.ID = strconv.FormatInt(time.Now().UnixNano(), 10)
	meta, err := sendConverted(ctx, clean, j, target, msgs, nil)
	// tracker retains IDs even when an adapter returns partial IDs with an error.
	if len(result.SentIDs) == 0 {
		result.SentIDs = append(result.SentIDs, meta.SentIDs...)
	}
	return result, err
}

type recoverySender struct {
	delivery.Sender
	result *RecoveryResult
}

func (s *recoverySender) SendMessage(ctx context.Context, id int64, text string) (int, error) {
	s.result.Began = true
	n, e := s.Sender.SendMessage(ctx, id, text)
	if n > 0 {
		s.result.SentIDs = append(s.result.SentIDs, n)
	}
	if e == nil && n <= 0 {
		e = apperr.New(apperr.CodeSendFailed, "发送成功但返回坐标无效")
	}
	return n, e
}
func (s *recoverySender) SendMedia(ctx context.Context, id int64, m message.Media, c message.Caption, r io.Reader) (int, error) {
	s.result.Began = true
	n, e := s.Sender.SendMedia(ctx, id, m, c, r)
	if n > 0 {
		s.result.SentIDs = append(s.result.SentIDs, n)
	}
	if e == nil && n <= 0 {
		e = apperr.New(apperr.CodeSendFailed, "发送成功但返回坐标无效")
	}
	return n, e
}
func (s *recoverySender) SendAlbum(ctx context.Context, id int64, es []delivery.AlbumEntry) ([]int, error) {
	s.result.Began = true
	ns, e := s.Sender.SendAlbum(ctx, id, es)
	for _, n := range ns {
		if n > 0 {
			s.result.SentIDs = append(s.result.SentIDs, n)
		}
	}
	if e == nil && (len(ns) != len(es) || len(s.result.SentIDs) < len(es)) {
		e = apperr.New(apperr.CodeSendFailed, "相册返回坐标数量不一致")
	}
	return ns, e
}
