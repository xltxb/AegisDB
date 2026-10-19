# 多渠道 IM 审批 · 第 3 期（飞书交互审批）Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 飞书从「只能发群通知」升级为交互审批渠道：审批卡片私信给已绑定的链上成员，点「通过 / 驳回」即决定；私聊机器人发绑定码完成绑定；入站默认走飞书长连接，可选 webhook。

**Architecture:** `internal/imchannel/lark` 新增 `*App`（开放平台自建应用）：出站（换 tenant_access_token、发卡片、改卡片、查机器人名、回私聊）用标准库直接调飞书 OpenAPI，与 Telegram 适配器同一个写法；入站用官方 SDK `larksuite/oapi-sdk-go/v3` 的长连接客户端 + 事件分发器（`im.message.receive_v1` 私聊、`card.action.trigger` 卡片回调），并把网关带 SSRF 防护的 HTTP 客户端和拨号器注入 SDK。webhook 模式复用同一个分发器，验签、解密、Verification Token 由适配器自己做（常量时间）。为了让飞书的 URL 校验与卡片回调能回响应体，`Webhooker` 的返回值从状态码升级为 `WebhookReply`。判定仍只在 `DecideBlockFor`。另建 `lark/larktest` 假开放平台：REST 接口 + 用 SDK 自己的 protobuf 帧扮演长连接网关，单元测试和端到端测试都不连真实飞书。

**Tech Stack:** Go 1.25 · 新依赖 `github.com/larksuite/oapi-sdk-go/v3 v3.12.2`（只用 `ws`、`event`、`event/dispatcher`、`event/dispatcher/callback`、`service/im/v1`、`core` 包；它带进 `github.com/gogo/protobuf`）· 已有的 `github.com/gorilla/websocket` · React 19 + TS。

**Spec:** `docs/superpowers/specs/2026-09-27-im-channel-approval-design.md`（§1 信任模型、§2.2 接口、§2.4 多实例、§4 绑定、§5 出站、§6 入站、§7 决策、§8 收卡、§9 配置、§14 分期、§15 失败朝严、§16 测试；§18、§19 是前两期的实现记录）。

**范围说明：** spec §14 的第 3 期。飞书群通知（第 1 期的自定义机器人）保持不变；本期把飞书登记为 `Caps{Notify: true, Approve: true}`。

## Global Constraints

- 模块名 `velagateway`，Go 1.25；后端命令都在 `backend/` 下执行；跑后端测试需要 `createdb vela_test`，**绝不用** `vela_gateway`。
- 本机没有 MySQL：`internal/osc` 整包与 bootstrap 的 `TestReleaseE2E_TheIndexChangeIsMadeByOSCAndTheReleaseCompletes` 是已知基线失败，不算回归。bootstrap 整包约 5 分钟，前台跑。
- 本期**没有**数据库迁移。新配置键全部落在 `tbl_setting`。
- 配置键（spec §9）：`im.lark.approval`（bool）、`im.lark.appID`、`im.lark.appSecret`🔒、`im.lark.domain`（`feishu` | `lark`，缺省 feishu）、`im.lark.mode`（`ws` | `webhook`，缺省 ws）、`im.lark.encryptKey`🔒、`im.lark.verificationToken`🔒、`im.lark.apiBase`（缺省按 domain：`https://open.feishu.cn` / `https://open.larksuite.com`；私有化部署与测试时填）。群通知键 `im.lark.notify` / `notifyWebhook` / `notifySecret`🔒 不变。`im.` 前缀且 `isSecretKey` 为真的键自动加密；**`encryptKey` 名字里没有 secret / token，本期要让 `isSecretKey` 认得它**（Task 5）。
- webhook 路径固定为 `/api/v1/im/lark/webhook`（`lark.WebhookPath`）；事件与卡片回调都推到这一个地址。
- 卡片按钮的 `value`：`{"ap": "<ApNo>", "act": "a" | "r"}`。ApNo 只认 `^[A-Za-z0-9-]{1,32}$`。按钮数据不带任何权限信息。
- 卡片的 msgRef：`<message_id>|<ApNo>`（message_id 形如 `om_…`）。收卡用「更新应用发送的消息卡片」（`PATCH /open-apis/im/v1/messages/:message_id`）整张替换，所以卡片 `config` 必须带 `"update_multi": true`。
- 卡片回调必须 3 秒内应答，toast 放在回调响应体里（长连接由 SDK 回写，webhook 由 `WebhookReply.Body` 回写）。
- 失败朝严（spec §15）：渠道未开交互审批 → webhook 403、长连接不启动；webhook 模式缺 Encrypt Key 或 Verification Token → 403；明文（未加密）请求、解不开、Token 不符、签名不符 → 403；来源 IP 不在白名单（非空时）→ 403；鉴权通过后处理失败 → 200 + 日志。所有密钥比较用 `crypto/subtle`。
- 出站只用 `Deps.HTTP`（带 SSRF 防护）。SDK 长连接的两条出网路径都要注入：`ws.WithHttpClient(d.HTTP)`（取连接地址）与 `ws.WithWebSocketDialer`（`NetDialContext` 取自 `d.HTTP.Transport.(*http.Transport).DialContext`）。拿不到受控拨号器时 `Run` 报错，**不许**退回默认拨号器。
- SDK 的日志接进 `slog`，**Debug 级一律丢弃**：SDK 在 Debug 级会打印整条事件 payload，里面有用户私聊发来的绑定码。
- 永久性错误（凭据被拒、应用没开机器人能力、开放平台地址填错导致的 4xx）要让 `Run` 返回错误，进 `LastError`；网络错误、5xx、频控（`99991400`）退避重试。这是第 2 期 Telegram 收口时学到的。
- Builder 与 `BotName` 在 Hub 锁内调用，必须廉价、不做 I/O；查机器人名在 `Run` 里做，结果缓存。
- 代码注释沿用仓库风格（中文，讲清「为什么」）；提交信息英文 conventional commits，结尾 `Co-Authored-By: <执行提交的模型名> <noreply@anthropic.com>`。
- 前端验证：`cd frontend && npm run build && npm run lint && npm run test:unit`。

## Review Focus

spec 没有逐条写出、但最可能伤到使用者的五类输入。每条的测试放进负责它的任务：

1. **飞书重投同一条私聊**：飞书对没及时确认的事件会重推；绑定码第二次到达时已被消费，用户会在「已绑定」之后又收到「无效或已过期的绑定码」。按 `message_id` 去重（Task 3 `TestRun_ARedeliveredMessageIsHandledOnce`）。
2. **发起人在「原因」里写一段 markdown 链接**：`[查看详情](https://evil)` 若按 lark_md 渲染，就在审批人的卡片里变成一个真链接。原因改用 `plain_text`（Task 2 `TestSendCard_ShowsTheReasonVerbatim`）。
3. **App Secret / 凭据错误**：设置页必须显示原因（停止运行 + LastError），且 LastError、日志里不能出现 App Secret（Task 2 `TestCall_ErrorsCarryLarksMessageButNotTheSecret`，Task 5 `TestLark_BadCredentialsShowInStatusWithoutTheSecret`）。
4. **缓存的 tenant_access_token 被提前作废**（管理员在后台重置了 Secret、token 过期边界）：飞书回 `99991663` 时换新 token 重试一次，而不是之后每张卡片都失败（Task 2 `TestCall_RefreshesARejectedToken`）。
5. **webhook 被伪造**：公网上的 `/api/v1/im/lark/webhook` 收到明文请求、错 Token、错签名、篡改过的密文 —— 一律 403，且不进 sink（Task 4 `TestHandleWebhook_*`，Task 5 `TestLark_WebhookModeEndToEnd`）。

---

## 文件结构

**新建（后端）**

| 文件 | 职责 |
|---|---|
| `backend/internal/imchannel/lark/app.go` | `*App`：构造、OpenAPI 调用（token 缓存与刷新、错误分类）、发卡 / 收卡 / 回私聊、`BotName` |
| `backend/internal/imchannel/lark/card.go` | 交互卡片与收起后的卡片 |
| `backend/internal/imchannel/lark/inbound.go` | `Run`（长连接 / webhook 待命）、事件分发器、点击与私聊处理、去重、SDK 日志适配 |
| `backend/internal/imchannel/lark/webhook.go` | `HandleWebhook`：解密、Token、签名、URL 校验、分发 |
| `backend/internal/imchannel/lark/larktest/server.go` | 假开放平台：REST 接口、调用记录、故障注入 |
| `backend/internal/imchannel/lark/larktest/ws.go` | 假长连接网关：发放连接地址、WebSocket、推事件并收回执 |
| `backend/internal/imchannel/lark/larktest/events.go` | 事件 payload、加密与签名工具 |
| `backend/internal/imchannel/lark/app_test.go` `card_test.go` `inbound_test.go` `webhook_test.go` | 适配器测试 |
| `backend/internal/bootstrap/im_lark_test.go` | 飞书端到端测试 |

**修改（后端）**：`imchannel/channel.go`（`WebhookReply`）、`imchannel/hub.go`（`ServeWebhook` 返回值）、`imchannel/imtest/fake.go`、`imchannel/hub_webhook_test.go`、`imchannel/telegram/inbound.go` 与 `inbound_test.go`、`handler/im.go`（写响应体）、`handler/admin.go`（`isSecretKey`、新键校验）、`imchannel/lark/lark.go`（`New` 分形态、原因改 plain_text、未配 webhook 的报错）、`imchannel/lark/lark_test.go`（`deps` 带受控 Transport）、`service/service.go`（登记能力）、`bootstrap/im_webhook_test.go`（带响应体的 helper）、`bootstrap/im_settings_test.go`（飞书现在能审批）、`backend/docs/openapi.yaml`、`backend/go.mod` / `go.sum`。

**修改（前端）**：`components/settings/ImChannelsPanel.tsx`、`locales/zh.ts`、`locales/en.ts`。

**修改（文档）**：`docs/external-approval-setup.md`、spec（新增 §20）、`docs/features.md`、`docs/adr/0020-im-channel-approval.md`（状态行）。

---

### Task 1: webhook 应答能带响应体

飞书 webhook 有两处必须回响应体：开发者后台保存请求地址时的 URL 校验（回 `{"challenge": …}`），以及卡片回调（toast 放在响应体里）。第 2 期的 `Webhooker` 只能回一个状态码。

**Files:**
- Modify: `backend/internal/imchannel/channel.go`
- Modify: `backend/internal/imchannel/hub.go`（`ServeWebhook`）
- Modify: `backend/internal/handler/im.go`（`IMWebhook`）
- Modify: `backend/internal/imchannel/imtest/fake.go`（`HandleWebhook`）
- Modify: `backend/internal/imchannel/telegram/inbound.go`（`HandleWebhook`）
- Test: `backend/internal/imchannel/hub_webhook_test.go`、`backend/internal/imchannel/telegram/inbound_test.go`、`backend/internal/bootstrap/im_webhook_test.go`
- Modify: `backend/docs/openapi.yaml`（`/im/{channel}/webhook` 的 200 响应说明）

**Interfaces:**
- Produces:
  ```go
  type WebhookReply struct {
      Status int
      Body   []byte // 非空时以 application/json 写回
  }
  func Reply(status int) WebhookReply
  type Webhooker interface {
      HandleWebhook(ctx context.Context, header http.Header, body []byte, sink ActionSink) WebhookReply
  }
  func (h *Hub) ServeWebhook(ctx context.Context, kind string, header http.Header, body []byte) WebhookReply
  ```
  bootstrap 测试 helper：`func (a *testApp) postIMWebhookFull(channel string, header map[string]string, body string) (int, string)`；原 `postIMWebhook` 保留，改为调用它并只返回状态码。
- 假渠道 `imtest.Fake.HandleWebhook` 鉴权通过后回 `{"toast":"<OnAction 的 toast>"}`。

- [ ] **Step 1: 写失败的 Hub 测试**

在 `backend/internal/imchannel/hub_webhook_test.go` 末尾加：

```go
func TestHub_ServeWebhookPassesTheReplyBodyThrough(t *testing.T) {
	s, st := newSettings(), newStore()
	s.set(imchannel.Key("a", "approval"), true)
	f := imtest.New("a")
	f.WebhookSecret = "s3"
	h := newHub(t, s, st)
	h.Register("a", both, f.Builder())
	h.SetSink(nopSink{})
	h.Reload()

	r := h.ServeWebhook(context.Background(), "a", http.Header{"X-Fake-Secret": []string{"s3"}}, []byte(`{"pid":"p","apNo":"AP-1","approve":true}`))
	if r.Status != http.StatusOK || !strings.Contains(string(r.Body), `"toast"`) {
		t.Fatalf("reply = %d %s, want 200 with the channel's body", r.Status, r.Body)
	}
}
```

（`newSettings`、`newStore`、`newHub(t, s, st)`、`both`、`nopSink` 都是 `hub_test.go` 里已有的 helper，`hub_webhook_test.go` 属于 `package imchannel_test`。import 补 `strings`。）

- [ ] **Step 2: 跑测试确认失败**

Run: `cd backend && go test ./internal/imchannel/ -run TestHub_ServeWebhookPassesTheReplyBodyThrough`
Expected: 编译失败（`r.Status undefined`）

- [ ] **Step 3: 改接口**

`backend/internal/imchannel/channel.go`，把 `Webhooker` 整段替换为：

```go
// WebhookReply 是一次 webhook 请求的应答。多数平台只看状态码;飞书保存请求地址时要回
// challenge,卡片回调要在响应体里带 toast —— 所以应答不止一个数字。
type WebhookReply struct {
	Status int
	Body   []byte // 非空时以 application/json 写回
}

// Reply 是只有状态码的应答。
func Reply(status int) WebhookReply { return WebhookReply{Status: status} }

// Webhooker 是可选能力:支持「平台把事件推过来」的渠道实现它。
//
// 它自己校验这次请求是不是真的来自平台(secret、签名):校验不过回 403;校验通过后,
// 不论处理成败都回 200 —— 平台把非 2xx 当成投递失败会一直重投,而重投不会让一次坏的
// 处理变好。它拿到的 sink 已由 Hub 钉住渠道名。
type Webhooker interface {
	HandleWebhook(ctx context.Context, header http.Header, body []byte, sink ActionSink) WebhookReply
}
```

`backend/internal/imchannel/hub.go` 的 `ServeWebhook`：签名改为 `(reply WebhookReply)`，`return http.StatusNotFound` → `return Reply(http.StatusNotFound)`，`return http.StatusForbidden` → `return Reply(http.StatusForbidden)`，recover 里 `status = http.StatusOK` → `reply = Reply(http.StatusOK)`。

