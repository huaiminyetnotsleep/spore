package notifycfg

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const maxResponseBody = 1 << 20

type webhookAdapter struct {
	descriptor Descriptor
	build      func(webhookSecrets, string, time.Time) ([]byte, http.Header, string, error)
	check      func(int, []byte) error
}

func (a webhookAdapter) send(ctx context.Context, client *http.Client, cfg webhookSecrets, message string, now time.Time) error {
	body, headers, target, err := a.build(cfg, message, now)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return errors.New("创建通知请求失败")
	}
	for key, values := range headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("通知请求失败，请检查网络和配置")
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody+1))
	if err != nil || len(responseBody) > maxResponseBody {
		return errors.New("通知服务响应无效")
	}
	return a.check(resp.StatusCode, responseBody)
}

var webhookRegistry = map[string]webhookAdapter{
	FormatGeneric: {
		descriptor: Descriptor{
			Format: FormatGeneric, Label: "通用 JSON", SupportsSecret: true,
			Fields: []FieldDescriptor{{Name: "generic_signature_header", Kind: "text"}},
		},
		build: buildGeneric,
		check: checkHTTPOnly,
	},
	FormatFeishu: {
		descriptor: Descriptor{
			Format: FormatFeishu, Label: "飞书", SupportsSecret: true,
			Fields: []FieldDescriptor{{Name: "feishu_open_ids", Kind: "string_list"}, {Name: "feishu_at_all", Kind: "boolean"}},
		},
		build: buildFeishu,
		check: checkFeishu,
	},
	FormatDingTalk: {
		descriptor: Descriptor{
			Format: FormatDingTalk, Label: "钉钉", SupportsSecret: true,
			Fields: []FieldDescriptor{{Name: "dingtalk_mobiles", Kind: "string_list"}, {Name: "dingtalk_at_all", Kind: "boolean"}},
		},
		build: buildDingTalk,
		check: checkDingTalk,
	},
	FormatDiscord: {
		descriptor: Descriptor{
			Format: FormatDiscord, Label: "Discord", SupportsSecret: false,
			Fields: []FieldDescriptor{{Name: "discord_user_ids", Kind: "string_list"}, {Name: "discord_role_ids", Kind: "string_list"}, {Name: "discord_everyone", Kind: "boolean"}},
		},
		build: buildDiscord,
		check: checkHTTPOnly,
	},
}

func lookupWebhook(format string) (webhookAdapter, bool) {
	adapter, ok := webhookRegistry[format]
	return adapter, ok
}

func webhookDescriptors() []Descriptor {
	formats := []string{FormatGeneric, FormatFeishu, FormatDingTalk, FormatDiscord}
	out := make([]Descriptor, 0, len(formats))
	for _, format := range formats {
		d := webhookRegistry[format].descriptor
		d.Fields = append([]FieldDescriptor(nil), d.Fields...)
		out = append(out, d)
	}
	return out
}

func jsonHeaders() http.Header {
	return http.Header{"Content-Type": []string{"application/json"}}
}

