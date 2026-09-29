# 多渠道 IM 审批 · 第 2 期（Telegram 交互审批）Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 接入第一个真实的交互审批渠道 Telegram：审批卡片私信给已绑定的链上成员，点按钮即通过 / 驳回；支持 long polling 与 webhook 两种入站方式；私聊机器人完成绑定码绑定。

**Architecture:** 新增 `internal/imchannel/telegram`（`Approver` + `BotNamer` + 新的可选能力 `Webhooker`），只用标准库调用 Telegram Bot API。Hub 增加 `ServeWebhook` 作为所有渠道共用的 webhook 入口，路由 `POST /api/v1/im/:channel/webhook` 在 JWT 之外，靠可选的来源 IP 白名单和渠道自己的 secret 认证。判定仍然只在 `DecideBlockFor`：适配器只把「谁点了哪张单的哪个按钮」交给 `ActionSink`。另建 `telegram/tgtest` 假 Bot API 服务器，单元测试和端到端测试都用它，不连真实 Telegram。

**Tech Stack:** Go 1.25 标准库（net/http、encoding/json、html）· 现有 Gin / GORM / PostgreSQL · React 19 + TS · 不引入新的第三方依赖（`pgconn` 已在模块图里，只是从间接依赖变成直接依赖）。

**Spec:** `docs/superpowers/specs/2026-09-27-im-channel-approval-design.md`（§2.4 多实例、§4 绑定、§5 出站、§6 入站、§7 决策、§8 收卡、§9 配置、§15 失败朝严）。Telegram 协议细节参考已被吸收的 `docs/superpowers/specs/2026-09-11-telegram-approval-design.md` §4–§7。第 1 期的暂缓事项见本地 `.scratch/im-channel-approval/phase1-ledger.md`，本计划的 Task 1 接住其中「第一个交互渠道上线前必须补」的部分。

**范围说明：** spec §14 的第 2 期。Telegram 群通知不在本期，属于第 6 期（群通知补齐）；本期 Telegram 只登记 `Caps{Approve: true}`。

## Global Constraints

- 模块名 `velagateway`，Go 1.25；后端命令都在 `backend/` 下执行；跑后端测试需要 `createdb vela_test`。
- 本机没有 MySQL：`internal/osc` 整包与 bootstrap 的 `TestReleaseE2E_TheIndexChangeIsMadeByOSCAndTheReleaseCompletes` 是已知基线失败，不算回归。bootstrap 整包约 5 分钟，前台跑。
- 本期**没有**数据库迁移。新配置键全部落在 `tbl_setting`。
- 配置键（spec §9）：`im.telegram.approval`（bool）、`im.telegram.botToken`🔒、`im.telegram.mode`（`polling` | `webhook`，缺省 polling）、`im.telegram.webhookSecret`🔒、`im.telegram.apiBase`（缺省 `https://api.telegram.org`，自建 Bot API server 时填）；公共键 `im.callbackBaseURL`、`im.callbackAllowIPs`。`im.` 前缀且 `isSecretKey` 为真的键自动加密（第 1 期已实现）。
- webhook 路径固定为 `/api/v1/im/telegram/webhook`；认证头 `X-Telegram-Bot-Api-Secret-Token`，常量时间比较。
- 按钮 `callback_data`：`ap:<ApNo>:a` / `ap:<ApNo>:r`；收卡后的状态按钮是 `done`。Telegram 上限 64 字节。
- `answerCallbackQuery` 的 text 上限 200 字符，截到 190 rune；消息正文上限为「解析实体后」4096 字符，命令截到 3000 rune。
- **bot token 在请求 URL 里**：适配器返回、记录的每一个错误都必须把 token 替换成 `<bot-token>`。
- 失败朝严（spec §15）：渠道未开交互审批 → webhook 403、长连接不启动；secret 未配置或不符 → 403；来源 IP 不在白名单（非空时）→ 403；鉴权通过后处理失败 → 200 + 日志，免得平台无限重投。
- long polling 同一时刻只允许一个实例（Telegram 对第二个 `getUpdates` 返回 409）。不做选主；启动日志和设置页写明。
- 出站只用 `Deps.HTTP`（带 SSRF 防护）。long polling 需要更长的超时：复制一份 `*d.HTTP` 只改 `Timeout`，Transport 仍是同一份。
- Builder 与 `BotName` 在 Hub 锁内调用，必须廉价、不做 I/O（第 1 期已写进接口注释）；`getMe` 在 `Run` 里做，结果缓存。
- 代码注释沿用仓库风格（中文，讲清「为什么」）；提交信息英文 conventional commits，结尾 `Co-Authored-By: <执行提交的模型名> <noreply@anthropic.com>`。
- 前端验证：`cd frontend && npm run build && npm run lint && npm run test:unit`。

## Review Focus

spec 没有逐条写出、但最可能伤到使用者的五类输入。每条的测试放进负责它的任务：

1. **命令里带 `<`、`&`、`</pre>` 之类的字符**：Telegram 按 HTML 解析，不转义整条消息会被拒，表现为「卡片发不出去」的静默失败（Task 3，`TestRenderCard_EscapesHTMLAndStaysUnderTheLimit`）。
2. **bot token 出现在错误里**：网络错误、平台错误、认证失败都不能把 token 带进日志、`LastError`、设置页状态（Task 3 `TestCall_ErrorsNeverContainTheToken`，Task 5 `TestTelegram_BadTokenShowsInStatusWithoutTheToken`）。
3. **同一次点击被投递两次**：polling 重启后 Telegram 会重投未确认的 update；offset 必须推进，重投必须幂等（Task 4 `TestPolling_AnUpdateIsHandledOnce`，端到端的幂等由第 1 期 `OnAction` 保证）。
4. **在 polling 与 webhook 之间来回切换**：进 webhook 模式要 `setWebhook`，回 polling 要先 `deleteWebhook`，否则 `getUpdates` 永远拿不到东西（Task 5 `TestTelegram_SwitchingModesMovesTheWebhook`）。
5. **绑定码发在群里 / 陌生人点按钮**：群消息一律不理、不回；陌生人点击被拒且留审计（Task 4 `TestDispatch_GroupTextIsIgnored`，Task 5 `TestTelegram_StrangerClickIsRefused`）。

---

## 文件结构

**新建（后端）**

| 文件 | 职责 |
|---|---|
| `backend/internal/imchannel/telegram/api.go` | Bot API 类型、`call`（含 token 脱敏）、Channel 构造 |
| `backend/internal/imchannel/telegram/card.go` | 卡片渲染（HTML 转义、长度控制）、按钮、状态按钮 |
| `backend/internal/imchannel/telegram/inbound.go` | `Run`（polling / webhook）、`dispatch`、`HandleWebhook` |
| `backend/internal/imchannel/telegram/tgtest/server.go` | 假 Bot API 服务器（测试用，非 `_test.go` 以便跨包复用） |
| `backend/internal/imchannel/telegram/card_test.go` `api_test.go` `inbound_test.go` | 适配器测试 |
| `backend/internal/imchannel/hub_webhook_test.go` | Hub webhook 入口测试 |
| `backend/internal/bootstrap/im_webhook_test.go` | webhook 路由端到端测试（假渠道） |
| `backend/internal/bootstrap/im_telegram_test.go` | Telegram 端到端测试 |
| `backend/internal/bootstrap/im_hardening_test.go` | Task 1 的端到端测试 |

**修改（后端）**：`imchannel/channel.go`（`Webhooker`、`ActionSink` 注释）、`imchannel/hub.go`（stop 栅栏、按 entry 记状态、`ServeWebhook`）、`imchannel/imtest/fake.go`（webhook 支持）、`service/im_approval.go`（控制台链接）、`service/im_bind.go`（代绑拒绝停用账户、解绑只在删到时审计）、`service/webhook.go`（回调地址报错文案）、`repository/im.go`（唯一约束冲突映射）、`handler/im.go`（`IMWebhook`、新错误映射）、`handler/admin.go`（新键校验）、`middleware/apiclient.go`（导出 `IPInList`）、`bootstrap/router.go`、`docs/openapi.yaml`、`service/service.go`（登记 Telegram）、`backend/go.mod`。

**修改（前端）**：`components/settings/ImChannelsPanel.tsx`、`locales/zh.ts`、`locales/en.ts`。

**修改（文档）**：`docs/external-approval-setup.md`、`docs/superpowers/specs/2026-09-27-im-channel-approval-design.md`（新增 §19）、`docs/features.md`、`docs/adr/0020-im-channel-approval.md`（状态行）。

---
### Task 1: 第一个交互渠道上线前的收尾（接住第 1 期暂缓事项）

第 1 期把这些推迟到「第一个真实 Approver 上线前」。它们现在都会被 Telegram 触发：长连接真的会启停、真的会有人换号、真的会点卡片里的链接。

**Files:**
- Modify: `backend/internal/imchannel/hub.go`（`Stop` 之后不再接新任务；`run` 按 entry 记状态；重复 `Register` 不卡死；`Run` 无声退出也留痕）
- Modify: `backend/internal/imchannel/channel.go`（`ActionSink` 注释：回调里不许调 `Reload`）
- Modify: `backend/internal/repository/im.go`（并发绑定的唯一约束冲突映射成 `ErrIMIdentityTaken`）
- Modify: `backend/go.mod`（`github.com/jackc/pgx/v5` 从 indirect 变 direct，`go mod tidy` 完成）
- Modify: `backend/internal/service/im_bind.go`（代绑拒绝停用账户与服务账号；代解绑只在真的删到时写审计）
- Modify: `backend/internal/service/im_approval.go`（卡片里的控制台链接改成 `/approvals`）
- Modify: `backend/internal/handler/im.go`（新错误的文案）
- Modify: `backend/internal/bootstrap/lark_test.go`（链接断言随之改）
- Test: `backend/internal/imchannel/hub_lifecycle_test.go`、`backend/internal/repository/im_test.go`（追加）、`backend/internal/bootstrap/im_hardening_test.go`

**Interfaces:**
- Consumes: 第 1 期的 `imchannel.Hub`、`imtest.Fake`、`Repo.BindIMIdentity`、`Services.AdminBindIM / AdminUnbindIM / imCardFor`
- Produces:
  - `service.ErrIMUserInactive`（代绑目标已停用或是服务账号）
  - Hub 行为：`Stop()` 之后 `ApprovalCreated / ApprovalSettled / NotifyApproval / Reload` 都是空操作；`Run` 在 ctx 仍有效时返回 nil，`Status.LastError` = `"长连接已退出(渠道未说明原因)"`；对正在运行的渠道再次 `Register` 会先取消旧的长连接
  - 卡片 `ConsoleURL` = `<notify.consoleURL 去尾斜杠>/approvals`

- [ ] **Step 1: 写失败的 Hub 测试 `imchannel/hub_lifecycle_test.go`**

```go
package imchannel_test

import (
	"context"
	"testing"
	"time"

	"velagateway/internal/imchannel"
	"velagateway/internal/imchannel/imtest"
)

// quitter 的 Run 一进来就返回 nil —— 一个「悄悄放弃」的渠道。
type quitter struct{ *imtest.Fake }

func (q quitter) Run(context.Context, imchannel.ActionSink) error { return nil }

func TestHub_WorkAfterStopIsDropped(t *testing.T) {
	s, st := newSettings(), newStore()
	a := imtest.New("a")
	h := imchannel.NewHub(imchannel.Deps{Settings: s}, st)
	h.Register("a", both, a.Builder())
	h.SetSink(nopSink{})
	s.set("im.a.approval", true)
	s.set("im.a.notify", true)
	h.Start(context.Background())
	a.WaitRunning(t)
	st.bind("a", 1, "pa1")

	h.Stop()
	if a.Running() {
		t.Fatal("Stop must end the run loop")
	}
	h.ApprovalCreated(10, imchannel.ApprovalCard{ApNo: "AP-10"}, []int64{1})
	h.NotifyApproval(imchannel.ApprovalCard{ApNo: "AP-10"})
	h.Reload()
	h.Flush()
	if len(a.Sent()) != 0 || len(a.Notified()) != 0 || a.Running() {
		t.Fatalf("after Stop: sent=%d notified=%d running=%v — want nothing", len(a.Sent()), len(a.Notified()), a.Running())
	}
}

func TestHub_RunThatGivesUpSilentlyIsReported(t *testing.T) {
	s, st := newSettings(), newStore()
	q := quitter{imtest.New("q")}
	h := newHub(t, s, st)
	h.Register("q", both, func(imchannel.Deps) imchannel.Channel { return q })
	h.SetSink(nopSink{})
	s.set("im.q.approval", true)
	h.Start(context.Background())

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if st := h.Status(); !st[0].Running && st[0].LastError != "" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("a run loop that returned nil while live must show up in status: %+v", h.Status())
}

func TestHub_ReRegisteringARunningChannelDoesNotHang(t *testing.T) {
	s, st := newSettings(), newStore()
	old, fresh := imtest.New("a"), imtest.New("a")
	h := newHub(t, s, st)
	h.Register("a", both, old.Builder())
	h.SetSink(nopSink{})
	s.set("im.a.approval", true)
	h.Start(context.Background())
	old.WaitRunning(t)

	done := make(chan struct{})
	go func() {
		h.Register("a", both, fresh.Builder())
		h.Reload()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Reload after re-Register hung")
	}
	fresh.WaitRunning(t)
	if old.Running() {
		t.Fatal("the replaced channel's loop must be stopped")
	}
	if st := h.Status(); !st[0].Running {
		t.Fatalf("the new entry must report running: %+v", st[0])
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd backend && go test -race ./internal/imchannel/ -run 'TestHub_WorkAfterStop|TestHub_RunThatGivesUp|TestHub_ReRegistering' -v`
Expected: 三个都 FAIL（Stop 之后照样发送 / 状态没有 LastError / Reload 卡住超时）。

- [ ] **Step 3: 改 `hub.go`**

1. `Hub` 结构体在 `sink ActionSink` 下面加：

```go
	// stopped 之后不再接任何新活。Stop 在 mu 里置位,而所有 work.Add 也在 mu 里做 ——
	// 于是 Stop 里的 work.Wait 不会撞上一个从零开始的 Add(sync.WaitGroup 禁止这样用)。
	stopped bool
```

2. `Register` 改成（替换现有函数体）：

