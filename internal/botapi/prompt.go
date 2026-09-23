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
	promptDownloadDestination
)

type promptStage uint8

const (
	promptAwaitInput promptStage = iota
	promptAwaitDownloadDestination
)

type pendingPrompt struct {
	command            string
	usage              string
	ownerID            int64
	chatID             int64
	promptMessageID    int64
	controlMessageID   int
	expiresAt          time.Time
	stage              promptStage
	input              string
	destinations       []string
	defaultDestination string
	destinationPage    int
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
		stage:           promptAwaitInput,
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
	return clonePendingPrompt(entry), true
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
		return text, promptPassthrough, clonePendingPrompt(entry)
	}
	if entry.ownerID != userID {
		return text, promptPassthrough, pendingPrompt{}
	}

	trimmed := strings.TrimSpace(text)
	// A reply that is itself a command is a deliberate change of intent. The
	// update layer will cancel the active prompt before dispatching that command.
	if strings.HasPrefix(trimmed, "/") {
		return text, promptPassthrough, clonePendingPrompt(entry)
	}
	if strings.EqualFold(trimmed, "usage") || trimmed == "用法" {
		return "", promptUsage, clonePendingPrompt(entry)
	}
	if strings.EqualFold(trimmed, "cancel") || trimmed == "取消" {
		p.removeLocked(replyMessageID)
		return "", promptCancel, clonePendingPrompt(entry)
	}

	// ForceReply 输入“目的地 链接”继续沿用现有直接提交；只输入链接时进入
	// 目的地选择准备阶段，pending 暂不消费。目的地选择阶段再次回复新链接
	// 时以最新链接重新进入选择流程。
	if entry.command == "/download" {
		dest, link := parseDownloadArgs(entry.command + " " + trimmed)
		if dest == "" && link != "" {
			return link, promptDownloadDestination, clonePendingPrompt(entry)
		}
	}

	p.removeLocked(replyMessageID)
	return entry.command + " " + trimmed, promptExecute, clonePendingPrompt(entry)
}

func (p *inputPrompter) beginDownloadSelection(messageID, userID int64, input string, destinations []string, defaultDestination string) (pendingPrompt, bool) {
	if p == nil || messageID == 0 || len(destinations) < 2 {
		return pendingPrompt{}, false
	}
	now := p.clock()
	p.mu.Lock()
	defer p.mu.Unlock()
	entry, ok := p.pending[messageID]
	if !ok || now.After(entry.expiresAt) || entry.ownerID != userID || entry.command != "/download" {
		if ok && now.After(entry.expiresAt) {
			p.removeLocked(messageID)
		}
		return pendingPrompt{}, false
	}
	entry.stage = promptAwaitDownloadDestination
	entry.input = strings.TrimSpace(input)
	entry.destinations = append([]string(nil), destinations...)
	entry.defaultDestination = defaultDestination
	entry.destinationPage = 0
	entry.expiresAt = now.Add(promptTTL)
	p.pending[messageID] = entry
	return clonePendingPrompt(entry), true
}

func (p *inputPrompter) setDownloadPage(messageID, userID int64, page int) (pendingPrompt, bool) {
	if p == nil || messageID == 0 || page < 0 {
		return pendingPrompt{}, false
	}
	now := p.clock()
	p.mu.Lock()
	defer p.mu.Unlock()
	entry, ok := p.pending[messageID]
	if !ok || now.After(entry.expiresAt) || entry.ownerID != userID || entry.stage != promptAwaitDownloadDestination {
		if ok && now.After(entry.expiresAt) {
			p.removeLocked(messageID)
		}
		return pendingPrompt{}, false
	}
	if page >= downloadDestinationPageCount(len(entry.destinations)) {
		return pendingPrompt{}, false
	}
	entry.destinationPage = page
	p.pending[messageID] = entry
	return clonePendingPrompt(entry), true
}

func (p *inputPrompter) selectDownloadDestination(messageID, userID int64, index int) (pendingPrompt, string, bool) {
	if p == nil || messageID == 0 || index < 0 {
		return pendingPrompt{}, "", false
	}
	now := p.clock()
	p.mu.Lock()
	defer p.mu.Unlock()
	entry, ok := p.pending[messageID]
	if !ok || now.After(entry.expiresAt) || entry.ownerID != userID || entry.stage != promptAwaitDownloadDestination || index >= len(entry.destinations) {
		if ok && now.After(entry.expiresAt) {
			p.removeLocked(messageID)
		}
		return pendingPrompt{}, "", false
	}
	destination := entry.destinations[index]
	p.removeLocked(messageID)
	return clonePendingPrompt(entry), destination, true
}

func (p *inputPrompter) consume(messageID, userID int64) (pendingPrompt, bool) {
	if p == nil || messageID == 0 {
		return pendingPrompt{}, false
	}
	now := p.clock()
	p.mu.Lock()
	defer p.mu.Unlock()
	entry, ok := p.pending[messageID]
	if !ok || now.After(entry.expiresAt) || entry.ownerID != userID {
		if ok && now.After(entry.expiresAt) {
			p.removeLocked(messageID)
		}
		return pendingPrompt{}, false
	}
	p.removeLocked(messageID)
	return clonePendingPrompt(entry), true
}

func (p *inputPrompter) cancel(messageID, userID int64) (pendingPrompt, bool) {
	return p.consume(messageID, userID)
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

func clonePendingPrompt(entry pendingPrompt) pendingPrompt {
	entry.destinations = append([]string(nil), entry.destinations...)
	return entry
}
