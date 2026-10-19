# 多渠道 IM 审批 · 第 4 期（钉钉交互审批）Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 钉钉成为交互审批渠道：审批卡片（互动卡片高级版）投放到已绑定成员与机器人的单聊里，点「通过 / 驳回」即决定；用户私聊机器人发绑定码完成绑定；入站走钉钉 Stream 长连接，不需要公网入口。

**Architecture:** 新包 `internal/imchannel/dingtalk` 的 `*App`（企业内部应用）。出站（换 access token、投放卡片、更新卡片、机器人单聊回复）用标准库直接调钉钉 OpenAPI，与飞书、Telegram 适配器同一个写法。入站自己实现 Stream 协议（一次 HTTP 换连接地址 + 一条 JSON 帧的 WebSocket），拨号走网关带 SSRF 防护的拨号器；帧、回调载荷的结构体直接用官方 `dingtalk-stream-sdk-go` 的 `payload` / `chatbot` / `card` 三个纯数据包，**不用它的 `client`**（见下面「与 spec 的偏离」）。三个渠道都要用的重试、拒绝重定向、取受控拨号器、消息去重先抽到 `imchannel` 里（Task 1）。另建 `dingtalk/dttest` 假开放平台：REST 接口 + 用 SDK 的 `payload` 类型扮演 Stream 网关，单元测试和端到端测试都不连真实钉钉。判定仍只在 `DecideBlockFor`。

**Tech Stack:** Go 1.25 · 新依赖 `github.com/open-dingtalk/dingtalk-stream-sdk-go v0.9.1`（2025-04-18 发布；只 import `payload`、`chatbot`、`card` 三个包，它们只依赖标准库与 `github.com/google/uuid`，后者已在 go.mod 里）· 已有的 `github.com/gorilla/websocket v1.5.3` · React 19 + TS。

**Spec:** `docs/superpowers/specs/2026-09-27-im-channel-approval-design.md`（§1 信任模型、§2.2 接口、§2.4 多实例、§4 绑定、§5 出站、§6 入站、§7 决策、§8 收卡、§9 配置、§14 分期、§15 失败朝严、§16 测试；§18–§20 是前三期的实现记录）。

**范围说明：** spec §14 的第 4 期。钉钉群通知（自定义机器人）属于第 6 期，本期钉钉登记为 `Caps{Approve: true}`。

### 与 spec 的偏离（执行前请人确认；Task 6 写进 spec §21）

1. **不用官方 Stream SDK 的 `client` 包，自己实现 Stream 协议。** spec §14 写「用官方 `dingtalk-stream-sdk-go`」。读了 v0.9.1 源码（`client/client.go`），它有三处与网关的约束冲突、且都不能经由选项关掉：
   - 取连接地址写死 `http.DefaultTransport`，WebSocket 拨号写死 `websocket.DefaultDialer`，只能配代理 —— 注入不了网关的受控出站客户端（spec §9 的 SSRF 要求）。
   - `GetConnectionEndpoint` 在 Debug 级把**整个请求体**（含 `clientSecret`）打进一个进程全局的 logger。
   - 断线重连跑在 `context.Background()` 上的无限循环里，`Start(ctx)` 的 ctx 管不到它：Hub `Reload` 之后旧连接不会退出，会继续消费事件（多个连接分走同一个应用的事件，新配置下的渠道收不到一部分点击）。
   协议本身很小：`POST /v1.0/gateway/connections/open` 换 `{endpoint, ticket}`，拨 `endpoint?ticket=…`，收发 JSON 帧（`SYSTEM ping` 原样回、`SYSTEM disconnect` 换连接、`CALLBACK` 处理后带 `messageId` 回执）。帧与载荷的结构体用 SDK 自己的 `payload.DataFrame` / `payload.DataFrameResponse` / `chatbot.BotCallbackDataModel` / `card.CardRequest` / `card.CardResponse`，假网关也用同一套类型，测到的是钉钉的线上格式。
2. **本期只有 Stream 模式，钉钉不实现 `Webhooker`。** spec §6 列了钉钉 webhook（可选）。钉钉的 HTTP 回调分两套：机器人消息走开发者后台配的地址（`timestamp` + `sign` 头），互动卡片回调要另调接口登记回调路由、卡片创建时带 `callbackRouteKey`，签名规则也不同。Stream 不需要公网入口、多实例安全，已覆盖全部场景。`POST /api/v1/im/dingtalk/webhook` 因此一律 403（Hub 找不到 `Webhooker`），失败朝严。
3. **卡片要管理员在钉钉卡片平台搭一个模板，新增配置键 `im.dingtalk.cardTemplateId`。** 钉钉的 Stream 卡片回调只支持「互动卡片高级版」，高级版只能用卡片平台上建好的模板（普通版 `StandardCard` 不支持 Stream 回调）。模板的变量名与按钮回传参数由网关规定（Global Constraints），启用手册给出逐步搭法（Task 6）。没填模板 ID 时 `Run` 在任何网络请求之前报错，设置页看得见。
4. **点击后的提示写回卡片上的 `tip` 变量。** 钉钉卡片回调没有 toast；回调应答里带 `cardData` 就能就地改卡片，网关把 `ActionResult.Toast` 写进 `tip`。
5. **收卡只改变量，不整张替换。** `PUT /v1.0/card/instances` 带 `updateCardDataByKey: true` 只改 `status` / `result` / `tip`，卡片原有内容（单号、命令、原因）保留 —— 比飞书好。
6. **没有机器人名。** 钉钉没有按 robotCode 查机器人名称的 OpenAPI，`*App` 不实现 `BotNamer`，绑定弹窗不显示名字（前端已支持 `botName` 缺省）。

## Global Constraints

- 模块名 `velagateway`，Go 1.25；后端命令都在 `backend/` 下执行；跑后端测试需要 `createdb vela_test`，**绝不用** `vela_gateway`。
- 本机没有 MySQL：`internal/osc` 整包与 bootstrap 的 `TestReleaseE2E_TheIndexChangeIsMadeByOSCAndTheReleaseCompletes` 是已知基线失败，不算回归。bootstrap 整包约 5 分钟，前台跑。
- 本期**没有**数据库迁移。新配置键全部落在 `tbl_setting`。
- 配置键：`im.dingtalk.approval`（bool）、`im.dingtalk.clientID`、`im.dingtalk.clientSecret`🔒、`im.dingtalk.robotCode`（缺省 = clientID）、`im.dingtalk.cardTemplateId`、`im.dingtalk.apiBase`（缺省 `https://api.dingtalk.com`；私有化与测试用）。**没有 `mode` 键**。`clientSecret` 名字里有 secret，`isSecretKey` 已经认得它（加密落库、接口不回显）。
- 渠道标识 `imchannel.DingTalk` = `"dingtalk"`，`imchannel.Label` = `钉钉`（已存在）。
- 平台用户 ID 是钉钉 **userid**（即 staffId）：绑定取私聊事件的 `senderStaffId`；投放卡片 `openSpaceId = "dtv1.card//IM_ROBOT.<userid>"`、`userIdType = 1`；认点击取卡片回调的 `userId`。`userid` 白名单 `^[A-Za-z0-9_-]{1,64}$`。`userIdType == 2`（unionId）的点击不认。
- `outTrackId`（即 msgRef）：`aegis-<ApNo>-<16 位小写十六进制>`，白名单 `^aegis-([A-Za-z0-9-]{1,32})-[0-9a-f]{16}$`；ApNo 白名单 `^[A-Za-z0-9-]{1,32}$`。
- 卡片模板变量（`cardParamMap` 的键，全部字符串）：`title`、`summary`、`reason`、`command`、`consoleUrl`、`status`（`pending` | `done`）、`result`、`tip`。按钮「回传请求」的参数名 `act`，值 `a`（通过）/ `r`（驳回）。按钮数据不带任何权限信息。
- Stream 订阅：`SYSTEM ping`、`SYSTEM disconnect`、`CALLBACK /v1.0/im/bot/messages/get`（机器人消息）、`CALLBACK /v1.0/card/instances/callback`（卡片回调）。不上报 `localIp`。
- 失败朝严（spec §15）：渠道未开交互审批 → 长连接不启动、不发卡片；钉钉 webhook 一律 403；伪造或畸形的点击（outTrackId 不是网关造的、未知动作、没有点击者、unionId）不进 sink；群聊、非文本、没有 `senderStaffId` 的消息不进 sink、不回复。
- 出站只用 `Deps.HTTP`（带 SSRF 防护，经 `imchannel.NoRedirects` 复制一份不跟随重定向的）。WebSocket 拨号用 `imchannel.GuardedDial(a.client)` 取出的受控拨号器；拿不到时 `Run` 报错，**不许**退回默认拨号器。钉钉下发的长连接地址也过一遍 `Deps.ValidateURL`（`wss` 按 `https`、`ws` 按 `http`）。
- 永久性错误（凭据被拒、没开 Stream / 卡片权限、开放平台地址填错导致的 3xx / 4xx / 解析不了的 2xx、出站校验拒绝）让 `Run` 返回错误，进 `LastError`；网络错误、DNS 解析失败、5xx、429、钉钉限流码（含 `QpsLimit` 或 `Throttl`）退避重试。纯配置错（没填模板 ID、拿不到受控拨号器）在任何网络请求之前报。
- Builder（`dingtalk.New`）在 Hub 锁内调用，必须廉价、不做 I/O。
- Client Secret 只在请求体里：所有错误文本先把它换成 `<client-secret>`。
- 代码注释沿用仓库风格（中文，讲清「为什么」）；提交信息英文 conventional commits，结尾 `Co-Authored-By: <执行提交的模型名> <noreply@anthropic.com>`。
- 前端验证：`cd frontend && npm run build && npm run lint && npm run test:unit`。

## Review Focus

spec 没有逐条写出、但最可能伤到使用者的五类输入。每条的测试放进负责它的任务：

1. **Reload 之后旧长连接不退出**：管理员改了配置、Hub 取消旧 `Run`，旧连接若还在重连，就和新连接分走同一个应用的事件，部分点击落到已经不认的实例上。`Run` 被取消后必须很快返回且不再建连（Task 3 `TestRun_StopsWhenCancelledAndStaysStopped`）。
2. **投放接口成功、投放本身失败**：对方不在应用可见范围内时钉钉回 HTTP 200，只在 `deliverResults[].success=false` 里说明。不当错误处理，库里会记一张不存在的卡片（Task 2 `TestSendCard_AFailedDeliveryIsAnError`）。
3. **管理员没建模板 / 没填模板 ID**：卡片一张都发不出去，设置页却显示「运行中」。必须在任何网络请求之前报错（Task 3 `TestRun_RefusesBeforeAnyRequestWhenTheCardTemplateIsMissing`，Task 4 `TestDingTalk_AMissingCardTemplateShowsInStatus`）。
4. **钉钉重投同一条私聊**：没及时收到回执会重推，绑定码第二次到达时已被消费，用户会在「已绑定」之后又收到「无效或已过期」。按 `msgId` 去重（Task 3 `TestRun_ARedeliveredMessageIsHandledOnce`）。
5. **伪造或畸形的卡片回调**：outTrackId 不是网关造的形状、单号里有怪字符、未知动作、没有点击者、unionId 身份 —— 一律只回「无法识别的操作」，不进 sink（Task 3 `TestRun_ClicksThatDoNotNameACardOrAnActionAreDropped`）。

---

## 文件结构

**新建（后端）**

| 文件 | 职责 |
|---|---|
| `backend/internal/imchannel/netutil.go` | 渠道共用：`Retry` / `Sleep`、`NoRedirects`、`GuardedDial`、`Recent`（消息去重） |
| `backend/internal/imchannel/netutil_test.go` | 上面四样的测试 |
| `backend/internal/imchannel/dingtalk/app.go` | `*App`：构造、OpenAPI 调用（token 缓存与刷新、错误分类、脱敏）、投放 / 收起卡片、单聊回复 |
| `backend/internal/imchannel/dingtalk/card.go` | 模板变量、outTrackId 的生成与解析 |
| `backend/internal/imchannel/dingtalk/stream.go` | `Run`、Stream 连接与重连、帧分发、点击与私聊处理 |
| `backend/internal/imchannel/dingtalk/dttest/server.go` | 假开放平台：REST 接口、调用记录、故障注入 |
| `backend/internal/imchannel/dingtalk/dttest/stream.go` | 假 Stream 网关：发 ticket、WebSocket、推帧并收回执、回调载荷 |
| `backend/internal/imchannel/dingtalk/app_test.go` `card_test.go` `stream_test.go` | 适配器测试 |
| `backend/internal/bootstrap/im_dingtalk_test.go` | 钉钉端到端测试 |

**修改（后端）**：`imchannel/lark/app.go` 与 `lark/inbound.go`（改用共用辅助函数）、`imchannel/telegram/inbound.go`（同上）、`service/service.go`（登记钉钉）、`handler/admin.go`（`im.dingtalk.*` 校验）、`bootstrap/im_settings_test.go`（状态里有钉钉）、`backend/go.mod` / `go.sum`。

**修改（前端）**：`components/settings/ImChannelsPanel.tsx`、`locales/zh.ts`、`locales/en.ts`。

**修改（文档）**：`docs/external-approval-setup.md`、spec（新增 §21）、`docs/features.md`、`docs/adr/0020-im-channel-approval.md`（状态行）。

---

### Task 1: 渠道共用的出站与去重辅助函数

飞书与 Telegram 各有一份退避重试，飞书还有拒绝重定向、取受控拨号器、私聊去重。钉钉四样都要用；先把它们抽到 `imchannel`，三个渠道共用一份，免得出现第三份拷贝。纯重构：飞书、Telegram 的行为与已有测试都不变。

**Files:**
- Create: `backend/internal/imchannel/netutil.go`
- Create: `backend/internal/imchannel/netutil_test.go`
- Modify: `backend/internal/imchannel/lark/app.go`（删 `noRedirects`、`retry`；字段 `seen recent` → `seen imchannel.Recent`）
- Modify: `backend/internal/imchannel/lark/inbound.go`（删 `guardedDial`、`recent`；改用共用函数）
- Modify: `backend/internal/imchannel/telegram/inbound.go`（删 `retry` 方法与 `sleep`；改用共用函数）

**Interfaces:**
- Produces（package `imchannel`）：
  ```go
  func Retry(ctx context.Context, name string, permanent func(error) bool, fn func() error) error
  func Sleep(ctx context.Context, d time.Duration) bool
  func NoRedirects(c *http.Client) *http.Client
  func GuardedDial(c *http.Client) (func(ctx context.Context, network, addr string) (net.Conn, error), error)
  type Recent struct { /* 未导出字段 */ }
  func (r *Recent) First(id string) bool
  ```

- [ ] **Step 1: 写失败的测试**

Create `backend/internal/imchannel/netutil_test.go`：