```go
// Register 登记一个渠道。登记完要 Reload 才生效。
//
// 同一个渠道登记第二次时,先取消旧实例的长连接:旧 entry 被替换后,stopRuns 再也找不到
// 它的 cancel,而 runs.Wait 会一直等那个没人能叫停的循环。
func (h *Hub) Register(kind string, caps Caps, b Builder) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if old, ok := h.entries[kind]; ok {
		if old.cancel != nil {
			old.cancel()
			old.cancel = nil
		}
	} else {
		h.order = append(h.order, kind)
	}
	h.entries[kind] = &entry{caps: caps, build: b}
}
```

3. `Reload` 在 `h.mu.Lock()`（第二次加锁、`defer h.mu.Unlock()` 之后）紧接着加：

```go
	if h.stopped {
		return
	}
```

并把 `go h.run(kind, ap, ctx, pinnedSink{kind: kind, next: h.sink})` 改成 `go h.run(e, kind, ap, ctx, pinnedSink{kind: kind, next: h.sink})`。

4. `Stop` 改成：

```go
func (h *Hub) Stop() {
	h.reloadMu.Lock()
	defer h.reloadMu.Unlock()
	h.mu.Lock()
	h.stopped = true
	h.mu.Unlock()
	h.stopRuns()
	h.work.Wait()
}
```

5. `run` 改成按 entry 记状态，并处理「无声退出」：

```go
// run 把状态记在启动它的那个 entry 上,而不是按 kind 去查:渠道被重新登记后,
// 旧循环退出时不该改写新 entry 的状态。
func (h *Hub) run(e *entry, kind string, ap Approver, ctx context.Context, sink ActionSink) {
	defer h.runs.Done()
	panicked := false
	var err error
	func() {
		defer func() {
			if p := recover(); p != nil {
				panicked, err = true, panicErr(kind, "run", p)
			}
		}()
		err = ap.Run(ctx, sink)
	}()
	h.mu.Lock()
	defer h.mu.Unlock()
	e.running = false
	switch {
	case err != nil && (ctx.Err() == nil || panicked):
		e.lastErr = err.Error()
		slog.Warn("im channel stopped", "channel", kind, "err", err)
	case err == nil && ctx.Err() == nil:
		// 没人叫它停,它却自己回来了,还不说为什么 —— 设置页上必须看得见,否则就是一个
		// 开关开着、状态「未运行」、没有任何原因的渠道。
		e.lastErr = "长连接已退出(渠道未说明原因)"
		slog.Warn("im channel run loop returned without error", "channel", kind)
	}
}
```

6. `enqueue` 开头的 `h.work.Add(1)` 改成：

```go
	h.mu.Lock()
	if h.stopped {
		h.mu.Unlock()
		return
	}
	h.work.Add(1)
	h.mu.Unlock()
```

7. `NotifyApproval` 整个替换为：

```go
// NotifyApproval 往每个开了群通知的渠道发一条新工单通知。
func (h *Hub) NotifyApproval(c ApprovalCard) {
	type notifyTarget struct {
		kind string
		n    Notifier
	}
	h.mu.Lock()
	if h.stopped {
		h.mu.Unlock()
		return
	}
	var ns []notifyTarget
	for _, kind := range h.order {
		e := h.entries[kind]
		if n, ok := e.ch.(Notifier); ok && e.caps.Notify && h.deps.Settings.Bool(Key(kind, "notify")) {
			ns = append(ns, notifyTarget{kind, n})
		}
	}
	h.work.Add(len(ns)) // 在锁内加:见 stopped 字段的注释
	h.mu.Unlock()
	for _, t := range ns {
		go func(t notifyTarget) {
			defer h.work.Done()
			err := h.notifyOne(t.kind, t.n, c)
			if err != nil {
				slog.Warn("im: notify failed", "channel", t.kind, "apNo", c.ApNo, "err", err)
			}
			h.recordNotify(t.kind, err)
		}(t)
	}
}
```

- [ ] **Step 4: `channel.go` 的 `ActionSink` 注释补一句**

在 `// ActionSink 由 service 实现:认人、决策、绑定都在那一侧。` 下加：

```go
//
// 回调跑在渠道的长连接里。回调里不许调用 Hub.Reload —— Reload 要等所有长连接退出,
// 而这一条正卡在回调里,两边会互相等死。
```

- [ ] **Step 5: 跑 Hub 测试确认通过**

Run: `cd backend && go test -race ./internal/imchannel/... -v 2>&1 | tail -30`
Expected: PASS（含第 1 期全部 Hub 测试）。

- [ ] **Step 6: 并发绑定的冲突映射 —— 先写失败测试**

`backend/internal/repository/im_test.go` 追加：

```go
// 两个人同时把同一个平台账号绑到自己身上:只能有一个成功,另一个必须拿到
// ErrIMIdentityTaken —— 而不是一个原样的唯一约束错误,那会在界面上变成「操作失败」。
func TestIMIdentity_ConcurrentBindOfOnePlatformIDHasOneWinner(t *testing.T) {
	r, a, b := newIMRepo(t)
	for i := 0; i < 20; i++ {
		pid := fmt.Sprintf("race-%d", i)
		errs := make([]error, 2)
		var wg sync.WaitGroup
		for j, uid := range []int64{a, b} {
			wg.Add(1)
			go func(j int, uid int64) {
				defer wg.Done()
				errs[j] = r.BindIMIdentity(&model.IMIdentity{UserID: uid, Channel: "race", PlatformUserID: pid, BoundBy: model.IMBoundSelf})
			}(j, uid)
		}
		wg.Wait()
		wins := 0
		for _, err := range errs {
			switch {
			case err == nil:
				wins++
			case errors.Is(err, ErrIMIdentityTaken):
			default:
				t.Fatalf("round %d: unexpected error %v", i, err)
			}
		}
		if wins != 1 {
			t.Fatalf("round %d: %d winners, want exactly 1", i, wins)
		}
		_, _ = r.UnbindIMIdentity(a, "race")
		_, _ = r.UnbindIMIdentity(b, "race")
	}
}
```

（import 补 `fmt`、`sync`。）

Run: `cd backend && go test ./internal/repository/ -run TestIMIdentity_ConcurrentBind -count=3 -v`
Expected: FAIL，出现 `unexpected error … duplicate key value violates unique constraint`（至少一轮）。若三次都恰好没撞上，也照样做 Step 7 —— 这条竞争是真实存在的，只是本机太快。

- [ ] **Step 7: 映射唯一约束冲突**

`repository/im.go` import 加 `"github.com/jackc/pgx/v5/pgconn"`，`BindIMIdentity` 改成：

```go
func (r *Repo) BindIMIdentity(id *model.IMIdentity) error {
	if id.CreatedAt.IsZero() {
		id.CreatedAt = time.Now()
	}
	err := r.db.Transaction(func(tx *gorm.DB) error {
		var n int64
		if err := tx.Model(&model.IMIdentity{}).
			Where("channel = ? AND platform_user_id = ? AND user_id <> ?", id.Channel, id.PlatformUserID, id.UserID).
			Count(&n).Error; err != nil {
			return err
		}
		if n > 0 {
			return ErrIMIdentityTaken
		}
		return tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "user_id"}, {Name: "channel"}},
			DoUpdates: clause.AssignmentColumns([]string{"platform_user_id", "bound_by", "created_at"}),
		}).Create(id).Error
	})
	// 并发下两边都查到「没人占」,后写的那个撞在 uk_im_identity_platform 上。它说的是同一件
	// 事 —— 这个平台账号已经是别人的 —— 所以给同一个错误。
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "uk_im_identity_platform" {
		return ErrIMIdentityTaken
	}
	return err
}
```

然后 `cd backend && go mod tidy`（`pgx/v5` 变成直接依赖），`git diff go.mod` 确认只有这一行的 `// indirect` 被去掉或挪到直接依赖块。

Run: `cd backend && go test ./internal/repository/ -run TestIM -count=3 -v`
Expected: PASS

- [ ] **Step 8: 代绑 / 代解绑 / 控制台链接 —— 先写失败测试 `bootstrap/im_hardening_test.go`**

```go
package bootstrap

import (
	"net/http"
	"testing"

	"velagateway/internal/model"
)

func TestIMBind_AdminCannotBindADisabledOrServiceAccount(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	app.fakeIM("fake")
	zw := app.userIDByEmail(admin, "zhangwei@vela.io")
	ln := app.userIDByEmail(admin, "lina@vela.io")
	if err := app.repo.UpdateUserFields(zw, map[string]any{"status": "disabled"}); err != nil {
		t.Fatal(err)
	}
	if err := app.repo.UpdateUserFields(ln, map[string]any{"kind": model.UserKindService}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{zw, ln} {
		r := app.do(http.MethodPut, "/api/v1/users/"+itoa(id)+"/im-identities/fake", admin, map[string]any{"platformUserId": "p-" + itoa(id)})
		if r.Code == 0 {
			t.Fatalf("user %d: binding an account that can never approve must be refused", id)
		}
	}
	var n int64
	app.repo.DB().Model(&model.IMIdentity{}).Count(&n)
	eq(t, n, int64(0), "nothing was bound")
}

func TestIMBind_AdminUnbindWithNothingBoundWritesNoAudit(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	app.fakeIM("fake")
	ln := app.userIDByEmail(admin, "lina@vela.io")

	eq(t, app.do(http.MethodDelete, "/api/v1/users/"+itoa(ln)+"/im-identities/fake", admin, nil).Code, 0, "unbind is idempotent")
	var n int64
	app.repo.DB().Model(&model.AuditLog{}).Where("command LIKE ?", "admin.im.unbind%").Count(&n)
	eq(t, n, int64(0), "removing nothing is not an event")
}

func TestIM_CardLinkPointsAtTheApprovalsPage(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	f := app.fakeIM("fake")
	app.enableIM(admin, "fake")
	app.bindIM("zhangwei@vela.io", "fake", "p-zw")
	app.setSettings(admin, map[string]any{"notify.consoleURL": "https://gw.corp.io/"})

	app.submitProdHighRisk(admin)
	app.flushIM()
	if s := f.Sent(); len(s) != 1 || s[0].Card.ConsoleURL != "https://gw.corp.io/approvals" {
		t.Fatalf("sent = %+v — the router uses paths, a /#/ link lands on the dashboard", s)
	}
}
```

Run: `cd backend && go test ./internal/bootstrap/ -run 'TestIMBind_Admin|TestIM_CardLink' -v`
Expected: 三个都 FAIL。

- [ ] **Step 9: 实现**

`service/im_bind.go`：

1. 在 `ErrIMChannelOff` 旁边加：

```go
// ErrIMUserInactive:代绑的目标账户已停用或是服务账号 —— 它们点了也认不出来(imUser
// 与登录同一条规则),绑上只会让管理员以为那个人能在 IM 里审批了。
var ErrIMUserInactive = fmt.Errorf("im: user disabled or service account")
```

2. `AdminBindIM` 在 `u, err := s.Repo.GetUserByID(userID)` 的 nil 判断之后加：

```go
	if u.Status != "active" || u.Kind == model.UserKindService {
		return ErrIMUserInactive
	}
```

3. `AdminUnbindIM` 把 `if _, err := s.Repo.UnbindIMIdentity(u.ID, channel); err != nil { return err }` 改成：

```go
	ok, err := s.Repo.UnbindIMIdentity(u.ID, channel)
	if err != nil {
		return err
	}
	if !ok {
		return nil // 本来就没绑:什么都没发生,不是一条审计
	}
```

`service/im_approval.go` 的 `imCardFor` 里把 `console = base + "/#/approvals"` 改成 `console = base + "/approvals"`，并在那行上方加注释：`// 前端是 BrowserRouter:/#/approvals 会落到根路径(总览页),而不是审批页。`

`handler/im.go` 的 `imFail` 在 `ErrIMIdentityTaken` 分支后加：

```go
	case errors.Is(err, service.ErrIMUserInactive):
		resp.Fail(c, resp.CodeBadRequest, "该账户已停用或是服务账号,绑定了也无法在 IM 里审批")
```

`bootstrap/lark_test.go` 第 53 行附近把 `"https://gw.corp.io/#/approvals"` 改成 `"https://gw.corp.io/approvals"`。

- [ ] **Step 10: 跑测试确认通过**

Run: `cd backend && go vet ./... && go test ./internal/bootstrap/ -run 'TestIMBind_|TestIM_|TestLark_' -v 2>&1 | tail -40`
Expected: PASS

然后整包一次（前台，600000ms）：`cd backend && go test ./internal/bootstrap/ 2>&1 | grep -E -- '--- FAIL|^FAIL|^ok|panic:'`
Expected: 只有基线的 `TestReleaseE2E_…`。

- [ ] **Step 11: 提交**

```bash
git add backend
git commit -m "fix(im): harden the hub and bindings before the first real channel

Work submitted after Stop is dropped instead of racing the final Wait;
a run loop that returns silently now shows up in the channel status;
re-registering a running channel cancels the old loop instead of
hanging the next Reload. Concurrent binds of one platform account now
lose with ErrIMIdentityTaken, admins can no longer bind accounts that
could never approve, and card links point at /approvals, which is the
route the browser router actually serves.

Co-Authored-By: <model> <noreply@anthropic.com>"
```

---

### Task 2: 所有渠道共用的 webhook 入口

**Files:**
- Modify: `backend/internal/imchannel/channel.go`（新增 `Webhooker`）
- Modify: `backend/internal/imchannel/hub.go`（新增 `ServeWebhook`）
- Modify: `backend/internal/imchannel/imtest/fake.go`（假渠道支持 webhook）
- Modify: `backend/internal/middleware/apiclient.go`（导出 `IPInList`）
- Modify: `backend/internal/handler/im.go`（`IMWebhook`）
- Modify: `backend/internal/handler/admin.go`（`im.callbackBaseURL` / `im.callbackAllowIPs` / `im.telegram.apiBase` / `im.telegram.mode` 保存时校验）
- Modify: `backend/internal/service/webhook.go`（`ValidateCallbackBaseURL` 报错文案里的 ADR 编号）
- Modify: `backend/internal/bootstrap/router.go`、`backend/docs/openapi.yaml`
- Test: `backend/internal/imchannel/hub_webhook_test.go`、`backend/internal/bootstrap/im_webhook_test.go`

