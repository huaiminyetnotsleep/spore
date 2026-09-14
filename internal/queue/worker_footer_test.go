package queue

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gotd/td/tg"

	"github.com/huaiminyetnotsleep/spore/internal/message"
)

// fakeChannelLinks 是 ChannelLinksProvider 的测试假实现。
type fakeChannelLinks struct {
	links []message.ChannelLink
	err   error
	calls []int64
}

func (f *fakeChannelLinks) PublicChannelLinks(_ context.Context, userID int64) ([]message.ChannelLink, error) {
	f.calls = append(f.calls, userID)
	return f.links, f.err
}

func testLinks() []message.ChannelLink {
	return []message.ChannelLink{
		{Label: "@pub", URL: "https://t.me/pub"},
		{Label: "私有频道", URL: "https://t.me/c/1234567890/1"},
	}
}

// 文本消息：脚注拼在正文（含原消息链接）末尾，Bot API HTML 形态。
func TestProcessTextWeavesChannelFooter(t *testing.T) {
	s := openStore(t)
	job, _ := newJobWithRequest(t, s, 0)
	provider := &fakeChannelLinks{links: testLinks()}
	deps := Deps{
		Fetcher:  &fakeFetcher{msgs: []*tg.Message{{ID: 7, Message: "hello"}}},
		Sender:   &fakeSender{},
		Store:    s,
		Channels: provider,
		Log:      testLog(),
	}

	Process(deps)(context.Background(), job)

	texts := deps.Sender.(*fakeSender).texts()
	if len(texts) != 1 {
		t.Fatalf("应发送一条文本，得到 %d", len(texts))
	}
	if !strings.Contains(texts[0], `href="https://t.me/pub"`) ||
		!strings.Contains(texts[0], `href="https://t.me/c/1234567890/1"`) ||
		!strings.Contains(texts[0], "📢 <b>频道</b>：") {
		t.Fatalf("文本应织入样式化脚注: %q", texts[0])
	}
	if provider.calls[0] != job.UserID {
		t.Fatalf("应按任务用户查询链接: %v", provider.calls)
	}
}

// 媒体消息：脚注织入 caption（Bot API 渲染路径）。
func TestProcessMediaCaptionWeavesChannelFooter(t *testing.T) {
	s := openStore(t)
	job, _ := newJobWithRequest(t, s, 0)
	sender := &fakeSender{}
	provider := &fakeChannelLinks{links: testLinks()}
	deps := Deps{
		Fetcher:  fetcherWith(errInvoker{}, docMsg(7, 1)),
		Sender:   &contractSender{fakeSender: sender},
		Media:    mediaOptionsForTest(t),
		Store:    s,
		Channels: provider,
		Log:      testLog(),
	}

	Process(deps)(context.Background(), job)

	calls := sender.mediaCallsSnapshot()
	if len(calls) != 1 {
		t.Fatalf("应发送一次媒体，得到 %d", len(calls))
	}
	html := calls[0].Caption.RenderHTML()
	if !strings.Contains(html, `href="https://t.me/pub"`) || !strings.Contains(html, "📢 <b>频道</b>：") {
		t.Fatalf("caption 应织入脚注: %q", html)
	}
}

// Provider 失败降级：任务照常成功，消息不带脚注。
func TestProcessChannelLinksFailureDegrades(t *testing.T) {
	s := openStore(t)
	job, _ := newJobWithRequest(t, s, 0)
	provider := &fakeChannelLinks{err: errors.New("db down")}
	deps := Deps{
		Fetcher:  &fakeFetcher{msgs: []*tg.Message{{ID: 7, Message: "hello"}}},
		Sender:   &fakeSender{},
		Store:    s,
		Channels: provider,
		Log:      testLog(),
	}

	Process(deps)(context.Background(), job)

	texts := deps.Sender.(*fakeSender).texts()
	if len(texts) != 1 || strings.Contains(texts[0], "📢") {
		t.Fatalf("降级后不应带脚注: %v", texts)
	}
}

// nil Provider：完全不带脚注，任务正常。
func TestProcessNilChannelsProvider(t *testing.T) {
	s := openStore(t)
	job, _ := newJobWithRequest(t, s, 0)
	deps := Deps{
		Fetcher: &fakeFetcher{msgs: []*tg.Message{{ID: 7, Message: "hello"}}},
		Sender:  &fakeSender{},
		Store:   s,
		Log:     testLog(),
	}

	Process(deps)(context.Background(), job)

	if texts := deps.Sender.(*fakeSender).texts(); len(texts) != 1 || strings.Contains(texts[0], "📢") {
		t.Fatalf("nil Provider 不应带脚注: %v", texts)
	}
}