`backend/internal/handler/im.go` 的 `IMWebhook` 最后一行替换为：

```go
	r := h.Svc.IM.ServeWebhook(c.Request.Context(), c.Param("channel"), c.Request.Header, body)
	if len(r.Body) > 0 {
		c.Data(r.Status, "application/json; charset=utf-8", r.Body)
		return
	}
	c.Status(r.Status)
```

`backend/internal/imchannel/imtest/fake.go` 的 `HandleWebhook`：返回类型改为 `imchannel.WebhookReply`；403 处 `return imchannel.Reply(http.StatusForbidden)`；body 解析失败处 `return imchannel.Reply(http.StatusOK)`；末尾改为：

```go
	b, _ := json.Marshal(map[string]string{"toast": res.Toast})
	return imchannel.WebhookReply{Status: http.StatusOK, Body: b}
```

`backend/internal/imchannel/telegram/inbound.go` 的 `HandleWebhook`：返回类型改为 `imchannel.WebhookReply`，每个 `return http.StatusX` 改为 `return imchannel.Reply(http.StatusX)`。Telegram 不需要响应体。

- [ ] **Step 4: 修其它调用点**

- `hub_webhook_test.go` 里 `check(h.ServeWebhook(...), code, msg)` 改为 `check(h.ServeWebhook(...).Status, code, msg)`；`panicHook.HandleWebhook` 的返回类型改为 `imchannel.WebhookReply`（返回 `imchannel.Reply(http.StatusOK)`），对应断言用 `.Status`。
- `telegram/inbound_test.go` 的 `TestHandleWebhook_AuthenticatesThenDispatches`：`got := ...HandleWebhook(...)` 之后比较 `got.Status`。
- `bootstrap/im_webhook_test.go`：把 `postIMWebhook` 改成：

```go
// postIMWebhookFull 直接打网关的 webhook 入口(不带 JWT),返回状态码与响应体。
func (a *testApp) postIMWebhookFull(channel string, header map[string]string, body string) (int, string) {
	a.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, a.srv.URL+"/api/v1/im/"+channel+"/webhook", bytes.NewReader([]byte(body)))
	for k, v := range header {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatalf("webhook: %v", err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func (a *testApp) postIMWebhook(channel string, header map[string]string, body string) int {
	a.t.Helper()
	code, _ := a.postIMWebhookFull(channel, header, body)
	return code
}
```

（补 `io` import。）

- [ ] **Step 5: 写端到端断言**

在 `bootstrap/im_webhook_test.go` 里，找到用 `webhookFake` 走通一次合法请求的那个测试，在它断言 200 的地方改用 `postIMWebhookFull`，并加：

```go
	if !strings.Contains(respBody, `"toast"`) {
		t.Fatalf("the channel's reply body must reach the platform, got %q", respBody)
	}
```

（`respBody` 是 `postIMWebhookFull` 的第二个返回值。）

- [ ] **Step 6: 跑测试**

Run: `cd backend && go vet ./internal/imchannel/... ./internal/handler/ && go test -race ./internal/imchannel/... && go test ./internal/bootstrap -run 'TestIMWebhook|TestTelegram|TestIM'`
Expected: 全部 PASS

- [ ] **Step 7: 同步 openapi**

`backend/docs/openapi.yaml` 里 `/im/{channel}/webhook` 的 `200` 说明改为：「已受理。部分渠道（飞书）在响应体里回 JSON：URL 校验的 challenge，或卡片回调的 toast」。

- [ ] **Step 8: 提交**

```bash
git add backend/internal/imchannel backend/internal/handler/im.go backend/internal/bootstrap/im_webhook_test.go backend/docs/openapi.yaml
git commit -m "feat(im): let a webhook reply carry a body

Co-Authored-By: <model> <noreply@anthropic.com>"
```

---

### Task 2: 飞书应用的出站：OpenAPI 客户端、卡片、发卡与收卡

**Files:**
- Create: `backend/internal/imchannel/lark/app.go`
- Create: `backend/internal/imchannel/lark/card.go`
- Create: `backend/internal/imchannel/lark/larktest/server.go`
- Modify: `backend/internal/imchannel/lark/lark.go`（`New` 分形态；未配 webhook 的群通知报错；原因改 plain_text）
- Modify: `backend/internal/imchannel/lark/lark_test.go`（`deps` 的 HTTP 客户端带一个 `*http.Transport`）
- Test: `backend/internal/imchannel/lark/app_test.go`、`backend/internal/imchannel/lark/card_test.go`

**Interfaces:**
- Consumes: `imchannel.Deps`、`imchannel.ApprovalCard`、`imchannel.CardState`、`imchannel.Key`、`imchannel.Lark`。
- Produces（同包内，后续任务用）：
  ```go
  type App struct { *Channel; appID, secret, base, mode, encryptKey, verifyToken string; client *http.Client; validate func(string) error; bot atomic.Value; tokMu sync.Mutex; tok string; tokExp time.Time }
  func New(d imchannel.Deps) imchannel.Channel               // *App | *Channel | nil
  func (a *App) BotName() string
  func (a *App) SendCard(ctx context.Context, openID string, c imchannel.ApprovalCard) (string, error) // ref = "<message_id>|<ApNo>"
  func (a *App) CloseCard(ctx context.Context, ref string, final imchannel.CardState) error
  func (a *App) botInfo(ctx context.Context) (string, error)
  func (a *App) sendText(ctx context.Context, openID, text string) error
  func (a *App) retry(ctx context.Context, fn func() error) error
  func permanent(err error) bool
  const WebhookPath = "/api/v1/im/lark/webhook"; const modeWS = "ws"; const modeWebhook = "webhook"
  ```
- larktest（Task 3、4、5 扩展它）：
  ```go
  type Call struct { Method string; Params map[string]any }   // Method: token | botInfo | send | patch | wsEndpoint
  type Failure struct { HTTP, Code int; Msg string }
  type Server struct { *httptest.Server; AppID, AppSecret, BotName, VerificationToken, EncryptKey string; ... }
  func New(t testing.TB) *Server
  func (s *Server) SetFail(method string, f Failure)
  func (s *Server) RotateToken()
  func (s *Server) Calls(method string) []Call
  func (s *Server) WaitCall(t testing.TB, method string, match func(Call) bool) Call
  func (c Call) Str(key string) string
  ```

- [ ] **Step 1: 写假开放平台**

Create `backend/internal/imchannel/lark/larktest/server.go`：

```go
// Package larktest 是一个假的飞书开放平台:记录每次 OpenAPI 调用,按需注入失败,并扮演
// 长连接网关把事件推给 SDK(ws.go)。适配器和端到端测试都用它,不连真实的飞书。
package larktest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type Call struct {
	Method string // token | botInfo | send | patch | wsEndpoint
	Params map[string]any
}

// Failure 让某个方法回一个飞书错误。HTTP 为 0 时按 400 回。
type Failure struct {
	HTTP int
	Code int
	Msg  string
}

type Server struct {
	*httptest.Server
	AppID             string
	AppSecret         string
	BotName           string
	VerificationToken string
	EncryptKey        string

	mu      sync.Mutex
	calls   []Call
	fail    map[string]Failure
	tenant  string
	rotated int
	nextMsg int
}

func New(t testing.TB) *Server {
	s := &Server{
		AppID: "cli_test_app", AppSecret: "test-app-secret-0000", BotName: "AegisDB 测试机器人",
		VerificationToken: "test-verification-token", EncryptKey: "test-encrypt-key",
		fail: map[string]Failure{}, tenant: "t-tenant-0", nextMsg: 100,
	}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.Close)
	return s
}

func (s *Server) SetFail(method string, f Failure) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fail[method] = f
}

// RotateToken 作废当前的 tenant_access_token:之后带旧 token 的调用回 99991663。
func (s *Server) RotateToken() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rotated++
	s.tenant = fmt.Sprintf("t-tenant-%d", s.rotated)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	var params map[string]any
	_ = json.NewDecoder(r.Body).Decode(&params)
	if params == nil {
		params = map[string]any{}
	}
	var method string
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
		method = "token"
	case r.Method == http.MethodGet && r.URL.Path == "/open-apis/bot/v3/info":
		method = "botInfo"
	case r.Method == http.MethodPost && r.URL.Path == "/open-apis/im/v1/messages":
		method = "send"
		params["receive_id_type"] = r.URL.Query().Get("receive_id_type")
	case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/open-apis/im/v1/messages/"):
		method = "patch"
		params["message_id"] = strings.TrimPrefix(r.URL.Path, "/open-apis/im/v1/messages/")
	default:
		http.NotFound(w, r)
		return
	}
	s.mu.Lock()
	s.calls = append(s.calls, Call{Method: method, Params: params})
	f, failing := s.fail[method]
	tenant := s.tenant
	s.mu.Unlock()
	if failing {
		status := f.HTTP
		if status == 0 {
			status = http.StatusBadRequest
		}
		writeJSON(w, status, map[string]any{"code": f.Code, "msg": f.Msg})
		return
	}
	if method == "token" {
		if params["app_id"] != s.AppID || params["app_secret"] != s.AppSecret {
			writeJSON(w, http.StatusBadRequest, map[string]any{"code": 10014, "msg": "app secret invalid"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"code": 0, "msg": "ok", "tenant_access_token": tenant, "expire": 7200})
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+tenant {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": 99991663, "msg": "Invalid access token for authorization"})
		return
	}
	switch method {
	case "botInfo":
		writeJSON(w, http.StatusOK, map[string]any{"code": 0, "msg": "ok", "bot": map[string]any{"app_name": s.BotName, "open_id": "ou_bot"}})
	case "send":
		s.mu.Lock()
		s.nextMsg++
		id := fmt.Sprintf("om_test%d", s.nextMsg)
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"code": 0, "msg": "success", "data": map[string]any{"message_id": id}})
	default:
		writeJSON(w, http.StatusOK, map[string]any{"code": 0, "msg": "success", "data": map[string]any{}})
	}
}

func (s *Server) Calls(method string) []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Call
	for _, c := range s.calls {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

// WaitCall 等到出现一次满足 match 的调用(match 为 nil 表示任意一次)。
func (s *Server) WaitCall(t testing.TB, method string, match func(Call) bool) Call {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, c := range s.Calls(method) {
			if match == nil || match(c) {
				return c
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("larktest: no %s call matched within 5s; got %+v", method, s.Calls(method))
	return Call{}
}

// Str 取一个参数的字符串形式;非字符串的值按 JSON 返回。
func (c Call) Str(key string) string {
	switch v := c.Params[key].(type) {
	case string:
		return v
	case nil:
		return ""
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}
```

- [ ] **Step 2: 写失败的测试**

`backend/internal/imchannel/lark/lark_test.go` 里把 `deps` 换成带 Transport 的客户端（长连接要从里面取受控拨号器，Task 3 用到），并加一个构造 helper：

```go
func deps(s kv) imchannel.Deps {
	return imchannel.Deps{
		Settings:    s,
		HTTP:        &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DialContext: (&net.Dialer{}).DialContext}},
		ValidateURL: func(string) error { return nil },
	}
}

// newTestApp 造一个指向假开放平台的 *App。
func newTestApp(t *testing.T, srv *larktest.Server, extra kv) *App {
	t.Helper()
	s := kv{"im.lark.appID": srv.AppID, "im.lark.appSecret": srv.AppSecret, "im.lark.apiBase": srv.URL}
	for k, v := range extra {
		s[k] = v
	}
	a, _ := New(deps(s)).(*App)
	if a == nil {
		t.Fatal("a configured app must build")
	}
	return a
}
```

（补 `net`、`time`、`larktest` import。）

Create `backend/internal/imchannel/lark/app_test.go`：