**Interfaces:**
- Consumes: 第 1 期的 `pinnedSink`、`panicErr`、`Key`
- Produces:
  - `imchannel.Webhooker{ HandleWebhook(ctx context.Context, header http.Header, body []byte, sink ActionSink) int }`
  - `func (h *Hub) ServeWebhook(ctx context.Context, kind string, header http.Header, body []byte) int`（未登记 404；不能审批 / 没配置 / 开关关 / 不收 webhook / 没有 sink → 403；否则交给渠道，渠道 panic → 200）
  - `func middleware.IPInList(list, clientIP string) bool`（空名单 = 不限）
  - 路由 `POST /api/v1/im/:channel/webhook`（JWT 之外）
  - `imtest.Fake.WebhookSecret string`、`(*Fake).HandleWebhook`、`(*Fake).WebhookToasts() []string`；请求头 `X-Fake-Secret`，body `{"pid","apNo","approve"}`
  - 设置保存校验：`im.callbackBaseURL` → `service.ValidateCallbackBaseURL`；`im.callbackAllowIPs` → `service.ValidateIPAllowlist`；`im.telegram.apiBase` → `service.ValidateOutboundURL`；`im.telegram.mode` 只接受 `polling` / `webhook`

- [ ] **Step 1: 接口与假渠道**

`channel.go` 在 `BotNamer` 之后加（import 补 `net/http`）：

```go
// Webhooker 是可选能力:支持「平台把事件推过来」的渠道实现它。
//
// 它自己校验这次请求是不是真的来自平台(secret、签名):校验不过回 403;校验通过后,
// 不论处理成败都回 200 —— 平台把非 2xx 当成投递失败会一直重投,而重投不会让一次坏的
// 处理变好。它拿到的 sink 已由 Hub 钉住渠道名。
type Webhooker interface {
	HandleWebhook(ctx context.Context, header http.Header, body []byte, sink ActionSink) int
}
```

`imtest/fake.go`：`Fake` 结构体加字段

```go
	// WebhookSecret 非空时假渠道接受 webhook:请求头 X-Fake-Secret 必须与之相等。
	WebhookSecret string
	webhookToasts []string
```

并追加方法（import 补 `crypto/subtle`、`encoding/json`、`net/http`）：

```go
// HandleWebhook 模拟一个「平台推点击过来」的渠道。body 是 {"pid","apNo","approve"}。
// 它故意把 Channel 填成一个错的值 —— Hub 必须把它钉回来。
func (f *Fake) HandleWebhook(ctx context.Context, h http.Header, body []byte, sink imchannel.ActionSink) int {
	if f.WebhookSecret == "" || subtle.ConstantTimeCompare([]byte(h.Get("X-Fake-Secret")), []byte(f.WebhookSecret)) != 1 {
		return http.StatusForbidden
	}
	var in struct {
		PID     string `json:"pid"`
		ApNo    string `json:"apNo"`
		Approve bool   `json:"approve"`
	}
	if json.Unmarshal(body, &in) != nil {
		return http.StatusOK
	}
	res := sink.OnAction(ctx, imchannel.Action{Channel: "spoofed", PlatformUserID: in.PID, ApNo: in.ApNo, Approve: in.Approve})
	f.mu.Lock()
	f.webhookToasts = append(f.webhookToasts, res.Toast)
	f.mu.Unlock()
	return http.StatusOK
}

func (f *Fake) WebhookToasts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.webhookToasts...)
}
```

- [ ] **Step 2: 写失败的 Hub 测试 `imchannel/hub_webhook_test.go`**

```go
package imchannel_test

import (
	"context"
	"net/http"
	"testing"

	"velagateway/internal/imchannel"
	"velagateway/internal/imchannel/imtest"
)

type recordingSink struct{ got []imchannel.Action }

func (r *recordingSink) OnAction(_ context.Context, a imchannel.Action) imchannel.ActionResult {
	r.got = append(r.got, a)
	return imchannel.ActionResult{Toast: "ok"}
}
func (r *recordingSink) OnDirectMessage(context.Context, imchannel.DM) string { return "" }

func TestHub_ServeWebhook(t *testing.T) {
	s, st := newSettings(), newStore()
	a := imtest.New("a")
	a.WebhookSecret = "s3"
	h := newHub(t, s, st)
	h.Register("a", both, a.Builder())
	h.Register("n", imchannel.Caps{Notify: true}, a.Builder()) // 能收 webhook 但不能审批
	sink := &recordingSink{}
	h.SetSink(sink)
	h.Reload()
	hdr := http.Header{"X-Fake-Secret": []string{"s3"}}
	body := []byte(`{"pid":"p1","apNo":"AP-1","approve":true}`)
	ctx := context.Background()

	eq := func(got, want int, what string) {
		t.Helper()
		if got != want {
			t.Fatalf("%s: status %d, want %d", what, got, want)
		}
	}
	eq(h.ServeWebhook(ctx, "nope", hdr, body), http.StatusNotFound, "unknown channel")
	eq(h.ServeWebhook(ctx, "a", hdr, body), http.StatusForbidden, "approval switch off")
	s.set("im.a.approval", true)
	s.set("im.n.approval", true)
	eq(h.ServeWebhook(ctx, "n", hdr, body), http.StatusForbidden, "notify-only channel")
	eq(h.ServeWebhook(ctx, "a", http.Header{"X-Fake-Secret": []string{"bad"}}, body), http.StatusForbidden, "bad secret")
	eq(h.ServeWebhook(ctx, "a", hdr, body), http.StatusOK, "good request")

	if len(sink.got) != 1 || sink.got[0].Channel != "a" || sink.got[0].PlatformUserID != "p1" {
		t.Fatalf("sink got %+v — Channel must be pinned to the delivering kind", sink.got)
	}
}

type panicHook struct{ *imtest.Fake }

func (panicHook) HandleWebhook(context.Context, http.Header, []byte, imchannel.ActionSink) int {
	panic("parser bug")
}

func TestHub_APanickingWebhookIsContained(t *testing.T) {
	s, st := newSettings(), newStore()
	p := panicHook{imtest.New("p")}
	h := newHub(t, s, st)
	h.Register("p", both, func(imchannel.Deps) imchannel.Channel { return p })
	h.SetSink(&recordingSink{})
	s.set("im.p.approval", true)
	h.Reload()
	if got := h.ServeWebhook(context.Background(), "p", http.Header{}, nil); got != http.StatusOK {
		t.Fatalf("status %d — a panic must be contained and acknowledged, not retried forever", got)
	}
}
```

Run: `cd backend && go test ./internal/imchannel/ -run 'TestHub_ServeWebhook|TestHub_APanickingWebhook' -v`
Expected: 编译失败 `h.ServeWebhook undefined`。

- [ ] **Step 3: 实现 `Hub.ServeWebhook`**

`hub.go` import 补 `net/http`，追加：

```go
// ServeWebhook 把一次入站 webhook 交给对应的渠道。
//
// 这是一扇在 JWT 之外、能驱动审批的门,所以每一步都失败朝严:渠道没登记 → 404;
// 不能审批、没配置、交互审批没打开、不收 webhook、Hub 还没有 sink → 403。真伪由渠道
// 自己验(Webhooker)。渠道 panic 时回 200:一个解析不了的请求,重投一万次也一样。
func (h *Hub) ServeWebhook(ctx context.Context, kind string, header http.Header, body []byte) (status int) {
	h.mu.Lock()
	e := h.entries[kind]
	var wh Webhooker
	if e != nil && e.caps.Approve {
		wh, _ = e.ch.(Webhooker)
	}
	sink := h.sink
	h.mu.Unlock()
	if e == nil {
		return http.StatusNotFound
	}
	if wh == nil || sink == nil || !h.deps.Settings.Bool(Key(kind, "approval")) {
		return http.StatusForbidden
	}
	defer func() {
		if p := recover(); p != nil {
			_ = panicErr(kind, "webhook", p)
			status = http.StatusOK
		}
	}()
	return wh.HandleWebhook(ctx, header, body, pinnedSink{kind: kind, next: sink})
}
```

Run: `cd backend && go test -race ./internal/imchannel/... -v 2>&1 | tail -20`
Expected: PASS

- [ ] **Step 4: 写失败的端到端测试 `bootstrap/im_webhook_test.go`**

```go
package bootstrap

import (
	"bytes"
	"net/http"
	"testing"

	"velagateway/internal/imchannel/imtest"
)

func (a *testApp) postIMWebhook(channel string, header map[string]string, body string) int {
	a.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, a.srv.URL+"/api/v1/im/"+channel+"/webhook", bytes.NewReader([]byte(body)))
	for k, v := range header {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatalf("webhook: %v", err)
	}
	res.Body.Close()
	return res.StatusCode
}

func (a *testApp) webhookFake(token string) *imtest.Fake {
	a.t.Helper()
	f := a.fakeIM("fake")
	f.WebhookSecret = "s3"
	a.enableIM(token, "fake")
	return f
}

func TestIMWebhook_ApprovesThroughTheSameDecisionPath(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	f := app.webhookFake(admin)
	app.bindIM("zhangwei@vela.io", "fake", "p-zw")
	ap := app.submitProdHighRisk(admin)

	eq(t, app.postIMWebhook("fake", map[string]string{"X-Fake-Secret": "s3"},
		`{"pid":"p-zw","apNo":"`+ap.ApNo+`","approve":true}`), http.StatusOK, "accepted")
	eq(t, app.approvalRow(admin, ap.ApNo).Status, "approved", "decided via webhook")
	if toasts := f.WebhookToasts(); len(toasts) != 1 || toasts[0] != "已批准,请发起人回控制台执行" {
		t.Fatalf("toasts = %q", toasts)
	}
}

func TestIMWebhook_FailsClosed(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	app.fakeIM("fake").WebhookSecret = "s3"
	good := map[string]string{"X-Fake-Secret": "s3"}
	body := `{"pid":"x","apNo":"AP-1","approve":true}`

	eq(t, app.postIMWebhook("nope", good, body), http.StatusNotFound, "unknown channel")
	eq(t, app.postIMWebhook("fake", good, body), http.StatusForbidden, "approval switch off")
	app.enableIM(admin, "fake")
	eq(t, app.postIMWebhook("fake", map[string]string{"X-Fake-Secret": "no"}, body), http.StatusForbidden, "bad secret")
	eq(t, app.postIMWebhook("fake", nil, body), http.StatusForbidden, "no secret")

	app.setSettings(admin, map[string]any{"im.callbackAllowIPs": "10.0.0.0/8"})
	good["X-Forwarded-For"] = "203.0.113.9" // 夹具信任回环代理,这个头就是客户端地址
	eq(t, app.postIMWebhook("fake", good, body), http.StatusForbidden, "source IP not allowed")
	good["X-Forwarded-For"] = "10.1.2.3"
	eq(t, app.postIMWebhook("fake", good, body), http.StatusOK, "allowed source")
}

func TestIMSettings_CallbackKeysAreValidatedOnSave(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	for k, v := range map[string]any{
		"im.callbackBaseURL":   "://nope",
		"im.callbackAllowIPs":  "10.0.0.300",
		"im.telegram.apiBase":  "://nope",
		"im.telegram.mode":     "carrier-pigeon",
	} {
		if r := app.do(http.MethodPut, "/api/v1/settings", admin, map[string]any{k: v}); r.Code == 0 {
			t.Errorf("%s=%v must be rejected at save time", k, v)
		}
	}
	app.setSettings(admin, map[string]any{"im.telegram.mode": "webhook", "im.callbackAllowIPs": "10.0.0.0/8, 192.168.1.5"})
}
```

Run: `cd backend && go test ./internal/bootstrap/ -run 'TestIMWebhook_|TestIMSettings_CallbackKeys' -v`
Expected: FAIL（路由 404 / 设置没被拒）。

- [ ] **Step 5: 路由、handler、IP 判定、设置校验**

1. `middleware/apiclient.go` 在 `apiIPAllowed` 之后加：

```go
// IPInList 是同一把尺子的导出版:逗号 / 换行 / 空白分隔的 IP 与网段,空名单 = 不限。
// IM 的 webhook 入口用它做来源白名单 —— 与开放接口凭据的白名单是同一套写法。
func IPInList(list, clientIP string) bool { return apiIPAllowed(list, clientIP) }
```

2. `handler/im.go` import 补 `io`、`net/http`、`velagateway/internal/middleware`（若尚未导入），追加：

```go
// IMWebhook 是 IM 平台推事件的入口。它在 JWT 之外:认证靠可选的来源 IP 白名单
// (im.callbackAllowIPs)加各渠道自己的 secret / 签名。这是一扇能驱动审批的公开门,
// 所以每一步都失败朝严,具体规则见 imchannel.Hub.ServeWebhook。
func (h *Handler) IMWebhook(c *gin.Context) {
	if !middleware.IPInList(h.Repo.SettingString("im.callbackAllowIPs", ""), c.ClientIP()) {
		c.Status(http.StatusForbidden)
		return
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 1<<20))
	if err != nil {
		c.Status(http.StatusBadRequest)
		return
	}
	c.Status(h.Svc.IM.ServeWebhook(c.Request.Context(), c.Param("channel"), c.Request.Header, body))
}
```

3. `bootstrap/router.go`：在开放接口 `open := v1.Group(...)` 那一段之后加：

```go
	// IM 平台的 webhook(Telegram 等):不走用户 JWT —— 调用方是平台,不是控制台用户。
	// 认证在 handler 与渠道里,见 handler.IMWebhook。
	v1.POST("/im/:channel/webhook", h.IMWebhook)
```

4. `handler/admin.go` `SaveSettings` 的 URL 校验 `switch k` 改成：

```go
			switch k {
			case "security.ipAllowlist", "im.callbackAllowIPs":
				// 判定层遇到认不出的条目是**静默跳过**的,所以校验必须在这一刻 ——
				// 否则一个手滑多打一位的地址存进去,人以为那台机器被放行了,直到它
				// 被挡在门外。前端早就在标红,但校验不能只长在界面上。
				verr = service.ValidateIPAllowlist(sv)
			case "im.callbackBaseURL":
				verr = service.ValidateCallbackBaseURL(sv)
			case "im.telegram.apiBase":
				verr = service.ValidateOutboundURL(sv)
			case "im.telegram.mode":
				if sv != "polling" && sv != "webhook" {
					verr = fmt.Errorf("只能是 polling 或 webhook")
				}
			}
```

（`fmt` 若未导入则补上。）

5. `service/webhook.go` `ValidateCallbackBaseURL` 里 `"回调地址必须使用 https(ADR 0003)"` 改成 `"回调地址必须使用 https(ADR 0020)"`。

6. `docs/openapi.yaml` 在 `/open/...` 一组之后加：

```yaml
  /im/{channel}/webhook:
    post:
      tags: [settings]
      summary: IM 平台推送事件(不走用户 JWT;来源 IP 白名单 + 渠道 secret)
      security: []
      description: |
        Telegram 等平台把按钮点击与私聊推到这里。渠道未开交互审批、secret 不符、来源不在
        im.callbackAllowIPs(非空时)一律 403;鉴权通过后无论处理成败都回 200。
      parameters: [{ name: channel, in: path, required: true, schema: { type: string } }]
      responses: { '200': { description: OK }, '403': { description: 鉴权失败 }, '404': { description: 未知渠道 } }
```

