# 12 · 批量粘贴 SQL 只按第一条语句定审批

Status: ready-for-human（已修复，待人工验收）

## 现象

prod 环境终端批量粘贴多条 SQL（单行 `A; B; C;` 一次提交）触发规则时，审批表现只跟第一条命中语句走：

1. **预检漏判**：`/risk/check` 不拆语句、只按首个动词查能力矩阵。`SELECT 1; UPDATE …` 预检返回 `allow`——终端不弹审批理由框、提示"安全"，实际执行时又被 Exec 拦截（自动建单、理由为空）。
2. **工单定级被第一条钉死**：`Exec`/`ExecAsync` 的 `strictestVerdict` 只按 action 档位（deny>approve>allow）取"第一条达到该档位"的判定，同档位内不比风险。`UPDATE …(mid·能力矩阵); DELETE FROM …(high·高危字典)` 生成的工单是 **mid**，字典规则整条丢失——审批人按 mid 定级审一个含 high 语句的批次，审计风险等级同样记低。

（排查确认：不存在"后续语句绕过审批直接执行"的通道——整批始终作为一张工单拦截，批准后整批执行。）

## 根因

- `service.RiskCheck`：对原始串整体 `EvaluateFor`，能力矩阵只看首动词。
- `service.strictestVerdict`：仅 `actionRank(v) > actionRank(strict)` 时替换判定，同 action 不同 risk 不升级；非胜者语句命中的规则名不保留。

## 修复（backend/internal/service/gateway.go）

- `RiskCheck` 改为与 `Exec` 相同的拆分后 `strictestVerdict` 判定。
- `strictestVerdict` 排序键改为 (action, risk) 双档位，新增 `riskRank`；聚合所有非 allow 语句命中的规则名（去重，` + ` 连接）写回 verdict，工单/审计/前端提示都能看到全部命中规则。

## 回归测试（backend/internal/bootstrap/exec_multistmt_test.go）

- `TestExec_BatchVerdictEscalatesToStrictestStatement`：批次含 mid+high 语句 → 拦截响应与落库工单均为 high，rule 同时含"能力矩阵"与"高危命令字典"。
- `TestRiskCheck_BatchTailStatementGoverns`：`SELECT 1; UPDATE … WHERE …` 预检 → `approve` / `requiresApproval=true` / `mid`。

修复前两测均红（mid/allow），修复后全量后端测试通过。