```go
package imchannel_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"velagateway/internal/imchannel"
)

func TestRetry_StopsOnAPermanentError(t *testing.T) {
	rejected := errors.New("rejected")
	calls := 0
	err := imchannel.Retry(context.Background(), "test", func(e error) bool { return errors.Is(e, rejected) }, func() error {
		calls++
		return rejected
	})
	if !errors.Is(err, rejected) || calls != 1 {
		t.Fatalf("err = %v after %d calls, want the permanent error after one call", err, calls)
	}
}

func TestRetry_RetriesATransientErrorUntilItSucceeds(t *testing.T) {
	calls := 0
	err := imchannel.Retry(context.Background(), "test", func(error) bool { return false }, func() error {
		calls++
		if calls == 1 {
			return errors.New("flaky")
		}
		return nil
	})
	if err != nil || calls != 2 {
		t.Fatalf("err = %v after %d calls, want success on the second", err, calls)
	}
}

func TestRetry_ReturnsNilWhenCancelledWhileBackingOff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	err := imchannel.Retry(ctx, "test", func(error) bool { return false }, func() error { return errors.New("down") })
	if err != nil || time.Since(start) > 900*time.Millisecond {
		t.Fatalf("err = %v after %v, want nil soon after the cancel", err, time.Since(start))
	}
}

func TestNoRedirects_HandsBackTheRedirectAndLeavesTheOriginalAlone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/elsewhere" {
			t.Error("the redirect must not be followed")
			return
		}
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	}))
	defer srv.Close()
	orig := &http.Client{Transport: &http.Transport{}}
	c := imchannel.NoRedirects(orig)
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want the 302 itself", resp.StatusCode)
	}
	if orig.CheckRedirect != nil {
		t.Error("the original client must stay untouched")
	}
	if c.Transport != orig.Transport {
		t.Error("the copy must keep the guarded transport")
	}
	if imchannel.NoRedirects(nil) != nil {
		t.Error("nil in, nil out")
	}
}

func TestGuardedDial_RefusesAClientWithoutAControlledDialer(t *testing.T) {
	for name, c := range map[string]*http.Client{
		"nil client":                    nil,
		"default transport":             {},
		"transport without DialContext": {Transport: &http.Transport{}},
	} {
		if _, err := imchannel.GuardedDial(c); err == nil {
			t.Errorf("%s: must be refused", name)
		}
	}
	dial, err := imchannel.GuardedDial(&http.Client{Transport: &http.Transport{DialContext: (&net.Dialer{}).DialContext}})
	if err != nil || dial == nil {
		t.Fatalf("a transport with its own DialContext must be accepted: %v", err)
	}
}

func TestRecent_RemembersTheLast512IDs(t *testing.T) {
	var r imchannel.Recent
	if !r.First("m-0") || r.First("m-0") {
		t.Fatal("the second sighting of an id is a duplicate")
	}
	if !r.First("") || !r.First("") {
		t.Fatal("empty ids are never deduplicated")
	}
	for i := 1; i <= 512; i++ {
		r.First(fmt.Sprintf("m-%d", i))
	}
	if !r.First("m-0") {
		t.Fatal("the oldest id is forgotten once 512 newer ones arrived")
	}
	if r.First("m-512") {
		t.Fatal("a recent id is still remembered")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd backend && go test ./internal/imchannel/ -run 'TestRetry_|TestNoRedirects_|TestGuardedDial_|TestRecent_'`
Expected: 编译失败（`undefined: imchannel.Retry` 等）

- [ ] **Step 3: 写共用函数**

Create `backend/internal/imchannel/netutil.go`：

```go
package imchannel

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"
)

// maxBackoff 是渠道重试的退避上限。
const maxBackoff = 30 * time.Second

// Retry 反复调 fn,直到成功、遇到 permanent 认定的错误,或 ctx 结束(后者返回 nil —— 那是有人
// 叫它停,不是失败)。退避从 1 秒翻倍到 30 秒。name 是渠道名,只进日志。
//
// 永久性错误(凭据被拒、地址填错)必须让调用方停下来,把原因交给设置页:无限重试只会留下一串
// WARN,而设置页上是一个开关开着、状态「运行中」、什么也发不出去的渠道。
func Retry(ctx context.Context, name string, permanent func(error) bool, fn func() error) error {
	backoff := time.Second
	for {
		err := fn()
		if err == nil || permanent(err) {
			return err
		}
		if ctx.Err() != nil {
			return nil
		}
		slog.Warn(name+": call failed, retrying", "err", err, "retryIn", backoff)
		if !Sleep(ctx, backoff) {
			return nil
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

// Sleep 睡 d;ctx 先结束就提前返回 false。
func Sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// NoRedirects 复制一份出站客户端(Transport 即受控拨号器照旧共用),遇到 3xx 不跟随、把它原样
// 交回来。网关的出站客户端本来就拒绝跟随,但它报的是一个普通错误,与网络错误分不开,只能当暂时
// 的无限重试;拿到 3xx 响应本身,渠道才能把它判成「开放平台地址填错」。
func NoRedirects(c *http.Client) *http.Client {
	if c == nil {
		return nil
	}
	cp := *c
	cp.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &cp
}

// GuardedDial 取出网关出站客户端里带 SSRF 拦截的拨号器,给长连接的 WebSocket 用。拿不到就拒绝,
// 不退回默认拨号器 —— 那样长连接就绕开了内网拦截。
func GuardedDial(c *http.Client) (func(ctx context.Context, network, addr string) (net.Conn, error), error) {
	if c != nil {
		if t, ok := c.Transport.(*http.Transport); ok && t.DialContext != nil {
			return t.DialContext, nil
		}
	}
	return nil, errors.New("出站客户端没有受控的拨号器,拒绝建立长连接")
}

// Recent 记住最近处理过的消息 ID。平台没及时收到确认会重推同一条消息;绑定码第二次到达时已被
// 消费,不去重的话用户会在「已绑定」之后又收到一句「无效或已过期」。零值可用。
type Recent struct {
	mu    sync.Mutex
	ids   map[string]struct{}
	order []string
}

const recentMax = 512

// First 报告 id 是不是第一次出现。空 id 不去重。
func (r *Recent) First(id string) bool {
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
```

- [ ] **Step 4: 跑新测试确认通过**

Run: `cd backend && go test ./internal/imchannel/ -run 'TestRetry_|TestNoRedirects_|TestGuardedDial_|TestRecent_'`
Expected: PASS（`TestRetry_RetriesATransientErrorUntilItSucceeds` 约 1 秒）

- [ ] **Step 5: 飞书改用共用函数**

`backend/internal/imchannel/lark/app.go`：
- 删掉整个 `noRedirects` 函数（连同它上面的注释）与整个 `retry` 方法（连同注释）。
- `newApp` 里 `client: noRedirects(d.HTTP)` 改为 `client: imchannel.NoRedirects(d.HTTP)`。
- `App` 的字段 `seen recent // 已处理的私聊 message_id` 改为 `seen imchannel.Recent // 已处理的私聊 message_id`。
- 删掉不再用到的 import（`log/slog` 若只有 `retry` 在用）。

`backend/internal/imchannel/lark/inbound.go`：
- `Run` 里 `} else if _, err := guardedDial(a.client); err != nil {` 改为 `} else if _, err := imchannel.GuardedDial(a.client); err != nil {`。
- `Run` 里 `err := a.retry(ctx, func() error {` 改为 `err := imchannel.Retry(ctx, "lark", permanent, func() error {`。
- `stream` 里 `dial, err := guardedDial(a.client)` 改为 `dial, err := imchannel.GuardedDial(a.client)`。
- 删掉整个 `guardedDial` 函数（连同注释）、整个 `recent` 类型、`recentMax` 常量与 `first` 方法（连同注释）。
- `onMessage` 里 `!a.seen.first(str(m.MessageId))` 改为 `!a.seen.First(str(m.MessageId))`。
- 删掉不再用到的 import（`net`、`sync`）。

- [ ] **Step 6: Telegram 改用共用函数**

`backend/internal/imchannel/telegram/inbound.go`：
- 三处 `c.retry(ctx, func() error {` 改为 `imchannel.Retry(ctx, "telegram", isPermanent, func() error {`（第 35 行是写在一行里的 `c.retry(ctx, func() error { return c.call(ctx, c.client, "getMe", map[string]any{}, &me) })`，同样替换）。
- `runPolling` 里 `if !sleep(ctx, backoff) {` 改为 `if !imchannel.Sleep(ctx, backoff) {`。
- 删掉 `retry` 方法与 `sleep` 函数（连同注释）。

- [ ] **Step 7: 跑三个渠道的全部测试**

Run: `cd backend && gofmt -l internal/imchannel && go vet ./internal/imchannel/... && go test -race -count=1 ./internal/imchannel/...`
Expected: gofmt 无输出；全部 PASS（飞书、Telegram 已有测试一个不改都要过）

- [ ] **Step 8: Commit**

```bash
cd backend
git add internal/imchannel/netutil.go internal/imchannel/netutil_test.go internal/imchannel/lark/app.go internal/imchannel/lark/inbound.go internal/imchannel/telegram/inbound.go
git commit -m "refactor(im): share retry, redirect, dial-guard and dedupe helpers across channels"
```

---

### Task 2: 钉钉应用的出站：OpenAPI 客户端、卡片模板变量、投放与收起

**Files:**
- Create: `backend/internal/imchannel/dingtalk/app.go`
- Create: `backend/internal/imchannel/dingtalk/card.go`
- Create: `backend/internal/imchannel/dingtalk/dttest/server.go`
- Test: `backend/internal/imchannel/dingtalk/app_test.go`、`backend/internal/imchannel/dingtalk/card_test.go`

**Interfaces:**
- Consumes: Task 1 的 `imchannel.NoRedirects`；已有的 `imchannel.Deps`、`ApprovalCard`、`CardState`、`Key`、`DingTalk`；`model.RiskHigh`。
- Produces（package `dingtalk`，后续任务用）：
  ```go
  type App struct {
      clientID, clientSecret, robotCode, template, base string
      client   *http.Client
      validate func(string) error
      tokMu    sync.Mutex
      tok      string
      tokExp   time.Time
  }
  func New(d imchannel.Deps) imchannel.Channel // *App | nil
  func (a *App) Kind() string
  func (a *App) SendCard(ctx context.Context, userID string, c imchannel.ApprovalCard) (string, error) // ref = outTrackId
  func (a *App) CloseCard(ctx context.Context, ref string, final imchannel.CardState) error
  func (a *App) request(ctx context.Context, method, path, token string, body any) ([]byte, error)
  func (a *App) call(ctx context.Context, method, path string, body any) ([]byte, error)
  func (a *App) token(ctx context.Context) (string, error)
  func (a *App) sendText(ctx context.Context, userID, text string) error
  func (a *App) scrub(err error) error
  func permanent(err error) bool
  var errBadBase, errNoTemplate error
  const pathOpen = "/v1.0/gateway/connections/open"
  func newTrackID(apNo string) (string, error)
  func trackApNo(track string) (string, bool)
  ```
- dttest（Task 3、4 扩展它）：
  ```go
  type Call struct { Method string; Params map[string]any } // Method: token | open | deliver | update | oto
  type Failure struct { HTTP int; Code, Msg, Raw, Location string }
  type Server struct { *httptest.Server; ClientID, ClientSecret string; /* 未导出 */ }
  func New(t testing.TB) *Server
  func (s *Server) SetFail(method string, f Failure)
  func (s *Server) RotateToken()
  func (s *Server) RejectDelivery(msg string)
  func (s *Server) Calls(method string) []Call
  func (s *Server) WaitCall(t testing.TB, method string, match func(Call) bool) Call
  func (c Call) At(keys ...string) string
  func (c Call) Str(key string) string
  ```

- [ ] **Step 1: 写假开放平台**

Create `backend/internal/imchannel/dingtalk/dttest/server.go`：

```go
// Package dttest 是一个假的钉钉开放平台:记录每次 OpenAPI 调用,按需注入失败,并扮演 Stream
// 网关把回调推给适配器(stream.go)。适配器和端到端测试都用它,不连真实的钉钉。
package dttest

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
	Method string // token | open | deliver | update | oto
	Params map[string]any
}

// Failure 让某个方法回一个钉钉错误(HTTP 为 0 时按 400)。
//
// Raw 非空时不回钉钉的 JSON,原样回这段 body(HTTP 为 0 时按 200);Location 非空时带上这个头
// —— 扮演填错的开放平台地址:一个回 200 HTML 的控制台页面,或一次重定向。
type Failure struct {
	HTTP     int
	Code     string
	Msg      string
	Raw      string
	Location string
}

type Server struct {
	*httptest.Server
	ClientID     string
	ClientSecret string

	mu         sync.Mutex
	calls      []Call
	fail       map[string]Failure
	token      string
	rotated    int
	rejectWith string
	tickets    map[string]bool
	nextTicket int
}

func New(t testing.TB) *Server {
	s := &Server{
		ClientID: "dingtestclientid", ClientSecret: "test-client-secret-0000",
		fail: map[string]Failure{}, token: "at-0", tickets: map[string]bool{},
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

// RotateToken 作废当前的 access token:之后带旧 token 的调用回 401。
func (s *Server) RotateToken() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rotated++
	s.token = fmt.Sprintf("at-%d", s.rotated)
}

// RejectDelivery 让之后的投放「接口成功、投放失败」:HTTP 200,deliverResults 里 success=false。
// 对方不在应用可见范围内时钉钉就是这么回的。
func (s *Server) RejectDelivery(msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rejectWith = msg
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// credentialError 把收到的 secret 回显进 message:扮演一个往错误里回显请求的代理 / 私有化部署,
// 让「错误里不出现 Client Secret」的测试真的有东西可拦(否则删掉 scrub 它们也照样绿)。
func credentialError(w http.ResponseWriter, got any) {
	g, _ := got.(string)
	writeJSON(w, http.StatusBadRequest, map[string]any{
		"code": "invalidClientIdOrSecret", "message": "无效的应用凭证: " + g, "requestid": "req-test",
	})
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	var params map[string]any
	_ = json.NewDecoder(r.Body).Decode(&params)
	if params == nil {
		params = map[string]any{}
	}
	var method string
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1.0/oauth2/accessToken":
		method = "token"
	case r.Method == http.MethodPost && r.URL.Path == "/v1.0/gateway/connections/open":
		method = "open"
	case r.Method == http.MethodPost && r.URL.Path == "/v1.0/card/instances/createAndDeliver":
		method = "deliver"
	case r.Method == http.MethodPut && r.URL.Path == "/v1.0/card/instances":
		method = "update"
	case r.Method == http.MethodPost && r.URL.Path == "/v1.0/robot/oToMessages/batchSend":
		method = "oto"
	default:
		http.NotFound(w, r)
		return
	}
	s.mu.Lock()
	s.calls = append(s.calls, Call{Method: method, Params: params})
	f, failing := s.fail[method]
	token, reject := s.token, s.rejectWith
	s.mu.Unlock()
	if failing && (f.Raw != "" || f.Location != "") {
		status := f.HTTP
		if status == 0 {
			status = http.StatusOK
		}
		if f.Location != "" {
			w.Header().Set("Location", f.Location)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(f.Raw))
		return
	}
	if failing {
		status := f.HTTP
		if status == 0 {
			status = http.StatusBadRequest
		}
		writeJSON(w, status, map[string]any{"code": f.Code, "message": f.Msg, "requestid": "req-test"})
		return
	}
	switch method {
	case "token":
		if params["appKey"] != s.ClientID || params["appSecret"] != s.ClientSecret {
			credentialError(w, params["appSecret"])
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"accessToken": token, "expireIn": 7200})
		return
	case "open":
		// 换长连接地址不带 access token,凭据直接在请求体里。
		if params["clientId"] != s.ClientID || params["clientSecret"] != s.ClientSecret {
			credentialError(w, params["clientSecret"])
			return
		}
		s.mu.Lock()
		s.nextTicket++
		ticket := fmt.Sprintf("ticket-%d", s.nextTicket)
		s.tickets[ticket] = true
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"endpoint": "ws" + strings.TrimPrefix(s.URL, "http") + "/connect", "ticket": ticket})
		return
	}
	if r.Header.Get("x-acs-dingtalk-access-token") != token {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"code": "InvalidAuthentication", "message": "不合法的access_token", "requestid": "req-test"})
		return
	}
	switch method {
	case "deliver":
		space, _ := params["openSpaceId"].(string)
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "result": map[string]any{
			"outTrackId": params["outTrackId"],
			"deliverResults": []any{map[string]any{
				"spaceType": "IM_ROBOT", "spaceId": strings.TrimPrefix(space, "dtv1.card//IM_ROBOT."),
				"success": reject == "", "errorMsg": reject, "carrierId": "carrier-1",
			}},
		}})
	case "update":
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "result": true})
	case "oto":
		writeJSON(w, http.StatusOK, map[string]any{"processQueryKey": "pqk-1", "invalidStaffIdList": []any{}, "flowControlledStaffIdList": []any{}})
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
	t.Fatalf("dttest: no %s call matched within 5s; got %+v", method, s.Calls(method))
	return Call{}
}

// At 取一个(可能嵌套的)参数:At("cardData", "cardParamMap", "command")。非字符串的值按 JSON
// 返回(数字 1 → "1",false → "false"),不存在时返回 ""。
func (c Call) At(keys ...string) string {
	var v any = c.Params
	for _, k := range keys {
		m, ok := v.(map[string]any)
		if !ok {
			return ""
		}
		v = m[k]
	}
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return ""
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}

// Str 取一个顶层参数,同 At(key)。
func (c Call) Str(key string) string { return c.At(key) }
```