（`tags` 照抄邻近条目的写法；若 `settings` 不合适，用邻居的。）

- [ ] **Step 6: 跑测试确认通过**

Run: `cd backend && go vet ./... && go test ./internal/bootstrap/ -run 'TestIMWebhook_|TestIMSettings_|TestOpenAPI|TestIM_' -v 2>&1 | tail -30`
Expected: PASS

- [ ] **Step 7: 提交**

```bash
git add backend
git commit -m "feat(im): add one webhook entry for every IM channel

Platforms push clicks to POST /api/v1/im/:channel/webhook, outside the
user JWT. The hub fails closed — unknown channel 404; channel off,
unconfigured or not taking webhooks 403 — the channel verifies its own
secret, and an optional source-IP allowlist sits in front. The sink the
channel sees is pinned to its kind, and a panicking parser is
acknowledged rather than retried forever.

Co-Authored-By: <model> <noreply@anthropic.com>"
```

---
### Task 3: Telegram 出站 —— API 客户端、卡片、收卡、假 Bot API 服务器

**Files:**
- Create: `backend/internal/imchannel/telegram/api.go`
- Create: `backend/internal/imchannel/telegram/card.go`
- Create: `backend/internal/imchannel/telegram/tgtest/server.go`
- Test: `backend/internal/imchannel/telegram/api_test.go`、`backend/internal/imchannel/telegram/card_test.go`

**Interfaces:**
- Consumes: `imchannel.Deps / Settings / ApprovalCard / CardState / Key / Telegram`
- Produces:
  - `telegram.New(d imchannel.Deps) imchannel.Channel`（`im.telegram.botToken` 为空返回 nil）
  - `*telegram.Channel`：`Kind()`、`BotName()`、`SendCard`、`CloseCard`（本任务）；`Run`、`HandleWebhook`（Task 4）
  - 常量 `telegram.WebhookPath = "/api/v1/im/telegram/webhook"`、`telegram.DoneData = "done"`
  - Bot API 类型：`User{ID int64; Username string}`、`Chat{ID int64; Type string}`、`Message{MessageID int64; From *User; Chat Chat; Text string}`、`CallbackQuery{ID string; From User; Message *Message; Data string}`、`Update{UpdateID int64; Message *Message; CallbackQuery *CallbackQuery}`（JSON 字段为 Telegram 原名）
  - 包内：`(*Channel).call(ctx, hc *http.Client, method string, params, out any) error`、`*apiError{Method string; Code int; Desc string}`、`renderCard(imchannel.ApprovalCard) string`、`keyboard(apNo string) map[string]any`、`stateLabel(imchannel.CardState) string`、`clipRunes(s string, n int) string`
  - `tgtest.New(t testing.TB) *tgtest.Server`：字段 `Token`、`BotUsername`、`URL`（来自内嵌 httptest.Server）、`Fail map[string]tgtest.Failure`（方法名 → 让它失败）；方法 `Push(u telegram.Update) int64`（UpdateID 为 0 时自动分配）、`PushText(fromID int64, chatType, text string)`、`PushCallback(fromID int64, data string) string`、`Calls(method string) []tgtest.Call`、`WaitCall(t, method string, match func(tgtest.Call) bool) tgtest.Call`、`SetFail(method string, f Failure)`；`Call{Method string; Params map[string]any}`、`Failure{Code int; Desc string}`

- [ ] **Step 1: 写 `telegram/api.go`**

```go
// Package telegram 是 Telegram 渠道:把审批卡片私信给已绑定的人,收回按钮点击与私聊。
//
// 只用标准库调 Bot API(https://core.telegram.org/bots/api)。它**不做任何判定** ——
// 点击交给 ActionSink,由 service 认人、走 DecideBlockFor。
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"velagateway/internal/imchannel"
)

const (
	defaultAPIBase = "https://api.telegram.org"
	// WebhookPath 是网关上接 Telegram 推送的路径,setWebhook 登记的就是 <回调根地址>+它。
	WebhookPath = "/api/v1/im/telegram/webhook"
	// DoneData 是收卡后那颗状态按钮的 callback_data:点它只回一句「已处理」。
	DoneData = "done"

	modePolling = "polling"
	modeWebhook = "webhook"

	// pollTimeout 是 getUpdates 的长轮询秒数。HTTP 客户端的超时要比它长,否则每一轮都以
	// 超时收场。
	pollTimeout = 25
)

// Bot API 的对象,只取用得到的字段。
type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username,omitempty"`
}

type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"` // private | group | supergroup | channel
}

type Message struct {
	MessageID int64  `json:"message_id"`
	From      *User  `json:"from,omitempty"`
	Chat      Chat   `json:"chat"`
	Text      string `json:"text,omitempty"`
}

type CallbackQuery struct {
	ID      string   `json:"id"`
	From    User     `json:"from"`
	Message *Message `json:"message,omitempty"`
	Data    string   `json:"data,omitempty"`
}

type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *Message       `json:"message,omitempty"`
	CallbackQuery *CallbackQuery `json:"callback_query,omitempty"`
}

// allowedUpdates 只订阅用得到的两类事件。
var allowedUpdates = []string{"message", "callback_query"}

type Channel struct {
	token    string
	secret   string // webhook 的 secret_token
	mode     string
	apiBase  string
	hookURL  string // webhook 模式下登记给 Telegram 的完整地址;没配回调根地址时为空
	client   *http.Client
	poll     *http.Client // 同一个受 SSRF 防护的 Transport,只是超时够 long polling 用
	validate func(string) error
	bot      atomic.Value // string:getMe 拿到的用户名,Run 里写
}

// New 在配置了 bot token 时造一个渠道,否则返回 nil(接口 nil)。不做任何 I/O ——
// 它在 Hub 的锁里被调用。
func New(d imchannel.Deps) imchannel.Channel {
	key := func(n string) string { return imchannel.Key(imchannel.Telegram, n) }
	token := strings.TrimSpace(d.Settings.Secret(key("botToken")))
	if token == "" {
		return nil
	}
	base := strings.TrimRight(strings.TrimSpace(d.Settings.String(key("apiBase"))), "/")
	if base == "" {
		base = defaultAPIBase
	}
	mode := d.Settings.String(key("mode"))
	if mode != modeWebhook {
		mode = modePolling
	}
	hook := ""
	if cb := strings.TrimRight(strings.TrimSpace(d.Settings.String("im.callbackBaseURL")), "/"); cb != "" {
		hook = cb + WebhookPath
	}
	// 复制一份客户端只改超时:Transport(拨号时的内网拦截)仍是同一份。
	poll := *d.HTTP
	poll.Timeout = (pollTimeout + 15) * time.Second
	c := &Channel{
		token: token, secret: d.Settings.Secret(key("webhookSecret")), mode: mode,
		apiBase: base, hookURL: hook, client: d.HTTP, poll: &poll, validate: d.ValidateURL,
	}
	c.bot.Store("")
	return c
}

func (c *Channel) Kind() string { return imchannel.Telegram }

// BotName 返回 Run 里 getMe 拿到并缓存的用户名。Hub 锁内调用,所以不做 I/O。
func (c *Channel) BotName() string { return c.bot.Load().(string) }

type apiResp struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	Description string          `json:"description"`
	ErrorCode   int             `json:"error_code"`
}

// apiError 是 Telegram 明确回了 ok:false。
type apiError struct {
	Method string
	Code   int
	Desc   string
}

func (e *apiError) Error() string { return fmt.Sprintf("telegram %s: %d %s", e.Method, e.Code, e.Desc) }

func isUnauthorized(err error) bool {
	var ae *apiError
	return errors.As(err, &ae) && ae.Code == http.StatusUnauthorized
}

// call 调一个 Bot API 方法。
//
// bot token 就在请求 URL 里(/bot<token>/method),而 Go 的 *url.Error 会把整个 URL 写进
// 错误文本 —— 一次网络抖动就能把 token 带进日志和设置页的状态。所以从这里出去的每一个
// 错误都先把 token 换掉。
func (c *Channel) call(ctx context.Context, hc *http.Client, method string, params, out any) error {
	if err := c.validate(c.apiBase); err != nil {
		return c.scrub(err)
	}
	body, err := json.Marshal(params)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiBase+"/bot"+c.token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return c.scrub(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return c.scrub(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var r apiResp
	if err := json.Unmarshal(raw, &r); err != nil {
		return fmt.Errorf("telegram %s: HTTP %d,响应无法解析", method, resp.StatusCode)
	}
	if !r.OK {
		return &apiError{Method: method, Code: r.ErrorCode, Desc: c.scrub(errors.New(r.Description)).Error()}
	}
	if out != nil {
		if err := json.Unmarshal(r.Result, out); err != nil {
			return fmt.Errorf("telegram %s: 结果无法解析: %v", method, err)
		}
	}
	return nil
}

func (c *Channel) scrub(err error) error {
	if err == nil || c.token == "" {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), c.token, "<bot-token>"))
}

// clipRunes 截到 n 个字符(不是字节 —— 中文一刀切在半个字上,Telegram 会拒收)。
func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
```

- [ ] **Step 2: 写 `telegram/card.go`**

```go
package telegram

import (
	"context"
	"errors"
	"fmt"
	"html"
	"strconv"
	"strings"

	"velagateway/internal/imchannel"
	"velagateway/internal/model"
)

// commandLimit:Telegram 消息正文上限是「解析实体后」4096 个字符。命令在通用层已截到 800,
// 这里再兜一次底,给标题、原因和链接留出余量。
const commandLimit = 3000

var kindTitle = map[string]string{
	"sql": "高危命令待审批", "window": "执行窗口申请待审批",
	"export": "敏感字段导出待审批", "release": "发布单审批",
}

// renderCard 把工单渲染成 HTML 消息。**每一段外部文本都先转义**:SQL 里出现 < 和 & 是
// 常态(WHERE a < b),不转义会让 Telegram 拒收整条消息 —— 那是一条「卡片发不出去」的
// 静默失败。命令放进 <pre>,等宽、可复制。
func renderCard(c imchannel.ApprovalCard) string {
	esc := html.EscapeString
	risk := "🟠 中危"
	if c.Risk == model.RiskHigh {
		risk = "🔴 高危"
	}
	title := kindTitle[c.Kind]
	if title == "" {
		title = kindTitle["sql"]
	}
	env := strings.ToUpper(c.Env)
	if c.Tier != "" && !strings.EqualFold(c.Tier, c.Env) {
		env += "(" + c.Tier + ")"
	}
	db := c.Database
	if strings.TrimSpace(db) == "" {
		db = "默认"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<b>%s · %s · %s</b>\n", risk, esc(title), esc(c.ApNo))
	fmt.Fprintf(&b, "环境 %s · 实例 %s / %s\n", esc(env), esc(c.Instance), esc(db))
	fmt.Fprintf(&b, "发起人 %s\n", esc(c.Initiator))
	if strings.TrimSpace(c.Reason) != "" {
		fmt.Fprintf(&b, "原因 %s\n", esc(c.Reason))
	}
	fmt.Fprintf(&b, "<pre>%s</pre>", esc(clipRunes(c.Command, commandLimit)))
	if c.ConsoleURL != "" {
		fmt.Fprintf(&b, "\n<a href=\"%s\">前往控制台</a>", esc(c.ConsoleURL))
	}
	return b.String()
}

// keyboard 是卡片上的两个按钮。callback_data 只说「哪张单、什么动作」,不带任何权限信息
// —— 权限完全由收到点击后的认人与 DecideBlockFor 决定。
func keyboard(apNo string) map[string]any {
	return map[string]any{"inline_keyboard": [][]map[string]string{{
		{"text": "✅ 通过", "callback_data": "ap:" + apNo + ":a"},
		{"text": "❌ 驳回", "callback_data": "ap:" + apNo + ":r"},
	}}}
}

// stateLabel 是收卡后那颗按钮上的字。
func stateLabel(s imchannel.CardState) string {
	icon := "✔"
	switch s.Status {
	case model.StatusApproved:
		icon = "✅"
	case model.StatusRejected:
		icon = "❌"
	case model.StatusExpired:
		icon = "⏱"
	case model.StatusCancelled:
		icon = "↩"
	}
	return clipRunes(icon+" "+s.Text, 60)
}

// SendCard 私信一张卡片。私聊里 chat_id 就是对方的用户 id —— 绑定时对方已经私聊过机器人,
// 所以机器人能主动给他发消息。
func (c *Channel) SendCard(ctx context.Context, pid string, card imchannel.ApprovalCard) (string, error) {
	var m Message
	err := c.call(ctx, c.client, "sendMessage", map[string]any{
		"chat_id": pid, "text": renderCard(card), "parse_mode": "HTML",
		"disable_web_page_preview": true, "reply_markup": keyboard(card.ApNo),
	}, &m)
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(m.Chat.ID, 10) + ":" + strconv.FormatInt(m.MessageID, 10), nil
}

// CloseCard 把两个按钮换成一颗显示终态的按钮。
//
// 不用 editMessageText:那要重写整条正文,而网关没存卡片原文 —— 保留原文、只换按钮,
// 收起之后审批人仍看得到这张单原来是什么。
func (c *Channel) CloseCard(ctx context.Context, ref string, final imchannel.CardState) error {
	chat, msg, ok := strings.Cut(ref, ":")
	if !ok || chat == "" || msg == "" {
		return fmt.Errorf("telegram: 无法识别的消息引用 %q", ref)
	}
	msgID, err := strconv.ParseInt(msg, 10, 64)
	if err != nil {
		return fmt.Errorf("telegram: 无法识别的消息引用 %q", ref)
	}
	err = c.call(ctx, c.client, "editMessageReplyMarkup", map[string]any{
		"chat_id": chat, "message_id": msgID,
		"reply_markup": map[string]any{"inline_keyboard": [][]map[string]string{{
			{"text": stateLabel(final), "callback_data": DoneData},
		}}},
	}, nil)
	// 已经是这个样子,或者对方把消息删了:这张卡片都不会再有能点的按钮了,算收起。
	var ae *apiError
	if errors.As(err, &ae) && (strings.Contains(ae.Desc, "message is not modified") ||
		strings.Contains(ae.Desc, "message to edit not found")) {
		return nil
	}
	return err
}
```

- [ ] **Step 3: 写假 Bot API 服务器 `telegram/tgtest/server.go`**

```go
// Package tgtest 是一个假的 Telegram Bot API 服务器:记录每次调用,按需返回 update,
// 让适配器和端到端测试都不必连真实的 Telegram。
package tgtest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"velagateway/internal/imchannel/telegram"
)

