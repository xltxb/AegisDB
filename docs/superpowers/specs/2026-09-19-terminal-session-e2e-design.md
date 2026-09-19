# 终端会话的端到端测试

> Issue [#81](https://github.com/xltxb/AegisDB/issues/81) · 2026-09-19

## 问题

终端这条路上的代码——`pages/terminal/index.tsx` 的 effect、`hooks/useTerminalSession.ts`
的 xterm/WebSocket 生命周期、`lib/lineEditor.ts` 的 busy/队列状态机——没有任何一层
测试能走完一次真实会话。

- `tests/unit`（273 条）在 Node 里跑 TypeScript，不渲染 React，没有浏览器。纯逻辑
  覆盖得好，组件和 effect 一行碰不到。
- `e2e`（25 条）跑真实浏览器，但 Vite 把 `/api/v1/terminal/ws` 转发给不存在的 Go
  后端。终端页能打开、能截图、能通过 25 条规格，**底下那条 WebSocket 从来没接通过**，
  所以没有一条语句真的提交过。跑 `npm run test:e2e` 时刷屏的
  `[vite] ws proxy error: ECONNREFUSED` 就是这个洞的现场。

于是改这段代码的验证方式只有手点。而这里典型的回归是「终端偶尔断连」「切实例后编辑器
一直 busy」——偶发，手点正好验不出来。

## 路线

**Playwright e2e + `page.routeWebSocket()` 脚本化替身。**

工单列的三条路都不走：

| 工单里的路 | 不走的理由 |
|---|---|
| 新增组件测试层（Vitest / Playwright CT） | 要重新权衡 `playwright.unit.config.ts` 注释里那个「只加一个 devDependency」的取舍，而它买不到我们要的东西——见下 |
| 让 e2e 起后端走真实 WS | e2e 从「纯前端、几秒钟」变成要准备数据库，代价和收益不成比例 |
| 两者都要 | 同上 |

`page.routeWebSocket()` 在浏览器里拦下 `new WebSocket()`，由测试脚本扮演服务端。它
让上面那个取舍整个消失：零新增 devDependency，e2e 仍然不需要后端。

决定性的理由是 **StrictMode**。`main.tsx` 是 `<StrictMode>` 包着的，e2e 跑的是
`npm run dev`，所以 effect 双跑在这一层是**白送的真实环境**；在组件测试里得手工伪造。
而工单点名的第一条坑恰恰就是 StrictMode 的。

### 这条路线已被探针验证

写规格之前跑过一个一次性探针（跑完即删），四个结论都直接改变了规格怎么写：

| 探针问的 | 答案 |
|---|---|
| `routeWebSocket` 顶得住 `Sec-WebSocket-Protocol` 带 token 的握手吗 | 能。`ws://localhost:5175/api/v1/terminal/ws` 被截住 |
| xterm 的字能断言吗 | 能。没加 canvas/webgl addon，走 DOM renderer，字落在 `.xterm-rows` |
| StrictMode 下开了几条 socket | **开 2 条，同时存活峰值 = 1**——清理函数是对的 |
| 开场白打了几遍 | 1 遍。但 `sandbox` 在屏幕上出现 **3 次**——提示符每行都带实例名 |

后两条是规格正确性的前提：

- 断言**不能**写成「只开一条 socket」。StrictMode 下必然开两条，那样写是误报。
  唯一正确的判据是**同时存活的永远只有一条**。
- 断言**不能**数实例名。3 次出现对应的是 1 个开场白 + 2 个提示符。要数开场白独有的
  那行帮助文字。

## 文件

| 文件 | 动作 |
|---|---|
| `frontend/e2e/fixtures.ts` | 把 `env-tier-tree.spec.ts` 里私有的 `stubCommon` 提上来成公共 `stubTerminal()`；补 `risk/check` 与 `risk-commands`（探针暴露的漏网代理错误） |
| `frontend/e2e/wsFake.ts` | 新增。WebSocket 替身 |
| `frontend/e2e/terminal-session.spec.ts` | 新增。主路 + 三条坑 |

`stubCommon` 现在是 `env-tier-tree.spec.ts` 的私有函数，而新规格需要同一套 stub。
提进 `fixtures.ts` 是这次改动**必要**的整理，不是顺手重构：留在原地就得抄第二份，
而两份 stub 迟早会对不上。

## 替身的形状

```ts
installWsFake(page, script?) → {
  sent, sentOf(type), waitFor(type, n?)   // 客户端发了什么
  send(frame), drop()                     // 按剧本回、在途单方面断
  stats() → { opened, peakLive, live }    // StrictMode 的判据
}
```

三条非显然的职责，都来自 `lib/wsTerminal.ts`：

1. **自动回 `pong`。** 客户端每 20s 发一次 `ping`，5s 内收不到 pong 就判定半开并强制
   重连（`startHeartbeat`）。不答这一下，跑得久一点的规格会毫无征兆地飘。
2. **`send` / `drop` 永远对最新那条活 socket 说话。** StrictMode 留活的是第二条。
3. **记录同时存活峰值。** 这是坑 1 唯一正确的判据，理由见上。

## 五条规格（设计时是四条，坑 1 落地后拆成两条）

### 主路

连接 → 开场白点名实例/角色/策略 → 键入 `select id,name from users;` → 提交 →
断言客户端**真正发出去的 `exec` 帧**带对了 `connectionId` / `sql` / `database` →
替身回 `output` → 行落屏、落结果表、导出日志按钮变可用 → `drop()` → 状态灯转断开 →
点「刷新会话」→ 新 socket 建起、状态回到 open。

断言发出去的帧而不只是看屏幕，是因为要验的是**送进网关的东西**。屏幕对了而帧错了，
正是最难手点出来的一类回归。

### 坑 1 · StrictMode 下开场白不双打、socket 不建两条

**落地时拆成了两条规格**，因为它们验的根本不是同一件事——这是实现过程中用
「先证明它抓得住」那一步测出来的，设计阶段想当然合成了一条。

**1a · socket 只活一条**：`opened > 1 && peakLive === 1`。
判据是同时存活数，不是开启数：StrictMode 下必然开两条（挂载 → 清理 → 再挂载）。
拆掉清理函数里的 `ws.close()` 后 `peakLive` 变 2，规格变红——真的抓得住。

**1b · 开场白只打一次**：必须**先制造一次与会话无关的重渲染**再数。

原设计里这条是装饰。实测发现开场白 effect 在正常流程里**只触发一次**：挂载时实例
列表还没回来，`connKey` 还是 0，StrictMode 那两遍都在第一行就 return 了；`connId`
是之后由自动挑选的 effect 设进去的。所以单独拆掉 `banneredFor` 那道闸，规格依然全绿。

这道闸真正防的是 `index.tsx` 注释写的那件事：依赖数组被写宽之后，与会话无关的重渲染
会把开场白再打一遍。所以规格改成先敲几个字（每个按键都更新补全状态，整页重渲染）
再改一次窗口大小，然后数。把闸拆掉并把 `session` 塞进依赖数组后，规格变红。

对应 `useTerminalSession.ts:86` 的注释与 `index.tsx` 的 `banneredFor`。

### 坑 2 · 语句在途时断连，编辑器必须被放出来

提交后**不回执**，直接 `drop()`，然后键入字符——字必须出现在提示符行上。

少了 `onStatus('closed') → editor.resume()`（`useTerminalSession.ts:211`），
`LineEditor` 停在 `busy`，之后每个按键都被吞进 `queued`（`lineEditor.ts:358-363`），
终端看着就像死了。三条里最有价值的一条：它复现的就是「某次切实例后编辑器一直 busy」。

### 坑 3 · 切实例要把屏幕和日志一起清

在实例 A 上跑一条留下输出，切到 B，断言 A 的输出从 `.xterm-rows` 消失且 B 的开场白在；
**日志那一半**用 Playwright 的 download 事件接住「导出日志」，断言导出的文件里没有
A 的输出。

对应 `index.tsx` 开场白 effect 里 `transcript.current.clear()` 与
`session.term.current?.clear()` 那一对。工单明写「两者说同一件事」，只断言屏幕就只验了
一半。

## 验收

- `npm run test:unit` 273 条不变。
- `npm run test:e2e` 25 → **30** 条，全绿（坑 1 拆成两条，所以比设计时多一条）。
- `npm run type-check`、`npm run lint` 通过。
- **每条坑规格都要先证明它抓得住**：把对应修复临时改坏（例如摘掉 `editor.resume()`），
  确认规格变红，再改回来。不做这一步，写出来的只是四条永远绿的装饰。

## 已知取舍（落地后已更新）

坑 3 的「日志那一半」靠 download 事件，是四条里最笨重的一处。原本准备了退路
（「断言导出按钮状态 + 屏幕已清」）。

**落地后这条退路作废，不要再走。** 验证时拆掉 `transcript.current.clear()`，
屏幕那一半**仍然是绿的**（屏幕确实清了），只有导出的文件里漏出了上一台的输出。
两半验的不是同一件事；砍掉 download 那半，这个回归就会从指缝里溜过去。

## 不做的事

- 不覆盖 `intercept` / `mfa_required` / `error` / `session_revoked` 的回执矩阵。
  替身已经能发这些帧，补规格是后续一个小工单的事。
- 不覆盖补全浮层、粘贴弹窗、快捷脚本、取消。
- 不动 `playwright.unit.config.ts`。那一层的取舍不变。
