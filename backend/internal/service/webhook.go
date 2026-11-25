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

	"velagateway/internal/apimsg"
	"velagateway/internal/gateway"
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

// cloudMetadataHosts / cloudMetadataIPs 是各家云的实例元数据入口。它们能吐出实例的临时凭据,
// 一个指向它们的推送地址就是一条把凭据送出去的路。
var (
	cloudMetadataHosts = map[string]bool{
		"metadata": true, "metadata.google.internal": true, "metadata.goog": true,
		"metadata.azure.com": true, "instance-data": true, "instance-data.ec2.internal": true,
	}
	cloudMetadataIPs = []net.IP{
		net.ParseIP("169.254.169.254"), // AWS / GCP / Azure / OpenStack / 华为云
		net.ParseIP("169.254.170.2"),   // AWS ECS 任务凭据
		net.ParseIP("100.100.100.200"), // 阿里云
		net.ParseIP("fd00:ec2::254"),   // AWS IPv6
	}
)

// ValidateWebhookEndpoint 是**保存时**对推送地址的检查(2026-10 黑盒测试,工单 02 第 4 条):
// 只认 http/https,拒绝 link-local(169.254.0.0/16、fe80::/10)与云元数据地址。
//
// 它与 AllowPrivateWebhookTargets 无关、始终生效:开发环境放开的是环回 / 内网(本机起的桩服务),
// 不是元数据地址 —— 那一类在哪个环境里都没有合法用途。私网地址在这里不挡:内网 SIEM 是常见的
// 接收方,真正发请求时的拨号检查(checkDialAddr)按部署环境另行把关。
func ValidateWebhookEndpoint(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return apimsg.Errorf("推送地址必须是 http:// 或 https:// 开头的地址")
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" {
		return apimsg.Errorf("推送地址缺少主机名")
	}
	if cloudMetadataHosts[host] {
		return apimsg.Errorf("推送地址不能指向云元数据服务:%s", host)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return nil
	}
	// NAT64 里裹着的 IPv4 照样拆出来看(与 isDisallowedIP 同一手法)。
	if ip16 := ip.To16(); ip16 != nil && nat64Prefix.Contains(ip16) {
		ip = net.IPv4(ip16[12], ip16[13], ip16[14], ip16[15])
	}
	for _, m := range cloudMetadataIPs {
		if ip.Equal(m) {
			return apimsg.Errorf("推送地址不能指向云元数据服务:%s", host)
		}
	}
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return apimsg.Errorf("推送地址不能是链路本地地址:%s", host)
	}
	return nil
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
		return apimsg.Errorf("无效的 URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return apimsg.Errorf("URL 必须使用 http/https")
	}
	host := u.Hostname()
	if host == "" {
		return apimsg.Errorf("URL 缺少主机名")
	}
	if AllowPrivateWebhookTargets {
		return nil
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return apimsg.Errorf("无法解析主机 %q: %w", host, err)
	}
	for _, ip := range ips {
		if isDisallowedIP(ip) {
			return apimsg.Errorf("目标解析到内网/环回地址，已拒绝(防 SSRF)")
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
			return apimsg.Errorf("重定向已被禁止(防 SSRF)")
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
	// Mask credential literals BEFORE anything leaves the building. This request
	// hands the command to a third-party approval service, which stores it, renders
	// it into a chat card and keeps it in its own history — the one sink where a
	// leaked password is furthest outside our control and least revocable. The
	// approver still sees the statement and the account it creates; only the secret
	// itself becomes '***', which is not information they need to decide.
	//
	// The interactive card built further down already did this. This path did not,
	// so a CREATE USER … IDENTIFIED BY '…' went out in the clear in both the
	// summary text and the payload.
	safeCommand := sqlutil.RedactSecrets(ap.Command)
	summary := fmt.Sprintf("%s/%s/%s 高危命令待审批:%s · 原因:%s · 风险:%s",
		ap.Env, ap.Instance, ap.Database, clip(safeCommand, 200), ap.Reason, ap.RiskLevel)
	body := map[string]any{
		"user":             userAccount,   // 发起人网关账户
		"message_id":       "gw-" + ap.ApNo, // 网关生成的确定性ID(幂等)
		"external_task_id": ap.ApNo,        // 回调关联主键(原样带回)
		"request_id":       ap.ApNo,        // 备用关联键
		"ai_group":         aiGroup,
		"callback_url":     callbackURL,
		"messages":         []map[string]string{{"role": "user", "content": summary}},
		"payload": map[string]any{
			// `env` keeps its meaning — where the command ran — but its value range
			// widens from the four fixed strings to any environment code an operator
			// creates (prod-hk, prod-sh, …). A downstream that validates it as a
			// closed enum will start rejecting tickets; see the contract note in
			// .scratch/env-tier-model/issues/03-dual-snapshot.md.
			//
			// `tier` is new and additive: the control level the command was judged
			// under, which is what actually explains why it needed approval.
			"env": ap.Env, "tier": ap.TierCode,
			"instance": ap.Instance, "database": ap.Database,
			"command": safeCommand, "risk": ap.RiskLevel, "initiator": ap.Initiator,
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
		return "", apimsg.Errorf("审批魔方返回 %d: %s", resp.StatusCode, clip(string(respBody), 200))
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
		return apimsg.Errorf("审批魔方 PATCH 返回 %d: %s", resp.StatusCode, clip(string(respBody), 200))
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

// platformAction 把审计行上存着的增删改查投影到事件中心的 action 枚举。
//
// 对方的枚举只有四个(create|read|update|delete,入库映射成 增|查|改|删),没有"其它"
// 这一格。所以 other —— 登录、配置变更、流程标签、认不出的语句 —— 只能挑一个送过去,
// 这里挑 read:它至少**不声称发生过一次数据变更**,而声称了就是在报假事件。
//
// 这是一次有损投影,而**真实的结论照样送得出去**:审计行整行在 raw_payload 里,
// 它自己的 dataAction 字段写着 other。对方的 action 列是给他们的报表用的,要准确答案的人
// 读 raw_payload。
//
// 空串(0040 之前写下的行)同样落到 read —— 那些行从来没有过这个结论,而枚举里没有"不知道"。
func platformAction(stored string) string {
	switch stored {
	case gateway.ActionCreate, gateway.ActionRead, gateway.ActionUpdate, gateway.ActionDelete:
		return stored
	}
	return gateway.ActionRead
}

// withDispatchMeta 把两样东西并进 raw_payload:**这条是什么事件**,以及**什么时候推出去的**。
//
// ## 为什么放在 raw_payload 里,而不是顶层
//
// 事件中心的入库表 platform_event 只有那么几列:event_uuid / source_system / occurred_at /
// action / resource / operator / summary / raw_payload / ingested_at(见接入文档「与表结构
// 映射关系」)。一个顶层的 event_type 没有列可落 —— 好一点是被接收端原样丢掉,差一点是撞上
// 字段校验让**每一次投递都失败**。raw_payload 是文档明说会原样入库的那一格,放这里才真的存
// 得下来。
//
// 而事件类型此前在请求体里**根本没有一个结构化的落点**:resource 一开始填的是它,但审计事件
// 会把 resource 覆盖成实例名;summary 里那个 [exec] 前缀是给人读的一句话,不是字段;剩下
// 只有 X-Vela-Event 这个头 —— 而接收端入库的是 body,头不会跟着进去。
//
// ## 三个时间各自回答什么
//
//   - occurred_at:这件事**发生**在什么时候(审计行自己的时间);
//   - pushed_at:网关**发出去**是什么时候 —— 新增的这个;
//   - ingested_at:接收端**收到**是什么时候(它自己盖的戳)。
//
// 三个摆在一起才看得出积压与重试:一条 occurred_at 与 pushed_at 差了 30 秒的事件,说明它重试
// 过;pushed_at 与 ingested_at 差很多,说明慢在网络或对方。只有一个时间的话,这些都看不出来。
//
// 并进去而不是包一层:raw_payload 现在是审计行本身,把它挪进 {data: …} 会让已经在读
// raw_payload.command 的接收端一起失效。新字段作为兄弟键加在旁边,老的路径原样还在。
func withDispatchMeta(data any, eventType string, pushedAt time.Time) map[string]any {
	out := map[string]any{}
	if data != nil {
		if raw, err := json.Marshal(data); err != nil || json.Unmarshal(raw, &out) != nil {
			// 不是一个 JSON 对象(数组、字符串、数字)就摊不开。原样挂到 data 下面,
			// 而不是把它丢掉 —— 丢掉的那一条正是接收端最需要的原始内容。
			out = map[string]any{"data": data}
		}
	}
	// 审计行自己也带一个 eventType(迁移 0039 起是表上的一列)。两者同值:recordAuditBy
	// 把同一个变量既写进行里、又传给这里。event_type 是**平台面**的拼写,与 source_system /
	// occurred_at 一致,而且非审计事件(连通性测试)也要有它,所以两个都留着。
	out["event_type"] = eventType
	out["pushed_at"] = pushedAt.Format(platformTimeLayout)
	return out
}

// buildPlatformEvent maps an internal audit event onto the Event Center ingest
// schema (docs/事件中心平台接入文档.md): source_system / occurred_at / action /
// resource / operator / summary / raw_payload. The original audit row travels
// verbatim in raw_payload so no detail is lost — plus event_type / pushed_at,
// see withDispatchMeta.
func buildPlatformEvent(eventType string, data any, pushedAt time.Time, source string) map[string]any {
	ev := map[string]any{
		"source_system": source,
		"occurred_at":   time.Now().Format(platformTimeLayout),
		"action":        "read",
		"resource":      eventType,
		"operator":      "system",
		"summary":       eventType,
		"raw_payload":   withDispatchMeta(data, eventType, pushedAt),
	}
	if a, ok := data.(*model.AuditLog); ok && a != nil {
		if !a.OccurredAt.IsZero() {
			ev["occurred_at"] = a.OccurredAt.Format(platformTimeLayout)
		}
		// 用**行上存着的**结论,不在这里重算:重算等于让推出去的和存下来的各说各话,
		// 而且这里只看得到命令文本,看不到写行那一刻的判断(见 model.AuditLog.DataAction)。
		ev["action"] = platformAction(a.DataAction)
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
// Dispatch 推一条事件。
//
// **订阅按「增删改查」匹配**(action),不是按事件类型。一个把审计流接到 SIEM 的人想说的
// 通常是"所有删除都推给我",而不是"所有被拦下来的推给我" —— 后者按类型分,前者按这条命令
// 对数据做了什么分。eventType 仍然一路带着走(进请求头与 raw_payload),它只是不再决定推不推。
//
// action 由调用方从审计行上取(a.DataAction),不在这里从 data 里反射 —— 从一个 any 里挖
// 出决定"推不推"的那个值,一旦哪天传进来的不是审计行,表现是**什么都不推**而且没人看得出来。
//
// **再按环境过滤**(cfg.Envs,空 = 全部):env 是审计行上那一刻的环境快照(a.Env)。没有环境的
// 事件(登录、改设置,不属于任何实例)不受环境约束 —— 它们本来就说不上"哪个环境的",按环境
// 挡掉只会让勾了「非数据」的人收不到。
func (d *Dispatcher) Dispatch(eventType, action, env string, data any) {
	cfg, err := d.repo.GetWebhook()
	if err != nil || cfg == nil || !cfg.Enabled {
		return
	}
	if !strings.Contains(","+cfg.Events+",", ","+action+",") {
		return
	}
	if !envSubscribed(cfg.Envs, env) {
		return
	}
	// 同步建好这份事件体,**不在下面的 goroutine 里碰 data**:withDispatchMeta 走了一趟
	// marshal/unmarshal,raw_payload 里已经是审计行的一份快照,调用方之后怎么动那个对象
	// 都影响不到这里。pushed_at 每次尝试再刷新(见循环里)。
	ev := buildPlatformEvent(eventType, data, time.Now(), LoadBrand(d.repo).Text(BrandDefaultUI, true))
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
			// 推送时间按**这一次**尝试算。重试可能在 30 秒之后发出去,沿用第一次的时间会让
			// 这个字段变成一句谎话 —— 而它存在的意义恰恰是让接收端看出投递延迟和重试。
			pushedAt := time.Now()
			if rp, ok := ev["raw_payload"].(map[string]any); ok {
				rp["pushed_at"] = pushedAt.Format(platformTimeLayout)
			}
			body, _ := json.Marshal(ev)
			req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
			if err != nil {
				d.record(eventType, endpoint, false, "bad request: "+err.Error(), attempts)
				return
			}
			req.Header.Set("Content-Type", "application/json")
			if secret != "" {
				req.Header.Set("Authorization", "Bearer "+secret)
			}
			// 两个头和 raw_payload 里的两个字段是同一件事,给的是不同的读者:头方便代理、
			// 网关、日志按事件类型分流,而入库的是 body。
			req.Header.Set("X-Vela-Event", eventType)
			req.Header.Set("X-Vela-Pushed-At", pushedAt.Format(platformTimeLayout))
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

// envSubscribed:推送环境清单为空 = 全部;事件没有环境 = 不受环境约束。
func envSubscribed(envs, env string) bool {
	if envs == "" || env == "" {
		return true
	}
	return strings.Contains(","+envs+",", ","+env+",")
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
	// 测试事件走和真事件**同一条组装路径**:连通性测试要能回答"真事件长什么样",
	// 各搭一份的话,测试通过而真事件被对方拒掉的情形就查不出来。
	pushedAt := time.Now()
	brand := LoadBrand(d.repo)
	ev := map[string]any{
		"source_system": brand.Text(BrandDefaultUI, true),
		"occurred_at":   pushedAt.Format(platformTimeLayout),
		"action":        "read",
		"resource":      "webhook/test",
		"operator":      "system",
		"summary":       brand.Text(BrandDefaultUI, false) + " Webhook 连通性测试",
		"raw_payload": withDispatchMeta(
			map[string]any{"event": "test", "ping": "dp-db-gateway"}, "test", pushedAt),
	}
	body, _ := json.Marshal(ev)
	req, err := http.NewRequest(http.MethodPost, cfg.Endpoint, bytes.NewReader(body))
	if err != nil {
		return false, err.Error()
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.Secret != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.Secret)
	}
	req.Header.Set("X-Vela-Event", "test")
	req.Header.Set("X-Vela-Pushed-At", pushedAt.Format(platformTimeLayout))
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
	card := larkApprovalCard(ap, consoleURL, LoadBrand(d.repo).Text(BrandDefaultLegacy, false))
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
		ApNo: "AP-TEST", Instance: "order-cluster", Env: "prod", TierCode: "prod", RiskLevel: "high",
		Initiator: "Vela", Command: "DROP TABLE orders_2024_q3;", Reason: "测试飞书审批卡片推送",
	}
	ok, msg := d.postLark(webhook, secret, larkApprovalCard(sample, consoleURL, LoadBrand(d.repo).Text(BrandDefaultLegacy, false)))
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
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
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

func larkApprovalCard(ap *model.Approval, consoleURL, brand string) map[string]any {
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
		map[string]any{"tag": "plain_text", "content": brand + " · " + ap.ApNo}}})
	return map[string]any{
		"config":   map[string]any{"wide_screen_mode": true},
		"header":   map[string]any{"template": tmpl, "title": map[string]any{"tag": "plain_text", "content": "高危命令待审批 · " + ap.ApNo}},
		"elements": elements,
	}
}