type Call struct {
	Method string
	Params map[string]any
}

// Failure 让某个方法回 ok:false。
type Failure struct {
	Code int
	Desc string
}

type Server struct {
	*httptest.Server
	Token       string
	BotUsername string

	mu      sync.Mutex
	calls   []Call
	updates []telegram.Update
	nextID  int64
	nextMsg int64
	fail    map[string]Failure
	wake    chan struct{}
}

func New(t testing.TB) *Server {
	s := &Server{
		Token: "123456:TEST-bot-token-abcdef", BotUsername: "aegis_test_bot",
		nextMsg: 100, fail: map[string]Failure{}, wake: make(chan struct{}, 1),
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

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	prefix := "/bot" + s.Token + "/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		writeJSON(w, map[string]any{"ok": false, "error_code": 401, "description": "Unauthorized"})
		return
	}
	method := strings.TrimPrefix(r.URL.Path, prefix)
	var params map[string]any
	_ = json.NewDecoder(r.Body).Decode(&params)
	s.mu.Lock()
	s.calls = append(s.calls, Call{Method: method, Params: params})
	f, failing := s.fail[method]
	s.mu.Unlock()
	if failing {
		writeJSON(w, map[string]any{"ok": false, "error_code": f.Code, "description": f.Desc})
		return
	}
	switch method {
	case "getMe":
		writeJSON(w, map[string]any{"ok": true, "result": map[string]any{"id": 42, "is_bot": true, "username": s.BotUsername}})
	case "getUpdates":
		writeJSON(w, map[string]any{"ok": true, "result": s.take(r.Context(), int64(num(params["offset"])))})
	case "sendMessage":
		s.mu.Lock()
		s.nextMsg++
		id := s.nextMsg
		s.mu.Unlock()
		writeJSON(w, map[string]any{"ok": true, "result": map[string]any{
			"message_id": id, "chat": map[string]any{"id": int64(num(params["chat_id"])), "type": "private"},
		}})
	default:
		writeJSON(w, map[string]any{"ok": true, "result": true})
	}
}

// take 返回 update_id >= offset 的 update。没有就最多等 300ms —— 真实的长轮询会挂 25 秒,
// 测试里不必。offset 之前的 update 永远不再返回,这正是 Telegram「确认」的语义。
func (s *Server) take(ctx context.Context, offset int64) []telegram.Update {
	deadline := time.After(300 * time.Millisecond)
	for {
		s.mu.Lock()
		out := []telegram.Update{}
		for _, u := range s.updates {
			if u.UpdateID >= offset {
				out = append(out, u)
			}
		}
		s.mu.Unlock()
		if len(out) > 0 {
			return out
		}
		select {
		case <-s.wake:
		case <-deadline:
			return out
		case <-ctx.Done():
			return out
		}
	}
}

// Push 排进一条 update。UpdateID 为 0 时自动分配;给定的话可以用来模拟重投。
func (s *Server) Push(u telegram.Update) int64 {
	s.mu.Lock()
	if u.UpdateID == 0 {
		s.nextID++
		u.UpdateID = s.nextID
	} else if u.UpdateID > s.nextID {
		s.nextID = u.UpdateID
	}
	s.updates = append(s.updates, u)
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
	return u.UpdateID
}

func (s *Server) PushText(fromID int64, chatType, text string) {
	chatID := fromID
	if chatType != "private" {
		chatID = -1000 - fromID
	}
	s.Push(telegram.Update{Message: &telegram.Message{
		MessageID: 1, From: &telegram.User{ID: fromID}, Chat: telegram.Chat{ID: chatID, Type: chatType}, Text: text,
	}})
}

// PushCallback 模拟一次按钮点击,返回 callback_query 的 id。
func (s *Server) PushCallback(fromID int64, data string) string {
	s.mu.Lock()
	id := fmt.Sprintf("cq-%d", s.nextID+1)
	s.mu.Unlock()
	s.Push(telegram.Update{CallbackQuery: &telegram.CallbackQuery{ID: id, From: telegram.User{ID: fromID}, Data: data}})
	return id
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
	t.Fatalf("tgtest: no %s call matched within 5s; got %+v", method, s.Calls(method))
	return Call{}
}

// Str 取一个参数的字符串形式(chat_id 可能是数字也可能是字符串)。
func (c Call) Str(key string) string {
	switch v := c.Params[key].(type) {
	case string:
		return v
	case float64:
		return strconv.FormatInt(int64(v), 10)
	case nil:
		return ""
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

func num(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case string:
		f, _ := strconv.ParseFloat(x, 64)
		return f
	}
	return 0
}
```

- [ ] **Step 4: 写测试 `telegram/card_test.go` 与 `telegram/api_test.go`**

`card_test.go`（包内测试，`package telegram`）：

```go
package telegram

import (
	"html"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"velagateway/internal/imchannel"
)

var tagRe = regexp.MustCompile(`<[^>]+>`)

// visible 近似 Telegram「解析实体后」的正文:去掉标签,再反转义。上限按它算。
func visible(s string) string { return html.UnescapeString(tagRe.ReplaceAllString(s, "")) }

func TestRenderCard_EscapesHTMLAndStaysUnderTheLimit(t *testing.T) {
	cmd := "SELECT * FROM t WHERE a < b && c > 1; -- </pre><b>fake</b>"
	out := renderCard(imchannel.ApprovalCard{
		ApNo: "AP-7", Kind: "sql", Env: "prod-hk", Tier: "prod", Instance: "order<1>", Risk: "high",
		Initiator: "张三 & 李四", Reason: "<script>", Command: cmd, ConsoleURL: "https://gw/approvals?x=1&y=2",
	})
	for _, bad := range []string{"<script>", "</pre><b>fake", "a < b", "order<1>"} {
		if strings.Contains(out, bad) {
			t.Errorf("unescaped %q leaked into the HTML: %s", bad, out)
		}
	}
	if !strings.Contains(visible(out), cmd) {
		t.Errorf("the command must survive round-trip as text: %s", visible(out))
	}
	if strings.Count(out, "<pre>") != 1 || strings.Count(out, "</pre>") != 1 {
		t.Errorf("exactly one code block expected: %s", out)
	}

	long := renderCard(imchannel.ApprovalCard{ApNo: "AP-8", Command: strings.Repeat("<", 20000), Reason: strings.Repeat("原", 200)})
	if n := utf8.RuneCountInString(visible(long)); n > 4096 {
		t.Fatalf("visible text is %d chars; Telegram's limit is 4096", n)
	}
}

func TestKeyboardAndStateLabel(t *testing.T) {
	kb := keyboard("AP-2301")["inline_keyboard"].([][]map[string]string)
	if kb[0][0]["callback_data"] != "ap:AP-2301:a" || kb[0][1]["callback_data"] != "ap:AP-2301:r" {
		t.Fatalf("keyboard = %+v", kb)
	}
	if len(kb[0][0]["callback_data"]) > 64 {
		t.Fatal("callback_data is capped at 64 bytes")
	}
	if got := stateLabel(imchannel.CardState{Status: "approved", Text: "已由 李四(Telegram) 通过"}); !strings.HasPrefix(got, "✅") {
		t.Fatalf("label = %q", got)
	}
}
```

`api_test.go`（外部测试包，`package telegram_test`）：

```go
package telegram_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"velagateway/internal/imchannel"
	"velagateway/internal/imchannel/telegram"
	"velagateway/internal/imchannel/telegram/tgtest"
)

type kv map[string]string

func (m kv) String(k string) string { return m[k] }
func (m kv) Bool(k string) bool     { return m[k] == "true" }
func (m kv) Secret(k string) string { return m[k] }

func deps(s kv) imchannel.Deps {
	return imchannel.Deps{Settings: s, HTTP: &http.Client{Timeout: 5 * time.Second}, ValidateURL: func(string) error { return nil }}
}

func newChannel(t *testing.T, srv *tgtest.Server, extra kv) *telegram.Channel {
	t.Helper()
	s := kv{"im.telegram.botToken": srv.Token, "im.telegram.apiBase": srv.URL}
	for k, v := range extra {
		s[k] = v
	}
	ch, _ := telegram.New(deps(s)).(*telegram.Channel)
	if ch == nil {
		t.Fatal("configured channel must build")
	}
	return ch
}

func TestNew_UnconfiguredIsNil(t *testing.T) {
	if ch := telegram.New(deps(kv{})); ch != nil {
		t.Fatalf("no token → nil, got %#v", ch)
	}
}

func TestSendCard_PostsHTMLWithTwoButtons(t *testing.T) {
	srv := tgtest.New(t)
	ch := newChannel(t, srv, nil)
	ref, err := ch.SendCard(context.Background(), "777", imchannel.ApprovalCard{ApNo: "AP-1", Command: "DROP TABLE t;"})
	if err != nil {
		t.Fatal(err)
	}
	if ref != "777:101" {
		t.Fatalf("ref = %q, want chat:message", ref)
	}
	c := srv.Calls("sendMessage")[0]
	if c.Str("chat_id") != "777" || c.Str("parse_mode") != "HTML" || !strings.Contains(c.Str("reply_markup"), "ap:AP-1:a") {
		t.Fatalf("sendMessage params = %+v", c.Params)
	}
}

func TestCloseCard_ReplacesTheButtonsAndToleratesGoneMessages(t *testing.T) {
	srv := tgtest.New(t)
	ch := newChannel(t, srv, nil)
	final := imchannel.CardState{Status: "approved", Text: "已由 李四(Telegram) 通过"}
	if err := ch.CloseCard(context.Background(), "777:101", final); err != nil {
		t.Fatal(err)
	}
	c := srv.Calls("editMessageReplyMarkup")[0]
	if c.Str("chat_id") != "777" || c.Str("message_id") != "101" || !strings.Contains(c.Str("reply_markup"), "已由 李四") ||
		!strings.Contains(c.Str("reply_markup"), `"callback_data":"done"`) {
		t.Fatalf("edit params = %+v", c.Params)
	}
	srv.SetFail("editMessageReplyMarkup", tgtest.Failure{Code: 400, Desc: "Bad Request: message to edit not found"})
	if err := ch.CloseCard(context.Background(), "777:102", final); err != nil {
		t.Fatalf("a message the user deleted counts as closed, got %v", err)
	}
	if err := ch.CloseCard(context.Background(), "garbage", final); err == nil {
		t.Fatal("an unparsable ref is an error")
	}
}

func TestCall_ErrorsNeverContainTheToken(t *testing.T) {
	srv := tgtest.New(t)
	ch := newChannel(t, srv, nil)

	srv.SetFail("sendMessage", tgtest.Failure{Code: 400, Desc: "Bad Request: chat not found"})
	_, err := ch.SendCard(context.Background(), "1", imchannel.ApprovalCard{ApNo: "AP-1"})
	if err == nil || strings.Contains(err.Error(), srv.Token) || !strings.Contains(err.Error(), "chat not found") {
		t.Fatalf("api error = %v", err)
	}

	srv.Close() // 网络错误:*url.Error 会带上整个 URL
	_, err = ch.SendCard(context.Background(), "1", imchannel.ApprovalCard{ApNo: "AP-1"})
	if err == nil || strings.Contains(err.Error(), srv.Token) {
		t.Fatalf("network error leaked the token: %v", err)
	}
	if !strings.Contains(err.Error(), "<bot-token>") {
		t.Fatalf("expected the token to be replaced, got %v", err)
	}
}
```

- [ ] **Step 5: 跑测试**

Run: `cd backend && gofmt -l ./internal/imchannel/telegram && go vet ./internal/imchannel/... && go test -race ./internal/imchannel/telegram/... -v 2>&1 | tail -30`
Expected: PASS。（先写测试再写实现也可以：测试文件先建，`go test` 会在编译期失败，符合 RED。报告里记录 RED 的输出。）

- [ ] **Step 6: 提交**

```bash
git add backend/internal/imchannel/telegram
git commit -m "feat(im): add the Telegram outbound side and a fake Bot API server

Cards go out as HTML with every outside string escaped and the command
kept under Telegram's parsed-length limit; closing a card swaps the two
buttons for one showing the outcome, keeping the original text. The bot
token lives in the request URL, so every error leaving the adapter has
it replaced before it can reach a log or the status page.

Co-Authored-By: <model> <noreply@anthropic.com>"
```

---
### Task 4: Telegram 入站 —— long polling、webhook 登记、事件分发

**Files:**
- Create: `backend/internal/imchannel/telegram/inbound.go`
- Test: `backend/internal/imchannel/telegram/inbound_test.go`

**Interfaces:**
- Consumes: Task 3 的 `Channel`、`call`、`apiError`、`isUnauthorized`、`clipRunes`、`Update/Message/CallbackQuery`、`DoneData`、`WebhookPath`、`allowedUpdates`、`pollTimeout`、`tgtest`；Task 2 的 `imchannel.Webhooker`
- Produces:
  - `(*Channel).Run(ctx, sink) error`：先 `getMe`（成功后缓存用户名；401 立即返回错误；其它错误退避重试）。webhook 模式：缺回调根地址或 secret → 返回配置错误；否则 `setWebhook(url, secret_token, allowed_updates)` 后阻塞到 ctx 结束。polling 模式：先 `deleteWebhook`，再循环 `getUpdates`（offset 推进；401 返回错误；其它错误 1s→30s 指数退避）
  - `(*Channel).HandleWebhook(ctx, header, body, sink) int`（实现 `imchannel.Webhooker`）
  - 包内 `(*Channel).dispatch(ctx, Update, sink)`：按钮 → `sink.OnAction` → `answerCallbackQuery`（必须应答）；私聊文本 → `/start` 回说明，其它交给 `sink.OnDirectMessage` 并把回复发回；群消息一律不理
  - 常量 `startReply`（`/start` 的回复）

- [ ] **Step 1: 写失败的测试 `telegram/inbound_test.go`**

```go
package telegram_test

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"velagateway/internal/imchannel"
	"velagateway/internal/imchannel/telegram"
	"velagateway/internal/imchannel/telegram/tgtest"
)

type sink struct {
	mu      sync.Mutex
	actions []imchannel.Action
	dms     []imchannel.DM
	toast   string
}

func (s *sink) OnAction(_ context.Context, a imchannel.Action) imchannel.ActionResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.actions = append(s.actions, a)
	return imchannel.ActionResult{Toast: s.toast}
}

