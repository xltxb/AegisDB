# ADR 0017:JWT 存 localStorage

- 状态:已采纳 —— 但这是一笔**记在账上的取舍**,不是最优解。附偿还条件。
- 日期:2026-09-12
- 相关:`frontend/src/api/http.ts`(TOKEN_KEY、请求拦截器)、`frontend/src/stores/auth.ts`、
  `frontend/src/hooks/useTerminalSession.ts`(WS 子协议)、
  `backend/internal/middleware/middleware.go`(TokenVersion 校验)

## 背景

登录后拿到一枚 JWT,每个请求以 `Authorization: Bearer` 带上。它存在浏览器的
`localStorage` 里(`aegis_token`)。

另一条路是 httpOnly Cookie:令牌由服务端下发、浏览器自动携带,JavaScript 读不到 ——
XSS 拿不走它。

这份记录存在的理由是:审查里反复被问到为什么选前者,而 README、DEPLOY 和此前的 ADR
里一个字都没有。没有依据的现状,和刻意的决定,在代码里长得一模一样。

## 决定

暂时保留 localStorage。

### 先说清楚哪些**不是**理由

这三条是这类讨论里最常被搬出来的,而在这个代码库里它们都不成立 —— 写下来是为了
让下一次评估不必再绕一遍:

- **「WebSocket 带不了 Cookie」**:生产是同源的(后端用 `r.Static` + `NoRoute` 自己
  伺服 SPA,见 `bootstrap/router.go`),Cookie 会随 WS 握手自动发送。当前令牌走
  `Sec-WebSocket-Protocol: vela-token, <token>` 是为了**不让它落进 URL**(query 会
  进访问日志和 Referer),这件事与存储位置无关,换成 Cookie 之后那条子协议就不需要了。
- **「开发时跨源,Cookie 不好使」**:`vite.config.ts` 已经把 `/api`(含 ws)代理到
  `localhost:8080`,浏览器看到的一直是 `localhost:5173` 同源。
- **「前后端分离部署」**:不是这个产品的形态。它是单二进制,前端由后端同源伺服。

### 真正剩下的成本

只有一条:**Cookie 会被浏览器自动带上,所以必须同时做 CSRF 防护**(SameSite=Lax
只挡住一部分,POST 跨站表单仍要一个双提交令牌或自定义头)。这是一次涉及每个写接口
的改造,而不是换一个存储位置。

## 现在靠什么兜住风险

- **令牌代次吊销**(`TokenVersion`):登出与改密把它 +1,中间件每次请求比对,旧令牌当场
  失效。这让一枚泄露的令牌有了终点 —— 而这正是 Cookie 方案本身也提供不了的东西。
- **空闲自动锁定**:走的是正经的 logout(代次 +1),不是只清本地。
- **令牌不进 URL**:WS 走子协议头,不用 query。
- **CORS 只回显白名单 Origin**,空列表 = 同源不发 CORS 头。

## 代价 —— 明确写下来

- **没有 CSP。** 整个后端没有下发 `Content-Security-Policy`。也就是说 XSS 这一层
  目前**没有任何纵深防御**:一旦有脚本注入,`localStorage.getItem('aegis_token')`
  是一行代码的事。
- localStorage 对同源的**任意**脚本可读 —— 包括依赖链上的任何一个包。前端有三十多个
  直接依赖,其中任何一个被投毒都够了。

换句话说:这个决定的安全性,此刻完全压在"不出 XSS"这一个假设上。

## 什么时候必须偿还

任一条成立就不能再拖:

1. 页面开始加载**任何第三方脚本**(埋点、客服、地图、CDN 上的库)。
2. 出现任何一份 XSS 报告 —— 哪怕是已修复的。它证明那个假设会破。
3. 要给这个网关接**外部用户**(不再是内网运维),攻击面和攻击动机都会变。

## 偿还路径

httpOnly + Secure + SameSite=Lax 的 Cookie,配一个双提交 CSRF 令牌(或要求所有写请求
带一个自定义头 —— 跨站表单发不出自定义头)。WS 那边可以顺手简化:同源下 Cookie 自动
带上,子协议那一套可以撤掉。

在那之前,**加 CSP 是性价比最高的一步**,它不需要动鉴权:它把"XSS 一旦发生令牌就没了"
变成"XSS 要先绕过 CSP"。这一步可以独立于本 ADR 先做。

## 与既有记录的关系

`issue.md` 第一轮把这件事记作 H5:「维持现设计,但 M1 令牌代次吊销已显著降低泄露影响;
彻底改 httpOnly Cookie + CSRF 需较大改造,留待评估。」本 ADR 把那句话展开成可以被检验
的形式,并纠正其中一处乐观:代次吊销限制的是令牌**泄露之后**的有效期,它不阻止泄露,
也不会让 XSS 拿不到令牌 —— 攻击者拿到的仍是一枚当下有效的令牌。