- [ ] **Step 2: 写失败的适配器测试**

Create `backend/internal/imchannel/dingtalk/card_test.go`：

```go
package dingtalk

import (
	"strings"
	"testing"
	"unicode/utf8"

	"velagateway/internal/imchannel"
)

func TestCardParams_ArePlainTextCarryingEveryTemplateVariable(t *testing.T) {
	p := cardParams(imchannel.ApprovalCard{
		ApNo: "AP-1", Env: "prod", Tier: "prod-core", Instance: "pg-main", Risk: "high", Initiator: "张伟",
		Reason: "[查看详情](https://evil.example) `x`", Command: "SELECT 1", ConsoleURL: "https://gw.corp.io/approvals",
	})
	for _, k := range []string{paramTitle, paramSummary, paramReason, paramCommand, paramConsole, paramStatus, paramResult, paramTip} {
		if _, ok := p[k]; !ok {
			t.Errorf("template variable %q missing", k)
		}
	}
	// 模板里用「文本」组件:原因原样交过去,不转义、不改写 —— 纯文本里没有能变成链接的东西。
	if p[paramReason] != "[查看详情](https://evil.example) `x`" {
		t.Errorf("reason must be passed verbatim, got %q", p[paramReason])
	}
	if !strings.Contains(p[paramTitle], "🔴") || !strings.Contains(p[paramTitle], "AP-1") {
		t.Errorf("title = %q", p[paramTitle])
	}
	for _, want := range []string{"PROD(prod-core)", "pg-main / 默认", "张伟"} {
		if !strings.Contains(p[paramSummary], want) {
			t.Errorf("summary %q missing %q", p[paramSummary], want)
		}
	}
	if p[paramConsole] != "https://gw.corp.io/approvals" || p[paramStatus] != statusPending || p[paramResult] != "" || p[paramTip] != "" {
		t.Errorf("params = %+v", p)
	}
}

func TestCardParams_ClipTheCommand(t *testing.T) {
	long := strings.Repeat("长", commandLimit+10)
	got := cardParams(imchannel.ApprovalCard{ApNo: "AP-1", Command: long})[paramCommand]
	if utf8.RuneCountInString(got) != commandLimit+1 || !strings.HasSuffix(got, "…") {
		t.Fatalf("clipped command has %d runes", utf8.RuneCountInString(got))
	}
}

func TestClosedParams_OnlyTouchTheStatusVariables(t *testing.T) {
	p := closedParams(imchannel.CardState{Status: "approved", Text: "已由 李四(钉钉) 通过"})
	if len(p) != 3 || p[paramStatus] != statusDone || p[paramResult] != "已由 李四(钉钉) 通过" || p[paramTip] != "" {
		t.Fatalf("closed params = %+v", p)
	}
}

func TestTrackID_RoundTrips(t *testing.T) {
	id, err := newTrackID("AP-2294")
	if err != nil {
		t.Fatal(err)
	}
	if ap, ok := trackApNo(id); !ok || ap != "AP-2294" {
		t.Fatalf("trackApNo(%q) = %q, %v", id, ap, ok)
	}
	if other, _ := newTrackID("AP-2294"); other == id {
		t.Fatal("two cards of the same approval need different outTrackIds")
	}
	for _, bad := range []string{
		"", "AP-2294", "aegis-AP-2294", "aegis-AP 1-0123456789abcdef",
		"aegis-AP-1-0123456789ABCDEF", "x-aegis-AP-1-0123456789abcdef",
	} {
		if _, ok := trackApNo(bad); ok {
			t.Errorf("trackApNo(%q) must be refused", bad)
		}
	}
	if _, err := newTrackID("AP 1"); err == nil {
		t.Fatal("an approval number outside the whitelist cannot name a card")
	}
}
```

Create `backend/internal/imchannel/dingtalk/app_test.go`：

```go
package dingtalk

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"velagateway/internal/imchannel"
	"velagateway/internal/imchannel/dingtalk/dttest"
)

type kv map[string]string

func (m kv) String(k string) string { return m[k] }
func (m kv) Bool(k string) bool     { return m[k] == "true" }
func (m kv) Secret(k string) string { return m[k] }

func deps(s kv) imchannel.Deps {
	return imchannel.Deps{
		Settings: s,
		HTTP: &http.Client{
			Timeout:   5 * time.Second,
			Transport: &http.Transport{DialContext: (&net.Dialer{}).DialContext},
			// 与网关的出站客户端(service.newOutboundClient)一样拒绝跟随重定向
			CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("重定向已被禁止(防 SSRF)") },
		},
		ValidateURL: func(string) error { return nil },
	}
}

const testTemplate = "tmpl-test.schema"

// newTestApp 造一个指向假开放平台、配好卡片模板的 *App。
func newTestApp(t *testing.T, srv *dttest.Server, extra kv) *App {
	t.Helper()
	s := kv{
		"im.dingtalk.clientID": srv.ClientID, "im.dingtalk.clientSecret": srv.ClientSecret,
		"im.dingtalk.cardTemplateId": testTemplate, "im.dingtalk.apiBase": srv.URL,
	}
	for k, v := range extra {
		s[k] = v
	}
	a, _ := New(deps(s)).(*App)
	if a == nil {
		t.Fatal("a configured app must build")
	}
	return a
}

func TestNew_NeedsBothCredentials(t *testing.T) {
	if ch := New(deps(kv{})); ch != nil {
		t.Fatalf("nothing configured → nil, got %#v", ch)
	}
	if ch := New(deps(kv{"im.dingtalk.clientID": "dingx"})); ch != nil {
		t.Fatal("a Client ID without its secret is not an app")
	}
	a, ok := New(deps(kv{"im.dingtalk.clientID": "dingx", "im.dingtalk.clientSecret": "s"})).(*App)
	if !ok || a.base != "https://api.dingtalk.com" || a.robotCode != "dingx" || a.template != "" || a.Kind() != imchannel.DingTalk {
		t.Fatalf("defaults: %#v", a)
	}
	a, _ = New(deps(kv{
		"im.dingtalk.clientID": "dingx", "im.dingtalk.clientSecret": "s",
		"im.dingtalk.robotCode": "dingrobot", "im.dingtalk.apiBase": "https://dt.corp.io/",
	})).(*App)
	if a.robotCode != "dingrobot" || a.base != "https://dt.corp.io" {
		t.Fatalf("overrides: robotCode=%s base=%s", a.robotCode, a.base)
	}
}

func TestSendCard_DeliversTheTemplateToTheUsersRobotChat(t *testing.T) {
	srv := dttest.New(t)
	a := newTestApp(t, srv, nil)
	ref, err := a.SendCard(context.Background(), "staff1", imchannel.ApprovalCard{
		ApNo: "AP-2294", Command: "DELETE FROM t WHERE a < b & c;", Risk: "high", Env: "prod", Instance: "pg-main", Initiator: "张伟",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ap, ok := trackApNo(ref); !ok || ap != "AP-2294" {
		t.Fatalf("ref = %q, want a gateway outTrackId for AP-2294", ref)
	}
	c := srv.Calls("deliver")[0]
	for k, want := range map[string]string{
		"cardTemplateId": testTemplate, "outTrackId": ref, "callbackType": "STREAM",
		"openSpaceId": "dtv1.card//IM_ROBOT.staff1", "userIdType": "1",
	} {
		if got := c.Str(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if c.At("imRobotOpenDeliverModel", "robotCode") != srv.ClientID || c.At("imRobotOpenDeliverModel", "spaceType") != "IM_ROBOT" ||
		c.At("imRobotOpenSpaceModel", "supportForward") != "false" {
		t.Fatalf("deliver models = %+v", c.Params)
	}
	if got := c.At("cardData", "cardParamMap", "command"); got != "DELETE FROM t WHERE a < b & c;" {
		t.Errorf("the command must arrive verbatim, got %q", got)
	}
	if c.At("cardData", "cardParamMap", "status") != statusPending {
		t.Errorf("a fresh card is pending: %+v", c.Params)
	}
	other, err := a.SendCard(context.Background(), "staff2", imchannel.ApprovalCard{ApNo: "AP-2294"})
	if err != nil || other == ref {
		t.Fatalf("each recipient's card needs its own outTrackId: %q vs %q (%v)", other, ref, err)
	}
}

func TestSendCard_RefusesWhatItCannotAddress(t *testing.T) {
	srv := dttest.New(t)
	a := newTestApp(t, srv, nil)
	if _, err := a.SendCard(context.Background(), "staff.1/x", imchannel.ApprovalCard{ApNo: "AP-1"}); err == nil {
		t.Error("a userid outside the whitelist must not reach openSpaceId")
	}
	if _, err := a.SendCard(context.Background(), "staff1", imchannel.ApprovalCard{ApNo: "AP 1"}); err == nil {
		t.Error("an approval number outside the whitelist cannot name a card")
	}
	noTmpl := newTestApp(t, srv, kv{"im.dingtalk.cardTemplateId": ""})
	if _, err := noTmpl.SendCard(context.Background(), "staff1", imchannel.ApprovalCard{ApNo: "AP-1"}); !errors.Is(err, errNoTemplate) {
		t.Errorf("no template → errNoTemplate, got %v", err)
	}
	if n := len(srv.Calls("deliver")); n != 0 {
		t.Fatalf("%d deliveries were attempted", n)
	}
}

func TestSendCard_AFailedDeliveryIsAnError(t *testing.T) {
	srv := dttest.New(t)
	a := newTestApp(t, srv, nil)
	srv.RejectDelivery("用户不在应用可见范围内")
	if _, err := a.SendCard(context.Background(), "staff1", imchannel.ApprovalCard{ApNo: "AP-1"}); err == nil || !strings.Contains(err.Error(), "可见范围") {
		t.Fatalf("an HTTP-200 delivery that failed must be an error, got %v", err)
	}
}

func TestCloseCard_UpdatesOnlyTheStatusVariables(t *testing.T) {
	srv := dttest.New(t)
	a := newTestApp(t, srv, nil)
	final := imchannel.CardState{Status: "approved", Text: "已由 李四(钉钉) 通过"}
	const ref = "aegis-AP-1-0123456789abcdef"
	if err := a.CloseCard(context.Background(), ref, final); err != nil {
		t.Fatal(err)
	}
	c := srv.Calls("update")[0]
	if c.Str("outTrackId") != ref || c.At("cardData", "cardParamMap", "status") != statusDone ||
		c.At("cardData", "cardParamMap", "result") != final.Text || c.At("cardUpdateOptions", "updateCardDataByKey") != "true" {
		t.Fatalf("update = %+v", c.Params)
	}
	if c.At("cardData", "cardParamMap", "command") != "" {
		t.Fatal("closing must not rewrite the rest of the card")
	}
	if err := a.CloseCard(context.Background(), "garbage", final); err == nil {
		t.Fatal("an unparsable ref is an error")
	}
}

func TestSendText_SendsARobotDirectMessage(t *testing.T) {
	srv := dttest.New(t)
	a := newTestApp(t, srv, nil)
	if err := a.sendText(context.Background(), "staff1", "已绑定到 张伟"); err != nil {
		t.Fatal(err)
	}
	c := srv.Calls("oto")[0]
	if c.Str("robotCode") != srv.ClientID || c.Str("userIds") != `["staff1"]` || c.Str("msgKey") != "sampleText" ||
		c.Str("msgParam") != `{"content":"已绑定到 张伟"}` {
		t.Fatalf("oto = %+v", c.Params)
	}
}

func TestCall_CachesTheTokenAndRefreshesARejectedOne(t *testing.T) {
	srv := dttest.New(t)
	a := newTestApp(t, srv, nil)
	for i := 0; i < 2; i++ {
		if _, err := a.SendCard(context.Background(), "staff1", imchannel.ApprovalCard{ApNo: "AP-1"}); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(srv.Calls("token")); n != 1 {
		t.Fatalf("token fetched %d times for two cards, want it cached", n)
	}
	srv.RotateToken() // 后台重置了 Secret、或 token 被提前作废
	if _, err := a.SendCard(context.Background(), "staff1", imchannel.ApprovalCard{ApNo: "AP-2"}); err != nil {
		t.Fatalf("a rejected cached token must be replaced and the call retried once: %v", err)
	}
	if n := len(srv.Calls("token")); n != 2 {
		t.Fatalf("token fetched %d times, want 2", n)
	}
	// 两张正常的,第三张先带缓存的旧 token 被拒(401),换新 token 再投一次。
	if n := len(srv.Calls("deliver")); n != 4 {
		t.Fatalf("deliver called %d times, want 4", n)
	}
}

func TestCall_ErrorsCarryDingTalksMessageButNotTheSecret(t *testing.T) {
	srv := dttest.New(t)
	a := newTestApp(t, srv, nil)
	srv.SetFail("deliver", dttest.Failure{Code: "param.templateNotExist", Msg: "卡片模板不存在"})
	_, err := a.SendCard(context.Background(), "staff1", imchannel.ApprovalCard{ApNo: "AP-1"})
	if err == nil || !strings.Contains(err.Error(), "param.templateNotExist") || !strings.Contains(err.Error(), "卡片模板不存在") {
		t.Fatalf("err = %v", err)
	}
	// 假开放平台会把收到的 secret 回显进 message:错误里只能剩 scrub 的占位符。
	bad := newTestApp(t, srv, kv{"im.dingtalk.clientSecret": "the-wrong-secret-value"})
	_, err = bad.token(context.Background())
	if err == nil || !strings.Contains(err.Error(), "invalidClientIdOrSecret") || !strings.Contains(err.Error(), "<client-secret>") ||
		strings.Contains(err.Error(), "the-wrong-secret-value") {
		t.Fatalf("credential error = %v", err)
	}
}

func TestPermanent(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{&apiError{HTTP: 400, Code: "invalidClientIdOrSecret"}, true},                   // 凭据被拒
		{&apiError{HTTP: 403, Code: "Forbidden.AccessDenied.AccessTokenPermissionDenied"}, true}, // 没开权限
		{&apiError{HTTP: 200, Msg: "响应无法解析,检查开放平台地址"}, true},                      // 地址指到了一个回 200 HTML 的页面
		{&apiError{HTTP: 302, Msg: "响应无法解析,检查开放平台地址"}, true},                      // 地址被重定向(出站不跟随)
		{fmt.Errorf("%w: 出站地址必须使用 https", errBadBase), true},
		{errNoTemplate, true},
		{&apiError{HTTP: 403, Code: "Forbidden.AccessDenied.QpsLimitForApi"}, false}, // 限流
		{&apiError{HTTP: 400, Code: "Throttling.Api"}, false},
		{&apiError{HTTP: 429}, false},
		{&apiError{HTTP: 503}, false},
		{&apiError{HTTP: 502, Msg: "响应无法解析"}, false},
		{errors.New("dial tcp: connection refused"), false},
	} {
		if got := permanent(tc.err); got != tc.want {
			t.Errorf("permanent(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

func TestRequest_AWrongOpenPlatformAddressIsPermanent(t *testing.T) {
	srv := dttest.New(t)
	a := newTestApp(t, srv, nil)
	srv.SetFail("token", dttest.Failure{Raw: "<html>console</html>"})
	if _, err := a.token(context.Background()); !permanent(err) {
		t.Fatalf("a 200 HTML page must be permanent, got %v", err)
	}
	srv.SetFail("token", dttest.Failure{HTTP: http.StatusFound, Location: "https://elsewhere.example/"})
	if _, err := a.token(context.Background()); !permanent(err) {
		t.Fatalf("a redirect must be permanent, got %v", err)
	}
	a.validate = func(string) error { return errors.New("目标解析到内网/环回地址，已拒绝(防 SSRF)") }
	if _, err := a.token(context.Background()); !permanent(err) {
		t.Fatalf("a refused open-platform address must be permanent, got %v", err)
	}
	a.validate = func(string) error {
		return fmt.Errorf("无法解析主机 %q: %w", "api.dingtalk.com", &net.DNSError{Err: "no such host", Name: "api.dingtalk.com"})
	}
	if _, err := a.token(context.Background()); err == nil || permanent(err) {
		t.Fatalf("a DNS lookup failure is transient, got %v", err)
	}
}
```