func (s *sink) OnDirectMessage(_ context.Context, dm imchannel.DM) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dms = append(s.dms, dm)
	return "reply:" + dm.Text
}

func (s *sink) snapshot() ([]imchannel.Action, []imchannel.DM) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]imchannel.Action(nil), s.actions...), append([]imchannel.DM(nil), s.dms...)
}

// runPolling 起一个 polling 循环,测试结束时停掉并等它退出。
func runPolling(t *testing.T, ch *telegram.Channel, sk *sink) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- ch.Run(ctx, sk) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Run did not return after cancel")
		}
	})
}

func TestPolling_ClicksReachTheSinkAndAreAnswered(t *testing.T) {
	srv := tgtest.New(t)
	ch := newChannel(t, srv, nil)
	sk := &sink{toast: strings.Repeat("长", 300)}
	runPolling(t, ch, sk)

	srv.WaitCall(t, "deleteWebhook", nil) // polling 先确保没有 webhook 占着
	id := srv.PushCallback(777, "ap:AP-9:a")
	c := srv.WaitCall(t, "answerCallbackQuery", func(c tgtest.Call) bool { return c.Str("callback_query_id") == id })
	if n := len([]rune(c.Str("text"))); n > 200 {
		t.Fatalf("toast is %d chars; Telegram caps it at 200", n)
	}
	acts, _ := sk.snapshot()
	if len(acts) != 1 || acts[0].PlatformUserID != "777" || acts[0].ApNo != "AP-9" || !acts[0].Approve {
		t.Fatalf("actions = %+v", acts)
	}
	if ch.BotName() != srv.BotUsername {
		t.Fatalf("BotName = %q after getMe", ch.BotName())
	}
}

func TestPolling_AnUpdateIsHandledOnce(t *testing.T) {
	srv := tgtest.New(t)
	ch := newChannel(t, srv, nil)
	sk := &sink{toast: "ok"}
	runPolling(t, ch, sk)

	id := srv.Push(telegram.Update{CallbackQuery: &telegram.CallbackQuery{ID: "q1", From: telegram.User{ID: 5}, Data: "ap:AP-1:r"}})
	srv.WaitCall(t, "answerCallbackQuery", nil)
	// 同一个 update_id 再来一次 —— 进程重启前没来得及确认的 update,Telegram 会重投。
	srv.Push(telegram.Update{UpdateID: id, CallbackQuery: &telegram.CallbackQuery{ID: "q1", From: telegram.User{ID: 5}, Data: "ap:AP-1:r"}})
	srv.PushCallback(5, "ap:AP-2:r") // 一条新的,用来确认循环还在往前走
	srv.WaitCall(t, "answerCallbackQuery", func(c tgtest.Call) bool { return c.Str("callback_query_id") != "q1" })
	acts, _ := sk.snapshot()
	if len(acts) != 2 {
		t.Fatalf("actions = %+v — the redelivered update must not be handled again", acts)
	}
}

func TestDispatch_DirectMessagesStartAndGroups(t *testing.T) {
	srv := tgtest.New(t)
	ch := newChannel(t, srv, nil)
	sk := &sink{}
	runPolling(t, ch, sk)

	srv.PushText(777, "private", "/start")
	srv.WaitCall(t, "sendMessage", func(c tgtest.Call) bool {
		return c.Str("chat_id") == "777" && strings.Contains(c.Str("text"), "IM 账号绑定")
	})
	srv.PushText(777, "private", "BIND-ABCDEFGH")
	srv.WaitCall(t, "sendMessage", func(c tgtest.Call) bool { return c.Str("text") == "reply:BIND-ABCDEFGH" })
	_, dms := sk.snapshot()
	if len(dms) != 1 || dms[0].PlatformUserID != "777" {
		t.Fatalf("dms = %+v — /start is answered locally, the code goes to the sink", dms)
	}
}

func TestDispatch_GroupTextIsIgnored(t *testing.T) {
	srv := tgtest.New(t)
	ch := newChannel(t, srv, nil)
	sk := &sink{}
	runPolling(t, ch, sk)

	srv.PushText(888, "group", "BIND-ABCDEFGH")
	srv.PushText(889, "private", "marker") // 一条私聊,等它被处理就说明前面那条也已经过去了
	srv.WaitCall(t, "sendMessage", func(c tgtest.Call) bool { return c.Str("text") == "reply:marker" })
	_, dms := sk.snapshot()
	if len(dms) != 1 {
		t.Fatalf("dms = %+v — group messages must never reach binding", dms)
	}
	for _, c := range srv.Calls("sendMessage") {
		if strings.HasPrefix(c.Str("chat_id"), "-") {
			t.Fatalf("replied into a group: %+v", c.Params)
		}
	}
}

func TestDispatch_DoneAndGarbageCallbacksAreAnsweredWithoutTheSink(t *testing.T) {
	srv := tgtest.New(t)
	ch := newChannel(t, srv, nil)
	sk := &sink{}
	runPolling(t, ch, sk)

	done := srv.PushCallback(1, telegram.DoneData)
	junk := srv.PushCallback(1, "ap:../../x:a")
	srv.WaitCall(t, "answerCallbackQuery", func(c tgtest.Call) bool { return c.Str("callback_query_id") == done })
	srv.WaitCall(t, "answerCallbackQuery", func(c tgtest.Call) bool { return c.Str("callback_query_id") == junk })
	if acts, _ := sk.snapshot(); len(acts) != 0 {
		t.Fatalf("actions = %+v", acts)
	}
}

func TestRun_BadTokenGivesUpWithoutLeakingIt(t *testing.T) {
	srv := tgtest.New(t)
	srv.SetFail("getMe", tgtest.Failure{Code: 401, Desc: "Unauthorized"})
	ch := newChannel(t, srv, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := ch.Run(ctx, &sink{})
	if err == nil || !strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), srv.Token) {
		t.Fatalf("Run err = %v — a rejected token is final, and must not appear in the error", err)
	}
}

func TestRun_WebhookModeRegistersAndNeedsItsConfig(t *testing.T) {
	srv := tgtest.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	bare := newChannel(t, srv, map[string]string{"im.telegram.mode": "webhook"})
	if err := bare.Run(ctx, &sink{}); err == nil || !strings.Contains(err.Error(), "webhook") {
		t.Fatalf("webhook mode without base URL / secret must fail loudly, got %v", err)
	}

	ch := newChannel(t, srv, map[string]string{
		"im.telegram.mode": "webhook", "im.telegram.webhookSecret": "s3",
		"im.callbackBaseURL": "https://gw.example/",
	})
	runCtx, stop := context.WithCancel(context.Background())
	go func() { _ = ch.Run(runCtx, &sink{}) }()
	defer stop()
	c := srv.WaitCall(t, "setWebhook", nil)
	if c.Str("url") != "https://gw.example"+telegram.WebhookPath || c.Str("secret_token") != "s3" {
		t.Fatalf("setWebhook params = %+v", c.Params)
	}
	if len(srv.Calls("getUpdates")) != 0 {
		t.Fatal("webhook mode must not poll")
	}
}

func TestHandleWebhook_AuthenticatesThenDispatches(t *testing.T) {
	srv := tgtest.New(t)
	sk := &sink{toast: "ok"}
	body := []byte(`{"update_id":1,"callback_query":{"id":"q","from":{"id":777},"data":"ap:AP-3:a"}}`)
	hdr := func(v string) http.Header { return http.Header{"X-Telegram-Bot-Api-Secret-Token": []string{v}} }
	ctx := context.Background()

	polling := newChannel(t, srv, map[string]string{"im.telegram.webhookSecret": "s3"})
	if got := polling.HandleWebhook(ctx, hdr("s3"), body, sk); got != http.StatusForbidden {
		t.Fatalf("polling mode must refuse webhooks, got %d", got)
	}
	noSecret := newChannel(t, srv, map[string]string{"im.telegram.mode": "webhook"})
	if got := noSecret.HandleWebhook(ctx, hdr(""), body, sk); got != http.StatusForbidden {
		t.Fatalf("no configured secret must refuse everything, got %d", got)
	}
	ch := newChannel(t, srv, map[string]string{"im.telegram.mode": "webhook", "im.telegram.webhookSecret": "s3"})
	if got := ch.HandleWebhook(ctx, hdr("nope"), body, sk); got != http.StatusForbidden {
		t.Fatalf("wrong secret, got %d", got)
	}
	if got := ch.HandleWebhook(ctx, hdr("s3"), []byte("{not json"), sk); got != http.StatusOK {
		t.Fatalf("an authenticated but unparsable body is acknowledged, got %d", got)
	}
	if got := ch.HandleWebhook(ctx, hdr("s3"), body, sk); got != http.StatusOK {
		t.Fatalf("good request, got %d", got)
	}
	if acts, _ := sk.snapshot(); len(acts) != 1 || acts[0].ApNo != "AP-3" {
		t.Fatalf("actions = %+v", acts)
	}
	srv.WaitCall(t, "answerCallbackQuery", nil)
}
```

Run: `cd backend && go test ./internal/imchannel/telegram/ -run 'TestPolling|TestDispatch|TestRun_|TestHandleWebhook' -v`
Expected: 编译失败 `ch.Run undefined` / `ch.HandleWebhook undefined`。

- [ ] **Step 2: 写 `telegram/inbound.go`**

```go
package telegram

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"velagateway/internal/imchannel"
)

// callbackRe 是卡片按钮的 callback_data。ApNo 只认字母数字和连字符 —— 按钮回调可以被
// 伪造,别让任意字符串流进后面的查询和提示语。
var callbackRe = regexp.MustCompile(`^ap:([A-Za-z0-9-]{1,32}):([ar])$`)

const startReply = "你好,我是数据库网关的审批机器人。\n" +
	"请在网关控制台右上角头像菜单「IM 账号绑定」里生成绑定码,再把绑定码发给我。\n" +
	"绑定之后,待你审批的工单会以卡片的形式私信给你。"

// Run 阻塞到 ctx 取消。它只在两种情况下提前返回错误:token 被拒(换 token 之前重试
// 多少次都一样),或 webhook 模式缺配置。其它错误一律退避重试。
func (c *Channel) Run(ctx context.Context, sink imchannel.ActionSink) error {
	var me User
	if err := c.retry(ctx, func() error { return c.call(ctx, c.client, "getMe", map[string]any{}, &me) }); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return nil
	}
	c.bot.Store(me.Username)
	if c.mode == modeWebhook {
		return c.runWebhook(ctx)
	}
	return c.runPolling(ctx, sink)
}

func (c *Channel) runWebhook(ctx context.Context) error {
	if c.hookURL == "" || c.secret == "" {
		return errors.New("webhook 模式需要配置回调根地址(im.callbackBaseURL)与 webhook secret")
	}
	err := c.retry(ctx, func() error {
		return c.call(ctx, c.client, "setWebhook", map[string]any{
			"url": c.hookURL, "secret_token": c.secret, "allowed_updates": allowedUpdates,
		}, nil)
	})
	if err != nil {
		return err
	}
	slog.Info("telegram: webhook registered", "url", c.hookURL)
	<-ctx.Done()
	return nil
}

func (c *Channel) runPolling(ctx context.Context, sink imchannel.ActionSink) error {
	// 切回 polling 时 Telegram 上可能还登记着上一次的 webhook,不删掉它 getUpdates 会一直 409。
	err := c.retry(ctx, func() error {
		return c.call(ctx, c.client, "deleteWebhook", map[string]any{"drop_pending_updates": false}, nil)
	})
	if err != nil || ctx.Err() != nil {
		return err
	}
	slog.Info("telegram: long polling started — only ONE gateway instance may poll a bot; use webhook mode when running several")
	var offset int64
	backoff := time.Second
	for ctx.Err() == nil {
		var ups []Update
		err := c.call(ctx, c.poll, "getUpdates", map[string]any{
			"offset": offset, "timeout": pollTimeout, "allowed_updates": allowedUpdates,
		}, &ups)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if isUnauthorized(err) {
				return err
			}
			// 409 = 另一个实例在 getUpdates,或 webhook 又被人登记上了。退避,不要互相踢。
			slog.Warn("telegram: getUpdates failed", "err", err, "retryIn", backoff)
			if !sleep(ctx, backoff) {
				return nil
			}
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		backoff = time.Second
		for _, u := range ups {
			// 先推进 offset 再处理:下一次 getUpdates 带着它就是在确认。处理到一半进程挂了,
			// 这条会被重投 —— 重投由 OnAction 的幂等(ErrAlreadyDecided)兜住。
			if u.UpdateID >= offset {
				offset = u.UpdateID + 1
			}
			c.dispatch(ctx, u, sink)
		}
	}
	return nil
}