func buildGeneric(cfg webhookSecrets, message string, _ time.Time) ([]byte, http.Header, string, error) {
	body, err := json.Marshal(struct {
		Text string `json:"text"`
	}{Text: message})
	if err != nil {
		return nil, nil, "", errors.New("编码通用 Webhook 消息失败")
	}
	headers := jsonHeaders()
	if cfg.Secret != "" {
		header := strings.TrimSpace(cfg.Config.GenericSignatureHeader)
		if header == "" {
			header = "X-Spore-Signature"
		}
		if !validHeaderName(header) {
			return nil, nil, "", errors.New("签名请求头名称无效")
		}
		mac := hmac.New(sha256.New, []byte(cfg.Secret))
		_, _ = mac.Write(body)
		headers.Set(header, "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	return body, headers, cfg.URL, nil
}

func buildFeishu(cfg webhookSecrets, message string, now time.Time) ([]byte, http.Header, string, error) {
	var mentions strings.Builder
	for _, id := range cfg.Config.FeishuOpenIDs {
		mentions.WriteString(`<at user_id="`)
		mentions.WriteString(escapeMention(id))
		mentions.WriteString(`"></at> `)
	}
	if cfg.Config.FeishuAtAll {
		mentions.WriteString(`<at user_id="all">所有人</at> `)
	}
	payload := map[string]any{
		"msg_type": "text",
		"content":  map[string]string{"text": mentions.String() + message},
	}
	if cfg.Secret != "" {
		timestamp := strconv.FormatInt(now.Unix(), 10)
		mac := hmac.New(sha256.New, []byte(timestamp+"\n"+cfg.Secret))
		payload["timestamp"] = timestamp
		payload["sign"] = base64.StdEncoding.EncodeToString(mac.Sum(nil))
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, "", errors.New("编码飞书消息失败")
	}
	return body, jsonHeaders(), cfg.URL, nil
}

func buildDingTalk(cfg webhookSecrets, message string, now time.Time) ([]byte, http.Header, string, error) {
	target := cfg.URL
	if cfg.Secret != "" {
		timestamp := strconv.FormatInt(now.UnixMilli(), 10)
		mac := hmac.New(sha256.New, []byte(cfg.Secret))
		_, _ = mac.Write([]byte(timestamp + "\n" + cfg.Secret))
		sign := base64.StdEncoding.EncodeToString(mac.Sum(nil))
		u, err := url.Parse(target)
		if err != nil {
			return nil, nil, "", errors.New("已保存的钉钉 Webhook URL 无效")
		}
		query := u.Query()
		query.Set("timestamp", timestamp)
		query.Set("sign", sign)
		u.RawQuery = query.Encode()
		target = u.String()
	}
	body, err := json.Marshal(map[string]any{
		"msgtype": "text",
		"text":    map[string]string{"content": message},
		"at": map[string]any{
			"atMobiles": cfg.Config.DingTalkMobiles,
			"isAtAll":   cfg.Config.DingTalkAtAll,
		},
	})
	if err != nil {
		return nil, nil, "", errors.New("编码钉钉消息失败")
	}
	return body, jsonHeaders(), target, nil
}

func buildDiscord(cfg webhookSecrets, message string, _ time.Time) ([]byte, http.Header, string, error) {
	parse := []string{}
	if cfg.Config.DiscordEveryone {
		parse = append(parse, "everyone")
	}
	body, err := json.Marshal(map[string]any{
		"content": message,
		"allowed_mentions": map[string]any{
			"parse": parse,
			"users": cfg.Config.DiscordUserIDs,
			"roles": cfg.Config.DiscordRoleIDs,
		},
	})
	if err != nil {
		return nil, nil, "", errors.New("编码 Discord 消息失败")
	}
	return body, jsonHeaders(), cfg.URL, nil
}

func checkHTTPOnly(status int, _ []byte) error {
	if status < 200 || status >= 300 {
		return fmt.Errorf("通知服务返回失败状态（HTTP %d）", status)
	}
	return nil
}

func checkFeishu(status int, body []byte) error {
	if err := checkHTTPOnly(status, body); err != nil {
		return err
	}
	var response struct {
		Code       *int `json:"code"`
		StatusCode *int `json:"StatusCode"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return errors.New("飞书通知服务响应无效")
	}
	code := response.Code
	if code == nil {
		code = response.StatusCode
	}
	if code == nil || *code != 0 {
		return errors.New("飞书通知服务拒绝了请求")
	}
	return nil
}

func checkDingTalk(status int, body []byte) error {
	if err := checkHTTPOnly(status, body); err != nil {
		return err
	}
	var response struct {
		ErrCode *int `json:"errcode"`
	}
	if err := json.Unmarshal(body, &response); err != nil || response.ErrCode == nil {
		return errors.New("钉钉通知服务响应无效")
	}
	if *response.ErrCode != 0 {
		return errors.New("钉钉通知服务拒绝了请求")
	}
	return nil
}

func validHeaderName(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			continue
		}
		switch r {
		case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
			continue
		default:
			return false
		}
	}
	return true
}

func escapeMention(value string) string {
	replacer := strings.NewReplacer("&", "&amp;", `"`, "&quot;", "<", "&lt;", ">", "&gt;")
	return replacer.Replace(value)
}