- [ ] **Step 3: 跑测试确认失败**

Run: `cd backend && go test ./internal/imchannel/dingtalk/`
Expected: 编译失败（`undefined: New`、`cardParams` 等）

- [ ] **Step 4: 写卡片变量与 outTrackId**

Create `backend/internal/imchannel/dingtalk/card.go`：

```go
package dingtalk

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"

	"velagateway/internal/imchannel"
	"velagateway/internal/model"
)

// 卡片模板变量(cardParamMap 的键)。模板由管理员在钉钉卡片平台搭,搭法见
// docs/external-approval-setup.md「钉钉交互审批」;这里的名字与手册一一对应,改一边就得改另一边。
const (
	paramTitle   = "title"
	paramSummary = "summary"
	paramReason  = "reason"
	paramCommand = "command"
	paramConsole = "consoleUrl"
	paramStatus  = "status" // pending:显示按钮;done:隐藏按钮、显示 result
	paramResult  = "result"
	paramTip     = "tip" // 点击后回给点击者的那句话(钉钉卡片回调没有 toast)

	statusPending = "pending"
	statusDone    = "done"
)

// commandLimit:钉钉卡片文本组件没有公开的长度上限。命令在通用层已截到 800,这里再兜一次底。
const commandLimit = 2000

// apNoRe / trackRe:点击回调里只凭 outTrackId 认出是哪张单,所以 outTrackId 只认网关自己造的形状,
// 单号只认白名单 —— 别让任意字符串流进后面的查询和提示语。
var (
	apNoRe  = regexp.MustCompile(`^[A-Za-z0-9-]{1,32}$`)
	trackRe = regexp.MustCompile(`^aegis-([A-Za-z0-9-]{1,32})-[0-9a-f]{16}$`)
)

// newTrackID 给一张卡片造 outTrackId。同一张单发给几个人就是几张卡片,所以带一段随机数。
func newTrackID(apNo string) (string, error) {
	if !apNoRe.MatchString(apNo) {
		return "", fmt.Errorf("无法用作卡片标识的单号 %q", apNo)
	}
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "aegis-" + apNo + "-" + hex.EncodeToString(b), nil
}

// trackApNo 从 outTrackId 里取回单号。
func trackApNo(track string) (string, bool) {
	m := trackRe.FindStringSubmatch(track)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// cardParams 是交给模板的变量。**全部是纯文本**:模板里用「文本」组件显示,不用 Markdown ——
// 发起人写的原因、SQL 里的 [ ]( ) * ` < & 都原样显示,没有可注入的链接或富文本,也就不必转义。
func cardParams(c imchannel.ApprovalCard) map[string]string {
	risk := "🟠 中危"
	if c.Risk == model.RiskHigh {
		risk = "🔴 高危"
	}
	env := strings.ToUpper(c.Env)
	if c.Tier != "" && !strings.EqualFold(c.Tier, c.Env) {
		env += "(" + c.Tier + ")"
	}
	db := c.Database
	if strings.TrimSpace(db) == "" {
		db = "默认"
	}
	return map[string]string{
		paramTitle:   risk + " · 待你审批 · " + c.ApNo,
		paramSummary: "环境 " + env + " · 实例 " + c.Instance + " / " + db + "\n发起人 " + c.Initiator,
		paramReason:  c.Reason,
		paramCommand: clipRunes(c.Command, commandLimit),
		paramConsole: c.ConsoleURL,
		paramStatus:  statusPending,
		paramResult:  "",
		paramTip:     "",
	}
}

// closedParams 是收卡时要改的变量。只改这三个(updateCardDataByKey):单号、命令、原因原样留在卡片上。
func closedParams(final imchannel.CardState) map[string]string {
	return map[string]string{paramStatus: statusDone, paramResult: final.Text, paramTip: ""}
}

func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
```

- [ ] **Step 5: 写应用与 OpenAPI 调用**

Create `backend/internal/imchannel/dingtalk/app.go`：

```go
// Package dingtalk 是钉钉渠道:企业内部应用的机器人在单聊里投放互动卡片、收起卡片,并经 Stream
// 长连接收回卡片点击与私聊(stream.go)。群通知(自定义机器人)在第 6 期。
package dingtalk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"velagateway/internal/imchannel"
)

const (
	defaultBase = "https://api.dingtalk.com"

	pathToken   = "/v1.0/oauth2/accessToken"
	pathOpen    = "/v1.0/gateway/connections/open"
	pathDeliver = "/v1.0/card/instances/createAndDeliver"
	pathCard    = "/v1.0/card/instances"
	pathOTO     = "/v1.0/robot/oToMessages/batchSend"

	tokenHeader = "x-acs-dingtalk-access-token"
)

// errBadBase:出站防护拒绝了开放平台地址(不是 https、解析到内网)。改配置之前怎么重试都一样。
var errBadBase = errors.New("开放平台地址不可用")

// errNoTemplate:钉钉的 Stream 卡片回调只支持互动卡片高级版,高级版只能用卡片平台上建好的模板。
var errNoTemplate = errors.New("未配置卡片模板 ID:按启用手册在钉钉卡片平台搭好模板,把模板 ID 填进设置")

// userIDRe 是钉钉 userid(staffId)。它会拼进以 . 分段的 openSpaceId,所以按白名单收。
var userIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// App 是钉钉开放平台的企业内部应用:投放卡片、收起卡片,并收回点击与私聊。
type App struct {
	clientID     string
	clientSecret string
	robotCode    string
	template     string
	base         string
	client       *http.Client
	validate     func(string) error

	tokMu  sync.Mutex
	tok    string
	tokExp time.Time
}

// New 在配了 Client ID 与 Client Secret 时造一个应用,否则返回 nil。不做任何 I/O —— 它在 Hub
// 的锁里被调用。卡片模板 ID 没填也照样造:Run 会把「缺模板」报到设置页上,比渠道静悄悄地显示
// 「未配置」更好排查。
func New(d imchannel.Deps) imchannel.Channel {
	key := func(s string) string { return imchannel.Key(imchannel.DingTalk, s) }
	id := strings.TrimSpace(d.Settings.String(key("clientID")))
	secret := strings.TrimSpace(d.Settings.Secret(key("clientSecret")))
	if id == "" || secret == "" {
		return nil
	}
	robot := strings.TrimSpace(d.Settings.String(key("robotCode")))
	if robot == "" {
		robot = id // 企业内部应用的机器人,robotCode 默认就是 Client ID(原 AppKey)
	}
	base := strings.TrimRight(strings.TrimSpace(d.Settings.String(key("apiBase"))), "/")
	if base == "" {
		base = defaultBase
	}
	return &App{
		clientID: id, clientSecret: secret, robotCode: robot, base: base,
		template: strings.TrimSpace(d.Settings.String(key("cardTemplateId"))),
		client:   imchannel.NoRedirects(d.HTTP), validate: d.ValidateURL,
	}
}

func (a *App) Kind() string { return imchannel.DingTalk }

// apiError 是钉钉明确回了错误:HTTP 非 2xx,或者回了个解析不了的响应。Code 是钉钉的字符串错误码。
type apiError struct {
	Path string
	HTTP int
	Code string
	Msg  string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("钉钉 %s: HTTP %d %s %s", e.Path, e.HTTP, e.Code, e.Msg)
}

// throttled 认出钉钉的限流码(如 Forbidden.AccessDenied.QpsLimitForApi):暂时的,退避重试。
func throttled(code string) bool {
	return strings.Contains(code, "QpsLimit") || strings.Contains(strings.ToLower(code), "throttl")
}

// permanent 判断重试无望的错误:钉钉明确拒绝(凭据错、没开权限 …)、开放平台地址填错(回了
// 3xx / 4xx、回了解析不了的 2xx、被出站防护拒绝)、没配卡片模板。限流、429、5xx、网络错误
// (含 DNS 解析失败)是暂时的。
func permanent(err error) bool {
	if errors.Is(err, errBadBase) || errors.Is(err, errNoTemplate) {
		return true
	}
	var ae *apiError
	if !errors.As(err, &ae) {
		return false
	}
	return ae.HTTP < 500 && ae.HTTP != http.StatusTooManyRequests && !throttled(ae.Code)
}

// request 发一次 OpenAPI 请求;HTTP 非 2xx、响应不是 JSON 都是 *apiError,出站校验拒绝是 errBadBase。
func (a *App) request(ctx context.Context, method, path, token string, body any) ([]byte, error) {
	if err := a.validate(a.base); err != nil {
		// DNS 解析失败可能只是一时的网络问题,照常重试;其余(不是 https、解析到内网)是配置错。
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %v", errBadBase, err)
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
		req.Header.Set(tokenHeader, token)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, a.scrub(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if !json.Valid(raw) {
		// 5xx 回 HTML 多半是钉钉那头临时出错;5xx 以下还不是 JSON(200 的页面、3xx 重定向),就是
		// 地址指错了地方。不把响应体带进错误:那里面什么都可能有。
		msg := "响应无法解析"
		if resp.StatusCode < 500 {
			msg = "响应无法解析,检查开放平台地址"
		}
		return nil, &apiError{Path: path, HTTP: resp.StatusCode, Msg: msg}
	}
	if resp.StatusCode >= 300 {
		var env struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(raw, &env)
		return nil, &apiError{Path: path, HTTP: resp.StatusCode, Code: env.Code, Msg: a.scrub(errors.New(env.Message)).Error()}
	}
	return raw, nil
}

// call 带着 access token 调一次;token 被拒(401)就换一个再试一次 —— 缓存的 token 被提前作废时
// (管理员在后台重置了 Secret),不至于之后每张卡片都失败到 token 自然过期。
func (a *App) call(ctx context.Context, method, path string, body any) ([]byte, error) {
	tok, err := a.token(ctx)
	if err != nil {
		return nil, err
	}
	raw, err := a.request(ctx, method, path, tok, body)
	var ae *apiError
	if errors.As(err, &ae) && ae.HTTP == http.StatusUnauthorized {
		a.dropToken(tok)
		if tok, err = a.token(ctx); err != nil {
			return nil, err
		}
		raw, err = a.request(ctx, method, path, tok, body)
	}
	return raw, err
}

// token 返回缓存的 access token,快过期(剩 5 分钟)就换新的。
func (a *App) token(ctx context.Context) (string, error) {
	a.tokMu.Lock()
	defer a.tokMu.Unlock()
	if a.tok != "" && time.Now().Before(a.tokExp) {
		return a.tok, nil
	}
	raw, err := a.request(ctx, http.MethodPost, pathToken, "", map[string]string{"appKey": a.clientID, "appSecret": a.clientSecret})
	if err != nil {
		return "", err
	}
	var r struct {
		AccessToken string `json:"accessToken"`
		ExpireIn    int    `json:"expireIn"`
	}
	if json.Unmarshal(raw, &r) != nil || r.AccessToken == "" {
		return "", errors.New("钉钉 accessToken 响应里没有 token")
	}
	a.tok = r.AccessToken
	a.tokExp = time.Now().Add(time.Duration(max(r.ExpireIn-300, 60)) * time.Second)
	return a.tok, nil
}

func (a *App) dropToken(old string) {
	a.tokMu.Lock()
	defer a.tokMu.Unlock()
	if a.tok == old {
		a.tok = ""
	}
}

// scrub 把 Client Secret 从错误文本里换掉。它只在请求体里,正常不会出现在错误里 —— 这是防一个
// 往错误里回显请求的代理或私有化部署。
func (a *App) scrub(err error) error {
	if err == nil || a.clientSecret == "" {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), a.clientSecret, "<client-secret>"))
}

// SendCard 在机器人与 userID 的单聊里投放一张审批卡片。ref 是 outTrackId:收卡、认点击都靠它。
func (a *App) SendCard(ctx context.Context, userID string, c imchannel.ApprovalCard) (string, error) {
	if a.template == "" {
		return "", errNoTemplate
	}
	if !userIDRe.MatchString(userID) {
		return "", fmt.Errorf("无法识别的钉钉 userid %q", userID)
	}
	track, err := newTrackID(c.ApNo)
	if err != nil {
		return "", err
	}
	raw, err := a.call(ctx, http.MethodPost, pathDeliver, map[string]any{
		"cardTemplateId": a.template,
		"outTrackId":     track,
		"callbackType":   "STREAM",
		"cardData":       map[string]any{"cardParamMap": cardParams(c)},
		"openSpaceId":    "dtv1.card//IM_ROBOT." + userID,
		// 不许转发:按钮谁点都只认点击者本人,转出去也批不了,只会让人困惑。
		"imRobotOpenSpaceModel":   map[string]any{"supportForward": false},
		"imRobotOpenDeliverModel": map[string]any{"spaceType": "IM_ROBOT", "robotCode": a.robotCode},
		"userIdType":              1,
	})
	if err != nil {
		return "", err
	}
	var r struct {
		Result struct {
			DeliverResults []struct {
				Success  bool   `json:"success"`
				ErrorMsg string `json:"errorMsg"`
			} `json:"deliverResults"`
		} `json:"result"`
	}
	_ = json.Unmarshal(raw, &r)
	// 接口成功不等于投放成功:对方不在应用可见范围内时钉钉照样回 200,只在 deliverResults 里说
	// 失败。当成功的话,库里会记下一张根本不存在的卡片。
	if len(r.Result.DeliverResults) == 0 {
		return "", errors.New("钉钉投放卡片:响应里没有投放结果")
	}
	for _, d := range r.Result.DeliverResults {
		if !d.Success {
			return "", fmt.Errorf("钉钉投放卡片失败: %s", a.scrub(errors.New(d.ErrorMsg)))
		}
	}
	return track, nil
}

// CloseCard 把卡片改成终态:只改 status / result / tip,卡片其余内容原样保留。
func (a *App) CloseCard(ctx context.Context, ref string, final imchannel.CardState) error {
	if _, ok := trackApNo(ref); !ok {
		return fmt.Errorf("无法识别的卡片引用 %q", ref)
	}
	_, err := a.call(ctx, http.MethodPut, pathCard, map[string]any{
		"outTrackId":        ref,
		"cardData":          map[string]any{"cardParamMap": closedParams(final)},
		"cardUpdateOptions": map[string]any{"updateCardDataByKey": true},
	})
	return err
}

