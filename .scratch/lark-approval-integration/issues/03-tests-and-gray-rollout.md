# 03 · httptest 回归 + 灰度开关

Status: ready-for-human

## What to build

黑盒回归网 + 灰度上线。

- **审批魔方 stub**:`httptest.NewServer` 模拟 `POST /api/v1/approvals`(返回 `task_id`),用于验证出站发起与 `ExternalTaskID` 落库、Bearer 头、字段映射。
- **回调回归**(打 `POST /api/v1/approvals/lark/callback`):
  - 正确 secret + `approved:true` → 命令执行、单 approved、审计 operator=审批人邮箱
  - `approved:false` → rejected、不执行
  - 缺失/错误 `X-Callback-Secret` → 403
  - 非白名单来源 IP → 403
  - 未知 `external_task_id` → 404
  - 重复回调 → 幂等(第二次不重复执行)
  - `approver ⊆ {发起人}` + `allowSelfApprove=false` → rejected
  - 站内 `DecideApproval` 先决 → 回调再来得 `ErrAlreadyDecided`/当前状态(竞争单赢)
- **灰度**:`approval.external.enabled` 默认关;文档化「先 dev/gli 开、验证回调闭环、再放 staging/prod」。README/部署说明补充配置项。

## Acceptance criteria

- [ ] 上述回调用例全部有对应 httptest 且通过
- [ ] 出站发起用例(stub)验证 Bearer + 字段映射 + `ExternalTaskID` 落库
- [ ] 全量 `go test ./...` 通过
- [ ] 配置项与灰度顺序写入部署文档

## Blocked by

02(回调端点)

## Comments

- 已实现:`external_approval_test.go` 4 个 httptest(出站 stub + 回调 approve/reject/鉴权/未知/幂等/禁自审);
  功能开关默认关,灰度顺序见 PRD;全量 `go test ./...` 通过。
- 待办(非阻塞):前端设置面板暴露 `approval.external.*`(目前经 PUT /settings 配置);部署文档补配置项说明。