```go
package lark

import (
	"context"
	"errors"
	"strings"
	"testing"

	"velagateway/internal/imchannel"
	"velagateway/internal/imchannel/lark/larktest"
)

func TestNew_PicksTheShapeFromWhatIsConfigured(t *testing.T) {
	if ch := New(deps(kv{})); ch != nil {
		t.Fatalf("nothing configured → nil, got %#v", ch)
	}
	if _, ok := New(deps(kv{"im.lark.notifyWebhook": "https://h"})).(*Channel); !ok {
		t.Fatal("only a group webhook → the notify-only *Channel")
	}
	if _, ok := New(deps(kv{"im.lark.appID": "cli_x"})).(*App); ok {
		t.Fatal("an App ID without its secret is not an app")
	}
	a, ok := New(deps(kv{"im.lark.appID": "cli_x", "im.lark.appSecret": "s"})).(*App)
	if !ok || a.base != "https://open.feishu.cn" || a.mode != modeWS {
		t.Fatalf("defaults: %#v", a)
	}
	a, _ = New(deps(kv{"im.lark.appID": "cli_x", "im.lark.appSecret": "s", "im.lark.domain": "lark", "im.lark.mode": "webhook"})).(*App)
	if a.base != "https://open.larksuite.com" || a.mode != modeWebhook {
		t.Fatalf("lark edition / webhook: base=%s mode=%s", a.base, a.mode)
	}
}

func TestApp_NotifyWithoutAGroupWebhookSaysSo(t *testing.T) {
	a := New(deps(kv{"im.lark.appID": "cli_x", "im.lark.appSecret": "s"})).(*App)
	err := a.NotifyApproval(context.Background(), imchannel.ApprovalCard{ApNo: "AP-1"})
	if err == nil || !strings.Contains(err.Error(), "未配置群通知") {
		t.Fatalf("err = %v", err)
	}
}

func TestSendCard_PostsAnInteractiveCardToTheOpenID(t *testing.T) {
	srv := larktest.New(t)
	a := newTestApp(t, srv, nil)
	ref, err := a.SendCard(context.Background(), "ou_1", imchannel.ApprovalCard{ApNo: "AP-1", Command: "DROP TABLE t;", Risk: "high"})
	if err != nil {
		t.Fatal(err)
	}
	if ref != "om_test101|AP-1" {
		t.Fatalf("ref = %q, want <message_id>|<ApNo>", ref)
	}
	c := srv.Calls("send")[0]
	if c.Str("receive_id_type") != "open_id" || c.Str("receive_id") != "ou_1" || c.Str("msg_type") != "interactive" {
		t.Fatalf("send params = %+v", c.Params)
	}
	for _, want := range []string{`"act":"a"`, `"act":"r"`, `"ap":"AP-1"`, `"update_multi":true`, "DROP TABLE t;"} {
		if !strings.Contains(c.Str("content"), want) {
			t.Errorf("card missing %s: %s", want, c.Str("content"))
		}
	}
}

func TestCloseCard_ReplacesTheCardWithItsFinalState(t *testing.T) {
	srv := larktest.New(t)
	a := newTestApp(t, srv, nil)
	final := imchannel.CardState{Status: "approved", Text: "已由 李四(飞书) 通过"}
	if err := a.CloseCard(context.Background(), "om_test7|AP-1", final); err != nil {
		t.Fatal(err)
	}
	c := srv.Calls("patch")[0]
	content := c.Str("content")
	if c.Str("message_id") != "om_test7" || !strings.Contains(content, "工单 AP-1") ||
		!strings.Contains(content, "已由 李四(飞书) 通过") || !strings.Contains(content, `"template":"green"`) ||
		!strings.Contains(content, `"update_multi":true`) {
		t.Fatalf("patch = %+v", c.Params)
	}
	if strings.Contains(content, `"act"`) {
		t.Fatalf("a closed card must have no buttons: %s", content)
	}
	if err := a.CloseCard(context.Background(), "garbage", final); err == nil {
		t.Fatal("an unparsable ref is an error")
	}
}

func TestCall_RefreshesARejectedToken(t *testing.T) {
	srv := larktest.New(t)
	a := newTestApp(t, srv, nil)
	if _, err := a.SendCard(context.Background(), "ou_1", imchannel.ApprovalCard{ApNo: "AP-1"}); err != nil {
		t.Fatal(err)
	}
	srv.RotateToken() // 后台重置了 Secret、或 token 被提前作废
	if _, err := a.SendCard(context.Background(), "ou_1", imchannel.ApprovalCard{ApNo: "AP-2"}); err != nil {
		t.Fatalf("a rejected cached token must be replaced and the call retried once: %v", err)
	}
	if n := len(srv.Calls("token")); n != 2 {
		t.Fatalf("token fetched %d times, want 2", n)
	}
}

func TestCall_ErrorsCarryLarksMessageButNotTheSecret(t *testing.T) {
	srv := larktest.New(t)
	a := newTestApp(t, srv, nil)
	srv.SetFail("send", larktest.Failure{Code: 230013, Msg: "Bot has NO availability to this user."})
	_, err := a.SendCard(context.Background(), "ou_1", imchannel.ApprovalCard{ApNo: "AP-1"})
	if err == nil || !strings.Contains(err.Error(), "230013") || !strings.Contains(err.Error(), "NO availability") {
		t.Fatalf("err = %v", err)
	}
	bad := newTestApp(t, srv, kv{"im.lark.appSecret": "the-wrong-secret-value"})
	_, err = bad.botInfo(context.Background())
	if err == nil || !strings.Contains(err.Error(), "10014") || strings.Contains(err.Error(), "the-wrong-secret-value") {
		t.Fatalf("credential error = %v", err)
	}
}

func TestBotInfo_ReturnsTheAppName(t *testing.T) {
	srv := larktest.New(t)
	name, err := newTestApp(t, srv, nil).botInfo(context.Background())
	if err != nil || name != srv.BotName {
		t.Fatalf("botInfo = %q, %v", name, err)
	}
}

func TestPermanent(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{&apiError{HTTP: 400, Code: 10014}, true},                  // 凭据被拒
		{&apiError{HTTP: 400, Code: 99991672}, true},               // 应用没开机器人能力之类
		{&apiError{HTTP: 404, Code: 0, Msg: "响应无法解析"}, true}, // 开放平台地址填错
		{&apiError{HTTP: 400, Code: codeRateLimited}, false},
		{&apiError{HTTP: 503, Code: 0}, false},
		{&apiError{HTTP: 429, Code: 0}, false},
		{errors.New("dial tcp: connection refused"), false},
	} {
		if got := permanent(tc.err); got != tc.want {
			t.Errorf("permanent(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}
```

Create `backend/internal/imchannel/lark/card_test.go`：

```go
package lark

import (
	"context"
	"strings"
	"testing"

	"velagateway/internal/imchannel"
	"velagateway/internal/imchannel/lark/larktest"
)

func TestSendCard_ShowsTheReasonVerbatim(t *testing.T) {
	srv := larktest.New(t)
	a := newTestApp(t, srv, nil)
	_, err := a.SendCard(context.Background(), "ou_1", imchannel.ApprovalCard{
		ApNo: "AP-1", Reason: "[查看详情](https://evil.example)", Command: "SELECT 1 WHERE a < b && c",
	})
	if err != nil {
		t.Fatal(err)
	}
	content := srv.Calls("send")[0].Str("content")
	// 原因走 plain_text:发起人写的 markdown 链接在审批人的卡片里只是一段字。
	if !strings.Contains(content, `"content":"原因:[查看详情](https://evil.example)","tag":"plain_text"`) {
		t.Fatalf("reason must be plain text: %s", content)
	}
}

func TestInteractiveCard_KeepsTheConsoleLinkAndPutsButtonsBeforeTheNote(t *testing.T) {
	card := interactiveCard(imchannel.ApprovalCard{ApNo: "AP-1", ConsoleURL: "https://gw/approvals"})
	els := card["elements"].([]any)
	last := els[len(els)-1].(map[string]any)
	if last["tag"] != "note" {
		t.Fatalf("the note stays last, got %v", last["tag"])
	}
	action := els[len(els)-2].(map[string]any)
	acts := action["actions"].([]any)
	if action["tag"] != "action" || len(acts) != 3 {
		t.Fatalf("approve + reject + console link, got %+v", action)
	}
	if acts[2].(map[string]any)["url"] != "https://gw/approvals" {
		t.Fatalf("console link = %+v", acts[2])
	}
	for _, e := range els[:len(els)-2] {
		if e.(map[string]any)["tag"] == "action" {
			t.Fatal("the notify card's own link button must not survive next to the decision buttons")
		}
	}
}
```

- [ ] **Step 3: 跑测试确认失败**

Run: `cd backend && go test ./internal/imchannel/lark/`
Expected: 编译失败（`App`、`interactiveCard`、`permanent` 未定义）

- [ ] **Step 4: 改 `lark.go`**

`backend/internal/imchannel/lark/lark.go`：

1. 包注释改为：

```go
// Package lark 是飞书渠道。群通知走自定义机器人 webhook(*Channel);交互审批走开放平台
// 自建应用(*App):出站直接调 OpenAPI,入站用官方 SDK 的长连接或 webhook。它不做任何判定 ——
// 点击交给 ActionSink,由 service 认人、走 DecideBlockFor。
```

2. 把 `New` 整个替换为：

```go
// New 按配置造飞书渠道:配了开放平台应用(App ID + App Secret)就是能做交互审批的 *App,
// 只配了群通知 webhook 就是只能通知的 *Channel,都没配返回 nil(接口 nil)。
//
// 两种类型而不是一个带开关的类型:Hub 靠类型断言 Approver 判断「能不能发卡片」。只配了
// 群通知的飞书若也满足 Approver,绑定弹窗会把它列成可绑定的渠道,而发出去的码没人收。
func New(d imchannel.Deps) imchannel.Channel {
	n := &Channel{
		webhook:  strings.TrimSpace(d.Settings.String(imchannel.Key(imchannel.Lark, "notifyWebhook"))),
		secret:   d.Settings.Secret(imchannel.Key(imchannel.Lark, "notifySecret")),
		client:   d.HTTP,
		validate: d.ValidateURL,
	}
	if a := newApp(d, n); a != nil {
		return a
	}
	if n.webhook == "" {
		return nil
	}
	return n
}

// errNoNotifyWebhook:配了应用、打开了群通知,却没填群机器人地址。
var errNoNotifyWebhook = errors.New("未配置群通知 webhook")
```

3. `NotifyApproval` 开头加：

```go
	if c.webhook == "" {
		return errNoNotifyWebhook
	}
```

4. `approvalCard` 里原因那段替换为：

```go
	if strings.TrimSpace(c.Reason) != "" {
		// 原因是发起人写的:按 plain_text 原样显示。lark_md 会把一段 [前往审批](http://…)
		// 渲染成真链接,出现在审批人的卡片里。
		elements = append(elements, map[string]any{"tag": "div", "text": map[string]any{"tag": "plain_text", "content": "原因:" + safe(c.Reason)}})
	}
```

（补 `errors` import。）

- [ ] **Step 5: 写 `app.go`**

Create `backend/internal/imchannel/lark/app.go`：

```go
package lark

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"velagateway/internal/imchannel"
)

const (
	feishuBase = "https://open.feishu.cn"
	larkBase   = "https://open.larksuite.com"

	// WebhookPath 是飞书推事件与卡片回调到网关的路径。开发者后台「事件配置」与「回调配置」的
	// 请求地址都填 <回调根地址>+它 —— 飞书不像 Telegram 那样由网关自己登记。
	WebhookPath = "/api/v1/im/lark/webhook"

	modeWS      = "ws"
	modeWebhook = "webhook"

	pathToken    = "/open-apis/auth/v3/tenant_access_token/internal"
	pathBotInfo  = "/open-apis/bot/v3/info"
	pathMessages = "/open-apis/im/v1/messages"

	// codeRateLimited 是飞书的频控错误码:暂时的,退避重试。
	codeRateLimited = 99991400
)

// tokenRejected 是「tenant_access_token 缺失 / 无效」。缓存的 token 被提前作废时(管理员在
// 后台重置了 App Secret)换一个再试一次,而不是之后每张卡片都失败到 token 自然过期。
var tokenRejected = map[int]bool{99991661: true, 99991663: true}

// msgIDRe 是飞书消息 ID。收卡时 ref 来自库里,但仍按白名单拼进 URL 路径。
var msgIDRe = regexp.MustCompile(`^om_[A-Za-z0-9_-]{1,128}$`)

// App 是开放平台自建应用:私信卡片、收起卡片,并收回点击与私聊。
type App struct {
	*Channel // 群通知那一半;没配群机器人 webhook 时 NotifyApproval 报「未配置」

	appID       string
	secret      string
	base        string
	mode        string
	encryptKey  string
	verifyToken string
	client      *http.Client
	validate    func(string) error
	bot         atomic.Value // string:Run 里查到的应用名

	tokMu  sync.Mutex
	tok    string
	tokExp time.Time
}

// newApp 在配了 App ID 与 App Secret 时造一个应用,否则返回 nil。不做任何 I/O ——
// 它在 Hub 的锁里被调用。
func newApp(d imchannel.Deps, n *Channel) *App {
	key := func(s string) string { return imchannel.Key(imchannel.Lark, s) }
	appID := strings.TrimSpace(d.Settings.String(key("appID")))
	secret := strings.TrimSpace(d.Settings.Secret(key("appSecret")))
	if appID == "" || secret == "" {
		return nil
	}
	base := strings.TrimRight(strings.TrimSpace(d.Settings.String(key("apiBase"))), "/")
	if base == "" {
		base = feishuBase
		if d.Settings.String(key("domain")) == "lark" {
			base = larkBase
		}
	}
	mode := d.Settings.String(key("mode"))
	if mode != modeWebhook {
		mode = modeWS
	}
	a := &App{
		Channel: n, appID: appID, secret: secret, base: base, mode: mode,
		encryptKey:  strings.TrimSpace(d.Settings.Secret(key("encryptKey"))),
		verifyToken: strings.TrimSpace(d.Settings.Secret(key("verificationToken"))),
		client:      d.HTTP, validate: d.ValidateURL,
	}
	a.bot.Store("")
	return a
}

// BotName 返回 Run 里查到并缓存的应用名。Hub 锁内调用,所以不做 I/O。
func (a *App) BotName() string { return a.bot.Load().(string) }

// apiError 是飞书明确回了错误:code 非零,或者回了个解析不了的 4xx/5xx。
type apiError struct {
	Path string
	HTTP int
	Code int
	Msg  string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("飞书 %s: HTTP %d, code %d %s", e.Path, e.HTTP, e.Code, e.Msg)
}

// permanent 判断重试无望的错误:飞书明确拒绝(凭据错、应用没开机器人能力 …),或开放平台
// 地址填错回了 4xx。频控、5xx、网络错误是暂时的。
func permanent(err error) bool {
	var ae *apiError
	if !errors.As(err, &ae) || ae.HTTP >= 500 || ae.Code == codeRateLimited {
		return false
	}
	return ae.Code != 0 || (ae.HTTP >= 400 && ae.HTTP != http.StatusTooManyRequests)
}

// request 发一次 OpenAPI 请求;code 非零或 HTTP 非 2xx 都是 *apiError。
func (a *App) request(ctx context.Context, method, path, token string, body any) ([]byte, error) {
	if err := a.validate(a.base); err != nil {
		return nil, err
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.base+path, rd)
	if err != nil {
		return nil, a.scrub(err)
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, a.scrub(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	short, _, _ := strings.Cut(path, "?")
	var env struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, &apiError{Path: short, HTTP: resp.StatusCode, Msg: "响应无法解析"}
	}
	if env.Code != 0 || resp.StatusCode >= 300 {
		return nil, &apiError{Path: short, HTTP: resp.StatusCode, Code: env.Code, Msg: a.scrub(errors.New(env.Msg)).Error()}
	}
	return raw, nil
}

// call 带着 tenant_access_token 调一次;token 被拒就换一个再试一次。
func (a *App) call(ctx context.Context, method, path string, body any) ([]byte, error) {
	tok, err := a.token(ctx)
	if err != nil {
		return nil, err
	}
	raw, err := a.request(ctx, method, path, tok, body)
	var ae *apiError
	if errors.As(err, &ae) && tokenRejected[ae.Code] {
		a.dropToken(tok)
		if tok, err = a.token(ctx); err != nil {
			return nil, err
		}
		raw, err = a.request(ctx, method, path, tok, body)
	}
	return raw, err
}

// token 返回缓存的 tenant_access_token,快过期(剩 5 分钟)就换新的。
func (a *App) token(ctx context.Context) (string, error) {
	a.tokMu.Lock()
	defer a.tokMu.Unlock()
	if a.tok != "" && time.Now().Before(a.tokExp) {
		return a.tok, nil
	}
	raw, err := a.request(ctx, http.MethodPost, pathToken, "", map[string]string{"app_id": a.appID, "app_secret": a.secret})
	if err != nil {
		return "", err
	}
	var r struct {
		Token  string `json:"tenant_access_token"`
		Expire int    `json:"expire"`
	}
	if json.Unmarshal(raw, &r) != nil || r.Token == "" {
		return "", errors.New("飞书 tenant_access_token 响应里没有 token")
	}
	a.tok = r.Token
	a.tokExp = time.Now().Add(time.Duration(max(r.Expire-300, 60)) * time.Second)
	return a.tok, nil
}

func (a *App) dropToken(old string) {
	a.tokMu.Lock()
	defer a.tokMu.Unlock()
	if a.tok == old {
		a.tok = ""
	}
}

// scrub 把 App Secret 从错误文本里换掉。它只在请求体里,正常不会出现在错误里 ——
// 这是防一个往错误里回显请求的代理或私有化部署。
func (a *App) scrub(err error) error {
	if err == nil || a.secret == "" {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), a.secret, "<app-secret>"))
}

// retry 反复调 fn 直到成功、遇到永久性错误或 ctx 结束(后者返回 nil)。
func (a *App) retry(ctx context.Context, fn func() error) error {
	backoff := time.Second
	for {
		err := fn()
		if err == nil || permanent(err) {
			return err
		}
		if ctx.Err() != nil {
			return nil
		}
		slog.Warn("lark: call failed, retrying", "err", err, "retryIn", backoff)
		t := time.NewTimer(backoff)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return nil
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

// botInfo 查应用名:绑定弹窗用它告诉用户私聊谁。查不到说明应用没开机器人能力 —— 那样
// 卡片也发不出去,所以 Run 把它当成配置错误。
func (a *App) botInfo(ctx context.Context) (string, error) {
	raw, err := a.call(ctx, http.MethodGet, pathBotInfo, nil)
	if err != nil {
		return "", err
	}
	var r struct {
		Bot struct {
			AppName string `json:"app_name"`
		} `json:"bot"`
	}
	_ = json.Unmarshal(raw, &r)
	return r.Bot.AppName, nil
}

// send 给一个 open_id 私信一条消息,返回 message_id。
func (a *App) send(ctx context.Context, openID, msgType string, content any) (string, error) {
	b, err := json.Marshal(content)
	if err != nil {
		return "", err
	}
	raw, err := a.call(ctx, http.MethodPost, pathMessages+"?receive_id_type=open_id", map[string]string{
		"receive_id": openID, "msg_type": msgType, "content": string(b),
	})
	if err != nil {
		return "", err
	}
	var r struct {
		Data struct {
			MessageID string `json:"message_id"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &r) != nil || r.Data.MessageID == "" {
		return "", errors.New("飞书发送消息:响应里没有 message_id")
	}
	return r.Data.MessageID, nil
}