// sendText 以机器人身份给 userID 发一条单聊文本(绑定结果的回复)。
func (a *App) sendText(ctx context.Context, userID, text string) error {
	param, err := json.Marshal(map[string]string{"content": text})
	if err != nil {
		return err
	}
	raw, err := a.call(ctx, http.MethodPost, pathOTO, map[string]any{
		"robotCode": a.robotCode, "userIds": []string{userID}, "msgKey": "sampleText", "msgParam": string(param),
	})
	if err != nil {
		return err
	}
	var r struct {
		Invalid []string `json:"invalidStaffIdList"`
		Limited []string `json:"flowControlledStaffIdList"`
	}
	_ = json.Unmarshal(raw, &r)
	if len(r.Invalid) > 0 || len(r.Limited) > 0 {
		return fmt.Errorf("钉钉单聊消息没有送达 %s(无效 %v,限流 %v)", userID, r.Invalid, r.Limited)
	}
	return nil
}
```

- [ ] **Step 6: 跑测试确认通过**

Run: `cd backend && gofmt -l internal/imchannel/dingtalk && go vet ./internal/imchannel/dingtalk/... && go test -race -count=1 ./internal/imchannel/dingtalk/...`
Expected: gofmt 无输出；PASS

- [ ] **Step 7: Commit**

```bash
cd backend
git add internal/imchannel/dingtalk/
git commit -m "feat(im): add the DingTalk app's outbound side and a fake open platform"
```

---

### Task 3: 钉钉 Stream 长连接：Run、帧分发、点击与私聊

**Files:**
- Modify: `backend/go.mod`、`backend/go.sum`（`go get github.com/open-dingtalk/dingtalk-stream-sdk-go@v0.9.1`）
- Create: `backend/internal/imchannel/dingtalk/stream.go`
- Modify: `backend/internal/imchannel/dingtalk/app.go`（`App` 加字段 `seen imchannel.Recent`）
- Create: `backend/internal/imchannel/dingtalk/dttest/stream.go`
- Modify: `backend/internal/imchannel/dingtalk/dttest/server.go`（Stream 用的字段、`New` 初始化、`/connect` 路由）
- Test: `backend/internal/imchannel/dingtalk/stream_test.go`

**Interfaces:**
- Consumes: Task 1 的 `imchannel.Retry` / `Sleep` / `GuardedDial` / `Recent`；Task 2 的 `App`、`request`、`token`、`sendText`、`scrub`、`permanent`、`errNoTemplate`、`pathOpen`、`trackApNo`、`paramTip`。
- Produces:
  ```go
  func (a *App) Run(ctx context.Context, sink imchannel.ActionSink) error // *App 至此满足 imchannel.Approver
  ```
  dttest：
  ```go
  func (s *Server) WaitConnected(t testing.TB)
  func (s *Server) WaitConnects(t testing.TB, n int)
  func (s *Server) ConnectCount() int
  func (s *Server) Push(t testing.TB, typ, topic, data string) payload.DataFrameResponse
  func (s *Server) Click(t testing.TB, userID, outTrackID, act string) string // 返回卡片上的 tip
  func (s *Server) SendDM(t testing.TB, staffID, conversationType, text string)
  func (s *Server) Disconnect(t testing.TB)
  func (s *Server) DropConnection()
  func CardCallback(userID, outTrackID, act string, userIDType int) string
  func Message(staffID, conversationType, msgType, text, msgID string) string
  func Tip(r payload.DataFrameResponse) string
  ```

- [ ] **Step 1: 加依赖**

Run: `cd backend && go get github.com/open-dingtalk/dingtalk-stream-sdk-go@v0.9.1`
Expected: `go.mod` 多一行 `github.com/open-dingtalk/dingtalk-stream-sdk-go v0.9.1`（先以 `// indirect` 出现，Step 6 之后 `go mod tidy` 会把它改成直接依赖）。**只 import 它的 `payload`、`chatbot`、`card` 三个包**；`client`、`logger` 不许出现在任何 import 里（原因见计划开头「与 spec 的偏离」第 1 条）。

- [ ] **Step 2: 假 Stream 网关**

`backend/internal/imchannel/dingtalk/dttest/server.go`：
- import 加 `"github.com/gorilla/websocket"` 与 `"github.com/open-dingtalk/dingtalk-stream-sdk-go/payload"`。
- `Server` 结构体在 `nextTicket int` 之后加：

```go
	// Stream(stream.go)
	conn      *websocket.Conn
	writeMu   sync.Mutex
	connects  int
	waiting   map[string]chan payload.DataFrameResponse
	nextFrame int
	nextMsg   int
```

- `New` 里的结构体字面量加 `waiting: map[string]chan payload.DataFrameResponse{},`。
- `handle` 的第一行之前加：

```go
	if r.URL.Path == "/connect" {
		s.upgrade(w, r)
		return
	}
```

Create `backend/internal/imchannel/dingtalk/dttest/stream.go`：

```go
package dttest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/open-dingtalk/dingtalk-stream-sdk-go/payload"
)

// upgrade 扮演 Stream 网关的 WebSocket 入口:只认 open 发出去、还没用过的 ticket。
func (s *Server) upgrade(w http.ResponseWriter, r *http.Request) {
	ticket := r.URL.Query().Get("ticket")
	s.mu.Lock()
	ok := s.tickets[ticket]
	delete(s.tickets, ticket)
	s.mu.Unlock()
	if !ok {
		http.Error(w, "invalid ticket", http.StatusUnauthorized)
		return
	}
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

// readLoop 收适配器发上来的回执,交给等它的 Push。帧的编解码直接用 SDK 的 payload 类型 —— 测到
// 的是钉钉 Stream 的线上格式。WebSocket 的 ping 由 gorilla 默认处理器回 pong。
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
		var resp payload.DataFrameResponse
		if json.Unmarshal(msg, &resp) != nil {
			continue
		}
		s.mu.Lock()
		ch := s.waiting[resp.GetHeader(payload.DataFrameHeaderKMessageId)]
		s.mu.Unlock()
		if ch != nil {
			ch <- resp
		}
	}
}

func (s *Server) write(conn *websocket.Conn, b []byte) error {
	if conn == nil {
		return fmt.Errorf("dttest: no stream connection")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return conn.WriteMessage(websocket.TextMessage, b)
}

func frame(typ, topic, id, data string) []byte {
	now := time.Now().UnixMilli()
	df := payload.DataFrame{SpecVersion: "1.0", Type: typ, Time: now, Data: data, Headers: payload.DataFrameHeader{
		payload.DataFrameHeaderKTopic:       topic,
		payload.DataFrameHeaderKMessageId:   id,
		payload.DataFrameHeaderKContentType: payload.DataFrameContentTypeKJson,
		payload.DataFrameHeaderKTime:        strconv.FormatInt(now, 10),
	}}
	return df.Encode()
}

// WaitConnected 等适配器连上来。
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
	t.Fatal("dttest: the stream connection never came up")
}

// WaitConnects 等到总共建立过至少 n 条连接,且当前有一条连着。
func (s *Server) WaitConnects(t testing.TB, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		ok := s.connects >= n && s.conn != nil
		s.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("dttest: %d connections within 5s, want %d", s.ConnectCount(), n)
}

// ConnectCount 是到目前为止建立过的连接数。
func (s *Server) ConnectCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connects
}

// Push 经 Stream 推一帧,等适配器回执并返回它。
func (s *Server) Push(t testing.TB, typ, topic, data string) payload.DataFrameResponse {
	t.Helper()
	s.WaitConnected(t)
	s.mu.Lock()
	conn := s.conn
	s.nextFrame++
	id := fmt.Sprintf("frame-%d", s.nextFrame)
	ch := make(chan payload.DataFrameResponse, 1)
	s.waiting[id] = ch
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.waiting, id)
		s.mu.Unlock()
	}()
	if err := s.write(conn, frame(typ, topic, id, data)); err != nil {
		t.Fatalf("dttest: push: %v", err)
	}
	select {
	case r := <-ch:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("dttest: no reply to the pushed frame within 5s")
		return payload.DataFrameResponse{}
	}
}

// CardCallback 造一次卡片按钮点击的载荷(topic /v1.0/card/instances/callback)。按钮的回传参数在
// content 这段 JSON 字符串的 cardPrivateData.params 里。
func CardCallback(userID, outTrackID, act string, userIDType int) string {
	content, _ := json.Marshal(map[string]any{"cardPrivateData": map[string]any{
		"actionIds": []string{"decide"}, "params": map[string]any{"act": act},
	}})
	b, _ := json.Marshal(map[string]any{
		"outTrackId": outTrackID, "userId": userID, "userIdType": userIDType, "type": "actionCallback",
		"corpId": "dingcorp", "spaceType": "IM_ROBOT", "spaceId": userID, "extension": "{}", "content": string(content),
	})
	return string(b)
}

// Tip 取卡片回调应答里写回卡片的 tip 变量。
func Tip(r payload.DataFrameResponse) string {
	var body struct {
		Response struct {
			CardData struct {
				CardParamMap map[string]string `json:"cardParamMap"`
			} `json:"cardData"`
		} `json:"response"`
	}
	_ = json.Unmarshal([]byte(r.Data), &body)
	return body.Response.CardData.CardParamMap["tip"]
}

// Click 推一次点击,返回卡片上更新出来的提示语。
func (s *Server) Click(t testing.TB, userID, outTrackID, act string) string {
	t.Helper()
	return Tip(s.Push(t, "CALLBACK", payload.CardInstanceCallbackTopic, CardCallback(userID, outTrackID, act, 1)))
}

// Message 造一条机器人收到的消息(topic /v1.0/im/bot/messages/get)。conversationType "1" 是单聊,
// "2" 是群聊。
func Message(staffID, conversationType, msgType, text, msgID string) string {
	now := time.Now().UnixMilli()
	b, _ := json.Marshal(map[string]any{
		"conversationId": "cid-" + staffID, "chatbotCorpId": "dingcorp", "chatbotUserId": "$:LWCP_v1:$bot",
		"msgId": msgID, "senderNick": "测试用户", "isAdmin": false, "senderStaffId": staffID,
		"sessionWebhookExpiredTime": now + 3_600_000, "createAt": now, "senderCorpId": "dingcorp",
		"conversationType": conversationType, "senderId": "$:LWCP_v1:$" + staffID,
		"sessionWebhook": "https://oapi.dingtalk.com/robot/sendBySession?session=test",
		"text":           map[string]any{"content": text}, "msgtype": msgType,
	})
	return string(b)
}

// SendDM 推一条文本消息并等回执。
func (s *Server) SendDM(t testing.TB, staffID, conversationType, text string) {
	t.Helper()
	s.mu.Lock()
	s.nextMsg++
	id := fmt.Sprintf("msg-%d", s.nextMsg)
	s.mu.Unlock()
	if r := s.Push(t, "CALLBACK", payload.BotMessageCallbackTopic, Message(staffID, conversationType, "text", text, id)); r.Code != http.StatusOK {
		t.Fatalf("dttest: message not acknowledged: %+v", r)
	}
}

// Disconnect 发一帧 SYSTEM disconnect(钉钉发布、迁移时这样要求客户端换连接),不等回执。
func (s *Server) Disconnect(t testing.TB) {
	t.Helper()
	s.WaitConnected(t)
	s.mu.Lock()
	conn := s.conn
	s.conn = nil // 之后的 Push 等新连接
	s.mu.Unlock()
	if err := s.write(conn, frame("SYSTEM", "disconnect", "disconnect-1", `{"reason":"server restart"}`)); err != nil {
		t.Fatalf("dttest: disconnect: %v", err)
	}
}

// DropConnection 直接断开当前连接(网络抖动)。
func (s *Server) DropConnection() {
	s.mu.Lock()
	conn := s.conn
	s.conn = nil
	s.mu.Unlock()
	if conn != nil {
		conn.Close()
	}
}
```

- [ ] **Step 3: 写失败的 Run 测试**

Create `backend/internal/imchannel/dingtalk/stream_test.go`：