// retry 反复调 fn 直到成功、token 被拒或 ctx 结束(后者返回 nil)。
func (c *Channel) retry(ctx context.Context, fn func() error) error {
	backoff := time.Second
	for {
		err := fn()
		if err == nil || isUnauthorized(err) {
			return err
		}
		if ctx.Err() != nil {
			return nil
		}
		slog.Warn("telegram: call failed, retrying", "err", err, "retryIn", backoff)
		if !sleep(ctx, backoff) {
			return nil
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// HandleWebhook 实现 imchannel.Webhooker。只有 webhook 模式、且配了 secret 时才收;
// secret 用常量时间比较。鉴权通过后一律 200 —— Telegram 会重投非 2xx 的请求。
func (c *Channel) HandleWebhook(ctx context.Context, header http.Header, body []byte, sink imchannel.ActionSink) int {
	if c.mode != modeWebhook || c.secret == "" {
		return http.StatusForbidden
	}
	got := header.Get("X-Telegram-Bot-Api-Secret-Token")
	if subtle.ConstantTimeCompare([]byte(got), []byte(c.secret)) != 1 {
		return http.StatusForbidden
	}
	var u Update
	if err := json.Unmarshal(body, &u); err != nil {
		slog.Warn("telegram: webhook body unparsable", "err", err)
		return http.StatusOK
	}
	c.dispatch(ctx, u, sink)
	return http.StatusOK
}

func (c *Channel) dispatch(ctx context.Context, u Update, sink imchannel.ActionSink) {
	switch {
	case u.CallbackQuery != nil:
		c.onCallback(ctx, u.CallbackQuery, sink)
	case u.Message != nil:
		c.onMessage(ctx, u.Message, sink)
	}
}

func (c *Channel) onCallback(ctx context.Context, q *CallbackQuery, sink imchannel.ActionSink) {
	var toast string
	switch m := callbackRe.FindStringSubmatch(q.Data); {
	case q.Data == DoneData:
		toast = "该工单已处理"
	case m == nil:
		toast = "无法识别的操作"
	default:
		// 认人只凭 from.id —— Telegram 保证它是真正的点击者。对应哪个网关账户、能不能批,
		// 都是 sink 那一侧的事。
		toast = sink.OnAction(ctx, imchannel.Action{
			PlatformUserID: strconv.FormatInt(q.From.ID, 10), ApNo: m[1], Approve: m[2] == "a",
		}).Toast
	}
	// 必须应答。不应答,按钮会在对方的客户端上一直转圈,而他刚刚可能批准了一条 DROP。
	if err := c.call(ctx, c.client, "answerCallbackQuery", map[string]any{
		"callback_query_id": q.ID, "text": clipRunes(toast, 190),
	}, nil); err != nil {
		slog.Warn("telegram: answerCallbackQuery failed", "err", err)
	}
}

func (c *Channel) onMessage(ctx context.Context, m *Message, sink imchannel.ActionSink) {
	// 只理私聊。群里的任何文字都不触发任何逻辑 —— 绑定码发在群里就是泄露了,更不能认。
	if m.Chat.Type != "private" || m.From == nil {
		return
	}
	text := strings.TrimSpace(m.Text)
	if text == "" {
		return
	}
	reply := startReply
	if !strings.HasPrefix(text, "/start") {
		reply = sink.OnDirectMessage(ctx, imchannel.DM{PlatformUserID: strconv.FormatInt(m.From.ID, 10), Text: text})
	}
	if reply == "" {
		return
	}
	if err := c.call(ctx, c.client, "sendMessage", map[string]any{"chat_id": m.Chat.ID, "text": reply}, nil); err != nil {
		slog.Warn("telegram: reply failed", "err", err)
	}
}
```

- [ ] **Step 3: 跑测试确认通过**

Run: `cd backend && gofmt -l ./internal/imchannel/telegram && go vet ./internal/imchannel/... && go test -race ./internal/imchannel/telegram/... -v 2>&1 | tail -40`
Expected: PASS（无数据竞争）。

- [ ] **Step 4: 提交**

```bash
git add backend/internal/imchannel/telegram
git commit -m "feat(im): receive Telegram clicks and direct messages

Run fetches the bot name, then either registers the webhook or deletes
any leftover one and long-polls, advancing the offset before handling
so a redelivered update is idempotent. Clicks become Actions keyed only
by from.id and are always answered; private texts go to binding, /start
gets instructions, group messages are ignored. A rejected token stops
the loop; everything else backs off and retries.

Co-Authored-By: <model> <noreply@anthropic.com>"
```

---
### Task 5: 登记 Telegram 渠道 + 端到端测试

**Files:**
- Modify: `backend/internal/service/service.go`（登记 Telegram）
- Test: `backend/internal/bootstrap/im_telegram_test.go`

**Interfaces:**
- Consumes: Task 3/4 的 `telegram.New`、`telegram.WebhookPath`、`tgtest`；Task 2 的 webhook 路由；第 1 期的绑定接口、`submitProdHighRisk`、`approvalRow`、`setSettings`、`bindIM`、`flushIM`、`repo.AuditByOperatorLike`
- Produces: `s.IM.Register(imchannel.Telegram, imchannel.Caps{Approve: true}, telegram.New)`；测试辅助 `(*testApp).telegramIM(token string, extra map[string]any) *tgtest.Server`

- [ ] **Step 1: 写失败的端到端测试 `bootstrap/im_telegram_test.go`**

```go
package bootstrap

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"velagateway/internal/imchannel/telegram"
	"velagateway/internal/imchannel/telegram/tgtest"
)

// telegramIM 起一个假 Bot API,把网关的 Telegram 渠道指过去并打开交互审批。
// polling 模式下等到第一次 getUpdates,webhook 模式下等到 setWebhook。
func (a *testApp) telegramIM(token string, extra map[string]any) *tgtest.Server {
	a.t.Helper()
	srv := tgtest.New(a.t)
	kv := map[string]any{
		"im.telegram.approval": true, "im.telegram.botToken": srv.Token, "im.telegram.apiBase": srv.URL,
	}
	for k, v := range extra {
		kv[k] = v
	}
	a.setSettings(token, kv)
	if kv["im.telegram.mode"] == "webhook" {
		srv.WaitCall(a.t, "setWebhook", nil)
	} else {
		srv.WaitCall(a.t, "getUpdates", nil)
	}
	return srv
}

func TestTelegram_BindApproveAndCloseEndToEnd(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	owner := app.login("zhangwei@vela.io", "vela123")
	srv := app.telegramIM(admin, nil)

	// 1. 自助绑定:控制台生成码,用自己的 Telegram 私聊机器人
	r := app.do(http.MethodPost, "/api/v1/me/im-identities/telegram/code", owner, nil)
	eq(t, r.Code, 0, "issue code")
	var issued struct {
		Code    string `json:"code"`
		BotName string `json:"botName"`
	}
	_ = json.Unmarshal(r.Data, &issued)
	eq(t, issued.BotName, srv.BotUsername, "the bind dialog names the bot")
	srv.PushText(777, "private", issued.Code)
	srv.WaitCall(t, "sendMessage", func(c tgtest.Call) bool {
		return c.Str("chat_id") == "777" && strings.Contains(c.Str("text"), "zhangwei@vela.io")
	})

	// 2. 建单 → 卡片私信给他
	ap := app.submitProdHighRisk(admin)
	app.flushIM()
	card := srv.WaitCall(t, "sendMessage", func(c tgtest.Call) bool {
		return c.Str("chat_id") == "777" && strings.Contains(c.Str("reply_markup"), "ap:"+ap.ApNo+":a")
	})
	if card.Str("parse_mode") != "HTML" || !strings.Contains(card.Str("text"), "DROP TABLE orders") {
		t.Fatalf("card = %+v", card.Params)
	}

	// 3. 点「通过」→ 走控制台同一条判定 → 应答 → 卡片收起
	q := srv.PushCallback(777, "ap:"+ap.ApNo+":a")
	ans := srv.WaitCall(t, "answerCallbackQuery", func(c tgtest.Call) bool { return c.Str("callback_query_id") == q })
	eq(t, ans.Str("text"), "已批准,请发起人回控制台执行", "toast")
	eq(t, app.approvalRow(admin, ap.ApNo).Status, "approved", "approved via Telegram")
	rows, _ := app.repo.AuditByOperatorLike("%(Telegram)")
	eq(t, len(rows), 1, "the audit names the channel")
	app.flushIM()
	srv.WaitCall(t, "editMessageReplyMarkup", func(c tgtest.Call) bool {
		return strings.Contains(c.Str("reply_markup"), "通过") && strings.Contains(c.Str("reply_markup"), `"done"`)
	})
}

func TestTelegram_StrangerClickIsRefused(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	srv := app.telegramIM(admin, nil)
	ap := app.submitProdHighRisk(admin)

	q := srv.PushCallback(999, "ap:"+ap.ApNo+":a")
	ans := srv.WaitCall(t, "answerCallbackQuery", func(c tgtest.Call) bool { return c.Str("callback_query_id") == q })
	if !strings.Contains(ans.Str("text"), "未绑定") {
		t.Fatalf("toast = %q", ans.Str("text"))
	}
	eq(t, app.approvalRow(admin, ap.ApNo).Status, "pending", "untouched")
	rows, _ := app.repo.AuditByOperatorLike("%(未绑定)%")
	if len(rows) != 1 || rows[0].ActorName != "telegram:999" {
		t.Fatalf("stranger audit = %+v", rows)
	}
}

func TestTelegram_WebhookModeEndToEnd(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	srv := app.telegramIM(admin, map[string]any{
		"im.telegram.mode": "webhook", "im.telegram.webhookSecret": "s3cret",
		"im.callbackBaseURL": "https://gw.example",
	})
	hook := srv.Calls("setWebhook")[0]
	eq(t, hook.Str("url"), "https://gw.example"+telegram.WebhookPath, "registered URL")
	eq(t, hook.Str("secret_token"), "s3cret", "registered secret")

	app.bindIM("zhangwei@vela.io", "telegram", "777")
	ap := app.submitProdHighRisk(admin)
	body := `{"update_id":1,"callback_query":{"id":"w1","from":{"id":777},"data":"ap:` + ap.ApNo + `:a"}}`

	eq(t, app.postIMWebhook("telegram", nil, body), http.StatusForbidden, "no secret header")
	eq(t, app.postIMWebhook("telegram", map[string]string{"X-Telegram-Bot-Api-Secret-Token": "nope"}, body), http.StatusForbidden, "wrong secret")
	eq(t, app.approvalRow(admin, ap.ApNo).Status, "pending", "nothing decided yet")
	eq(t, app.postIMWebhook("telegram", map[string]string{"X-Telegram-Bot-Api-Secret-Token": "s3cret"}, body), http.StatusOK, "accepted")
	eq(t, app.approvalRow(admin, ap.ApNo).Status, "approved", "decided via webhook")
	srv.WaitCall(t, "answerCallbackQuery", func(c tgtest.Call) bool { return c.Str("callback_query_id") == "w1" })
	if len(srv.Calls("getUpdates")) != 0 {
		t.Fatal("webhook mode must not poll")
	}
}

func TestTelegram_SwitchingModesMovesTheWebhook(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	srv := app.telegramIM(admin, nil)
	eq(t, len(srv.Calls("deleteWebhook")), 1, "polling clears any webhook first")

	app.setSettings(admin, map[string]any{
		"im.telegram.mode": "webhook", "im.telegram.webhookSecret": "s3", "im.callbackBaseURL": "https://gw.example",
	})
	srv.WaitCall(t, "setWebhook", nil)
	before := len(srv.Calls("getUpdates"))
	time.Sleep(400 * time.Millisecond) // 一个轮询周期:webhook 模式下不该再有 getUpdates
	eq(t, len(srv.Calls("getUpdates")), before, "polling stopped")

	app.setSettings(admin, map[string]any{"im.telegram.mode": "polling"})
	srv.WaitCall(t, "deleteWebhook", func(tgtest.Call) bool { return len(srv.Calls("deleteWebhook")) >= 2 })
	srv.WaitCall(t, "getUpdates", func(tgtest.Call) bool { return len(srv.Calls("getUpdates")) > before })
}

func TestTelegram_BadTokenShowsInStatusWithoutTheToken(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	srv := tgtest.New(t)
	srv.SetFail("getMe", tgtest.Failure{Code: 401, Desc: "Unauthorized"})
	app.setSettings(admin, map[string]any{
		"im.telegram.approval": true, "im.telegram.botToken": srv.Token, "im.telegram.apiBase": srv.URL,
	})

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r := app.do(http.MethodGet, "/api/v1/settings/im/status", admin, nil)
		if strings.Contains(string(r.Data), srv.Token) {
			t.Fatalf("status leaked the bot token: %s", r.Data)
		}
		var rows []struct {
			Kind      string `json:"kind"`
			Running   bool   `json:"running"`
			LastError string `json:"lastError"`
		}
		_ = json.Unmarshal(r.Data, &rows)
		for _, row := range rows {
			if row.Kind == "telegram" && !row.Running && strings.Contains(row.LastError, "401") {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("a rejected token must surface as a stopped channel with a 401 error")
}
```

Run: `cd backend && go test ./internal/bootstrap/ -run 'TestTelegram_' -v`
Expected: FAIL（没有登记 Telegram 渠道：`/me/im-identities/telegram/code` 404，等不到 getUpdates）。

- [ ] **Step 2: 登记渠道**

`service/service.go` import 加 `"velagateway/internal/imchannel/telegram"`，在登记飞书那一行之后加：

```go
	// Telegram:第 2 期起能做交互审批;群通知在第 6 期。
	s.IM.Register(imchannel.Telegram, imchannel.Caps{Approve: true}, telegram.New)
```

- [ ] **Step 3: 跑测试确认通过**

Run: `cd backend && go vet ./... && go test ./internal/bootstrap/ -run 'TestTelegram_|TestIM|TestOpenAPI' -v 2>&1 | tail -40`
Expected: PASS

然后整包一次（前台，600000ms）：`cd backend && go test ./... 2>&1 | grep -E -- '--- FAIL|^FAIL|^ok|panic:' | grep -v '^ok'`
Expected: 只剩基线（`internal/osc` 整包与 `TestReleaseE2E_…`）。

- [ ] **Step 4: 提交**

```bash
git add backend
git commit -m "feat(im): register Telegram as the first interactive approval channel

End to end against a fake Bot API: a user binds by DMing the code, the
chain member gets a card, a click approves through the console's own
decision path and the card closes; strangers are refused and audited;
webhook mode authenticates by secret; switching modes moves the
webhook; a rejected token surfaces in status without the token itself.

Co-Authored-By: <model> <noreply@anthropic.com>"
```

---
### Task 6: 前端 · Telegram 配置与公共回调设置

**Files:**
- Modify: `frontend/src/components/settings/ImChannelsPanel.tsx`
- Modify: `frontend/src/locales/zh.ts`、`frontend/src/locales/en.ts`
- Modify: `frontend/src/styles/theme.css`

**Interfaces:**
- Consumes: 后端设置键（Global Constraints）、`GET /settings/im/status`（Telegram 行 `canApprove: true`、`botName`、`lastError`）
- Produces: `FieldDef` 新增 `options?: { value: string; labelKey: string }[]` 与 `defaultValue?: string`；导出 `IM_COMMON: FieldDef[]`；`IM_CHANNELS.telegram`；内部组件 `FieldInput`、`TelegramHint`；`Capability` 新增可选 `extra?: ReactNode`

- [ ] **Step 1: 改 `ImChannelsPanel.tsx`**

1. 顶部 import 改为 `import type { ReactNode } from 'react'` 加上原有 import。
2. `FieldDef` 改成：

```ts
type FieldDef = {
  key: string
  label: string
  /** 字面的占位符(URL 这类不翻译的) */
  placeholder?: string
  /** 需要翻译的占位符 */
  placeholderKey?: string
  secret?: boolean
  /** 有这项就渲染成下拉:value 存进设置,labelKey 走翻译 */
  options?: { value: string; labelKey: string }[]
  /** 设置里还没有值时的初值(下拉必须有一个合法的初值) */
  defaultValue?: string
}
```

3. `IM_CHANNELS` 上方加公共回调配置，并在 `IM_CHANNELS` 里加 `telegram`：

```ts
/**
 * 所有渠道共用的回调配置。只有 webhook 模式用得到 —— polling / 长连接由网关主动外连,
 * 不需要公网入口。
 */
export const IM_COMMON: FieldDef[] = [
  { key: 'im.callbackBaseURL', label: 'imCallbackBase', placeholder: 'https://gw.corp.io' },
  { key: 'im.callbackAllowIPs', label: 'imCallbackAllowIPs', placeholderKey: 'imCallbackAllowIPsPh' },
]
```

```ts
  telegram: {
    approval: [
      { key: 'im.telegram.botToken', label: 'imTgToken', placeholderKey: 'imTgTokenPh', secret: true },
      {
        key: 'im.telegram.mode', label: 'imTgMode', defaultValue: 'polling',
        options: [{ value: 'polling', labelKey: 'imTgModePolling' }, { value: 'webhook', labelKey: 'imTgModeWebhook' }],
      },
      { key: 'im.telegram.webhookSecret', label: 'imTgSecret', placeholderKey: 'imTgSecretPh', secret: true },
      { key: 'im.telegram.apiBase', label: 'imTgApiBase', placeholder: 'https://api.telegram.org' },
    ],
  },
```

并把 `IM_CHANNELS` 上方注释里「第 1 期只有飞书群通知。」改成「飞书群通知(第 1 期)、Telegram 交互审批(第 2 期)。」

4. `imInitialValues` 改成：

```ts
export function imInitialValues(g: Record<string, string>): ImValues {
  // 密钥从不回传。输入框留空 = 保持不变,所以初值只能是空串。
  const init = (f: FieldDef) => (f.secret ? '' : parseSetting<string>(g[f.key], f.defaultValue ?? ''))
  const v: ImValues = {}
  for (const f of IM_COMMON) v[f.key] = init(f)
  for (const [kind, def] of Object.entries(IM_CHANNELS)) {
    if (def.notify) v[`im.${kind}.notify`] = parseSetting(g[`im.${kind}.notify`], false)
    if (def.approval) v[`im.${kind}.approval`] = parseSetting(g[`im.${kind}.approval`], false)
    for (const f of [...(def.notify ?? []), ...(def.approval ?? [])]) v[f.key] = init(f)
  }
  return v
}
```

5. 新增 `FieldInput`，并让 `Capability` 用它（替换 `Capability` 里 `fields.map(...)` 那段 `<label>…</label>`）：

```tsx
function FieldInput({
  f, values, onChange, secretsSet,
}: {
  f: FieldDef
  values: ImValues
  onChange: (key: string, v: string | boolean) => void
  secretsSet?: Record<string, boolean>
}) {
  const { t } = useTranslation()
  const value = String(values[f.key] ?? '')
  return (
    <label className="set-field">
      <span>{t(f.label)}</span>
      {f.options ? (
        <select className="set-in" value={value} onChange={(e) => onChange(f.key, e.target.value)}>
          {f.options.map((o) => <option key={o.value} value={o.value}>{t(o.labelKey)}</option>)}
        </select>
      ) : (
        <input
          className="set-in"
          type={f.secret ? 'password' : 'text'}
          value={value}
          placeholder={
            f.secret && secretsSet?.[f.key] ? t('secretConfigured')
              : f.placeholderKey ? t(f.placeholderKey) : f.placeholder
          }
          onChange={(e) => onChange(f.key, e.target.value)}
        />
      )}
    </label>
  )
}
```

`Capability` 的 props 加 `extra?: ReactNode`，在 `fields.map((f) => <FieldInput key={f.key} f={f} values={values} onChange={onChange} secretsSet={secretsSet} />)` 之后、`set-sub-foot` 之前渲染 `{extra}`。

6. 新增 `TelegramHint`：

```tsx
/**
 * Telegram 两种模式各有一件必须让人知道的事:polling 只能单实例跑(第二个实例会与它
 * 互相踢掉),webhook 要把这个地址填进 setWebhook —— 网关会自己登记,但管理员要确认
 * 平台能从公网访问到它。
 */
function TelegramHint({ values }: { values: ImValues }) {
  const { t } = useTranslation()
  if (values['im.telegram.mode'] !== 'webhook') {
    return <div className="set-hint">{t('imTgPollingNote')}</div>
  }
  const base = String(values['im.callbackBaseURL'] ?? '').trim().replace(/\/+$/, '')
  if (!base) return <div className="set-hint warn">{t('imTgWebhookNeedsBase')}</div>
  return <div className="set-hint">{t('imTgWebhookURL')}<code>{`${base}/api/v1/im/telegram/webhook`}</code></div>
}
```

7. 面板主体：在 `<div className="im-list">` 里、`rows.map` 之前渲染公共回调配置；交互审批那个 `Capability` 加 `extra`：

```tsx
      <div className="im-common">
        {IM_COMMON.map((f) => (
          <FieldInput key={f.key} f={f} values={values} onChange={onChange} secretsSet={secretsSet} />
        ))}
        <div className="set-hint">{t('imCallbackD')}</div>
      </div>
```

```tsx
                extra={s.kind === 'telegram' ? <TelegramHint values={values} /> : undefined}
```

（面板函数里需要 `const { t } = useTranslation()`。）

- [ ] **Step 2: 文案**

`locales/zh.ts`（放在 `imStateNotifyError` 附近）：

```ts
  imCallbackBase: '回调根地址',
  imCallbackAllowIPs: '回调来源 IP 白名单',
  imCallbackAllowIPsPh: '逗号分隔的 IP / 网段,留空表示不限',
  imCallbackD: '只有 webhook 模式用得到:平台把事件推到「根地址 + /api/v1/im/<渠道>/webhook」。长连接 / polling 模式不需要公网入口。',
  imTgToken: 'Bot Token',
  imTgTokenPh: '在 @BotFather 创建机器人后拿到',
  imTgMode: '接收方式',
  imTgModePolling: 'Long polling(无需公网入口)',
  imTgModeWebhook: 'Webhook(需要公网 HTTPS)',
  imTgSecret: 'Webhook secret',
  imTgSecretPh: 'Telegram 每次推送都带上它,网关据此认证',
  imTgApiBase: 'Bot API 地址',
  imTgPollingNote: 'Long polling 同一时刻只允许一个网关实例:多实例部署请改用 webhook。',
  imTgWebhookURL: '网关会把这个地址登记到 Telegram:',
  imTgWebhookNeedsBase: 'webhook 模式需要先填写上面的「回调根地址」。',
```

`locales/en.ts` 同位置：

```ts
  imCallbackBase: 'Callback base URL',
  imCallbackAllowIPs: 'Callback source IP allowlist',
  imCallbackAllowIPsPh: 'Comma-separated IPs / CIDRs; empty means any',
  imCallbackD: 'Only webhook mode needs this: platforms push events to "<base>/api/v1/im/<channel>/webhook". Long connections / polling need no public entry.',
  imTgToken: 'Bot token',
  imTgTokenPh: 'Issued by @BotFather when you create the bot',
  imTgMode: 'Delivery',
  imTgModePolling: 'Long polling (no public entry needed)',
  imTgModeWebhook: 'Webhook (needs public HTTPS)',
  imTgSecret: 'Webhook secret',
  imTgSecretPh: 'Telegram sends it with every push; the gateway authenticates by it',
  imTgApiBase: 'Bot API URL',
  imTgPollingNote: 'Long polling allows only one gateway instance at a time; use webhook when running several.',
  imTgWebhookURL: 'The gateway registers this URL with Telegram: ',
  imTgWebhookNeedsBase: 'Webhook mode needs the callback base URL above.',
```

- [ ] **Step 3: 样式**

`styles/theme.css` 的 `.im-*` 一组末尾追加：

```css
.im-common { display: flex; flex-direction: column; gap: 10px; padding: 4px 0 8px; }
.set-hint.warn { color: var(--warning-text); }
```

- [ ] **Step 4: 验证**

Run: `cd frontend && npm run build && npm run lint && npm run test:unit`
Expected: 全部通过。

- [ ] **Step 5: 提交**

```bash
git add frontend
git commit -m "feat(ui): configure Telegram and the shared callback settings

Channel fields can now be dropdowns with a default, so the Telegram
card gets token, delivery mode, webhook secret and Bot API URL from the
same table as every other channel. The panel explains the one thing each
mode demands: polling is single-instance, webhook needs the callback
base URL, whose resulting address is shown.

Co-Authored-By: <model> <noreply@anthropic.com>"
```

---

### Task 7: 文档

**Files:**
- Modify: `docs/external-approval-setup.md`
- Modify: `docs/superpowers/specs/2026-09-27-im-channel-approval-design.md`（新增 §19）
- Modify: `docs/features.md`
- Modify: `docs/adr/0020-im-channel-approval.md`（状态行）

**Interfaces:** 无代码。

- [ ] **Step 1: `docs/external-approval-setup.md`**

1. 「当前支持」表格里 Telegram 一行改成：`| Telegram | 规划中(第 6 期) | ✅ long polling / webhook |`。
2. 在「飞书群通知」一节之后新增一节（整段照写）：

```markdown
## Telegram 交互审批

1. 在 Telegram 里找 **@BotFather**,`/newbot` 建一个机器人,拿到 bot token。
2. 网关「设置 › IM 渠道 › Telegram」:填 Bot Token,打开「交互审批」,选接收方式:
   - **Long polling(默认)**:网关主动去 Telegram 取事件,内网部署也能用,不需要公网入口。
     **同一时刻只允许一个网关实例** —— 第二个实例会与它互相踢掉(Telegram 返回 409)。
   - **Webhook**:Telegram 把事件推到网关。需要在「回调根地址」填一个 Telegram 能访问到的
     公网 HTTPS 地址,并设置 webhook secret;网关保存后会自动调用 `setWebhook` 登记
     `<回调根地址>/api/v1/im/telegram/webhook`。多实例部署用这个模式。可以在「回调来源 IP
     白名单」里填 Telegram 的出口网段(官方公布为 `149.154.160.0/20`、`91.108.4.0/22`)。
3. 保存后看渠道状态:「运行中」即连通;token 错误会显示 401。
4. 自建了 Bot API server 的,把「Bot API 地址」改成它的地址,其余不变。

**绑定**:每个审批人在头像菜单「IM 账号绑定」里点 Telegram 的「绑定」,拿到绑定码后,
用**自己的** Telegram 私聊机器人,先发 `/start`,再把绑定码发过去。机器人回复「已绑定到 …」
即完成。管理员代绑需要对方的 Telegram **数字** user id(可以让对方私聊 @userinfobot 查看),
不是 @用户名。

**审批**:建单后,审批链上已绑定 Telegram 的成员(发起人本人除外)会收到一张卡片,点
「✅ 通过」或「❌ 驳回」。结果由按钮上方的提示告诉点击者;工单一结束(无论在哪里决定),
所有人手上的卡片按钮都会换成一颗显示结果的按钮。

**安全**:点击只认绑定表 —— 群里转发了卡片、陌生人点了按钮,都会被拒绝并写审计。bot token
与 webhook secret 加密落库、不回显;任何错误信息里的 token 都会被替换成 `<bot-token>`。
```

- [ ] **Step 2: spec 新增 §19**

`docs/superpowers/specs/2026-09-27-im-channel-approval-design.md` 末尾追加：

```markdown
## 19. 第 2 期(Telegram)实现记录

- **收卡用 `editMessageReplyMarkup`,不用 `editMessageText`**:网关不存卡片原文,重写正文会丢掉
  「这张单原来是什么」。改为把两个按钮换成一颗显示终态的按钮(`callback_data = done`,点它只回
  「该工单已处理」)。
- **webhook 入口是所有渠道共用的一条路由** `POST /api/v1/im/:channel/webhook`,由
  `Hub.ServeWebhook` 按渠道分派;渠道实现可选接口 `Webhooker` 自己验 secret / 签名。
- **`im.telegram.apiBase`**:新增的配置键,缺省 `https://api.telegram.org`,供自建 Bot API server
  与测试使用。
- **bot token 在请求 URL 里**,适配器返回的所有错误都先把 token 换成 `<bot-token>`,状态页与日志
  都看不到它。
- **卡片里的控制台链接改为 `/approvals`**:前端是 BrowserRouter,原来的 `/#/approvals` 会落到总览页。
- **第 1 期暂缓项在本期收口的**:Hub `Stop` 之后不再接任务;长连接无声退出留痕;重复登记不卡死
  `Reload`;并发绑定同一平台账号只有一个成功且得到明确的错误;代绑拒绝停用账户与服务账号;
  代解绑只在真的删到时写审计。
- Telegram 群通知不在本期,属于第 6 期。
```

- [ ] **Step 3: `docs/features.md` 与 ADR-0020**

1. `docs/features.md` 里「IM 渠道审批」那一条中「第 1 期只有飞书**群通知**，交互渠道分期接入。」改成「目前支持飞书**群通知**与 Telegram **交互审批**(long polling / webhook)，其余渠道分期接入。」；「设置」相关的 IM 渠道条目补一句「公共回调根地址与来源 IP 白名单(webhook 模式用)」。
2. `docs/adr/0020-im-channel-approval.md` 状态行改成：`- 状态:已采纳(第 1 期:框架 + 飞书群通知;第 2 期:Telegram 交互审批;其余渠道分期接入)`。

- [ ] **Step 4: 核对文档与代码一致**

Run: `grep -rn "im.telegram\|/im/telegram/webhook\|im.callback" docs/external-approval-setup.md docs/superpowers/specs/2026-09-27-im-channel-approval-design.md | head -20`，逐条对照 `backend/internal/handler/admin.go`、`backend/internal/imchannel/telegram/api.go` 与前端 `ImChannelsPanel.tsx` 里的键名与路径，确认一致。

- [ ] **Step 5: 提交**

```bash
git add docs
git commit -m "docs: document Telegram approvals and record phase two

Co-Authored-By: <model> <noreply@anthropic.com>"
```

---

## 完成标准

- `cd backend && go vet ./... && go test ./...` 只剩基线失败（`internal/osc` 整包与 `TestReleaseE2E_…`）；`go test -race ./internal/imchannel/...` 通过。
- `cd frontend && npm run build && npm run lint && npm run test:unit` 通过。
- 在独立实例上（**不要**连 `vela_gateway`：用临时库和另外的端口）打开「设置 › IM 渠道」：Telegram 卡片可配置，模式切换时提示随之变化；头像菜单「IM 账号绑定」里出现 Telegram 行。没有真实 bot token 时渠道状态显示「未配置」。
- 用真实 bot 的人工验收（可选，需要一个测试 bot 与一个 Telegram 账号）：polling 模式下完成一次绑定 → 建单 → 点通过 → 卡片收起。
