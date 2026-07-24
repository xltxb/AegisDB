package service

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"

	"velagateway/internal/model"
	"velagateway/internal/repository"
	"velagateway/pkg/sqlutil"
)

// AllowPrivateWebhookTargets permits loopback/private webhook destinations.
// Default false (full SSRF guard). Bootstrap enables it in dev, where stub /
// on-host receivers legitimately live on loopback; prod keeps it off.
var AllowPrivateWebhookTargets = false

// nat64Prefix is the well-known NAT64 range (RFC 6052); its low 32 bits embed an
// IPv4 address. cgnat is the RFC 6598 carrier-grade-NAT shared space.
var (
	nat64Prefix = &net.IPNet{IP: net.ParseIP("64:ff9b::"), Mask: net.CIDRMask(96, 128)}
	cgnat       = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}
)

// isDisallowedIP reports whether an IP is one an outbound webhook must never
// reach: loopback / private / link-local / unspecified (incl. 169.254 metadata).
// It also sees through NAT64-wrapped and IPv4-mapped addresses (which embed an
// IPv4 target inside IPv6) and blocks CGNAT shared space (C1).
func isDisallowedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	// NAT64 (64:ff9b::/96) carries a real IPv4 in its last 4 bytes — extract and
	// re-check so it can't tunnel to a private/metadata target via an IPv6 literal.
	if ip16 := ip.To16(); ip16 != nil && nat64Prefix.Contains(ip16) {
		return isDisallowedIP(net.IPv4(ip16[12], ip16[13], ip16[14], ip16[15]))
	}
	if v4 := ip.To4(); v4 != nil && cgnat.Contains(v4) {
		return true
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}

// validateOutboundURL guards the server-side webhook/Lark callers against SSRF:
// only http(s) is allowed, and (unless AllowPrivateWebhookTargets) the target
// must not resolve to a loopback / private / link-local / unspecified address
// (e.g. 127.0.0.1, 10/8, 169.254 the cloud metadata endpoint). This is a
// fail-fast pre-check; the dial-time control (newOutboundClient) closes the
// DNS-rebinding TOCTOU. See L2/R20.
func validateOutboundURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("无效的 URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("URL 必须使用 http/https")
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("URL 缺少主机名")
	}
	if AllowPrivateWebhookTargets {
		return nil
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("无法解析主机 %q: %w", host, err)
	}
	for _, ip := range ips {
		if isDisallowedIP(ip) {
			return fmt.Errorf("目标解析到内网/环回地址，已拒绝(防 SSRF)")
		}
	}
	return nil
}

// checkDialAddr rejects a connection whose already-resolved target IP is
// non-public. Because Go resolves the host before calling this, it validates the
// ACTUAL address being dialed — closing the gap where a name re-resolves to an
// internal IP between validateOutboundURL and the request (DNS rebinding, R20).
func checkDialAddr(_, address string) error {
	if AllowPrivateWebhookTargets {
		return nil
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); ip != nil && isDisallowedIP(ip) {
		return fmt.Errorf("blocked connection to non-public address %s (SSRF)", host)
	}
	return nil
}

// newOutboundClient builds the HTTP client used for all server-initiated
// webhook/Lark calls: it refuses redirects (which could bounce to an internal
// address) and validates the resolved IP at dial time (R20).
func newOutboundClient() *http.Client {
	return &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return fmt.Errorf("重定向已被禁止(防 SSRF)")
		},
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout: 5 * time.Second,
				Control: func(network, address string, _ syscall.RawConn) error {
					return checkDialAddr(network, address)
				},
			}).DialContext,
		},
	}
}

// SendExternalApproval posts a high-risk ticket to审批魔方's create-approval API
// (POST {baseURL}/api/v1/approvals, Bearer auth) and returns the vendor task_id.
// Reuses the SSRF-guarded outbound client. See ADR-0003 for the field mapping.
func (d *Dispatcher) SendExternalApproval(baseURL, token, aiGroup, callbackURL, userAccount string, ap *model.Approval) (string, error) {
	endpoint := baseURL + "/api/v1/approvals"
	if err := validateOutboundURL(endpoint); err != nil {
		return "", err
	}
	summary := fmt.Sprintf("%s/%s/%s 高危命令待审批:%s · 原因:%s · 风险:%s",
		ap.Env, ap.Instance, ap.Database, clip(ap.Command, 200), ap.Reason, ap.RiskLevel)
	body := map[string]any{
		"user":             userAccount,   // 发起人网关账户
		"message_id":       "gw-" + ap.ApNo, // 网关生成的确定性ID(幂等)
		"external_task_id": ap.ApNo,        // 回调关联主键(原样带回)
		"request_id":       ap.ApNo,        // 备用关联键
		"ai_group":         aiGroup,
		"callback_url":     callbackURL,
		"messages":         []map[string]string{{"role": "user", "content": summary}},
		"payload": map[string]any{
			"env": ap.Env, "instance": ap.Instance, "database": ap.Database,
			"command": ap.Command, "risk": ap.RiskLevel, "initiator": ap.Initiator,
			"reason": ap.Reason, "apNo": ap.ApNo,
		},
	}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := d.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("审批魔方返回 %d: %s", resp.StatusCode, clip(string(respBody), 200))
	}
	var out struct {
		TaskID string `json:"task_id"`
	}
	_ = json.Unmarshal(respBody, &out)
	return out.TaskID, nil
}

