# 11 · 终端 WebSocket 流式执行 + REST 回退 + 布局变体 A/B/C

Status: ready-for-agent

## What to build

把 03/04 的 REST 终端升级为 WebSocket 流式执行，并实现《终端布局变体》文档的三种布局。命令经 `/terminal/ws` 流式下发由网关代执行，输出流式回显；WS 不可用时自动回退 REST。三种变体可切换：A 三栏 IDE（右侧常驻风险/审批面板）、B 终端优先 + 底部抽屉升起审批卡（非打断）、C 拦截焦点态（居中强制模态 + 必填原因 + 审批链可视化）。

- 后端：`WS /terminal/ws`（Token 经 query 鉴权），消息协议 `{type:'exec',connectionId,sql}` → `{type:'output',rows,ms}` / `{type:'intercept',rule,approvalNo,auditId}` / `{type:'error',message}`；复用 03/04 风险引擎、审批与审计。
- 前端：terminal store `run()` 优先走 WS、失败回退 REST；流式输出渲染 + 光标闪烁；三种布局变体组件（A 固定三栏 264/1fr/336；B 底部 330px 抽屉；C 半透明遮罩居中模态）+ 切换入口；拦截块（红色边框 + 规则说明 + 审批单号）；审批链可视化（发起→组长→负责人）。

## Acceptance criteria

- [ ] 命令经 `/terminal/ws` 流式执行并回显，Token 经 query 鉴权（FR-TERM-02）
- [ ] WS 不可用时自动回退 REST 执行，不影响功能
- [ ] 右侧上下文/审批面板展示目标实例/库/角色/策略/受限命令/审批链（FR-TERM-04）
- [ ] 底部状态栏展示连接状态/角色/策略/编码/审计开关（FR-TERM-05）
- [ ] A/B/C 三种布局变体可切换；C 为强制确认模态（必填原因 + 审批链路径可视化）
- [ ] `prefers-reduced-motion` 下动画收敛为 0

## Blocked by

- 03（终端 + 风险引擎 + 审计）
- 04（拦截/审批闭环）
