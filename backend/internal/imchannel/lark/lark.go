// Package lark 是飞书渠道。第 1 期只做群通知(自定义机器人 webhook);交互审批在第 3 期
// 接飞书开放平台应用 + 长连接。
package lark

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"velagateway/internal/imchannel"
)

type Channel struct {
	webhook  string
	secret   string
	client   *http.Client
	validate func(string) error
}

// New 在配置了群通知 webhook 时返回渠道,否则返回 nil(接口 nil,不是类型化的 nil 指针)。
func New(d imchannel.Deps) imchannel.Channel {
	webhook := strings.TrimSpace(d.Settings.String(imchannel.Key(imchannel.Lark, "notifyWebhook")))
	if webhook == "" {
		return nil
	}
	return &Channel{
		webhook:  webhook,
		secret:   d.Settings.Secret(imchannel.Key(imchannel.Lark, "notifySecret")),
		client:   d.HTTP,
		validate: d.ValidateURL,
	}
}

func (c *Channel) Kind() string { return imchannel.Lark }

func (c *Channel) NotifyApproval(ctx context.Context, card imchannel.ApprovalCard) error {
	return c.post(ctx, approvalCard(card))
}

// post 把 {msg_type:interactive, card} 发给自定义机器人;配了密钥就加签。
func (c *Channel) post(ctx context.Context, card any) error {
	if err := c.validate(c.webhook); err != nil {
		return err
	}
	payload := map[string]any{"msg_type": "interactive", "card": card}
	if c.secret != "" {
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		payload["timestamp"] = ts
		payload["sign"] = sign(ts, c.secret)
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.webhook, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	// 飞书成功时回 HTTP 200 + {"code":0};code 非零就是失败。
	var lr struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	_ = json.Unmarshal(raw, &lr)
	if resp.StatusCode < 300 && lr.Code == 0 {
		return nil
	}
	if lr.Msg != "" {
		return fmt.Errorf("飞书返回 %d: %s", lr.Code, lr.Msg)
	}
	return fmt.Errorf("飞书返回 %s", resp.Status)
}

// sign 是自定义机器人的签名:base64(HMAC-SHA256(key, "")),key = "<timestamp>\n<secret>"。
func sign(timestamp, secret string) string {
	mac := hmac.New(sha256.New, []byte(timestamp+"\n"+secret))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// safe 中和反引号,免得嵌在 lark_md 代码块里的命令 / 原因把代码块闭合、注入富文本
// (假按钮、假链接)。代码块里没有转义机制,所以换成外观相同的 U+02CB(C10)。
func safe(s string) string { return strings.ReplaceAll(s, "`", "ˋ") }

func dbOrDefault(db string) string {
	if strings.TrimSpace(db) == "" {
		return "默认"
	}
	return db
}

func approvalCard(c imchannel.ApprovalCard) map[string]any {
	tmpl, riskLabel := "orange", "🟠 中危"
	if c.Risk == "high" {
		tmpl, riskLabel = "red", "🔴 高危"
	}
	shortField := func(k, v string) map[string]any {
		return map[string]any{"is_short": true, "text": map[string]any{"tag": "lark_md", "content": "**" + k + "**\n" + safe(v)}}
	}
	elements := []any{
		map[string]any{"tag": "div", "fields": []any{
			shortField("实例", c.Instance),
			shortField("数据库", dbOrDefault(c.Database)),
			shortField("环境", strings.ToUpper(c.Env)),
			shortField("风险", riskLabel),
			shortField("发起人", c.Initiator),
		}},
		map[string]any{"tag": "div", "text": map[string]any{"tag": "lark_md", "content": "**命令**\n```sql\n" + safe(c.Command) + "\n```"}},
	}
	if strings.TrimSpace(c.Reason) != "" {
		elements = append(elements, map[string]any{"tag": "div", "text": map[string]any{"tag": "lark_md", "content": "**原因**\n" + safe(c.Reason)}})
	}
	if c.ConsoleURL != "" {
		elements = append(elements, map[string]any{"tag": "action", "actions": []any{
			map[string]any{"tag": "button", "type": "primary",
				"text": map[string]any{"tag": "plain_text", "content": "前往审批"},
				"url":  c.ConsoleURL},
		}})
	}
	elements = append(elements, map[string]any{"tag": "note", "elements": []any{
		map[string]any{"tag": "plain_text", "content": "AegisDB 数据库网关 · " + c.ApNo}}})
	return map[string]any{
		"config":   map[string]any{"wide_screen_mode": true},
		"header":   map[string]any{"template": tmpl, "title": map[string]any{"tag": "plain_text", "content": "高危命令待审批 · " + c.ApNo}},
		"elements": elements,
	}
}