// PatchExternalStatus writes a terminal status back to审批魔方 for a ticket the
// gateway resolved on its own (an internal timeout auto-reject). approval_status
// 2 = cancel — collapses the still-open飞书 card so the two sides stay in sync.
// Best-effort; reuses the SSRF-guarded outbound client. See ADR-0003.
func (d *Dispatcher) PatchExternalStatus(baseURL, token, taskID string) error {
	endpoint := baseURL + "/api/v1/approvals/" + url.PathEscape(taskID) + "/status"
	if err := validateOutboundURL(endpoint); err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]any{
		"task_id":         taskID,
		"approver":        "gateway-timeout",
		"approval_status": 2, // cancel
	})
	req, err := http.NewRequest(http.MethodPatch, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("审批魔方 PATCH 返回 %d: %s", resp.StatusCode, clip(string(respBody), 200))
	}
	return nil
}

// Dispatcher pushes audit events to the configured webhook, authenticating with
// an Authorization: Bearer <token> header (token = the configured webhook secret)
// and retrying with exponential backoff (backend doc §8).
type Dispatcher struct {
	repo   *repository.Repo
	client *http.Client
}

func NewDispatcher(repo *repository.Repo) *Dispatcher {
	return &Dispatcher{repo: repo, client: newOutboundClient()}
}

// platformTimeLayout is the timestamp format the Event Center ingest API expects
// (e.g. "2026-05-06 14:42:00"). See docs/事件中心平台接入文档.md.
const platformTimeLayout = "2006-01-02 15:04:05"

// eventAction maps a SQL/audit command to the Event Center `action` enum
// (create|read|update|delete) by its leading verb. Anything non-mutating or
// unrecognized (login, EXPORT, \i script, SELECT, …) falls back to "read".
func eventAction(command string) string {
	fields := strings.Fields(strings.TrimSpace(command))
	if len(fields) == 0 {
		return "read"
	}
	switch strings.ToUpper(fields[0]) {
	case "INSERT", "CREATE":
		return "create"
	case "UPDATE", "ALTER", "REPLACE", "GRANT", "REVOKE":
		return "update"
	case "DELETE", "DROP", "TRUNCATE":
		return "delete"
	default:
		return "read"
	}
}

// buildPlatformEvent maps an internal audit event onto the Event Center ingest
// schema (docs/事件中心平台接入文档.md): source_system / occurred_at / action /
// resource / operator / summary / raw_payload. The original audit row travels
// verbatim in raw_payload so no detail is lost.
func buildPlatformEvent(eventType string, data any) map[string]any {
	ev := map[string]any{
		"source_system": "DP DB GATEWAY",
		"occurred_at":   time.Now().Format(platformTimeLayout),
		"action":        "read",
		"resource":      eventType,
		"operator":      "system",
		"summary":       eventType,
		"raw_payload":   data,
	}
	if a, ok := data.(*model.AuditLog); ok && a != nil {
		if !a.OccurredAt.IsZero() {
			ev["occurred_at"] = a.OccurredAt.Format(platformTimeLayout)
		}
		ev["action"] = eventAction(a.Command)
		if a.Instance != "" {
			ev["resource"] = a.Instance
		}
		if a.ActorName != "" {
			ev["operator"] = a.ActorName
		}
		ev["summary"] = fmt.Sprintf("[%s] %s风险 · %s · %s", eventType, a.Risk, a.Result, clip(a.Command, 120))
	}
	return ev
}