func (a *App) sendText(ctx context.Context, openID, text string) error {
	_, err := a.send(ctx, openID, "text", map[string]string{"text": text})
	return err
}

// SendCard 私信一张审批卡片。ref 是 "<message_id>|<ApNo>":收卡时整张替换,得知道是哪张单。
func (a *App) SendCard(ctx context.Context, openID string, c imchannel.ApprovalCard) (string, error) {
	id, err := a.send(ctx, openID, "interactive", interactiveCard(c))
	if err != nil {
		return "", err
	}
	return id + "|" + c.ApNo, nil
}

// CloseCard 把卡片换成终态。
func (a *App) CloseCard(ctx context.Context, ref string, final imchannel.CardState) error {
	id, apNo, _ := strings.Cut(ref, "|")
	if !msgIDRe.MatchString(id) {
		return fmt.Errorf("无法识别的卡片引用 %q", ref)
	}
	b, err := json.Marshal(closedCard(apNo, final))
	if err != nil {
		return err
	}
	_, err = a.call(ctx, http.MethodPatch, pathMessages+"/"+id, map[string]string{"content": string(b)})
	return err
}
```

- [ ] **Step 6: 写 `card.go`**

Create `backend/internal/imchannel/lark/card.go`：

```go
package lark

import "velagateway/internal/imchannel"

// cardConfig:update_multi 让卡片能被「更新应用发送的消息卡片」接口改写,收卡靠它。
func cardConfig() map[string]any {
	return map[string]any{"wide_screen_mode": true, "update_multi": true}
}

// decisionButton 的 value 只说哪张单、什么动作,不带任何权限信息(spec §2.2)。
func decisionButton(label, style, apNo, act string) map[string]any {
	return map[string]any{
		"tag": "button", "type": style,
		"text":  map[string]any{"tag": "plain_text", "content": label},
		"value": map[string]any{"ap": apNo, "act": act},
	}
}

// interactiveCard 是私信给审批人的卡片:群通知那张的内容,把「前往审批」换成
// 「通过 / 驳回 / 前往控制台」一组按钮。
func interactiveCard(c imchannel.ApprovalCard) map[string]any {
	card := approvalCard(c)
	actions := []any{
		decisionButton("通过", "primary", c.ApNo, "a"),
		decisionButton("驳回", "danger", c.ApNo, "r"),
	}
	if c.ConsoleURL != "" {
		actions = append(actions, map[string]any{
			"tag": "button", "type": "default",
			"text": map[string]any{"tag": "plain_text", "content": "前往控制台"},
			"url":  c.ConsoleURL,
		})
	}
	var els []any
	for _, e := range card["elements"].([]any) {
		if m, ok := e.(map[string]any); ok && m["tag"] == "action" {
			continue
		}
		els = append(els, e)
	}
	note := els[len(els)-1] // approvalCard 的最后一个元素总是 note
	els = append(els[:len(els)-1], map[string]any{"tag": "action", "actions": actions}, note)
	card["elements"] = els
	card["header"].(map[string]any)["title"] = map[string]any{"tag": "plain_text", "content": "高危命令待你审批 · " + c.ApNo}
	card["config"] = cardConfig()
	return card
}

// closedCard 是收起后的卡片。网关不存卡片原文,改写只能整张替换,所以只留单号和终态,
// 详情在控制台。不带按钮:收起之后无处可点。
func closedCard(apNo string, final imchannel.CardState) map[string]any {
	tmpl := "grey"
	switch final.Status {
	case "approved":
		tmpl = "green"
	case "rejected":
		tmpl = "red"
	}
	title := "审批卡片已收起"
	if apNo != "" {
		title = "工单 " + apNo + " · 已处理"
	}
	return map[string]any{
		"config": cardConfig(),
		"header": map[string]any{"template": tmpl, "title": map[string]any{"tag": "plain_text", "content": title}},
		"elements": []any{
			map[string]any{"tag": "div", "text": map[string]any{"tag": "plain_text", "content": final.Text}},
			map[string]any{"tag": "note", "elements": []any{map[string]any{"tag": "plain_text", "content": "AegisDB 数据库网关"}}},
		},
	}
}
```

- [ ] **Step 7: 跑测试**

Run: `cd backend && gofmt -l internal/imchannel && go vet ./internal/imchannel/... && go test -race ./internal/imchannel/lark/...`
Expected: gofmt 无输出；全部 PASS（包括第 1 期的 `TestNotifyApproval_*`）

- [ ] **Step 8: 提交**

```bash
git add backend/internal/imchannel/lark
git commit -m "feat(im): add the Lark app's outbound side and a fake open platform

Co-Authored-By: <model> <noreply@anthropic.com>"
```

---

### Task 3: 飞书长连接：SDK 接入、点击与私聊

**Files:**
- Modify: `backend/go.mod`、`backend/go.sum`（`go get github.com/larksuite/oapi-sdk-go/v3@v3.12.2`）
- Create: `backend/internal/imchannel/lark/inbound.go`
- Modify: `backend/internal/imchannel/lark/app.go`（`App` 加 `disp`、`seen` 字段；`newApp` 里建分发器）
- Create: `backend/internal/imchannel/lark/larktest/ws.go`、`backend/internal/imchannel/lark/larktest/events.go`
- Modify: `backend/internal/imchannel/lark/larktest/server.go`（`/ws` 与 `/callback/ws/endpoint` 路由、新字段初始化）
- Test: `backend/internal/imchannel/lark/inbound_test.go`

**Interfaces:**
- Consumes: Task 2 的 `App`、`call`、`botInfo`、`sendText`、`retry`、`modeWS`/`modeWebhook`。
- Produces:
  ```go
  func (a *App) Run(ctx context.Context, sink imchannel.ActionSink) error   // *App 至此满足 imchannel.Approver
  func withSink(ctx context.Context, s imchannel.ActionSink) context.Context
  func (a *App) newDispatcher() *dispatcher.EventDispatcher                 // Task 4 的 webhook 复用 a.disp
  ```
  larktest：
  ```go
  func (s *Server) WaitConnected(t testing.TB)
  func (s *Server) ConnectCount() int
  func (s *Server) Push(t testing.TB, payload []byte) larkws.Response
  func (s *Server) CardAction(openID string, value map[string]any) []byte
  func (s *Server) Message(openID, chatType, text, messageID string) []byte
  func (s *Server) Click(t testing.TB, openID string, value map[string]any) string   // 返回 toast
  func (s *Server) SendDM(t testing.TB, openID, chatType, text string)
  ```

- [ ] **Step 1: 加依赖**

Run: `cd backend && go get github.com/larksuite/oapi-sdk-go/v3@v3.12.2`
Expected: `go.mod` 多一行 `github.com/larksuite/oapi-sdk-go/v3 v3.12.2`（`gorilla/websocket` 仍是 v1.5.3）。依赖要等第一次 import 之后才会从 indirect 变成 direct，所以 `go mod tidy` 放到 Step 8。

- [ ] **Step 2: 写假长连接网关**

`backend/internal/imchannel/lark/larktest/server.go` 的 `Server` 结构体加字段：

```go
	// 长连接(ws.go)
	conn      *websocket.Conn
	writeMu   sync.Mutex
	connects  int
	waiting   map[string]chan larkws.Response
	nextFrame int
	nextEvent int
```

`New` 里初始化 `waiting: map[string]chan larkws.Response{}`。`handle` 开头（解析 body 之前）加：

```go
	if r.URL.Path == "/ws" {
		s.upgrade(w, r)
		return
	}
```

`switch` 里加一个 case：

```go
	case r.Method == http.MethodPost && r.URL.Path == "/callback/ws/endpoint":
		method = "wsEndpoint"
```

在 `if method == "token" {` 之前加：

```go
	if method == "wsEndpoint" {
		s.endpoint(w, params)
		return
	}
```

（import 补 `github.com/gorilla/websocket` 与 `larkws "github.com/larksuite/oapi-sdk-go/v3/ws"`。）

Create `backend/internal/imchannel/lark/larktest/ws.go`：

```go
package larktest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

// endpoint 扮演 /callback/ws/endpoint:校验应用凭据,发放 WebSocket 地址。凭据不对回 514 ——
// SDK 把它当成不可重试的 ClientError。
func (s *Server) endpoint(w http.ResponseWriter, params map[string]any) {
	if params["AppID"] != s.AppID || params["AppSecret"] != s.AppSecret {
		writeJSON(w, http.StatusOK, map[string]any{"code": 514, "msg": "auth failed"})
		return
	}
	wsURL := "ws" + strings.TrimPrefix(s.URL, "http") + "/ws?device_id=dev-1&service_id=1"
	writeJSON(w, http.StatusOK, map[string]any{"code": 0, "data": map[string]any{
		"URL": wsURL,
		// 重连快、不加随机延迟、两分钟一次 ping:测试里连接断了马上能重连上。
		"ClientConfig": map[string]any{"ReconnectCount": -1, "ReconnectInterval": 1, "ReconnectNonce": 0, "PingInterval": 120},
	}})
}

func (s *Server) upgrade(w http.ResponseWriter, r *http.Request) {
	conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
	if err != nil {
		return
	}
	s.mu.Lock()
	s.conn = conn
	s.connects++
	s.mu.Unlock()
	go s.readLoop(conn)
}

// readLoop 收 SDK 发上来的帧:ping 回 pong,数据帧是某条事件的回执,交给等它的 Push。
// 帧的编解码直接用 SDK 的 protobuf 类型 —— 测到的是 SDK 真正的协议。
func (s *Server) readLoop(conn *websocket.Conn) {
	defer func() {
		s.mu.Lock()
		if s.conn == conn {
			s.conn = nil
		}
		s.mu.Unlock()
		conn.Close()
	}()
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var f larkws.Frame
		if f.Unmarshal(msg) != nil {
			continue
		}
		hs := larkws.Headers(f.Headers)
		if f.Method == int32(larkws.FrameTypeControl) {
			if hs.GetString(larkws.HeaderType) == string(larkws.MessageTypePing) {
				pong := larkws.Frame{Method: int32(larkws.FrameTypeControl), Service: f.Service,
					Headers: []larkws.Header{{Key: larkws.HeaderType, Value: string(larkws.MessageTypePong)}}}
				s.write(conn, &pong)
			}
			continue
		}
		var resp larkws.Response
		_ = json.Unmarshal(f.Payload, &resp)
		s.mu.Lock()
		ch := s.waiting[hs.GetString(larkws.HeaderMessageID)]
		s.mu.Unlock()
		if ch != nil {
			ch <- resp
		}
	}
}

func (s *Server) write(conn *websocket.Conn, f *larkws.Frame) error {
	b, err := f.Marshal()
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return conn.WriteMessage(websocket.BinaryMessage, b)
}

// WaitConnected 等 SDK 连上来。
func (s *Server) WaitConnected(t testing.TB) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		up := s.conn != nil
		s.mu.Unlock()
		if up {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("larktest: the long connection never came up")
}

// ConnectCount 是到目前为止建立过的长连接数。
func (s *Server) ConnectCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connects
}

// Push 经长连接推一条事件(type=event 的数据帧),等 SDK 回执并返回它。
func (s *Server) Push(t testing.TB, payload []byte) larkws.Response {
	t.Helper()
	s.WaitConnected(t)
	s.mu.Lock()
	conn := s.conn
	s.nextFrame++
	id := fmt.Sprintf("frame-%d", s.nextFrame)
	ch := make(chan larkws.Response, 1)
	s.waiting[id] = ch
	seq := s.nextFrame
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.waiting, id)
		s.mu.Unlock()
	}()
	f := larkws.Frame{
		SeqID: uint64(seq), Service: 1, Method: int32(larkws.FrameTypeData),
		Headers: []larkws.Header{
			{Key: larkws.HeaderType, Value: string(larkws.MessageTypeEvent)},
			{Key: larkws.HeaderMessageID, Value: id},
			{Key: larkws.HeaderSum, Value: "1"},
			{Key: larkws.HeaderSeq, Value: "0"},
			{Key: larkws.HeaderTraceID, Value: "trace-" + id},
		},
		Payload: payload,
	}
	if err := s.write(conn, &f); err != nil {
		t.Fatalf("larktest: push: %v", err)
	}
	select {
	case r := <-ch:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("larktest: no reply to the pushed event within 5s")
		return larkws.Response{}
	}
}
```

Create `backend/internal/imchannel/lark/larktest/events.go`：

```go
package larktest

