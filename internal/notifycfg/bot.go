package notifycfg

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

type telegramResponse[T any] struct {
	OK     bool `json:"ok"`
	Result T    `json:"result"`
}

func (m *Manager) botRequest(ctx context.Context, token, method string, requestBody any, response any) error {
	body, err := json.Marshal(requestBody)
	if err != nil {
		return errors.New("编码 Bot API 请求失败")
	}
	target := m.botAPIBase + "/bot" + token + "/" + method
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return errors.New("创建 Bot API 请求失败")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.client.Do(req)
	if err != nil {
		return errors.New("Bot API 请求失败，请检查网络和配置")
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody+1))
	if err != nil || len(payload) > maxResponseBody {
		return errors.New("Bot API 响应无效")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Bot API 返回失败状态（HTTP %d）", resp.StatusCode)
	}
	if err := json.Unmarshal(payload, response); err != nil {
		return errors.New("Bot API 响应无效")
	}
	return nil
}

func (m *Manager) sendBotMessage(ctx context.Context, token, chatID, message string) error {
	var response telegramResponse[json.RawMessage]
	if err := m.botRequest(ctx, token, "sendMessage", map[string]any{
		"chat_id": chatID,
		"text":    message,
	}, &response); err != nil {
		return err
	}
	if !response.OK {
		return errors.New("Bot API 拒绝了发送请求")
	}
	return nil
}

type telegramChat struct {
	ID        int64  `json:"id"`
	Type      string `json:"type"`
	Title     string `json:"title"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
}

type telegramMessage struct {
	Chat telegramChat `json:"chat"`
}

type telegramUpdate struct {
	UpdateID          int64            `json:"update_id"`
	Message           *telegramMessage `json:"message"`
	EditedMessage     *telegramMessage `json:"edited_message"`
	ChannelPost       *telegramMessage `json:"channel_post"`
	EditedChannelPost *telegramMessage `json:"edited_channel_post"`
}

func (m *Manager) getRecentChat(ctx context.Context, token string) (RecentChat, error) {
	var response telegramResponse[[]telegramUpdate]
	if err := m.botRequest(ctx, token, "getUpdates", map[string]any{
		"limit":           100,
		"timeout":         0,
		"allowed_updates": []string{"message", "edited_message", "channel_post", "edited_channel_post"},
	}, &response); err != nil {
		return RecentChat{}, err
	}
	if !response.OK {
		return RecentChat{}, errors.New("Bot API 拒绝了更新查询")
	}
	var latest *telegramUpdate
	var chat telegramChat
	for i := range response.Result {
		update := &response.Result[i]
		candidate, ok := updateChat(*update)
		if !ok || (latest != nil && update.UpdateID <= latest.UpdateID) {
			continue
		}
		latest = update
		chat = candidate
	}
	if latest == nil {
		return RecentChat{}, errors.New("尚未发现最近会话，请先向 Bot 发送一条消息")
	}
	title := strings.TrimSpace(chat.Title)
	if title == "" {
		title = strings.TrimSpace(strings.Join([]string{chat.FirstName, chat.LastName}, " "))
	}
	if title == "" && chat.Username != "" {
		title = "@" + chat.Username
	}
	return RecentChat{ID: strconv.FormatInt(chat.ID, 10), Type: chat.Type, Title: title}, nil
}

func updateChat(update telegramUpdate) (telegramChat, bool) {
	for _, message := range []*telegramMessage{update.Message, update.EditedMessage, update.ChannelPost, update.EditedChannelPost} {
		if message != nil {
			return message.Chat, true
		}
	}
	return telegramChat{}, false
}