// Dispatch sends an event if the webhook is enabled and subscribes to it.
// Runs the network I/O in the background so it never blocks command execution.
func (d *Dispatcher) Dispatch(eventType string, data any) {
	cfg, err := d.repo.GetWebhook()
	if err != nil || cfg == nil || !cfg.Enabled {
		return
	}
	if !strings.Contains(","+cfg.Events+",", ","+eventType+",") {
		return
	}
	body, _ := json.Marshal(buildPlatformEvent(eventType, data))
	endpoint, secret, retryMax := cfg.Endpoint, cfg.Secret, cfg.RetryMax

	if err := validateOutboundURL(endpoint); err != nil {
		d.record(eventType, endpoint, false, "blocked: "+err.Error(), 0)
		return
	}

	go func() {
		attempts, status := 0, ""
		// Bound the goroutine's lifetime regardless of the configured retryMax so a
		// persistently-failing target can't spawn long-lived accumulating goroutines (L9).
		deadline := time.Now().Add(2 * time.Minute)
		for i := 0; i < retryMax; i++ {
			attempts++
			req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
			if err != nil {
				d.record(eventType, endpoint, false, "bad request: "+err.Error(), attempts)
				return
			}
			req.Header.Set("Content-Type", "application/json")
			if secret != "" {
				req.Header.Set("Authorization", "Bearer "+secret)
			}
			req.Header.Set("X-Vela-Event", eventType)
			resp, err := d.client.Do(req)
			if err == nil && resp.StatusCode < 300 {
				st := resp.Status
				resp.Body.Close()
				slog.Debug("webhook delivered", "event", eventType, "status", st)
				d.record(eventType, endpoint, true, st, attempts)
				return
			}
			if resp != nil {
				status = resp.Status
				resp.Body.Close()
			} else if err != nil {
				status = err.Error()
			}
			// 2^i backoff, capped at 30s; stop early if the deadline is exceeded.
			backoff := time.Duration(1<<uint(i)) * time.Second
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
			if time.Now().Add(backoff).After(deadline) {
				break
			}
			time.Sleep(backoff)
		}
		slog.Warn("webhook delivery failed", "event", eventType, "endpoint", endpoint)
		d.record(eventType, endpoint, false, status, attempts)
	}()
}

// Test sends a single synchronous test event and reports the outcome.
func (d *Dispatcher) Test() (bool, string) {
	cfg, err := d.repo.GetWebhook()
	if err != nil || cfg == nil {
		return false, "未配置 Webhook"
	}
	if err := validateOutboundURL(cfg.Endpoint); err != nil {
		return false, err.Error()
	}
	body, _ := json.Marshal(map[string]any{
		"source_system": "DP DB GATEWAY",
		"occurred_at":   time.Now().Format(platformTimeLayout),
		"action":        "read",
		"resource":      "webhook/test",
		"operator":      "system",
		"summary":       "DP DB GATEWAY Webhook 连通性测试",
		"raw_payload":   map[string]any{"event": "test", "ping": "dp-db-gateway"},
	})
	req, err := http.NewRequest(http.MethodPost, cfg.Endpoint, bytes.NewReader(body))
	if err != nil {
		return false, err.Error()
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.Secret != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.Secret)
	}
	start := time.Now()
	resp, err := d.client.Do(req)
	if err != nil {
		d.record("test", cfg.Endpoint, false, err.Error(), 1)
		return false, "投递失败 · " + err.Error()
	}
	defer resp.Body.Close()
	ok := resp.StatusCode < 300
	d.record("test", cfg.Endpoint, ok, resp.Status, 1)
	return ok, "测试事件已投递 · " + resp.Status + " · " + time.Since(start).Round(time.Millisecond).String()
}

// record appends a delivery-outcome row (best-effort; logging-grade, never fatal).
func (d *Dispatcher) record(event, endpoint string, success bool, status string, attempts int) {
	_ = d.repo.InsertWebhookDelivery(&model.WebhookDelivery{
		Event: event, Endpoint: endpoint, Success: success, Status: status, Attempts: attempts,
	})
}

// ---------------------------------------------------------------- Lark (飞书) cards

func (d *Dispatcher) setStr(key string) string {
	v, err := d.repo.GetSetting(key)
	if err != nil || v == "" {
		return ""
	}
	var s string
	if json.Unmarshal([]byte(v), &s) == nil {
		return s
	}
	return strings.Trim(v, "\"")
}

func (d *Dispatcher) setBool(key string, def bool) bool {
	v, err := d.repo.GetSetting(key)
	if err != nil || v == "" {
		return def
	}
	var b bool
	if json.Unmarshal([]byte(v), &b) == nil {
		return b
	}
	return def
}

// SendLarkApproval pushes an interactive Lark card for a newly created approval
// to the configured bot webhook (async, best-effort). No-op unless Lark is
// enabled and a webhook URL is set.
func (d *Dispatcher) SendLarkApproval(ap *model.Approval) {
	if ap == nil || !d.setBool("notify.lark", true) {
		return
	}
	webhook := d.setStr("notify.larkWebhook")
	if webhook == "" {
		return
	}
	secret, consoleURL := d.setStr("notify.larkSecret"), d.setStr("notify.consoleURL")
	card := larkApprovalCard(ap, consoleURL)
	go func() {
		ok, msg := d.postLark(webhook, secret, card)
		d.record("lark-approval", webhook, ok, msg, 1)
		if !ok {
			slog.Warn("lark approval card failed", "ap", ap.ApNo, "err", msg)
		}
	}()
}

