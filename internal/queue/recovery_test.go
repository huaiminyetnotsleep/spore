package queue

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/delivery"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

type recoveryTextSender struct {
	delivery.Sender
	targets []int64
	texts   []string
	failAt  int
}

func (s *recoveryTextSender) SendMessage(ctx context.Context, target int64, text string) (int, error) {
	s.targets = append(s.targets, target)
	s.texts = append(s.texts, text)
	if s.failAt > 0 && len(s.targets) == s.failAt {
		return 700, context.DeadlineExceeded
	}
	return 100 + len(s.targets), nil
}
func TestRecoverySourceOnlyKnownMembersAndExplicitTarget(t *testing.T) {
	f := &fakeFetcher{msgs: []*tg.Message{{ID: 10, Message: "one", GroupedID: 5}, {ID: 11, Message: "two", GroupedID: 5}}}
	fallback := &fakeSender{}
	exact := &recoveryTextSender{}
	d := Deps{Fetcher: f, Sender: fallback, SenderFor: func(Job) delivery.Sender { t.Fatal("normal router called"); return nil }}
	result, err := RecoverySource(context.Background(), d, store.RecoveryItem{ChannelKey: "news", MessageID: 10, MemberIDs: []int{10}}, -100999, exact)
	if err != nil || !result.Began || len(result.SentIDs) != 1 || len(exact.targets) != 1 || exact.targets[0] != -100999 {
		t.Fatalf("direct send %+v targets %v err %v", result, exact.targets, err)
	}
	if len(fallback.sent)+len(fallback.copyCalls)+len(fallback.edits)+len(fallback.deleted) != 0 {
		t.Fatal("normal pipeline side effects")
	}
}
func TestRecoverySourceMissingKnownMemberBeforeSideEffects(t *testing.T) {
	f := &fakeFetcher{msgs: []*tg.Message{{ID: 10, Message: "one"}}}
	exact := &recoveryTextSender{}
	r, err := RecoverySource(context.Background(), Deps{Fetcher: f}, store.RecoveryItem{ChannelKey: "news", MessageID: 10, MemberIDs: []int{10, 11}}, -100999, exact)
	if apperr.From(err).Code != apperr.CodeMessageNotFound || r.Began || len(exact.targets) != 0 {
		t.Fatalf("sent incomplete group %+v %v", r, err)
	}
}
func TestRecoverySourcePartialIDsPreserved(t *testing.T) {
	f := &fakeFetcher{msgs: []*tg.Message{{ID: 10, Message: "one"}, {ID: 11, Message: "two"}}}
	exact := &recoveryTextSender{failAt: 2}
	r, err := RecoverySource(context.Background(), Deps{Fetcher: f}, store.RecoveryItem{ChannelKey: "news", MessageID: 10, MemberIDs: []int{10, 11}}, -100999, exact)
	if !errors.Is(err, context.DeadlineExceeded) || !r.Began || len(r.SentIDs) != 2 || r.SentIDs[1] != 700 {
		t.Fatalf("lost partial IDs %+v %v", r, err)
	}
}
func TestRecoveryAlbumCountMismatchReturnsKnownIDs(t *testing.T) {
	r := RecoveryResult{}
	s := &recoverySender{Sender: recoveryAlbumSender{}, result: &r}
	ids, err := s.SendAlbum(context.Background(), -1009, []delivery.AlbumEntry{{}, {}})
	if err == nil || len(ids) != 1 || len(r.SentIDs) != 1 || !r.Began {
		t.Fatalf("mismatch %+v ids %v err %v", r, ids, err)
	}
}

type recoveryAlbumSender struct{ delivery.Sender }

func (recoveryAlbumSender) SendAlbum(context.Context, int64, []delivery.AlbumEntry) ([]int, error) {
	return []int{300}, nil
}