import (
	"encoding/json"
	"fmt"
	"strconv"
	"testing"
	"time"
)

// envelope 包成飞书 schema 2.0 的事件。
func (s *Server) envelope(eventType string, event any) []byte {
	s.mu.Lock()
	s.nextEvent++
	n := s.nextEvent
	s.mu.Unlock()
	b, _ := json.Marshal(map[string]any{
		"schema": "2.0",
		"header": map[string]any{
			"event_id": fmt.Sprintf("ev-%d", n), "event_type": eventType, "app_id": s.AppID,
			"token": s.VerificationToken, "create_time": strconv.FormatInt(time.Now().UnixMilli(), 10),
		},
		"event": event,
	})
	return b
}

// CardAction 造一次卡片按钮点击(card.action.trigger 回调)。
func (s *Server) CardAction(openID string, value map[string]any) []byte {
	return s.envelope("card.action.trigger", map[string]any{
		"operator": map[string]any{"open_id": openID},
		"action":   map[string]any{"tag": "button", "value": value},
		"context":  map[string]any{"open_message_id": "om_card"},
	})
}

// Message 造一条 im.message.receive_v1。messageID 相同就是飞书在重投同一条消息。
func (s *Server) Message(openID, chatType, text, messageID string) []byte {
	content, _ := json.Marshal(map[string]string{"text": text})
	return s.envelope("im.message.receive_v1", map[string]any{
		"sender": map[string]any{"sender_id": map[string]any{"open_id": openID}, "sender_type": "user"},
		"message": map[string]any{
			"message_id": messageID, "chat_id": "oc_" + chatType, "chat_type": chatType,
			"message_type": "text", "content": string(content),
		},
	})
}

// Click 经长连接推一次按钮点击,返回网关回的 toast。
func (s *Server) Click(t testing.TB, openID string, value map[string]any) string {
	t.Helper()
	r := s.Push(t, s.CardAction(openID, value))
	var out struct {
		Toast struct {
			Content string `json:"content"`
		} `json:"toast"`
	}
	_ = json.Unmarshal(r.Data, &out)
	return out.Toast.Content
}

// SendDM 经长连接推一条消息,等网关处理完(包括它的回复)才返回。
func (s *Server) SendDM(t testing.TB, openID, chatType, text string) {
	t.Helper()
	s.mu.Lock()
	s.nextEvent++
	id := fmt.Sprintf("om_dm%d", s.nextEvent)
	s.mu.Unlock()
	s.Push(t, s.Message(openID, chatType, text, id))
}
```

- [ ] **Step 3: 写失败的测试**

Create `backend/internal/imchannel/lark/inbound_test.go`：

```go
package lark

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"velagateway/internal/imchannel"
	"velagateway/internal/imchannel/lark/larktest"
)

// recSink 记下交上来的点击与私聊,回一个能看出收到了什么的 toast / 回复。
type recSink struct {
	mu      sync.Mutex
	actions []imchannel.Action
	dms     []imchannel.DM
}

func (s *recSink) OnAction(_ context.Context, a imchannel.Action) imchannel.ActionResult {
	s.mu.Lock()
	s.actions = append(s.actions, a)
	s.mu.Unlock()
	return imchannel.ActionResult{Toast: "ok " + a.PlatformUserID + " " + a.ApNo + " " + strconv.FormatBool(a.Approve)}
}

func (s *recSink) OnDirectMessage(_ context.Context, dm imchannel.DM) string {
	s.mu.Lock()
	s.dms = append(s.dms, dm)
	s.mu.Unlock()
	return "reply to " + dm.Text
}

func (s *recSink) counts() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.actions), len(s.dms)
}

// startRun 在后台跑 Run;返回的 stop 取消它并拿到返回值。
func startRun(t *testing.T, a *App, sink imchannel.ActionSink) (stop func() error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx, sink) }()
	stopped := false
	stop = func() error {
		if stopped {
			return nil
		}
		stopped = true
		cancel()
		select {
		case err := <-done:
			return err
		case <-time.After(10 * time.Second):
			t.Fatal("Run did not return after cancel")
			return nil
		}
	}
	t.Cleanup(func() { _ = stop() })
	return stop
}

func TestRun_ConnectsAndLearnsTheBotName(t *testing.T) {
	srv := larktest.New(t)
	a := newTestApp(t, srv, nil)
	var _ imchannel.Approver = a
	stop := startRun(t, a, &recSink{})
	srv.WaitConnected(t)
	if a.BotName() != srv.BotName {
		t.Fatalf("bot name = %q", a.BotName())
	}
	if err := stop(); err != nil {
		t.Fatalf("a cancelled Run returns nil, got %v", err)
	}
}

func TestRun_AClickReachesTheSinkAndTheToastComesBack(t *testing.T) {
	srv := larktest.New(t)
	sink := &recSink{}
	startRun(t, newTestApp(t, srv, nil), sink)
	if toast := srv.Click(t, "ou_1", map[string]any{"ap": "AP-7", "act": "a"}); toast != "ok ou_1 AP-7 true" {
		t.Fatalf("toast = %q", toast)
	}
	if toast := srv.Click(t, "ou_1", map[string]any{"ap": "AP-7", "act": "r"}); toast != "ok ou_1 AP-7 false" {
		t.Fatalf("reject toast = %q", toast)
	}
}

func TestRun_AnUnrecognisedButtonValueIsRefused(t *testing.T) {
	srv := larktest.New(t)
	sink := &recSink{}
	startRun(t, newTestApp(t, srv, nil), sink)
	for _, v := range []map[string]any{
		{"ap": "AP 7; drop", "act": "a"},
		{"ap": "AP-7", "act": "x"},
		{"ap": 7, "act": "a"},
		{},
	} {
		if toast := srv.Click(t, "ou_1", v); toast != "无法识别的操作" {
			t.Errorf("value %v → toast %q", v, toast)
		}
	}
	if n, _ := sink.counts(); n != 0 {
		t.Fatalf("a forged value must never reach the sink, got %d actions", n)
	}
}

func TestRun_ADirectMessageIsAnsweredAndGroupTextIsIgnored(t *testing.T) {
	srv := larktest.New(t)
	sink := &recSink{}
	startRun(t, newTestApp(t, srv, nil), sink)

	srv.SendDM(t, "ou_9", "group", "BIND-AAAA1111")
	if _, n := sink.counts(); n != 0 {
		t.Fatal("a code posted in a group must not be looked at")
	}
	srv.SendDM(t, "ou_9", "p2p", "  BIND-AAAA1111 ")
	if _, n := sink.counts(); n != 1 || sink.dms[0].PlatformUserID != "ou_9" || sink.dms[0].Text != "BIND-AAAA1111" {
		t.Fatalf("dms = %+v", sink.dms)
	}
	srv.WaitCall(t, "send", func(c larktest.Call) bool {
		return c.Str("receive_id") == "ou_9" && c.Str("msg_type") == "text" && strings.Contains(c.Str("content"), "reply to BIND-AAAA1111")
	})
}

func TestRun_ARedeliveredMessageIsHandledOnce(t *testing.T) {
	srv := larktest.New(t)
	sink := &recSink{}
	startRun(t, newTestApp(t, srv, nil), sink)
	msg := srv.Message("ou_9", "p2p", "BIND-AAAA1111", "om_same")
	srv.Push(t, msg)
	srv.Push(t, msg) // 飞书没及时收到确认,把同一条消息又推了一次
	if _, n := sink.counts(); n != 1 {
		t.Fatalf("handled %d times; a redelivered bind code would be consumed twice", n)
	}
}

func TestRun_RejectedCredentialsStopTheLoop(t *testing.T) {
	srv := larktest.New(t)
	a := newTestApp(t, srv, kv{"im.lark.appSecret": "the-wrong-secret-value"})
	done := make(chan error, 1)
	go func() { done <- a.Run(context.Background(), &recSink{}) }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "10014") || strings.Contains(err.Error(), "the-wrong-secret-value") {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("rejected credentials must end Run, not retry forever")
	}
}

func TestRun_RefusesAClientWithoutAGuardedDialer(t *testing.T) {
	srv := larktest.New(t)
	d := deps(kv{"im.lark.appID": srv.AppID, "im.lark.appSecret": srv.AppSecret, "im.lark.apiBase": srv.URL})
	d.HTTP = &http.Client{Timeout: 5 * time.Second} // 没有 *http.Transport,也就没有受控拨号器
	a := New(d).(*App)
	err := a.Run(context.Background(), &recSink{})
	if err == nil || !strings.Contains(err.Error(), "拨号") {
		t.Fatalf("err = %v, want a refusal to fall back to an unguarded dialer", err)
	}
	if srv.ConnectCount() != 0 {
		t.Fatal("nothing may be dialled without the guard")
	}
}
```

- [ ] **Step 4: 跑测试确认失败**

Run: `cd backend && go test ./internal/imchannel/lark/ -run TestRun_`
Expected: 编译失败（`a.Run undefined`）

- [ ] **Step 5: 写 `inbound.go`**

Create `backend/internal/imchannel/lark/inbound.go`：

```go
package lark

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"

	"velagateway/internal/imchannel"
)

// apNoRe:按钮回调可以被伪造(webhook 模式下尤其如此),别让任意字符串流进后面的查询和提示语。
var apNoRe = regexp.MustCompile(`^[A-Za-z0-9-]{1,32}$`)

type sinkKey struct{}

// withSink 把 sink 放进 ctx。分发器在 newApp 里就建好了,而 sink 要到 Run / HandleWebhook
// 才拿得到 —— SDK 会把同一个 ctx 一路传给处理函数。
func withSink(ctx context.Context, s imchannel.ActionSink) context.Context {
	return context.WithValue(ctx, sinkKey{}, s)
}

func sinkFrom(ctx context.Context) imchannel.ActionSink {
	s, _ := ctx.Value(sinkKey{}).(imchannel.ActionSink)
	return s
}

// Run 阻塞到 ctx 取消。先查应用名(顺带验证凭据与机器人能力:失败是永久的,返回让设置页
// 看见),然后长连接模式交给 SDK,webhook 模式只是待命 —— 事件从 HandleWebhook 进来。
func (a *App) Run(ctx context.Context, sink imchannel.ActionSink) error {
	var name string
	err := a.retry(ctx, func() error {
		n, err := a.botInfo(ctx)
		name = n
		return err
	})
	if err != nil || ctx.Err() != nil {
		return err
	}
	a.bot.Store(name)
	if a.mode == modeWebhook {
		if a.encryptKey == "" || a.verifyToken == "" {
			return errors.New("webhook 模式需要配置 Encrypt Key 与 Verification Token")
		}
		slog.Info("lark: webhook mode — point both the event and the callback request URL at the gateway", "path", WebhookPath)
		<-ctx.Done()
		return nil
	}
	return a.stream(withSink(ctx, sink))
}

// stream 用官方 SDK 维持长连接。SDK 的两条出网路径都换成网关受控的:取连接地址走 a.client,
// WebSocket 拨号走同一个 Transport 的 DialContext。
func (a *App) stream(ctx context.Context) error {
	dial, err := guardedDial(a.client)
	if err != nil {
		return err
	}
	if err := a.validate(a.base); err != nil {
		return err
	}
	cli := larkws.NewClient(a.appID, a.secret,
		larkws.WithEventHandler(a.disp),
		larkws.WithDomain(a.base),
		larkws.WithHttpClient(a.client),
		larkws.WithWebSocketDialer(&websocket.Dialer{NetDialContext: dial, HandshakeTimeout: 10 * time.Second}),
		larkws.WithLogger(sdkLogger{}),
		larkws.WithLogLevel(larkcore.LogLevelWarn),
		larkws.WithAutoReconnect(true),
	)
	slog.Info("lark: long connection starting — safe with several gateway instances; Lark delivers each event to one of them")
	err = cli.Start(ctx)
	if ctx.Err() != nil {
		return nil
	}
	return a.scrub(err)
}

// guardedDial 取出网关出站客户端里带 SSRF 拦截的拨号器。拿不到就拒绝,不退回默认拨号器 ——
// 那样长连接就绕开了内网拦截。
func guardedDial(c *http.Client) (func(ctx context.Context, network, addr string) (net.Conn, error), error) {
	if t, ok := c.Transport.(*http.Transport); ok && t.DialContext != nil {
		return t.DialContext, nil
	}
	return nil, errors.New("出站客户端没有受控的拨号器,拒绝建立长连接")
}

// newDispatcher 建事件分发器:私聊消息与卡片回调。签名与 Token 不在这里验 —— 长连接本身
// 已认证;webhook 由 HandleWebhook 自己验。
func (a *App) newDispatcher() *dispatcher.EventDispatcher {
	d := dispatcher.NewEventDispatcher("", "")
	d.Config.Logger = sdkLogger{}
	d.OnP2MessageReceiveV1(a.onMessage)
	d.OnP2CardActionTrigger(a.onCardAction)
	return d
}

func (a *App) onCardAction(ctx context.Context, ev *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, error) {
	toast := "无法识别的操作"
	sink := sinkFrom(ctx)
	if ev != nil && ev.Event != nil && ev.Event.Operator != nil && ev.Event.Action != nil && sink != nil {
		apNo, approve, ok := parseValue(ev.Event.Action.Value)
		if ok && ev.Event.Operator.OpenID != "" {
			// 认人只凭 operator.open_id —— 飞书保证它是真正的点击者。对应哪个网关账户、
			// 能不能批,都是 sink 那一侧的事。
			toast = sink.OnAction(ctx, imchannel.Action{
				PlatformUserID: ev.Event.Operator.OpenID, ApNo: apNo, Approve: approve,
			}).Toast
		}
	}
	return &callback.CardActionTriggerResponse{Toast: &callback.Toast{Type: "info", Content: toast}}, nil
}

// parseValue 读按钮的 value:{"ap": "<ApNo>", "act": "a" | "r"}。
func parseValue(v map[string]interface{}) (apNo string, approve, ok bool) {
	ap, _ := v["ap"].(string)
	act, _ := v["act"].(string)
	if !apNoRe.MatchString(ap) || (act != "a" && act != "r") {
		return "", false, false
	}
	return ap, act == "a", true
}