```go
package dingtalk

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/open-dingtalk/dingtalk-stream-sdk-go/payload"

	"velagateway/internal/imchannel"
	"velagateway/internal/imchannel/dingtalk/dttest"
)

// recSink 记下交上来的点击与私聊,回一个能看出收到了什么的提示 / 回复。
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

func (s *recSink) dmList() []imchannel.DM {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]imchannel.DM(nil), s.dms...)
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

// runUntilError 跑 Run,期望它自己很快返回一个错误(永久性错误 / 配置错误)。
func runUntilError(t *testing.T, a *App) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- a.Run(context.Background(), &recSink{}) }()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("Run kept going; a permanent failure must stop it with a reason")
		return nil
	}
}

const testTrack = "aegis-AP-2294-0123456789abcdef"

func TestRun_ConnectsWithItsSubscriptions(t *testing.T) {
	srv := dttest.New(t)
	a := newTestApp(t, srv, nil)
	var _ imchannel.Approver = a
	startRun(t, a, &recSink{})
	srv.WaitConnected(t)
	open := srv.Calls("open")[0]
	if open.Str("clientId") != srv.ClientID || open.Str("clientSecret") != srv.ClientSecret {
		t.Fatalf("open = %+v", open.Params)
	}
	subs := open.Str("subscriptions")
	for _, want := range []string{
		`"topic":"ping"`, `"topic":"disconnect"`,
		`"topic":"/v1.0/im/bot/messages/get"`, `"topic":"/v1.0/card/instances/callback"`,
	} {
		if !strings.Contains(subs, want) {
			t.Errorf("subscriptions %s missing %s", subs, want)
		}
	}
	if open.Str("localIp") != "" {
		t.Errorf("the gateway's LAN address must not be reported: %q", open.Str("localIp"))
	}
}

func TestRun_AClickReachesTheSinkAndItsTipComesBack(t *testing.T) {
	srv := dttest.New(t)
	a := newTestApp(t, srv, nil)
	startRun(t, a, &recSink{})
	if tip := srv.Click(t, "staff1", testTrack, "a"); tip != "ok staff1 AP-2294 true" {
		t.Fatalf("approve tip = %q", tip)
	}
	if tip := srv.Click(t, "staff1", testTrack, "r"); tip != "ok staff1 AP-2294 false" {
		t.Fatalf("reject tip = %q", tip)
	}
}

func TestRun_ClicksThatDoNotNameACardOrAnActionAreDropped(t *testing.T) {
	srv := dttest.New(t)
	a := newTestApp(t, srv, nil)
	sink := &recSink{}
	startRun(t, a, sink)
	for _, c := range []struct {
		user, track, act string
		idType           int
	}{
		{"staff1", "AP-2294", "a", 1},                       // 不是网关造的 outTrackId
		{"staff1", "aegis-AP 1-0123456789abcdef", "a", 1},   // 单号里有空格
		{"staff1", testTrack, "x", 1},                           // 不认识的动作
		{"", testTrack, "a", 1},                                 // 没有点击者
		{"union1", testTrack, "a", 2},                           // unionId:绑定表里存的是 userid
	} {
		r := srv.Push(t, "CALLBACK", payload.CardInstanceCallbackTopic, dttest.CardCallback(c.user, c.track, c.act, c.idType))
		if tip := dttest.Tip(r); r.Code != http.StatusOK || tip != "无法识别的操作" {
			t.Errorf("%+v → code %d tip %q", c, r.Code, tip)
		}
	}
	if n, _ := sink.counts(); n != 0 {
		t.Fatalf("%d malformed clicks reached the sink", n)
	}
}

func TestRun_ADirectMessageIsPassedOnAndAnswered(t *testing.T) {
	srv := dttest.New(t)
	a := newTestApp(t, srv, nil)
	sink := &recSink{}
	startRun(t, a, sink)
	srv.SendDM(t, "staff1", "1", "  BIND-7K3QX9MP ")
	if dms := sink.dmList(); len(dms) != 1 || dms[0].PlatformUserID != "staff1" || dms[0].Text != "BIND-7K3QX9MP" {
		t.Fatalf("dms = %+v", dms)
	}
	// 回复走机器人单聊接口(带受控出站客户端),不用事件里带来的 sessionWebhook 地址。
	oto := srv.WaitCall(t, "oto", nil)
	if oto.Str("userIds") != `["staff1"]` || !strings.Contains(oto.Str("msgParam"), "reply to BIND-7K3QX9MP") {
		t.Fatalf("reply = %+v", oto.Params)
	}
}

func TestRun_GroupMessagesAndOtherSendersAreIgnored(t *testing.T) {
	srv := dttest.New(t)
	a := newTestApp(t, srv, nil)
	sink := &recSink{}
	startRun(t, a, sink)
	srv.SendDM(t, "staff1", "2", "BIND-7K3QX9MP") // 群聊里的码:就是泄露了,更不能认
	srv.SendDM(t, "", "1", "BIND-7K3QX9MP")       // 没有 senderStaffId:组织外的人
	srv.Push(t, "CALLBACK", payload.BotMessageCallbackTopic, dttest.Message("staff1", "1", "picture", "", "msg-pic"))
	if _, n := sink.counts(); n != 0 {
		t.Fatalf("%d messages reached the sink", n)
	}
	if n := len(srv.Calls("oto")); n != 0 {
		t.Fatalf("%d replies were sent", n)
	}
}

func TestRun_ARedeliveredMessageIsHandledOnce(t *testing.T) {
	srv := dttest.New(t)
	a := newTestApp(t, srv, nil)
	sink := &recSink{}
	startRun(t, a, sink)
	m := dttest.Message("staff1", "1", "text", "BIND-7K3QX9MP", "msg-dup")
	for i := 0; i < 2; i++ {
		if r := srv.Push(t, "CALLBACK", payload.BotMessageCallbackTopic, m); r.Code != http.StatusOK {
			t.Fatalf("redelivery must still be acknowledged: %+v", r)
		}
	}
	if _, n := sink.counts(); n != 1 {
		t.Fatalf("the same msgId was handled %d times", n)
	}
}

func TestRun_AnswersThePlatformsPingWithItsData(t *testing.T) {
	srv := dttest.New(t)
	a := newTestApp(t, srv, nil)
	startRun(t, a, &recSink{})
	r := srv.Push(t, "SYSTEM", "ping", `{"opaque":"p-1"}`)
	if r.Code != http.StatusOK || r.Data != `{"opaque":"p-1"}` {
		t.Fatalf("pong = %+v", r)
	}
}

func TestRun_AnUnsubscribedFrameIsAnswered404(t *testing.T) {
	srv := dttest.New(t)
	a := newTestApp(t, srv, nil)
	startRun(t, a, &recSink{})
	if r := srv.Push(t, "EVENT", "chat_update_title", `{}`); r.Code != http.StatusNotFound {
		t.Fatalf("event frame → %+v", r)
	}
	if r := srv.Push(t, "CALLBACK", "/v1.0/graph/api/invoke", `{}`); r.Code != http.StatusNotFound {
		t.Fatalf("unknown callback topic → %+v", r)
	}
}

func TestRun_ReconnectsWhenAskedAndWhenTheConnectionDrops(t *testing.T) {
	srv := dttest.New(t)
	a := newTestApp(t, srv, nil)
	startRun(t, a, &recSink{})
	srv.WaitConnected(t)
	srv.Disconnect(t)
	srv.WaitConnects(t, 2)
	srv.DropConnection()
	srv.WaitConnects(t, 3)
	if n := len(srv.Calls("open")); n != 3 {
		t.Fatalf("each connection needs a fresh ticket; open called %d times", n)
	}
	if tip := srv.Click(t, "staff1", testTrack, "a"); tip != "ok staff1 AP-2294 true" {
		t.Fatalf("clicks must work after reconnecting, tip = %q", tip)
	}
}

func TestRun_StopsWhenCancelledAndStaysStopped(t *testing.T) {
	srv := dttest.New(t)
	a := newTestApp(t, srv, nil)
	stop := startRun(t, a, &recSink{})
	srv.WaitConnected(t)
	if err := stop(); err != nil {
		t.Fatalf("a cancelled Run returns nil, got %v", err)
	}
	n := srv.ConnectCount()
	time.Sleep(1500 * time.Millisecond)
	if srv.ConnectCount() != n || len(srv.Calls("open")) != n {
		t.Fatal("a cancelled Run must not reconnect: after Reload the old connection would keep taking events")
	}
}

func TestRun_RejectedCredentialsStopTheLoopWithoutTheSecret(t *testing.T) {
	srv := dttest.New(t)
	a := newTestApp(t, srv, kv{"im.dingtalk.clientSecret": "the-wrong-client-secret"})
	err := runUntilError(t, a)
	if err == nil || !strings.Contains(err.Error(), "invalidClientIdOrSecret") || !strings.Contains(err.Error(), "<client-secret>") ||
		strings.Contains(err.Error(), "the-wrong-client-secret") {
		t.Fatalf("err = %v", err)
	}
	if srv.ConnectCount() != 0 {
		t.Fatal("no connection with rejected credentials")
	}
}

func TestRun_AMissingStreamPermissionStopsTheLoop(t *testing.T) {
	srv := dttest.New(t)
	a := newTestApp(t, srv, nil)
	srv.SetFail("open", dttest.Failure{HTTP: http.StatusForbidden, Code: "Forbidden.AccessDenied", Msg: "应用未开通 Stream 模式"})
	if err := runUntilError(t, a); err == nil || !strings.Contains(err.Error(), "Stream 模式") {
		t.Fatalf("err = %v", err)
	}
}

func TestRun_AWrongOpenPlatformAddressStopsTheLoop(t *testing.T) {
	srv := dttest.New(t)
	a := newTestApp(t, srv, nil)
	srv.SetFail("token", dttest.Failure{Raw: "<html>console</html>"})
	if err := runUntilError(t, a); err == nil || !strings.Contains(err.Error(), "检查开放平台地址") {
		t.Fatalf("err = %v", err)
	}
}

func TestRun_RefusesBeforeAnyRequestWhenTheCardTemplateIsMissing(t *testing.T) {
	srv := dttest.New(t)
	a := newTestApp(t, srv, kv{"im.dingtalk.cardTemplateId": ""})
	if err := runUntilError(t, a); !errors.Is(err, errNoTemplate) {
		t.Fatalf("err = %v, want errNoTemplate", err)
	}
	if n := len(srv.Calls("token")) + len(srv.Calls("open")); n != 0 {
		t.Fatalf("%d requests went out before the configuration error", n)
	}
}

func TestRun_RefusesAClientWithoutAGuardedDialer(t *testing.T) {
	srv := dttest.New(t)
	d := deps(kv{
		"im.dingtalk.clientID": srv.ClientID, "im.dingtalk.clientSecret": srv.ClientSecret,
		"im.dingtalk.cardTemplateId": testTemplate, "im.dingtalk.apiBase": srv.URL,
	})
	d.HTTP = &http.Client{Timeout: 5 * time.Second} // 没有 *http.Transport,也就没有受控拨号器
	a := New(d).(*App)
	if err := runUntilError(t, a); err == nil || !strings.Contains(err.Error(), "受控的拨号器") {
		t.Fatalf("err = %v", err)
	}
	if n := len(srv.Calls("token")); n != 0 {
		t.Fatalf("%d requests went out before the configuration error", n)
	}
}
```

- [ ] **Step 4: 跑测试确认失败**

Run: `cd backend && go test ./internal/imchannel/dingtalk/ -run TestRun_`
Expected: 编译失败（`a.Run undefined`）

- [ ] **Step 5: 写 Stream 客户端与入站处理**

`backend/internal/imchannel/dingtalk/app.go` 的 `App` 结构体，在 `validate func(string) error` 之后加一行：

```go
	seen         imchannel.Recent // 已处理的私聊 msgId
```

Create `backend/internal/imchannel/dingtalk/stream.go`：

```go
package dingtalk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/open-dingtalk/dingtalk-stream-sdk-go/card"
	"github.com/open-dingtalk/dingtalk-stream-sdk-go/chatbot"
	"github.com/open-dingtalk/dingtalk-stream-sdk-go/payload"

	"velagateway/internal/imchannel"
)

// Stream 协议自己实现,只借官方 SDK 的数据类型:SDK 的 client 写死默认拨号器(绕开 SSRF 拦截)、
// 在 Debug 级把含 clientSecret 的请求体打进全局 logger、断线重连跑在 ctx 管不到的循环里(Reload
// 之后旧连接不退出,继续分走事件)。见 spec §21。
const (
	typeSystem      = "SYSTEM"
	typeCallback    = "CALLBACK"
	topicPing       = "ping"
	topicDisconnect = "disconnect"

	userAgent   = "aegisdb-gateway (dingtalk-stream)"
	frameLimit  = 1 << 20          // 单帧上限:一条超长私聊不该撑爆内存
	pingEvery   = 30 * time.Second // WebSocket ping 间隔
	readTimeout = 90 * time.Second // 这么久什么都没收到(帧或 pong)就当连接半开,重连
	stableAfter = time.Minute      // 连上超过这么久再断,退避从头算
)

// errReconnect:钉钉发来 SYSTEM disconnect,要求换一条连接(服务端发布、负载迁移)。不是故障。
var errReconnect = errors.New("钉钉要求重连")

// Run 阻塞到 ctx 取消。纯配置错(没填模板 ID、拿不到受控拨号器)先查,不等网络;然后换一次
// access token —— 凭据错在这里就停下、交给设置页;最后维持 Stream 长连接。
func (a *App) Run(ctx context.Context, sink imchannel.ActionSink) error {
	if a.template == "" {
		return errNoTemplate
	}
	if _, err := imchannel.GuardedDial(a.client); err != nil {
		return err
	}
	err := imchannel.Retry(ctx, "dingtalk", permanent, func() error {
		_, err := a.token(ctx)
		return err
	})
	if err != nil || ctx.Err() != nil {
		return err
	}
	slog.Info("dingtalk: stream connection starting — safe with several gateway instances; DingTalk delivers each event to one of them")
	return a.stream(ctx, sink)
}

// stream 一条连接断了就换下一条,直到 ctx 取消或遇到永久性错误。
func (a *App) stream(ctx context.Context, sink imchannel.ActionSink) error {
	backoff := time.Second
	for {
		started := time.Now()
		err := a.session(ctx, sink)
		if ctx.Err() != nil {
			return nil
		}
		if permanent(err) {
			return err
		}
		if errors.Is(err, errReconnect) || time.Since(started) > stableAfter {
			backoff = time.Second
		}
		if errors.Is(err, errReconnect) {
			slog.Info("dingtalk: server asked to reconnect")
		} else {
			slog.Warn("dingtalk: stream connection lost, reconnecting", "err", err, "retryIn", backoff)
		}
		if !imchannel.Sleep(ctx, backoff) {
			return nil
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

// open 换一个长连接地址(带一次性 ticket)。
func (a *App) open(ctx context.Context) (string, error) {
	raw, err := a.request(ctx, http.MethodPost, pathOpen, "", payload.ConnectionEndpointRequest{
		ClientId: a.clientID, ClientSecret: a.clientSecret, UserAgent: userAgent,
		// 不报 localIp:钉钉不需要它,网关的内网拓扑也不该交出去。
		Subscriptions: []*payload.SubscriptionModel{
			{Type: typeSystem, Topic: topicPing},
			{Type: typeSystem, Topic: topicDisconnect},
			{Type: typeCallback, Topic: payload.BotMessageCallbackTopic},
			{Type: typeCallback, Topic: payload.CardInstanceCallbackTopic},
		},
	})
	if err != nil {
		return "", err
	}
	var ep payload.ConnectionEndpointResponse
	if json.Unmarshal(raw, &ep) != nil || ep.Valid() != nil {
		return "", errors.New("钉钉没有返回长连接地址")
	}
	u, err := url.Parse(ep.Endpoint)
	if err != nil || (u.Scheme != "wss" && u.Scheme != "ws") || u.Host == "" {
		return "", fmt.Errorf("钉钉返回的长连接地址无法使用: %q", ep.Endpoint)
	}
	// 长连接地址是钉钉下发的,不是管理员填的;仍过一遍出站校验(wss 按 https、ws 按 http 查),
	// 拨号本身也走受控拨号器 —— 两道都拦内网。
	check := url.URL{Scheme: "https", Host: u.Host}
	if u.Scheme == "ws" {
		check.Scheme = "http"
	}
	if err := a.validate(check.String()); err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("ticket", ep.Ticket)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// session 是一条连接的一生:拨号、收帧、分发,直到断开、钉钉要求重连或 ctx 取消。
func (a *App) session(ctx context.Context, sink imchannel.ActionSink) error {
	dial, err := imchannel.GuardedDial(a.client)
	if err != nil {
		return err
	}
	endpoint, err := a.open(ctx)
	if err != nil {
		return err
	}
	d := websocket.Dialer{NetDialContext: dial, HandshakeTimeout: 10 * time.Second}
	conn, _, err := d.DialContext(ctx, endpoint, nil)
	if err != nil {
		return a.scrub(err)
	}
	defer conn.Close()
	conn.SetReadLimit(frameLimit)
	// ctx 取消(Reload)时立刻关连接,ReadMessage 随之返回 —— 旧连接不许继续收事件。
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	alive := func() { _ = conn.SetReadDeadline(time.Now().Add(readTimeout)) }
	alive()
	conn.SetPongHandler(func(string) error { alive(); return nil })
	done := make(chan struct{})
	defer close(done)
	go keepAlive(conn, done)

	w := &frameWriter{conn: conn}
	var handlers sync.WaitGroup
	defer handlers.Wait() // 先等处理中的回调写完回执,再关连接
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		alive()
		var df payload.DataFrame
		if json.Unmarshal(msg, &df) != nil {
			continue
		}
		switch {
		case df.Type == typeSystem && df.GetTopic() == topicPing:
			pong := payload.NewDataFrameAckPong(df.GetMessageId())
			pong.Data = df.Data
			w.send(pong)
		case df.Type == typeSystem && df.GetTopic() == topicDisconnect:
			return errReconnect
		case df.Type == typeCallback:
			// 处理要查库、可能还要回一条私聊:放到协程里,别让一条慢回调挡住后面的帧和 ping。
			handlers.Add(1)
			go func(df payload.DataFrame) {
				defer handlers.Done()
				defer func() {
					if p := recover(); p != nil {
						slog.Error("dingtalk: callback handler panicked", "topic", df.GetTopic(), "panic", p)
					}
				}()
				w.send(a.handle(ctx, sink, &df))
			}(df)
		default:
			w.send(ack(&df, payload.DataFrameResponseStatusCodeKHandlerNotFound, ""))
		}
	}
}

// keepAlive 定时发 WebSocket ping;钉钉回的 pong 把 read deadline 往后推。
func keepAlive(conn *websocket.Conn, done <-chan struct{}) {
	t := time.NewTicker(pingEvery)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
			if conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)) != nil {
				return
			}
		}
	}
}

// frameWriter 串行化回执的写入:gorilla 的 Conn 同一时刻只许一个写者。
type frameWriter struct {
	mu   sync.Mutex
	conn *websocket.Conn
}

func (w *frameWriter) send(r *payload.DataFrameResponse) {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := w.conn.WriteJSON(r); err != nil {
		slog.Warn("dingtalk: stream reply failed", "err", err)
	}
}

// ack 是对一帧的回执:带上它的 messageId,钉钉据此认定投递成功、不再重推。
func ack(df *payload.DataFrame, code int, data string) *payload.DataFrameResponse {
	r := payload.NewDataFrameResponse(code)
	r.SetHeader(payload.DataFrameHeaderKMessageId, df.GetMessageId())
	r.SetHeader(payload.DataFrameHeaderKContentType, payload.DataFrameContentTypeKJson)
	r.SetData(data)
	return r
}

func (a *App) handle(ctx context.Context, sink imchannel.ActionSink, df *payload.DataFrame) *payload.DataFrameResponse {
	switch df.GetTopic() {
	case payload.CardInstanceCallbackTopic:
		return a.onCard(ctx, sink, df)
	case payload.BotMessageCallbackTopic:
		a.onMessage(ctx, sink, df)
		return ack(df, payload.DataFrameResponseStatusCodeKOK, "")
	}
	return ack(df, payload.DataFrameResponseStatusCodeKHandlerNotFound, "")
}

// onCard 处理一次按钮点击。认人只凭回调里的 userId —— 它由钉钉在已认证的长连接上给出,是真正的
// 点击者;对应哪个网关账户、能不能批,都是 sink 那一侧的事。提示语写回卡片的 tip 变量。
func (a *App) onCard(ctx context.Context, sink imchannel.ActionSink, df *payload.DataFrame) *payload.DataFrameResponse {
	tip := "无法识别的操作"
	var req card.CardRequest
	if json.Unmarshal([]byte(df.Data), &req) == nil {
		_ = json.Unmarshal([]byte(req.Content), &req.CardActionData)
		apNo, ok := trackApNo(req.OutTrackId)
		act := req.GetActionString("act")
		// userIdType 2 是 unionId:绑定表里存的是 userid,两者不能混着认。
		if ok && (act == "a" || act == "r") && req.UserId != "" && req.UserIdType != 2 {
			tip = sink.OnAction(ctx, imchannel.Action{PlatformUserID: req.UserId, ApNo: apNo, Approve: act == "a"}).Toast
		}
	}
	b, _ := json.Marshal(map[string]any{"response": card.CardResponse{
		CardUpdateOptions: &card.CardUpdateOptions{UpdateCardDataByKey: true},
		CardData:          &card.CardDataDto{CardParamMap: map[string]string{paramTip: tip}},
	}})
	return ack(df, payload.DataFrameResponseStatusCodeKOK, string(b))
}

// onMessage 只理组织内用户发来的单聊文本。群里的任何文字都不触发任何逻辑 —— 绑定码发在群里就是
// 泄露了,更不能认。
func (a *App) onMessage(ctx context.Context, sink imchannel.ActionSink, df *payload.DataFrame) {
	var m chatbot.BotCallbackDataModel
	if json.Unmarshal([]byte(df.Data), &m) != nil {
		return
	}
	// conversationType "1" 是单聊;没有 senderStaffId 的是组织外的人(外部联系人)。
	if m.ConversationType != "1" || m.Msgtype != "text" || m.SenderStaffId == "" || !a.seen.First(m.MsgId) {
		return
	}
	text := strings.TrimSpace(m.Text.Content)
	if text == "" {
		return
	}
	reply := sink.OnDirectMessage(ctx, imchannel.DM{PlatformUserID: m.SenderStaffId, Text: text})
	if reply == "" {
		return
	}
	// 回复走机器人单聊接口(受控出站客户端),不用事件里带来的 sessionWebhook —— 那是一个从事件
	// 正文里拿到的 URL。绑定已经落库,回复不能因为 ctx 刚好被取消(Reload)而丢掉。
	ackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := a.sendText(ackCtx, m.SenderStaffId, reply); err != nil {
		slog.Warn("dingtalk: reply to direct message failed", "err", err)
	}
}
```

