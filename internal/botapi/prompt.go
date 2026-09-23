package botapi

import (
	"strings"
	"sync"
	"time"
)

const promptTTL = 5 * time.Minute

type promptOutcome uint8

const (
	promptPassthrough promptOutcome = iota
	promptExecute
	promptCancel
	promptUsage
)

type pendingPrompt struct {
	command          string
	usage            string
	ownerID          int64
	chatID           int64
	promptMessageID  int64
	controlMessageID int
	expiresAt        time.Time
}

type inputPrompter struct {
	mu      sync.Mutex
	pending map[int64]pendingPrompt
	active  map[int64]int64 // chat ID -> active prompt message ID
	now     func() time.Time
}

func newInputPrompter() *inputPrompter {
	return &inputPrompter{
		pending: make(map[int64]pendingPrompt),
		active:  make(map[int64]int64),
		now:     time.Now,
	}
}

func (p *inputPrompter) register(messageID int64, command, usage string, ownerID, chatID int64) {
	if p == nil || messageID == 0 {
		return
	}
	now := p.clock()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.purgeLocked(now)
	if previousID := p.active[chatID]; previousID != 0 && previousID != messageID {
		p.removeLocked(previousID)
	}
	p.pending[messageID] = pendingPrompt{
		command:         command,
		usage:           usage,
		ownerID:         ownerID,
		chatID:          chatID,
		promptMessageID: messageID,
		expiresAt:       now.Add(promptTTL),
	}
	p.active[chatID] = messageID
}

func (p *inputPrompter) takeForChat(chatID, ownerID int64) (pendingPrompt, bool) {
	if p == nil || chatID == 0 {
		return pendingPrompt{}, false
	}
	now := p.clock()
	p.mu.Lock()
	defer p.mu.Unlock()
	messageID := p.active[chatID]
	if messageID == 0 {
		return pendingPrompt{}, false
	}
	entry, ok := p.pending[messageID]
	if !ok {
		delete(p.active, chatID)
		return pendingPrompt{}, false
	}
	if now.After(entry.expiresAt) || entry.ownerID != ownerID {
		p.removeLocked(messageID)
		return pendingPrompt{}, false
	}
	p.removeLocked(messageID)
	return entry, true
}

func (p *inputPrompter) attachControl(promptMessageID int64, controlMessageID int) {
	if p == nil || promptMessageID == 0 || controlMessageID == 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	entry, ok := p.pending[promptMessageID]
	if !ok || p.clock().After(entry.expiresAt) {
		return
	}
	entry.controlMessageID = controlMessageID
	p.pending[promptMessageID] = entry
}

func (p *inputPrompter) get(messageID, userID int64) (pendingPrompt, bool) {
	if p == nil || messageID == 0 {
		return pendingPrompt{}, false
	}
	now := p.clock()
	p.mu.Lock()
	defer p.mu.Unlock()
	entry, ok := p.pending[messageID]
	if !ok {
		return pendingPrompt{}, false
	}
	if now.After(entry.expiresAt) {
		p.removeLocked(messageID)
		return pendingPrompt{}, false
	}
	if entry.ownerID != userID {
		return pendingPrompt{}, false
	}
	return entry, true
}

func (p *inputPrompter) resolve(text string, replyMessageID, userID int64) (string, promptOutcome, pendingPrompt) {
	if p == nil || replyMessageID == 0 {
		return text, promptPassthrough, pendingPrompt{}
	}
	now := p.clock()
	p.mu.Lock()
	defer p.mu.Unlock()
	entry, ok := p.pending[replyMessageID]
	if !ok {
		return text, promptPassthrough, pendingPrompt{}
	}
	if now.After(entry.expiresAt) {
		p.removeLocked(replyMessageID)
		return text, promptPassthrough, entry
	}
	if entry.ownerID != userID {
		return text, promptPassthrough, pendingPrompt{}
	}

	trimmed := strings.TrimSpace(text)
	// A reply that is itself a command is a deliberate change of intent. Keep
	// the prompt available so the user can return to it after that command.
	if strings.HasPrefix(trimmed, "/") {
		return text, promptPassthrough, entry
	}
	// “用法”查看说明但不消费 pending，输入等待继续有效。
	if strings.EqualFold(trimmed, "usage") || trimmed == "用法" {
		return "", promptUsage, entry
	}
	p.removeLocked(replyMessageID)
	if strings.EqualFold(trimmed, "cancel") || trimmed == "取消" {
		return "", promptCancel, entry
	}
	return entry.command + " " + trimmed, promptExecute, entry
}

func (p *inputPrompter) cancel(messageID, userID int64) (pendingPrompt, bool) {
	if p == nil || messageID == 0 {
		return pendingPrompt{}, false
	}
	now := p.clock()
	p.mu.Lock()
	defer p.mu.Unlock()
	entry, ok := p.pending[messageID]
	if !ok {
		return pendingPrompt{}, false
	}
	p.removeLocked(messageID)
	if now.After(entry.expiresAt) || entry.ownerID != userID {
		return pendingPrompt{}, false
	}
	return entry, true
}

func (p *inputPrompter) clock() time.Time {
	if p != nil && p.now != nil {
		return p.now()
	}
	return time.Now()
}

func (p *inputPrompter) purgeLocked(now time.Time) {
	for id, entry := range p.pending {
		if now.After(entry.expiresAt) {
			p.removeLocked(id)
		}
	}
}

func (p *inputPrompter) removeLocked(messageID int64) {
	entry, ok := p.pending[messageID]
	if !ok {
		return
	}
	delete(p.pending, messageID)
	if p.active[entry.chatID] == messageID {
		delete(p.active, entry.chatID)
	}
}
