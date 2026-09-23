package botapi

import (
	"testing"
	"time"
)

func TestInputPrompterResolveAndConsume(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	p := newInputPrompter()
	p.now = func() time.Time { return now }
	p.register(101, "/pin", "pin usage", 7, 7)

	got, outcome, entry := p.resolve("https://t.me/example/42", 101, 7)
	if outcome != promptExecute || got != "/pin https://t.me/example/42" {
		t.Fatalf("resolve = (%q, %v), want pin execution", got, outcome)
	}
	if entry.promptMessageID != 101 {
		t.Fatalf("entry = %+v, want prompt ID 101", entry)
	}
	if _, outcome, _ := p.resolve("https://t.me/example/42", 101, 7); outcome != promptPassthrough {
		t.Fatalf("second reply outcome = %v, want passthrough", outcome)
	}
}

func TestInputPrompterCommandReplyKeepsPending(t *testing.T) {
	p := newInputPrompter()
	p.register(101, "/download", "download usage", 7, 7)

	got, outcome, _ := p.resolve("/help", 101, 7)
	if outcome != promptPassthrough || got != "/help" {
		t.Fatalf("command reply = (%q, %v), want passthrough", got, outcome)
	}
	got, outcome, _ = p.resolve("https://t.me/example/1", 101, 7)
	if outcome != promptExecute || got != "/download https://t.me/example/1" {
		t.Fatalf("pending reply = (%q, %v), want download execution", got, outcome)
	}
}

func TestInputPrompterCancelAndOwner(t *testing.T) {
	p := newInputPrompter()
	p.register(101, "/unbind", "unbind usage", 7, 7)
	if _, outcome, _ := p.resolve("取消", 101, 8); outcome != promptPassthrough {
		t.Fatalf("wrong owner should not consume prompt, got %v", outcome)
	}
	if _, outcome, entry := p.resolve("cancel", 101, 7); outcome != promptCancel || entry.command != "/unbind" {
		t.Fatalf("cancel = (%v, %+v), want cancel for /unbind", outcome, entry)
	}
	if _, outcome, _ := p.resolve("cancel", 101, 7); outcome != promptPassthrough {
		t.Fatalf("cancelled prompt outcome = %v, want passthrough", outcome)
	}
}

func TestInputPrompterExpiryAndMultiplePrompts(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	p := newInputPrompter()
	p.now = func() time.Time { return now }
	p.register(101, "/pin", "pin usage", 7, 7)
	p.register(102, "/watch", "watch usage", 7, 7)

	got, outcome, _ := p.resolve("@source", 102, 7)
	if outcome != promptExecute || got != "/watch @source" {
		t.Fatalf("second prompt = (%q, %v), want watch execution", got, outcome)
	}

	now = now.Add(promptTTL + time.Second)
	if _, outcome, _ := p.resolve("https://t.me/example/1", 101, 7); outcome != promptPassthrough {
		t.Fatalf("expired prompt outcome = %v, want passthrough", outcome)
	}
}

func TestInputPrompterUsageKeywordKeepsPending(t *testing.T) {
	p := newInputPrompter()
	p.register(101, "/pin", "pin usage", 7, 7)

	// 回复“用法”：发送说明但 pending 保留，随后仍可正常输入。
	got, outcome, entry := p.resolve("用法", 101, 7)
	if outcome != promptUsage || got != "" || entry.usage != "pin usage" {
		t.Fatalf("usage reply = (%q, %v, %+v), want promptUsage", got, outcome, entry)
	}
	if _, ok := p.get(101, 7); !ok {
		t.Fatal("usage reply must not consume pending prompt")
	}
	if _, outcome, _ := p.resolve("https://t.me/example/1", 101, 7); outcome != promptExecute {
		t.Fatal("pending should still execute after usage reply")
	}
}

func TestInputPrompterTakeForChatAutoCancelSemantics(t *testing.T) {
	p := newInputPrompter()

	// 连续命令：新提示注册后，chat 的活动 pending 是最新一条，旧条已被移除。
	p.register(201, "/pin", "pin usage", 7, 7)
	p.register(202, "/watch", "watch usage", 7, 7)
	if _, ok := p.get(201, 7); ok {
		t.Fatal("previous prompt should be auto-cancelled by newer command")
	}
	entry, ok := p.takeForChat(7, 7)
	if !ok || entry.command != "/watch" {
		t.Fatalf("takeForChat = (%+v, %v), want /watch", entry, ok)
	}

	// 已执行（消费）或已取消的 pending 不可再被自动取消。
	p.register(203, "/pin", "pin usage", 7, 7)
	if _, outcome, _ := p.resolve("https://t.me/example/1", 203, 7); outcome != promptExecute {
		t.Fatal("reply should consume pending")
	}
	if _, ok := p.takeForChat(7, 7); ok {
		t.Fatal("executed prompt must not be auto-cancelled")
	}

	p.register(204, "/pin", "pin usage", 7, 7)
	if _, ok := p.cancel(204, 7); !ok {
		t.Fatal("explicit cancel should consume pending")
	}
	if _, ok := p.takeForChat(7, 7); ok {
		t.Fatal("cancelled prompt must not be auto-cancelled")
	}

	// 过期的 pending 同样不会触发自动取消。
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	p.now = func() time.Time { return now }
	p.register(205, "/pin", "pin usage", 7, 7)
	p.now = func() time.Time { return now.Add(promptTTL + time.Second) }
	if _, ok := p.takeForChat(7, 7); ok {
		t.Fatal("expired prompt must not be auto-cancelled")
	}
}