- [ ] **Step 6: 整理依赖并跑测试**

Run: `cd backend && go mod tidy && gofmt -l internal/imchannel && go vet ./internal/imchannel/... && go test -race -count=3 ./internal/imchannel/dingtalk/ -run TestRun_ && go test -race -count=1 ./internal/imchannel/...`
Expected: `go.mod` 里 `github.com/open-dingtalk/dingtalk-stream-sdk-go v0.9.1` 是直接依赖；gofmt 无输出；PASS。再确认没有误用 SDK 的 client：

Run: `cd backend && grep -rn "dingtalk-stream-sdk-go/\(client\|logger\)" --include=*.go . ; echo "exit=$?"`
Expected: 没有匹配行（`exit=1`）

- [ ] **Step 7: Commit**

```bash
cd backend
git add go.mod go.sum internal/imchannel/dingtalk/
git commit -m "feat(im): receive DingTalk card clicks and direct messages over Stream"
```

---

### Task 4: 登记钉钉为交互渠道、配置校验与端到端

**Files:**
- Modify: `backend/internal/service/service.go`（登记钉钉）
- Modify: `backend/internal/handler/admin.go`（`im.dingtalk.apiBase` / `cardTemplateId` / `robotCode` 校验）
- Modify: `backend/internal/bootstrap/im_settings_test.go`（状态里有钉钉）
- Create: `backend/internal/bootstrap/im_dingtalk_test.go`

**Interfaces:**
- Consumes: `dingtalk.New`、`dttest.*`（Task 2–3）、已有 helper `newTestApp`、`login`、`do`、`setSettings`、`flushIM`、`submitProdHighRisk`、`approvalRow`、`postIMWebhook`、`repo.AuditByOperatorLike`、`repo.SettingString`、`eq`。
- Produces: bootstrap helper `func (a *testApp) dingtalkIM(token string, extra map[string]any) *dttest.Server`。

- [ ] **Step 1: 写失败的端到端测试**

Create `backend/internal/bootstrap/im_dingtalk_test.go`：

```go
package bootstrap

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"velagateway/internal/imchannel/dingtalk/dttest"
)

const dtTemplate = "tmpl-test.schema"

// dingtalkIM 起一个假钉钉开放平台,把网关的钉钉渠道指过去、打开交互审批,等到 Stream 连上。
func (a *testApp) dingtalkIM(token string, extra map[string]any) *dttest.Server {
	a.t.Helper()
	srv := dttest.New(a.t)
	kv := map[string]any{
		"im.dingtalk.approval": true, "im.dingtalk.clientID": srv.ClientID, "im.dingtalk.clientSecret": srv.ClientSecret,
		"im.dingtalk.cardTemplateId": dtTemplate, "im.dingtalk.apiBase": srv.URL,
	}
	for k, v := range extra {
		kv[k] = v
	}
	a.setSettings(token, kv)
	srv.WaitConnected(a.t)
	return srv
}

// dingtalkStatus 轮询设置页状态,直到钉钉那一行满足 match。
func (a *testApp) dingtalkStatus(token string, match func(running bool, lastError string) bool) (bool, string) {
	a.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r := a.do(http.MethodGet, "/api/v1/settings/im/status", token, nil)
		var rows []struct {
			Kind      string `json:"kind"`
			Running   bool   `json:"running"`
			LastError string `json:"lastError"`
		}
		_ = json.Unmarshal(r.Data, &rows)
		for _, row := range rows {
			if row.Kind == "dingtalk" && match(row.Running, row.LastError) {
				return row.Running, row.LastError
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	a.t.Fatal("the DingTalk status never reached the expected state")
	return false, ""
}

func TestDingTalk_BindApproveAndCloseEndToEnd(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	owner := app.login("zhangwei@vela.io", "vela123")
	srv := app.dingtalkIM(admin, nil)

	// 1. 自助绑定:控制台生成码,用自己的钉钉私聊机器人
	r := app.do(http.MethodPost, "/api/v1/me/im-identities/dingtalk/code", owner, nil)
	eq(t, r.Code, 0, "issue code")
	var issued struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(r.Data, &issued)
	srv.SendDM(t, "staff_owner", "1", issued.Code)
	srv.WaitCall(t, "oto", func(c dttest.Call) bool {
		return c.Str("userIds") == `["staff_owner"]` && strings.Contains(c.Str("msgParam"), "zhangwei@vela.io")
	})

	// 2. 建单 → 卡片投放到他和机器人的单聊
	ap := app.submitProdHighRisk(admin)
	app.flushIM()
	card := srv.WaitCall(t, "deliver", func(c dttest.Call) bool {
		return c.Str("openSpaceId") == "dtv1.card//IM_ROBOT.staff_owner" && strings.Contains(c.Str("outTrackId"), ap.ApNo)
	})
	if card.Str("cardTemplateId") != dtTemplate || card.Str("callbackType") != "STREAM" ||
		!strings.Contains(card.At("cardData", "cardParamMap", "command"), "DROP TABLE orders") {
		t.Fatalf("card = %+v", card.Params)
	}

	// 3. 点「通过」→ 走控制台同一条判定 → 卡片上的提示 → 卡片收起
	tip := srv.Click(t, "staff_owner", card.Str("outTrackId"), "a")
	eq(t, tip, "已批准,请发起人回控制台执行", "tip")
	eq(t, app.approvalRow(admin, ap.ApNo).Status, "approved", "approved via DingTalk")
	rows, _ := app.repo.AuditByOperatorLike("%(钉钉)")
	eq(t, len(rows), 1, "the audit names the channel")
	app.flushIM()
	srv.WaitCall(t, "update", func(c dttest.Call) bool {
		return c.Str("outTrackId") == card.Str("outTrackId") && c.At("cardData", "cardParamMap", "status") == "done" &&
			strings.Contains(c.At("cardData", "cardParamMap", "result"), "通过") && c.At("cardUpdateOptions", "updateCardDataByKey") == "true"
	})
}

func TestDingTalk_StrangerClickIsRefused(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	srv := app.dingtalkIM(admin, nil)
	ap := app.submitProdHighRisk(admin)

	if tip := srv.Click(t, "staff_stranger", "aegis-"+ap.ApNo+"-0123456789abcdef", "a"); !strings.Contains(tip, "未绑定") {
		t.Fatalf("tip = %q", tip)
	}
	eq(t, app.approvalRow(admin, ap.ApNo).Status, "pending", "untouched")
	rows, _ := app.repo.AuditByOperatorLike("%(未绑定)%")
	if len(rows) != 1 || rows[0].ActorName != "dingtalk:staff_stranger" {
		t.Fatalf("stranger audit = %+v", rows)
	}
}

func TestDingTalk_WebhookIsRefused(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	app.dingtalkIM(admin, nil)
	// 钉钉本期只有 Stream:这扇门没有渠道来验真伪,一律关着。
	eq(t, app.postIMWebhook("dingtalk", nil, `{}`), http.StatusForbidden, "webhook")
}

func TestDingTalk_BadCredentialsShowInStatusWithoutTheSecret(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	srv := dttest.New(t)
	const wrong = "the-wrong-client-secret-value"
	app.setSettings(admin, map[string]any{
		"im.dingtalk.approval": true, "im.dingtalk.clientID": srv.ClientID, "im.dingtalk.clientSecret": wrong,
		"im.dingtalk.cardTemplateId": dtTemplate, "im.dingtalk.apiBase": srv.URL,
	})
	_, lastErr := app.dingtalkStatus(admin, func(running bool, e string) bool {
		return !running && strings.Contains(e, "invalidClientIdOrSecret")
	})
	// 假开放平台把收到的 secret 回显进了错误 message:LastError 里只能剩 scrub 的占位符
	if !strings.Contains(lastErr, "<client-secret>") || strings.Contains(lastErr, wrong) {
		t.Fatalf("lastError = %q", lastErr)
	}
	if r := app.do(http.MethodGet, "/api/v1/settings/im/status", admin, nil); strings.Contains(string(r.Data), wrong) {
		t.Fatalf("status leaked the client secret: %s", r.Data)
	}
}

func TestDingTalk_AMissingCardTemplateShowsInStatus(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	srv := dttest.New(t)
	app.setSettings(admin, map[string]any{
		"im.dingtalk.approval": true, "im.dingtalk.clientID": srv.ClientID, "im.dingtalk.clientSecret": srv.ClientSecret,
		"im.dingtalk.apiBase": srv.URL,
	})
	app.dingtalkStatus(admin, func(running bool, e string) bool { return !running && strings.Contains(e, "卡片模板") })
	if n := len(srv.Calls("token")) + len(srv.Calls("open")); n != 0 {
		t.Fatalf("%d requests went out before the configuration error", n)
	}
}

func TestDingTalk_ClientSecretIsStoredEncrypted(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	const secret = "dingtalk-client-secret-123456"
	app.setSettings(admin, map[string]any{"im.dingtalk.clientSecret": secret})
	if raw := app.repo.SettingString("im.dingtalk.clientSecret", ""); raw == "" || strings.Contains(raw, secret) {
		t.Errorf("client secret must be stored encrypted, stored %q", raw)
	}
	if get := app.do(http.MethodGet, "/api/v1/settings", admin, nil); strings.Contains(string(get.Data), secret) {
		t.Error("GET /settings leaked the client secret")
	}
}

func TestIMSettings_DingTalkSettingsAreValidatedOnSave(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	for k, bad := range map[string]string{
		"im.dingtalk.apiBase":        "ftp://api.dingtalk.com",
		"im.dingtalk.cardTemplateId": "tmpl id with spaces",
		"im.dingtalk.robotCode":      "ding/robot",
	} {
		if app.do(http.MethodPut, "/api/v1/settings", admin, map[string]any{k: bad}).Code == 0 {
			t.Errorf("%s = %q must be rejected at save time", k, bad)
		}
	}
	app.setSettings(admin, map[string]any{
		"im.dingtalk.apiBase":        "https://api.dingtalk.com",
		"im.dingtalk.cardTemplateId": "dac1dbec-55af-40c3-be91-6d6882ef3b66.schema",
		"im.dingtalk.robotCode":      "dingabc123",
	})
}
```

`backend/internal/bootstrap/im_settings_test.go` 的 `TestIMSettings_StatusListsChannels`：`var sawLark, sawFake bool` 改为 `var sawLark, sawDingTalk, sawFake bool`；`switch` 里在 `case "fake":` 之前加：

```go
		case "dingtalk":
			sawDingTalk = true
			if !row.CanApprove {
				t.Error("dingtalk does interactive approval since phase 4")
			}
```

末尾的 `if !sawLark || !sawFake {` 改为 `if !sawLark || !sawDingTalk || !sawFake {`。

- [ ] **Step 2: 跑测试确认失败**

Run: `cd backend && go test ./internal/bootstrap/ -run 'TestDingTalk_|TestIMSettings_' -count=1`
Expected: FAIL（钉钉没登记：`dingtalkIM` 等不到连接；状态里没有 `dingtalk`；`ftp://` 的 apiBase 被存下）

- [ ] **Step 3: 登记钉钉**

`backend/internal/service/service.go`：import 加 `"velagateway/internal/imchannel/dingtalk"`；在 Telegram 那一行之后加：

```go
	// 钉钉:第 4 期起能做交互审批(Stream 长连接);群通知在第 6 期。
	s.IM.Register(imchannel.DingTalk, imchannel.Caps{Approve: true}, dingtalk.New)
```

- [ ] **Step 4: 保存时校验钉钉配置**

`backend/internal/handler/admin.go`，紧跟 `telegramWebhookSecretRe` 那两行之后加：

```go
// dingtalkTemplateRe 约束钉钉卡片模板 ID(形如 dac1dbec-….schema)。
var dingtalkTemplateRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// dingtalkRobotCodeRe 约束钉钉机器人的 robotCode(企业内部应用默认即 Client ID)。
var dingtalkRobotCodeRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
```