// TestLark sends a sample approval card synchronously and reports the outcome.
func (d *Dispatcher) TestLark() (bool, string) {
	webhook := d.setStr("notify.larkWebhook")
	if webhook == "" {
		return false, "未配置飞书 Webhook 地址"
	}
	secret, consoleURL := d.setStr("notify.larkSecret"), d.setStr("notify.consoleURL")
	sample := &model.Approval{
		ApNo: "AP-TEST", Instance: "order-cluster", Env: "prod", RiskLevel: "high",
		Initiator: "Vela", Command: "DROP TABLE orders_2024_q3;", Reason: "测试飞书审批卡片推送",
	}
	ok, msg := d.postLark(webhook, secret, larkApprovalCard(sample, consoleURL))
	d.record("lark-test", webhook, ok, msg, 1)
	if ok {
		return true, "测试卡片已发送到飞书"
	}
	return false, "发送失败 · " + msg
}

// postLark POSTs {msg_type:interactive, card} to a Lark custom-bot webhook,
// signing the request when a secret is configured.
func (d *Dispatcher) postLark(webhook, secret string, card any) (bool, string) {
	if err := validateOutboundURL(webhook); err != nil {
		return false, err.Error()
	}
	payload := map[string]any{"msg_type": "interactive", "card": card}
	if secret != "" {
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		payload["timestamp"] = ts
		payload["sign"] = larkSign(ts, secret)
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, webhook, bytes.NewReader(body))
	if err != nil {
		return false, err.Error()
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.client.Do(req)
	if err != nil {
		return false, err.Error()
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	// Lark returns HTTP 200 with {"code":0,...} on success; non-zero code = error.
	var lr struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	_ = json.Unmarshal(raw, &lr)
	if resp.StatusCode < 300 && lr.Code == 0 {
		return true, "ok"
	}
	if lr.Msg != "" {
		return false, lr.Msg
	}
	return false, resp.Status
}

// larkSign computes the Lark custom-bot signature: base64(HMAC-SHA256(key, "")),
// where key = "<timestamp>\n<secret>".
func larkSign(timestamp, secret string) string {
	mac := hmac.New(sha256.New, []byte(timestamp+"\n"+secret))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// larkSafe neutralizes backticks so a command/reason embedded in a lark_md code
// fence can't close the fence and inject rich text (fake buttons/links). There is
// no escape mechanism inside a fence, so backticks become a look-alike modifier
// grave accent (U+02CB) — visually faithful, functionally inert (C10).
func larkSafe(s string) string {
	return strings.ReplaceAll(s, "`", "ˋ")
}

// dbOrDefault labels an empty target database as the instance default.
func dbOrDefault(db string) string {
	if strings.TrimSpace(db) == "" {
		return "默认"
	}
	return db
}

func larkApprovalCard(ap *model.Approval, consoleURL string) map[string]any {
	tmpl, riskLabel := "orange", "🟠 中危"
	if ap.RiskLevel == "high" {
		tmpl, riskLabel = "red", "🔴 高危"
	}
	shortField := func(k, v string) map[string]any {
		return map[string]any{"is_short": true, "text": map[string]any{"tag": "lark_md", "content": "**" + k + "**\n" + v}}
	}
	elements := []any{
		map[string]any{"tag": "div", "fields": []any{
			shortField("实例", ap.Instance),
			shortField("数据库", dbOrDefault(ap.Database)),
			shortField("环境", strings.ToUpper(ap.Env)),
			shortField("风险", riskLabel),
			shortField("发起人", ap.Initiator),
		}},
		map[string]any{"tag": "div", "text": map[string]any{"tag": "lark_md", "content": "**命令**\n```sql\n" + larkSafe(clip(sqlutil.RedactSecrets(ap.Command), 400)) + "\n```"}},
	}
	if strings.TrimSpace(ap.Reason) != "" {
		elements = append(elements, map[string]any{"tag": "div", "text": map[string]any{"tag": "lark_md", "content": "**原因**\n" + larkSafe(ap.Reason)}})
	}
	if consoleURL != "" {
		elements = append(elements, map[string]any{"tag": "action", "actions": []any{
			map[string]any{"tag": "button", "type": "primary",
				"text": map[string]any{"tag": "plain_text", "content": "前往审批"},
				"url":  strings.TrimRight(consoleURL, "/") + "/#/approvals"},
		}})
	}
	elements = append(elements, map[string]any{"tag": "note", "elements": []any{
		map[string]any{"tag": "plain_text", "content": "Vela 数据库网关 · " + ap.ApNo}}})
	return map[string]any{
		"config":   map[string]any{"wide_screen_mode": true},
		"header":   map[string]any{"template": tmpl, "title": map[string]any{"tag": "plain_text", "content": "高危命令待审批 · " + ap.ApNo}},
		"elements": elements,
	}
}