func (a *App) onMessage(ctx context.Context, ev *larkim.P2MessageReceiveV1) error {
	if ev == nil || ev.Event == nil || ev.Event.Message == nil || ev.Event.Sender == nil || ev.Event.Sender.SenderId == nil {
		return nil
	}
	m, s := ev.Event.Message, ev.Event.Sender
	// 只理用户发来的单聊文本。群里的任何文字都不触发任何逻辑 —— 绑定码发在群里就是泄露了,
	// 更不能认。
	if str(m.ChatType) != "p2p" || str(s.SenderType) != "user" || str(m.MessageType) != "text" {
		return nil
	}
	openID := str(s.SenderId.OpenId)
	sink := sinkFrom(ctx)
	if openID == "" || sink == nil || !a.seen.first(str(m.MessageId)) {
		return nil
	}
	var body struct {
		Text string `json:"text"`
	}
	if json.Unmarshal([]byte(str(m.Content)), &body) != nil {
		return nil
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		return nil
	}
	reply := sink.OnDirectMessage(ctx, imchannel.DM{PlatformUserID: openID, Text: text})
	if reply == "" {
		return nil
	}
	// 绑定已经落库,回复不能因为 ctx 刚好被取消(Reload)而丢掉。
	ackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := a.sendText(ackCtx, openID, reply); err != nil {
		slog.Warn("lark: reply to direct message failed", "err", err)
	}
	return nil
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// recent 记住最近处理过的消息 ID。飞书没及时收到确认会重推同一条消息;绑定码第二次到达时
// 已被消费,不去重的话用户会在「已绑定」之后又收到一句「无效或已过期」。
type recent struct {
	mu    sync.Mutex
	ids   map[string]struct{}
	order []string
}

const recentMax = 512

// first 报告 id 是不是第一次出现。空 id 不去重。
func (r *recent) first(id string) bool {
	if id == "" {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ids == nil {
		r.ids = map[string]struct{}{}
	}
	if _, dup := r.ids[id]; dup {
		return false
	}
	r.ids[id] = struct{}{}
	r.order = append(r.order, id)
	if len(r.order) > recentMax {
		delete(r.ids, r.order[0])
		r.order = r.order[1:]
	}
	return true
}

// sdkLogger 把 SDK 的日志接进 slog。Debug 一律丢弃:SDK 在 Debug 级会把整条事件 payload
// 打出来,其中有用户私聊机器人发的绑定码。Info 降为 Debug:连上、重连这类事网关自己会说。
type sdkLogger struct{}

func (sdkLogger) Debug(context.Context, ...interface{}) {}
func (sdkLogger) Info(_ context.Context, args ...interface{}) {
	slog.Debug("lark sdk: " + fmt.Sprint(args...))
}
func (sdkLogger) Warn(_ context.Context, args ...interface{}) {
	slog.Warn("lark sdk: " + fmt.Sprint(args...))
}
func (sdkLogger) Error(_ context.Context, args ...interface{}) {
	slog.Error("lark sdk: " + fmt.Sprint(args...))
}
```

- [ ] **Step 6: 把分发器与去重挂到 `App` 上**

`backend/internal/imchannel/lark/app.go`：`App` 结构体在 `bot` 字段后加：

```go
	disp *dispatcher.EventDispatcher // 长连接与 webhook 共用;处理函数从 ctx 里取 sink
	seen recent                      // 已处理的私聊 message_id
```

`newApp` 里 `a.bot.Store("")` 之后加 `a.disp = a.newDispatcher()`。import 补 `"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"`。

（`NewEventDispatcher` 构造时会用 SDK 自带的 logger 打一行 "event-dispatch is ready"，每次 Reload 一行，不含敏感信息，接受。）

- [ ] **Step 7: 跑测试**

Run: `cd backend && go test -race ./internal/imchannel/lark/... -count=3`
Expected: 全部 PASS。若 `TestRun_RejectedCredentialsStopTheLoop` 超时，检查 `permanent` 是否把 HTTP 400 + code 10014 判为永久。

- [ ] **Step 8: tidy 并提交**

Run: `cd backend && go mod tidy && go build ./... && gofmt -l internal/imchannel`
Expected: `go.mod` 里 `github.com/larksuite/oapi-sdk-go/v3` 在直接依赖块；gofmt 无输出

```bash
git add backend/go.mod backend/go.sum backend/internal/imchannel/lark
git commit -m "feat(im): receive Lark clicks and direct messages over the long connection

Co-Authored-By: <model> <noreply@anthropic.com>"
```

---

### Task 4: 飞书 webhook 模式

**Files:**
- Create: `backend/internal/imchannel/lark/webhook.go`
- Modify: `backend/internal/imchannel/lark/larktest/events.go`（加密、签名、URL 校验 payload）
- Test: `backend/internal/imchannel/lark/webhook_test.go`

**Interfaces:**
- Consumes: Task 1 的 `imchannel.WebhookReply` / `imchannel.Reply`；Task 3 的 `a.disp`、`withSink`。
- Produces:
  ```go
  func (a *App) HandleWebhook(ctx context.Context, h http.Header, body []byte, sink imchannel.ActionSink) imchannel.WebhookReply
  ```
  larktest：
  ```go
  func Encrypt(key string, plain []byte) string
  func (s *Server) Signed(plain []byte) (http.Header, []byte)
  func (s *Server) Challenge(token string) []byte
  ```

- [ ] **Step 1: 给 larktest 加加密与签名**

在 `backend/internal/imchannel/lark/larktest/events.go` 末尾加（import 补 `bytes`、`crypto/aes`、`crypto/cipher`、`crypto/rand`、`crypto/sha256`、`encoding/base64`、`net/http`、`larkevent "github.com/larksuite/oapi-sdk-go/v3/event"`）：

```go
// Encrypt 按飞书的方式加密事件体:AES-256-CBC,key = sha256(EncryptKey),随机 IV 放在
// 密文前,PKCS#7 填充,base64。是 SDK larkevent.EventDecrypt 的逆过程。
func Encrypt(key string, plain []byte) string {
	k := sha256.Sum256([]byte(key))
	block, _ := aes.NewCipher(k[:])
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	buf := append(append([]byte{}, plain...), bytes.Repeat([]byte{byte(pad)}, pad)...)
	out := make([]byte, aes.BlockSize+len(buf))
	_, _ = rand.Read(out[:aes.BlockSize])
	cipher.NewCBCEncrypter(block, out[:aes.BlockSize]).CryptBlocks(out[aes.BlockSize:], buf)
	return base64.StdEncoding.EncodeToString(out)
}

// Signed 把明文事件包成飞书 webhook 发来的样子:加密的 body 加三个签名头。
func (s *Server) Signed(plain []byte) (http.Header, []byte) {
	body, _ := json.Marshal(map[string]string{"encrypt": Encrypt(s.EncryptKey, plain)})
	ts, nonce := strconv.FormatInt(time.Now().Unix(), 10), "nonce-1"
	h := http.Header{}
	h.Set("X-Lark-Request-Timestamp", ts)
	h.Set("X-Lark-Request-Nonce", nonce)
	h.Set("X-Lark-Signature", larkevent.Signature(ts, nonce, s.EncryptKey, string(body)))
	return h, body
}

// Challenge 是开发者后台保存请求地址时发来的 URL 校验(明文,发送前要 Encrypt)。
func (s *Server) Challenge(token string) []byte {
	b, _ := json.Marshal(map[string]string{"type": "url_verification", "challenge": "c-123", "token": token})
	return b
}
```

- [ ] **Step 2: 写失败的测试**

Create `backend/internal/imchannel/lark/webhook_test.go`：

```go
package lark

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"velagateway/internal/imchannel/lark/larktest"
)

func webhookApp(t *testing.T, srv *larktest.Server, extra kv) *App {
	t.Helper()
	s := kv{"im.lark.mode": "webhook", "im.lark.encryptKey": srv.EncryptKey, "im.lark.verificationToken": srv.VerificationToken}
	for k, v := range extra {
		s[k] = v
	}
	return newTestApp(t, srv, s)
}

func encrypted(key string, plain []byte) []byte {
	b, _ := json.Marshal(map[string]string{"encrypt": larktest.Encrypt(key, plain)})
	return b
}

func TestHandleWebhook_RefusesUntilFullyConfigured(t *testing.T) {
	srv := larktest.New(t)
	h, body := srv.Signed(srv.CardAction("ou_1", map[string]any{"ap": "AP-1", "act": "a"}))
	ctx := context.Background()
	if r := newTestApp(t, srv, nil).HandleWebhook(ctx, h, body, &recSink{}); r.Status != http.StatusForbidden {
		t.Fatalf("long-connection mode must refuse webhooks, got %d", r.Status)
	}
	if r := webhookApp(t, srv, kv{"im.lark.encryptKey": ""}).HandleWebhook(ctx, h, body, &recSink{}); r.Status != http.StatusForbidden {
		t.Fatalf("no encrypt key → 403, got %d", r.Status)
	}
	if r := webhookApp(t, srv, kv{"im.lark.verificationToken": ""}).HandleWebhook(ctx, h, body, &recSink{}); r.Status != http.StatusForbidden {
		t.Fatalf("no verification token → 403, got %d", r.Status)
	}
}

func TestHandleWebhook_AnswersTheURLChallenge(t *testing.T) {
	srv := larktest.New(t)
	a := webhookApp(t, srv, nil)
	r := a.HandleWebhook(context.Background(), http.Header{}, encrypted(srv.EncryptKey, srv.Challenge(srv.VerificationToken)), &recSink{})
	if r.Status != http.StatusOK || string(r.Body) != `{"challenge":"c-123"}` {
		t.Fatalf("challenge reply = %d %s", r.Status, r.Body)
	}
	r = a.HandleWebhook(context.Background(), http.Header{}, encrypted(srv.EncryptKey, srv.Challenge("nope")), &recSink{})
	if r.Status != http.StatusForbidden {
		t.Fatalf("a challenge with the wrong token → 403, got %d", r.Status)
	}
}

func TestHandleWebhook_AClickNeedsEncryptionTokenAndSignature(t *testing.T) {
	srv := larktest.New(t)
	a := webhookApp(t, srv, nil)
	sink := &recSink{}
	ctx := context.Background()
	plain := srv.CardAction("ou_1", map[string]any{"ap": "AP-1", "act": "a"})

	if r := a.HandleWebhook(ctx, http.Header{}, plain, sink); r.Status != http.StatusForbidden {
		t.Fatalf("plaintext → 403, got %d", r.Status)
	}
	h, body := srv.Signed(plain)
	if r := a.HandleWebhook(ctx, http.Header{}, body, sink); r.Status != http.StatusForbidden {
		t.Fatalf("no signature → 403, got %d", r.Status)
	}
	bad := h.Clone()
	bad.Set("X-Lark-Signature", strings.Repeat("0", 64))
	if r := a.HandleWebhook(ctx, bad, body, sink); r.Status != http.StatusForbidden {
		t.Fatalf("wrong signature → 403, got %d", r.Status)
	}
	if r := a.HandleWebhook(ctx, h, []byte(`{"encrypt":"bm90IGEgcmVhbCBjaXBoZXJ0ZXh0IQ=="}`), sink); r.Status != http.StatusForbidden {
		t.Fatalf("tampered ciphertext → 403, got %d", r.Status)
	}
	srv.VerificationToken = "someone-elses-token"
	wrongTok, wrongBody := srv.Signed(srv.CardAction("ou_1", map[string]any{"ap": "AP-1", "act": "a"}))
	srv.VerificationToken = "test-verification-token"
	if r := a.HandleWebhook(ctx, wrongTok, wrongBody, sink); r.Status != http.StatusForbidden {
		t.Fatalf("wrong verification token → 403, got %d", r.Status)
	}
	if n, _ := sink.counts(); n != 0 {
		t.Fatal("nothing refused may reach the sink")
	}

	r := a.HandleWebhook(ctx, h, body, sink)
	if r.Status != http.StatusOK || !strings.Contains(string(r.Body), "ok ou_1 AP-1 true") {
		t.Fatalf("good click → %d %s", r.Status, r.Body)
	}
}

func TestHandleWebhook_AnEventItDoesNotHandleIsStillAcknowledged(t *testing.T) {
	srv := larktest.New(t)
	a := webhookApp(t, srv, nil)
	plain, _ := json.Marshal(map[string]any{"schema": "2.0", "header": map[string]any{
		"event_type": "im.chat.updated_v1", "token": srv.VerificationToken, "event_id": "ev-x"}, "event": map[string]any{}})
	h, body := srv.Signed(plain)
	if r := a.HandleWebhook(context.Background(), h, body, &recSink{}); r.Status != http.StatusOK {
		t.Fatalf("authenticated but unhandled → 200 (else Lark redelivers forever), got %d", r.Status)
	}
}

func TestRun_WebhookModeNeverDialsTheLongConnection(t *testing.T) {
	srv := larktest.New(t)
	stop := startRun(t, webhookApp(t, srv, nil), &recSink{})
	srv.WaitCall(t, "botInfo", nil)
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if len(srv.Calls("wsEndpoint")) != 0 || srv.ConnectCount() != 0 {
		t.Fatal("webhook mode must not open the long connection")
	}

	a := webhookApp(t, srv, kv{"im.lark.encryptKey": ""})
	if err := a.Run(context.Background(), &recSink{}); err == nil || !strings.Contains(err.Error(), "Encrypt Key") {
		t.Fatalf("webhook mode without keys must stop with a reason, got %v", err)
	}
}
```

- [ ] **Step 3: 跑测试确认失败**

Run: `cd backend && go test ./internal/imchannel/lark/ -run 'TestHandleWebhook|TestRun_WebhookMode'`
Expected: 编译失败（`a.HandleWebhook undefined`）

- [ ] **Step 4: 写 `webhook.go`**

Create `backend/internal/imchannel/lark/webhook.go`：

```go
package lark

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"

	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"

	"velagateway/internal/imchannel"
)

var ackBody = []byte(`{"msg":"success"}`)