`SaveSettings` 的 `switch k {` 里：`case "im.telegram.apiBase", "im.lark.apiBase":` 改为 `case "im.telegram.apiBase", "im.lark.apiBase", "im.dingtalk.apiBase":`；在 `case "im.lark.domain":` 那一段之后加：

```go
			case "im.dingtalk.cardTemplateId":
				if !dingtalkTemplateRe.MatchString(sv) {
					verr = fmt.Errorf("卡片模板 ID 只能含字母、数字、.、_ 或 -,最长 128 位")
				}
			case "im.dingtalk.robotCode":
				if !dingtalkRobotCodeRe.MatchString(sv) {
					verr = fmt.Errorf("robotCode 只能含字母、数字、_ 或 -,最长 64 位")
				}
```

- [ ] **Step 5: 跑测试确认通过**

Run: `cd backend && gofmt -l internal/service internal/handler internal/bootstrap && go vet ./... && go test ./internal/bootstrap/ -run 'TestDingTalk_|TestIMSettings_|TestLark_|TestTelegram' -count=1`
Expected: gofmt 无输出；PASS

- [ ] **Step 6: 跑后端全量**

Run: `cd backend && go test ./... 2>&1 | grep -E '^(FAIL|ok|---)' | grep -v '^ok'`（bootstrap 约 5 分钟，前台跑，超时设 600000ms）
Expected: 只有已知基线失败（`internal/osc` 整包、`TestReleaseE2E_TheIndexChangeIsMadeByOSCAndTheReleaseCompletes`）

- [ ] **Step 7: Commit**

```bash
cd backend
git add internal/service/service.go internal/handler/admin.go internal/bootstrap/im_settings_test.go internal/bootstrap/im_dingtalk_test.go
git commit -m "feat(im): register DingTalk as an interactive approval channel"
```

---

### Task 5: 设置页配置钉钉交互审批

**Files:**
- Modify: `frontend/src/components/settings/ImChannelsPanel.tsx`
- Modify: `frontend/src/locales/zh.ts`、`frontend/src/locales/en.ts`

**Interfaces:**
- Consumes: 后端配置键（Global Constraints）、状态接口里钉钉的 `canApprove: true`（Task 4）。
- Produces: `IM_CHANNELS.dingtalk.approval`；`DingTalkHint` 组件；10 个文案键（zh / en 都要有，`Dict` 类型要求两边一致）。

- [ ] **Step 1: 加字段表**

`frontend/src/components/settings/ImChannelsPanel.tsx`，`IM_CHANNELS` 上方注释的最后一句改为：

```ts
 * 这里加一项,不必再去动设置页。飞书群通知（第 1 期）与交互审批（第 3 期）、Telegram 交互审批（第 2 期）、
 * 钉钉交互审批（第 4 期）。
```

`IM_CHANNELS` 里 `telegram: { … },` 之后加：

```ts
  dingtalk: {
    approval: [
      { key: 'im.dingtalk.clientID', label: 'imDtClientID', placeholderKey: 'imDtClientIDPh' },
      { key: 'im.dingtalk.clientSecret', label: 'imDtClientSecret', placeholderKey: 'imDtClientSecretPh', secret: true },
      { key: 'im.dingtalk.robotCode', label: 'imDtRobotCode', placeholderKey: 'imDtRobotCodePh' },
      { key: 'im.dingtalk.cardTemplateId', label: 'imDtTemplate', placeholderKey: 'imDtTemplatePh' },
      { key: 'im.dingtalk.apiBase', label: 'imDtApiBase', placeholder: 'https://api.dingtalk.com' },
    ],
  },
```

- [ ] **Step 2: 加提示**

同一文件，交互审批那块的 `extra={…}` 改为：

```tsx
                extra={
                  s.kind === 'telegram' ? <TelegramHint values={values} />
                    : s.kind === 'lark' ? <LarkHint values={values} />
                      : s.kind === 'dingtalk' ? <DingTalkHint />
                        : undefined
                }
```

在 `LarkHint` 函数之后加：

```tsx
/**
 * 钉钉只有 Stream 一种接收方式,不需要公网入口;要让管理员知道的是:机器人得设成 Stream 模式,
 * 卡片模板得按手册搭(变量名与网关一一对应,搭错了卡片发不出去或按钮点了没反应)。
 */
function DingTalkHint() {
  const { t } = useTranslation()
  return <div className="set-hint">{t('imDtStreamNote')}</div>
}
```

- [ ] **Step 3: 加文案**

`frontend/src/locales/zh.ts`，紧跟 `imLarkWebhookNeedsBase` 那一行之后加：

```ts
  imDtClientID: 'Client ID',
  imDtClientIDPh: '开发者后台 › 凭证与基础信息(原 AppKey)',
  imDtClientSecret: 'Client Secret',
  imDtClientSecretPh: '与 Client ID 在同一页(原 AppSecret)',
  imDtRobotCode: 'RobotCode',
  imDtRobotCodePh: '留空 = Client ID(企业内部应用的默认值)',
  imDtTemplate: '卡片模板 ID',
  imDtTemplatePh: '卡片平台 › 模板列表,形如 xxxx.schema',
  imDtApiBase: '开放平台地址',
  imDtStreamNote: 'Stream 模式:在开发者后台把机器人的消息接收模式设为 Stream,开通互动卡片相关权限;卡片模板按启用手册搭建,变量名必须一致。无需公网入口,多实例部署也安全。',
```

`frontend/src/locales/en.ts`，紧跟 `imLarkWebhookNeedsBase` 那一行之后加：

```ts
  imDtClientID: 'Client ID',
  imDtClientIDPh: 'Developer console › Credentials (formerly AppKey)',
  imDtClientSecret: 'Client Secret',
  imDtClientSecretPh: 'On the same page as the Client ID (formerly AppSecret)',
  imDtRobotCode: 'RobotCode',
  imDtRobotCodePh: 'Blank = the Client ID (the default for internal apps)',
  imDtTemplate: 'Card template ID',
  imDtTemplatePh: 'Card platform › Templates, e.g. xxxx.schema',
  imDtApiBase: 'Open platform URL',
  imDtStreamNote: 'Stream mode: set the bot\'s message receiving mode to Stream in the developer console and grant the interactive-card permissions; build the card template as the setup guide describes — the variable names must match. No public endpoint needed; safe with several gateway instances.',
```

- [ ] **Step 4: 验证**

Run: `cd frontend && npm run build && npm run lint && npm run test:unit`
Expected: build 退出码 0、lint 无输出、单元测试全部通过

- [ ] **Step 5: Commit**

```bash
cd frontend
git add src/components/settings/ImChannelsPanel.tsx src/locales/zh.ts src/locales/en.ts
git commit -m "feat(ui): configure DingTalk interactive approval"
```

---

### Task 6: 文档

**Files:**
- Modify: `docs/external-approval-setup.md`
- Modify: `docs/superpowers/specs/2026-09-27-im-channel-approval-design.md`（新增 §21）
- Modify: `docs/features.md`
- Modify: `docs/adr/0020-im-channel-approval.md`（状态行）

文档必须描述实际建成的东西：遇到下面文字与代码不一致的地方，以 `backend/internal/imchannel/dingtalk/` 的代码为准并在报告里说明。

- [ ] **Step 1: 启用手册**

`docs/external-approval-setup.md`：

「当前支持」表格里钉钉那一行改为：

```markdown
| 钉钉 | 规划中(第 6 期) | ✅ 企业内部应用(Stream 长连接) |
```

在「## Telegram 交互审批」整节之前插入：

```markdown
## 钉钉交互审批

1. 钉钉开发者后台 › 创建**企业内部应用**,在「添加应用能力」里添加**机器人**;机器人配置里把「消息接收模式」设为
   **Stream 模式**(不需要公网入口,多实例部署也安全 —— 钉钉把每个事件只投给其中一个连接)。发布机器人。
2. 「权限管理」开通互动卡片的实例写权限、机器人发送单聊消息的权限;把应用的「可见范围」设成需要审批的人 ——
   不在可见范围内的人收不到卡片(网关会在日志里记一条投放失败)。
3. **搭卡片模板**(钉钉的 Stream 卡片回调只支持互动卡片高级版,高级版只能用卡片平台上的模板):
   1. 打开钉钉卡片平台,新建模板,关联上面的应用。
   2. 添加 8 个字符串变量,名字必须一字不差:`title`、`summary`、`reason`、`command`、`consoleUrl`、`status`、
      `result`、`tip`。
   3. 组件(全部用「文本」组件,**不要用 Markdown**:原因与命令要原样显示,Markdown 会把发起人写的链接渲染成真链接):
      - 标题:`${title}`
      - 文本:`${summary}`
      - 文本:`原因 ${reason}`,显示条件「`reason` 不为空」
      - 文本:`${command}`
      - 按钮「通过」:点击事件选「回传请求」,回传参数 `act` = `a`;按钮「驳回」:回传参数 `act` = `r`;
        两个按钮的显示条件都是「`status` 等于 `pending`」
      - 按钮「前往控制台」:点击事件「打开链接」,地址 `${consoleUrl}`
      - 文本:`${result}`,显示条件「`status` 等于 `done`」
      - 文本:`${tip}`,显示条件「`tip` 不为空」(点按钮后网关把结果写在这里;钉钉卡片没有弹出提示)
   4. 保存并发布,在模板列表里复制**模板 ID**(形如 `xxxx.schema`)。
4. 网关「设置 › IM 渠道 › 钉钉」打开「交互审批」,填 Client ID、Client Secret(开发者后台「凭证与基础信息」)和模板 ID。
   RobotCode 留空即用 Client ID(企业内部应用的默认值)。私有化部署在「开放平台地址」里填自己的地址 —— 它与其他出站
   地址走同一套校验(https 且解析到公网地址),生产环境指向内网需部署配置 `webhook.allow_private: true`
   (`VELA_WEBHOOK_ALLOW_PRIVATE`),见 DEPLOY.md。保存。
5. 状态显示「运行中」且没有错误提示即已连通。Client Secret 错、应用没开 Stream / 卡片权限、开放平台地址填错、没填模板
   ID 时,状态会显示错误原因。
6. 钉钉只支持 Stream,不提供 webhook 模式;`/api/v1/im/dingtalk/webhook` 一律 403。
7. 卡片里的「通过 / 驳回」只做决定,不执行;决定后所有渠道上这张单的卡片都会收起。钉钉卡片收起时只隐藏按钮、显示结果,
   单号、命令、原因原样保留。只理组织内成员发给机器人的单聊文本:群里的消息、外部联系人的消息一律不理。
```

「## 交互审批的账号绑定」一节若有列举渠道的地方，把钉钉加进去（绑定码发给机器人的单聊，钉钉里搜索应用的机器人名即可打开单聊）；钉钉的绑定弹窗不显示机器人名（钉钉没有查询机器人名称的接口）。

- [ ] **Step 2: spec 新增 §21**

`docs/superpowers/specs/2026-09-27-im-channel-approval-design.md` 末尾追加：

```markdown
## 21. 第 4 期（钉钉交互审批）实现记录

- **不用官方 Stream SDK 的客户端，只用它的数据类型**：`dingtalk-stream-sdk-go` v0.9.1 的 `client` 写死
  `http.DefaultTransport` 与 `websocket.DefaultDialer`（注入不了受控出站客户端）、在 Debug 级把含 `clientSecret`
  的请求体打进进程全局 logger、断线重连跑在 `context.Background()` 上（`Reload` 之后旧连接不退出，继续分走事件）。
  Stream 协议由适配器自己实现（换连接地址 → 拨 `endpoint?ticket=` → JSON 帧：`SYSTEM ping` 原样回、`SYSTEM
  disconnect` 换连接、`CALLBACK` 处理后带 `messageId` 回执），拨号走受控拨号器，钉钉下发的连接地址也过出站校验；
  帧与载荷用 SDK 的 `payload` / `chatbot` / `card` 类型，假网关用同一套。不上报 `localIp`。
- **只有 Stream，没有 webhook**：钉钉的 HTTP 回调要分别处理机器人消息（`timestamp` + `sign`）与另行登记的卡片回调
  路由；Stream 已覆盖全部场景且不需要公网入口。钉钉不实现 `Webhooker`，`/api/v1/im/dingtalk/webhook` 一律 403。
  没有 `im.dingtalk.mode` 键。
- **互动卡片高级版 + 管理员搭的模板**：Stream 卡片回调只支持高级版，高级版只能用卡片平台的模板。新增配置键
  `im.dingtalk.cardTemplateId`。模板变量 `title` / `summary` / `reason` / `command` / `consoleUrl` / `status`
  （`pending` | `done`）/ `result` / `tip`，全部用纯文本组件；按钮回传参数 `act` = `a` | `r`。没填模板 ID 时 `Run`
  在任何网络请求之前报错。
- **卡片标识**：`outTrackId` = `aegis-<ApNo>-<16 位十六进制>`，点击回调只凭它认单；认人只凭回调的 `userId`
  （`userIdType` 为 userid），unionId 的点击不认。投放到 `dtv1.card//IM_ROBOT.<userid>`，不许转发。
- **投放结果逐个检查**：接口 200 但 `deliverResults[].success=false`（对方不在可见范围内）按发送失败处理，不记卡片。
- **点击的提示写回卡片的 `tip` 变量**：钉钉卡片回调没有 toast。
- **收卡只改 `status` / `result` / `tip`**（`updateCardDataByKey`），卡片其余内容保留 —— 与飞书整张替换不同。
- **私聊回复走机器人单聊接口**（`/v1.0/robot/oToMessages/batchSend`），不用事件里带来的 `sessionWebhook` 地址。
  只理 `conversationType=1`、`msgtype=text`、带 `senderStaffId` 的消息；按 `msgId` 去重（最近 512 条）。
- **没有机器人名**：钉钉没有按 robotCode 查机器人名称的接口，`*App` 不实现 `BotNamer`。
- **access token** 缓存到过期前 5 分钟；钉钉回 401 时换新 token 重试一次。
- **永久性错误停循环**：凭据被拒、没开权限、开放平台地址填错（3xx / 4xx、解析不了的 2xx、被出站校验拒绝）、没填模板。
  限流码（含 `QpsLimit` / `Throttl`）、429、5xx、网络错误与 DNS 解析失败退避重试。Client Secret 在所有错误里换成
  `<client-secret>`。
- 新配置键：`im.dingtalk.clientID`、`clientSecret`🔒、`robotCode`（缺省 = clientID）、`cardTemplateId`、`apiBase`
  （缺省 `https://api.dingtalk.com`，走出站 URL 校验）。
- 重试退避、拒绝重定向、取受控拨号器、消息去重抽成 `imchannel` 的 `Retry` / `Sleep` / `NoRedirects` /
  `GuardedDial` / `Recent`，飞书与 Telegram 改用它们。
```

- [ ] **Step 3: 功能清单与 ADR**

`docs/features.md` 里「目前支持飞书**群通知**与**交互审批**(长连接 / webhook)、Telegram **交互审批**(long polling / webhook)，其余渠道分期接入」改为「目前支持飞书**群通知**与**交互审批**(长连接 / webhook)、Telegram **交互审批**(long polling / webhook)、钉钉**交互审批**(Stream 长连接)，其余渠道分期接入」。

`docs/adr/0020-im-channel-approval.md` 状态行改为：

```markdown
- 状态:已采纳(第 1 期:框架 + 飞书群通知;第 2 期:Telegram 交互审批;第 3 期:飞书交互审批;第 4 期:钉钉交互审批;其余渠道分期接入)
```

- [ ] **Step 4: Commit**

```bash
git add docs/external-approval-setup.md docs/superpowers/specs/2026-09-27-im-channel-approval-design.md docs/features.md docs/adr/0020-im-channel-approval.md
git commit -m "docs: document DingTalk approvals and record phase four"
```