// HandleWebhook 实现 imchannel.Webhooker。只在 webhook 模式、且 Encrypt Key 与 Verification
// Token 都配了时才收;只收加密的请求。
//
// 顺序:解密 → 比对 Token →(URL 校验到此回 challenge)→ 验签 → 分发。URL 校验请求飞书不带
// 签名头,所以签名只能放在认出它之后;但每一种失败都回同一个 403,不让人分辨是解不开、
// Token 不对还是签名不对。鉴权通过后一律 200 —— 飞书会重投非 2xx 的请求。
//
// 不用 SDK 的 dispatcher.Handle:它的签名比较不是常量时间,验签失败回 500(飞书会重投),
// 而且允许不加密的请求只靠 Token 过关。
func (a *App) HandleWebhook(ctx context.Context, h http.Header, body []byte, sink imchannel.ActionSink) imchannel.WebhookReply {
	forbidden := imchannel.Reply(http.StatusForbidden)
	if a.mode != modeWebhook || a.encryptKey == "" || a.verifyToken == "" {
		return forbidden
	}
	var env struct {
		Encrypt string `json:"encrypt"`
	}
	if json.Unmarshal(body, &env) != nil || env.Encrypt == "" {
		return forbidden
	}
	plain, err := larkevent.EventDecrypt(env.Encrypt, a.encryptKey)
	if err != nil {
		return forbidden
	}
	var p struct {
		Type      string `json:"type"`
		Challenge string `json:"challenge"`
		Token     string `json:"token"`
		Header    *struct {
			Token string `json:"token"`
		} `json:"header"`
	}
	if json.Unmarshal(plain, &p) != nil {
		return forbidden
	}
	token := p.Token
	if p.Header != nil {
		token = p.Header.Token
	}
	if subtle.ConstantTimeCompare([]byte(token), []byte(a.verifyToken)) != 1 {
		return forbidden
	}
	if p.Type == "url_verification" {
		b, _ := json.Marshal(map[string]string{"challenge": p.Challenge})
		return imchannel.WebhookReply{Status: http.StatusOK, Body: b}
	}
	want := larkevent.Signature(h.Get(larkevent.EventRequestTimestamp), h.Get(larkevent.EventRequestNonce), a.encryptKey, string(body))
	if subtle.ConstantTimeCompare([]byte(h.Get(larkevent.EventSignature)), []byte(want)) != 1 {
		return forbidden
	}
	resp, err := a.disp.Do(withSink(ctx, sink), plain)
	if err != nil {
		slog.Warn("lark: webhook event not handled", "err", err)
		return imchannel.WebhookReply{Status: http.StatusOK, Body: ackBody}
	}
	if resp == nil {
		return imchannel.WebhookReply{Status: http.StatusOK, Body: ackBody}
	}
	b, err := json.Marshal(resp)
	if err != nil {
		return imchannel.WebhookReply{Status: http.StatusOK, Body: ackBody}
	}
	return imchannel.WebhookReply{Status: http.StatusOK, Body: b}
}
```

- [ ] **Step 5: 跑测试**

Run: `cd backend && gofmt -l internal/imchannel && go vet ./internal/imchannel/... && go test -race ./internal/imchannel/lark/...`
Expected: 全部 PASS

- [ ] **Step 6: 提交**

```bash
git add backend/internal/imchannel/lark
git commit -m "feat(im): accept Lark events and card callbacks by webhook

Co-Authored-By: <model> <noreply@anthropic.com>"
```

---

### Task 5: 登记飞书为交互渠道、配置校验与端到端

**Files:**
- Modify: `backend/internal/service/service.go`（飞书登记 `Caps{Notify: true, Approve: true}`）
- Modify: `backend/internal/handler/admin.go`（`isSecretKey` 认得 `encryptKey`；`im.lark.domain` / `mode` / `apiBase` 校验）
- Modify: `backend/internal/bootstrap/im_settings_test.go`（`TestIMSettings_StatusListsChannels` 里飞书现在能审批）
- Create: `backend/internal/bootstrap/im_lark_test.go`

**Interfaces:**
- Consumes: `lark.New`、`lark.WebhookPath`、`larktest.*`（Task 2–4）、`postIMWebhookFull`（Task 1）、已有 helper `newTestApp`、`login`、`do`、`setSettings`、`bindIM`、`flushIM`、`submitProdHighRisk`、`approvalRow`、`repo.AuditByOperatorLike`、`eq`。
- Produces: bootstrap helper `func (a *testApp) larkIM(token string, extra map[string]any) *larktest.Server`。

- [ ] **Step 1: 写失败的端到端测试**

Create `backend/internal/bootstrap/im_lark_test.go`：

```go
package bootstrap

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"velagateway/internal/imchannel/lark/larktest"
)

// larkIM 起一个假飞书开放平台,把网关的飞书渠道指过去并打开交互审批。长连接模式下等到
// SDK 连上;webhook 模式自动带上假平台的 Encrypt Key 与 Verification Token,等到 Run 查过
// 应用信息。
func (a *testApp) larkIM(token string, extra map[string]any) *larktest.Server {
	a.t.Helper()
	srv := larktest.New(a.t)
	kv := map[string]any{
		"im.lark.approval": true, "im.lark.appID": srv.AppID, "im.lark.appSecret": srv.AppSecret, "im.lark.apiBase": srv.URL,
	}
	if extra["im.lark.mode"] == "webhook" {
		kv["im.lark.encryptKey"] = srv.EncryptKey
		kv["im.lark.verificationToken"] = srv.VerificationToken
	}
	for k, v := range extra {
		kv[k] = v
	}
	a.setSettings(token, kv)
	if kv["im.lark.mode"] == "webhook" {
		srv.WaitCall(a.t, "botInfo", nil)
	} else {
		srv.WaitConnected(a.t)
	}
	return srv
}

func TestLark_BindApproveAndCloseEndToEnd(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	owner := app.login("zhangwei@vela.io", "vela123")
	srv := app.larkIM(admin, nil)

	// 1. 自助绑定:控制台生成码,用自己的飞书私聊机器人
	r := app.do(http.MethodPost, "/api/v1/me/im-identities/lark/code", owner, nil)
	eq(t, r.Code, 0, "issue code")
	var issued struct {
		Code    string `json:"code"`
		BotName string `json:"botName"`
	}
	_ = json.Unmarshal(r.Data, &issued)
	eq(t, issued.BotName, srv.BotName, "the bind dialog names the bot")
	srv.SendDM(t, "ou_owner", "p2p", issued.Code)
	srv.WaitCall(t, "send", func(c larktest.Call) bool {
		return c.Str("receive_id") == "ou_owner" && c.Str("msg_type") == "text" && strings.Contains(c.Str("content"), "zhangwei@vela.io")
	})

	// 2. 建单 → 卡片私信给他
	ap := app.submitProdHighRisk(admin)
	app.flushIM()
	card := srv.WaitCall(t, "send", func(c larktest.Call) bool {
		return c.Str("receive_id") == "ou_owner" && c.Str("msg_type") == "interactive" && strings.Contains(c.Str("content"), ap.ApNo)
	})
	if !strings.Contains(card.Str("content"), "DROP TABLE orders") {
		t.Fatalf("card = %s", card.Str("content"))
	}

	// 3. 点「通过」→ 走控制台同一条判定 → toast → 卡片收起
	toast := srv.Click(t, "ou_owner", map[string]any{"ap": ap.ApNo, "act": "a"})
	eq(t, toast, "已批准,请发起人回控制台执行", "toast")
	eq(t, app.approvalRow(admin, ap.ApNo).Status, "approved", "approved via Lark")
	rows, _ := app.repo.AuditByOperatorLike("%(飞书)")
	eq(t, len(rows), 1, "the audit names the channel")
	app.flushIM()
	srv.WaitCall(t, "patch", func(c larktest.Call) bool {
		return strings.Contains(c.Str("content"), "工单 "+ap.ApNo) && strings.Contains(c.Str("content"), "通过") &&
			!strings.Contains(c.Str("content"), `"act"`)
	})
}

func TestLark_StrangerClickIsRefused(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	srv := app.larkIM(admin, nil)
	ap := app.submitProdHighRisk(admin)

	if toast := srv.Click(t, "ou_stranger", map[string]any{"ap": ap.ApNo, "act": "a"}); !strings.Contains(toast, "未绑定") {
		t.Fatalf("toast = %q", toast)
	}
	eq(t, app.approvalRow(admin, ap.ApNo).Status, "pending", "untouched")
	rows, _ := app.repo.AuditByOperatorLike("%(未绑定)%")
	if len(rows) != 1 || rows[0].ActorName != "lark:ou_stranger" {
		t.Fatalf("stranger audit = %+v", rows)
	}
}

func TestLark_WebhookModeEndToEnd(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	srv := app.larkIM(admin, map[string]any{"im.lark.mode": "webhook"})

	// 开发者后台保存请求地址时的 URL 校验(不带签名头)
	challenge, _ := json.Marshal(map[string]string{"encrypt": larktest.Encrypt(srv.EncryptKey, srv.Challenge(srv.VerificationToken))})
	code, body := app.postIMWebhookFull("lark", nil, string(challenge))
	if code != http.StatusOK || !strings.Contains(body, "c-123") {
		t.Fatalf("challenge → %d %s", code, body)
	}

	app.bindIM("zhangwei@vela.io", "lark", "ou_owner")
	ap := app.submitProdHighRisk(admin)
	h, click := srv.Signed(srv.CardAction("ou_owner", map[string]any{"ap": ap.ApNo, "act": "a"}))
	headers := map[string]string{}
	for k := range h {
		headers[k] = h.Get(k)
	}

	eq(t, app.postIMWebhook("lark", nil, string(click)), http.StatusForbidden, "unsigned")
	eq(t, app.approvalRow(admin, ap.ApNo).Status, "pending", "nothing decided yet")
	code, body = app.postIMWebhookFull("lark", headers, string(click))
	if code != http.StatusOK || !strings.Contains(body, "已批准") {
		t.Fatalf("signed click → %d %s", code, body)
	}
	eq(t, app.approvalRow(admin, ap.ApNo).Status, "approved", "decided via webhook")
	if len(srv.Calls("wsEndpoint")) != 0 {
		t.Fatal("webhook mode must not open the long connection")
	}
}

func TestLark_BadCredentialsShowInStatusWithoutTheSecret(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	srv := larktest.New(t)
	const wrong = "the-wrong-app-secret-value"
	app.setSettings(admin, map[string]any{
		"im.lark.approval": true, "im.lark.appID": srv.AppID, "im.lark.appSecret": wrong, "im.lark.apiBase": srv.URL,
	})

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r := app.do(http.MethodGet, "/api/v1/settings/im/status", admin, nil)
		if strings.Contains(string(r.Data), wrong) {
			t.Fatalf("status leaked the app secret: %s", r.Data)
		}
		var rows []struct {
			Kind      string `json:"kind"`
			Running   bool   `json:"running"`
			LastError string `json:"lastError"`
		}
		_ = json.Unmarshal(r.Data, &rows)
		for _, row := range rows {
			if row.Kind == "lark" && !row.Running && strings.Contains(row.LastError, "10014") {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("rejected credentials must surface as a stopped channel with Lark's error")
}

func TestLark_EncryptKeyIsTreatedAsASecret(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	const key = "lark-encrypt-key-123456"
	app.setSettings(admin, map[string]any{"im.lark.encryptKey": key})
	if raw := app.repo.SettingString("im.lark.encryptKey", ""); raw == "" || strings.Contains(raw, key) {
		t.Errorf("encrypt key must be stored encrypted, stored %q", raw)
	}
	if get := app.do(http.MethodGet, "/api/v1/settings", admin, nil); strings.Contains(string(get.Data), key) {
		t.Error("GET /settings leaked the encrypt key")
	}
}

func TestIMSettings_LarkSettingsAreValidatedOnSave(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	for k, bad := range map[string]string{
		"im.lark.domain":  "feishu.cn",
		"im.lark.mode":    "polling",
		"im.lark.apiBase": "ftp://open.feishu.cn",
	} {
		if app.do(http.MethodPut, "/api/v1/settings", admin, map[string]any{k: bad}).Code == 0 {
			t.Errorf("%s = %q must be rejected at save time", k, bad)
		}
	}
	app.setSettings(admin, map[string]any{"im.lark.domain": "lark", "im.lark.mode": "webhook", "im.lark.apiBase": "https://open.larksuite.com"})
}
```

`backend/internal/bootstrap/im_settings_test.go` 的 `TestIMSettings_StatusListsChannels`，把

```go
			if row.CanApprove {
				t.Error("phase 1 lark is notify-only")
			}
```

改为

```go
			if !row.CanApprove {
				t.Error("lark does interactive approval since phase 3")
			}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd backend && go test ./internal/bootstrap -run 'TestLark_|TestIMSettings_'`
Expected: FAIL（飞书还没登记成交互渠道：绑定码接口拒绝 `lark`、`StatusListsChannels` 断言失败、encryptKey 明文落库、domain / mode 不校验）

- [ ] **Step 3: 登记能力**

`backend/internal/service/service.go`，把

```go
	s.IM.Register(imchannel.Lark, imchannel.Caps{Notify: true}, lark.New)
```

（连同它上面那行讲第 1 期的注释）改为

```go
	// 飞书:群通知(自定义机器人,第 1 期)+ 交互审批(开放平台应用,第 3 期)。
	s.IM.Register(imchannel.Lark, imchannel.Caps{Notify: true, Approve: true}, lark.New)
```

- [ ] **Step 4: 配置校验与密钥识别**

`backend/internal/handler/admin.go`：

`isSecretKey` 改为：

```go
func isSecretKey(k string) bool {
	l := strings.ToLower(k)
	// 飞书的 encryptKey 名字里没有 secret / token,但拿到它就能伪造飞书推来的事件。
	return strings.Contains(l, "secret") || strings.Contains(l, "password") || strings.Contains(l, "token") ||
		strings.HasSuffix(l, "encryptkey")
}
```

`SaveSettings` 的 `switch k` 里，`case "im.telegram.apiBase":` 改为 `case "im.telegram.apiBase", "im.lark.apiBase":`，并在 `case "im.telegram.mode":` 之后加：

```go
			case "im.lark.mode":
				if sv != "ws" && sv != "webhook" {
					verr = fmt.Errorf("只能是 ws 或 webhook")
				}
			case "im.lark.domain":
				if sv != "feishu" && sv != "lark" {
					verr = fmt.Errorf("只能是 feishu 或 lark")
				}
```

- [ ] **Step 5: 跑测试**

Run: `cd backend && go test ./internal/bootstrap -run 'TestLark|TestIM|TestTelegram|TestSettings'`
Expected: 全部 PASS。若有既有测试枚举了「能做交互审批的渠道」（比如绑定弹窗的渠道列表、`ApproveKinds` 的顺序）而现在多出了飞书，按「飞书在前、Telegram 在后」（登记顺序）更新断言 —— 那是本期要的行为变化，不是回归。

- [ ] **Step 6: 全量回归**

Run: `cd backend && go vet ./... && go test ./... 2>&1 | grep -E "^(FAIL|---|panic)"`
Expected: 只有基线失败（`internal/osc` 与 `TestReleaseE2E_TheIndexChangeIsMadeByOSCAndTheReleaseCompletes`）

- [ ] **Step 7: 提交**

```bash
git add backend/internal/service/service.go backend/internal/handler/admin.go backend/internal/bootstrap/im_lark_test.go backend/internal/bootstrap/im_settings_test.go
git commit -m "feat(im): register Lark as an interactive approval channel

Co-Authored-By: <model> <noreply@anthropic.com>"
```

---

### Task 6: 设置页配置飞书交互审批

**Files:**
- Modify: `frontend/src/components/settings/ImChannelsPanel.tsx`
- Modify: `frontend/src/locales/zh.ts`、`frontend/src/locales/en.ts`

**Interfaces:**
- Consumes: 后端配置键（Global Constraints）、`lark.WebhookPath` = `/api/v1/im/lark/webhook`、状态接口里飞书的 `canApprove: true`（Task 5）。
- Produces: `IM_CHANNELS.lark.approval`；`LarkHint` 组件；16 个文案键（zh / en 都要有，`Dict` 类型要求两边一致）。

- [ ] **Step 1: 加配置项**

`ImChannelsPanel.tsx` 的 `IM_CHANNELS.lark` 改为（`notify` 保持原样，加 `approval`）：

```tsx
  lark: {
    notify: [
      { key: 'im.lark.notifyWebhook', label: 'imLarkWebhook', placeholder: 'https://open.feishu.cn/open-apis/bot/v2/hook/xxxx' },
      { key: 'im.lark.notifySecret', label: 'imLarkSecret', placeholderKey: 'imLarkSecretPh', secret: true },
    ],
    approval: [
      { key: 'im.lark.appID', label: 'imLarkAppID', placeholder: 'cli_xxxxxxxxxxxxxxxx' },
      { key: 'im.lark.appSecret', label: 'imLarkAppSecret', placeholderKey: 'imLarkAppSecretPh', secret: true },
      {
        key: 'im.lark.domain', label: 'imLarkDomain', defaultValue: 'feishu',
        options: [{ value: 'feishu', labelKey: 'imLarkDomainFeishu' }, { value: 'lark', labelKey: 'imLarkDomainLark' }],
      },
      {
        key: 'im.lark.mode', label: 'imLarkMode', defaultValue: 'ws',
        options: [{ value: 'ws', labelKey: 'imLarkModeWs' }, { value: 'webhook', labelKey: 'imLarkModeWebhook' }],
      },
      { key: 'im.lark.encryptKey', label: 'imLarkEncryptKey', placeholderKey: 'imLarkEncryptKeyPh', secret: true },
      { key: 'im.lark.verificationToken', label: 'imLarkVerifyToken', placeholderKey: 'imLarkVerifyTokenPh', secret: true },
      { key: 'im.lark.apiBase', label: 'imLarkApiBase', placeholderKey: 'imLarkApiBasePh' },
    ],
  },
```

`IM_CHANNELS` 上方的注释最后一句改为：「飞书群通知（第 1 期）与交互审批（第 3 期）、Telegram 交互审批（第 2 期）。」

`Capability` 的 `extra` 改为：

```tsx
                extra={
                  s.kind === 'telegram' ? <TelegramHint values={values} />
                    : s.kind === 'lark' ? <LarkHint values={values} />
                      : undefined
                }
```

在 `TelegramHint` 之后加：

```tsx
/**
 * 飞书两种接收方式各要管理员在开发者后台做一件事:长连接要把订阅方式切成「使用长连接接收」,
 * webhook 要把这个地址同时填进「事件配置」与「回调配置」的请求地址 —— 飞书不像 Telegram 那样
 * 由网关自己登记。
 */
function LarkHint({ values }: { values: ImValues }) {
  const { t } = useTranslation()
  if (values['im.lark.mode'] !== 'webhook') {
    return <div className="set-hint">{t('imLarkWsNote')}</div>
  }
  const base = String(values['im.callbackBaseURL'] ?? '').trim().replace(/\/+$/, '')
  if (!base) return <div className="set-hint warn">{t('imLarkWebhookNeedsBase')}</div>
  return <div className="set-hint">{t('imLarkWebhookURL')}<code>{`${base}/api/v1/im/lark/webhook`}</code></div>
}
```

- [ ] **Step 2: 加文案**

`frontend/src/locales/zh.ts`，紧跟 `imTgWebhookNeedsBase` 之后加：

```ts
  imLarkAppID: 'App ID',
  imLarkAppSecret: 'App Secret',
  imLarkAppSecretPh: '开发者后台 › 凭证与基础信息',
  imLarkDomain: '版本',
  imLarkDomainFeishu: '飞书(中国)',
  imLarkDomainLark: 'Lark(国际)',
  imLarkMode: '接收方式',
  imLarkModeWs: '长连接(无需公网入口)',
  imLarkModeWebhook: 'Webhook(需要公网 HTTPS)',
  imLarkEncryptKey: 'Encrypt Key',
  imLarkEncryptKeyPh: 'webhook 模式必填:事件与回调 › 加密策略',
  imLarkVerifyToken: 'Verification Token',
  imLarkVerifyTokenPh: 'webhook 模式必填:与 Encrypt Key 在同一页',
  imLarkApiBase: '开放平台地址',
  imLarkApiBasePh: '私有化部署才填,留空按上面的版本',
  imLarkWsNote: '长连接:在开发者后台「事件与回调」把订阅方式设为「使用长连接接收」,添加事件 im.message.receive_v1 与回调 card.action.trigger。多实例部署也安全。',
  imLarkWebhookURL: '在开发者后台「事件配置」与「回调配置」里都填这个地址:',
  imLarkWebhookNeedsBase: 'webhook 模式需要先填写上面的「回调根地址」。',
```

`frontend/src/locales/en.ts`，同一位置加：

```ts
  imLarkAppID: 'App ID',
  imLarkAppSecret: 'App Secret',
  imLarkAppSecretPh: 'Developer console › Credentials & Basic Info',
  imLarkDomain: 'Edition',
  imLarkDomainFeishu: 'Feishu (China)',
  imLarkDomainLark: 'Lark (international)',
  imLarkMode: 'Receive events via',
  imLarkModeWs: 'Long connection (no public endpoint)',
  imLarkModeWebhook: 'Webhook (public HTTPS)',
  imLarkEncryptKey: 'Encrypt Key',
  imLarkEncryptKeyPh: 'Required for webhook: Events & Callbacks › Encryption',
  imLarkVerifyToken: 'Verification Token',
  imLarkVerifyTokenPh: 'Required for webhook: on the same page as the Encrypt Key',
  imLarkApiBase: 'Open platform URL',
  imLarkApiBasePh: 'Only for private deployments; blank follows the edition above',
  imLarkWsNote: 'Long connection: in Events & Callbacks set the subscription mode to "long connection", then add the event im.message.receive_v1 and the callback card.action.trigger. Safe with several gateway instances.',
  imLarkWebhookURL: 'Enter this URL as both the event and the callback request URL: ',
  imLarkWebhookNeedsBase: 'Webhook mode needs the callback base URL above.',
```

- [ ] **Step 3: 验证**

Run: `cd frontend && npm run build && npm run lint && npm run test:unit`
Expected: 全部通过（zh / en 键不一致时 `npm run build` 的类型检查会报错）

- [ ] **Step 4: 提交**

```bash
git add frontend/src/components/settings/ImChannelsPanel.tsx frontend/src/locales/zh.ts frontend/src/locales/en.ts
git commit -m "feat(ui): configure Lark interactive approval

Co-Authored-By: <model> <noreply@anthropic.com>"
```

---

### Task 7: 文档

**Files:**
- Modify: `docs/external-approval-setup.md`
- Modify: `docs/superpowers/specs/2026-09-27-im-channel-approval-design.md`（新增 §20）
- Modify: `docs/features.md`
- Modify: `docs/adr/0020-im-channel-approval.md`（状态行）

- [ ] **Step 1: 启用手册**

`docs/external-approval-setup.md`：

1. 「当前支持」表里飞书那行的交互审批列，`规划中(第 3 期)` 改为 `✅ 开放平台应用(长连接 / webhook)`。
2. 在「## 飞书群通知」一节之后、「## Telegram 交互审批」之前插入：

```markdown
## 飞书交互审批

1. 飞书开发者后台(国际版 Lark 同理)› 创建**企业自建应用**,在「添加应用能力」里打开**机器人**。
2. 「权限管理」开通:`im:message`(以应用身份发消息)、`im:message.p2p_msg:readonly`(读取用户发给机器人的单聊消息)。
   发布一个版本,让权限生效,并把「可用范围」设成需要审批的人。
3. 「事件与回调」:
   - **长连接(推荐)**:订阅方式选「使用长连接接收事件 / 回调」;添加事件 `im.message.receive_v1`,
     添加回调 `card.action.trigger`。网关主动连飞书,不需要公网入口,多实例部署也安全(飞书把每个事件只投给
     其中一个连接)。
   - **webhook**:先在网关「设置 › IM 渠道」填好「回调根地址」;把 `<回调根地址>/api/v1/im/lark/webhook`
     同时填进「事件配置」与「回调配置」的请求地址;在「加密策略」里设置 Encrypt Key,并记下同一页的
     Verification Token。网关只收加密且签名正确的请求,其余一律 403。
4. 网关「设置 › IM 渠道 › 飞书」打开「交互审批」,填 App ID、App Secret,选版本(飞书 / Lark)与接收方式;
   webhook 模式再填 Encrypt Key 与 Verification Token。私有化部署在「开放平台地址」里填自己的地址。保存。
5. 状态显示「运行中」且没有错误提示即已连通。App Secret 错、应用没开机器人能力、开放平台地址填错时,状态会
   显示飞书返回的错误码与原因。
6. 卡片里的「通过 / 驳回」只做决定,不执行;决定后所有渠道上的这张单的卡片都会收起,收起后的卡片只保留单号和
   结果,详情在控制台。
```

3. 「交互审批的账号绑定」一节里，若有「私聊 Telegram 机器人」之类只针对 Telegram 的措辞，改成对两个渠道都成立的说法（如「私聊对应渠道的机器人」），并补一句：飞书里在搜索框搜机器人的名字（绑定弹窗里显示的那个）打开单聊。

- [ ] **Step 2: spec 实现记录**

在 spec 末尾追加：

```markdown
## 20. 第 3 期（飞书交互审批）实现记录

- **出站不用 SDK，入站用 SDK**：发卡、改卡、查机器人名、回私聊都是几个固定的 OpenAPI，用标准库直接调，
  与 Telegram 适配器一个写法，测试也能用同一种假服务器；长连接是 protobuf 帧 + 心跳 + 重连，用官方 SDK
  （`larksuite/oapi-sdk-go/v3` 的 `ws` 包）。SDK 的取连接地址与 WebSocket 拨号都注入网关受控的出站客户端；
  拿不到受控拨号器就拒绝建立长连接。SDK 的 Debug 日志一律丢弃（会打印含绑定码的事件 payload）。
- **飞书渠道按配置分两种形态**：只配群机器人 webhook 是只能通知的 `*Channel`；配了 App ID + App Secret 是
  `*App`（嵌着群通知那一半）。Hub 靠类型断言 `Approver` 判断能否发卡片，只配群通知的飞书不能出现在绑定弹窗里。
- **收卡整张替换**：飞书改卡片只能整张替换，网关不存卡片原文，所以收起后的卡片只留单号与终态。msgRef 是
  `<message_id>|<ApNo>`；卡片 `config.update_multi = true` 才能被改写。
- **按钮 value**：`{"ap": "<ApNo>", "act": "a" | "r"}`，ApNo 白名单 `^[A-Za-z0-9-]{1,32}$`。
- **`Webhooker` 的返回值改为 `WebhookReply{Status, Body}`**：飞书的 URL 校验要回 challenge，卡片回调的 toast
  在响应体里。Telegram 不受影响。
- **webhook 不用 SDK 的 `dispatcher.Handle`**：它的签名比较不是常量时间、验签失败回 500（飞书会重投）、允许
  未加密请求只靠 Token 过关。网关自己做：只收加密请求 → 解密 → 常量时间比对 Verification Token →
  （URL 校验到此回 challenge，飞书不给它签名）→ 常量时间验签 → 交给同一个分发器。任何一步失败都是同一个 403。
- **私聊按 `message_id` 去重**（最近 512 条）：飞书没及时收到确认会重推，绑定码第二次到达时已被消费。
- **原因改用 `plain_text`**：群通知与交互卡片都是。lark_md 会把发起人写的 markdown 链接渲染成真链接。
- **tenant_access_token** 缓存到过期前 5 分钟；飞书回 `99991661` / `99991663` 时换新 token 重试一次。
- **永久性错误停循环**：飞书明确拒绝（code 非零，频控 `99991400` 除外）或开放平台地址回了 4xx，`Run` 返回
  错误，设置页显示原因。
- `isSecretKey` 认得 `encryptKey`：它名字里没有 secret / token，但同样能伪造飞书推送。
- 新增配置键 `im.lark.apiBase`（私有化部署与测试用）。
```

- [ ] **Step 3: features 与 ADR**

- `docs/features.md`：「目前支持飞书**群通知**与 Telegram **交互审批**(long polling / webhook)」改为「目前支持飞书**群通知**与**交互审批**(长连接 / webhook)、Telegram **交互审批**(long polling / webhook)」；「设置」一节里 IM 渠道的描述若列举了渠道能力，同步加上飞书交互审批。
- `docs/adr/0020-im-channel-approval.md` 状态行改为：`- 状态:已采纳(第 1 期:框架 + 飞书群通知;第 2 期:Telegram 交互审批;第 3 期:飞书交互审批;其余渠道分期接入)`

- [ ] **Step 4: 提交**

```bash
git add docs/external-approval-setup.md docs/superpowers/specs/2026-09-27-im-channel-approval-design.md docs/features.md docs/adr/0020-im-channel-approval.md
git commit -m "docs: document Lark approvals and record phase three

Co-Authored-By: <model> <noreply@anthropic.com>"
```
