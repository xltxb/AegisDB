# 20 · 导出任务:带换行的(CTE)SQL 无法执行

Status: ready-for-human（已修复，待线上验证）

## 现象

导出任务提交多行 SQL 被拒:"数据导出仅允许单条只读查询(SELECT/SHOW 等)"。

## 排查

用 LF/CRLF/前导注释/尾分号/括号换行等十余种形态探针轰后端提交接口——换行本身全部放行;
真正被拒的是 **WITH … SELECT(CTE)**:多行分析型查询的日常写法(DWS 场景尤甚),用户
观察为"带换行的 SQL 无法执行"。

## 根因

导出只读闸门与能力矩阵都按**首动词**判定:`MapVerbToCapability(ParseVerb(sql))`,WITH
不在映射表里落进默认档 write → 导出拒绝、终端里 CTE 读也会被当写操作判。而 IsRead
早已按 ER9 正确处理 CTE(读,除非 CTE 体内藏变更)——两套判定不一致。

## 修复(backend/internal/gateway/risk.go)

动词解析层新增 `cteEffectiveVerb`:WITH 语句由其**实际动作**定动词——
- 变更动词出现在任何位置(主句或 CTE 体内,引号内容先置空)即以该变更动词为准:
  `WITH d AS (DELETE …) SELECT …` 判为 DELETE(ER9 语义上移到动词本身);
- 否则按括号深度 0 扫描主句首个关键字(SELECT/VALUES/…);
- 解析不出回退 WITH(仍落 write 档,保守)。

导出闸门、能力矩阵、IsRead 三处自动一致。

## 回归测试

- gateway/cte_verb_test.go:8 种 CTE 形态的 ParseVerb + IsRead(含引号诱饵、递归 CTE、
  未闭合回退);
- bootstrap/export_cte_test.go:只读 CTE 导出放行、含 DELETE 的 CTE 仍拒绝。
- 另以探针锁定:换行(LF/CRLF)、前导注释、尾分号、分号后注释等形态提交均放行(未入库,
  行为已由既有 split/判定测试覆盖)。

后端全量测试通过。无表结构变更。

## Comments

- 2026-08-21 用户澄清:失败语句是**纯多行 SELECT,非 CTE**。补端到端探针(真实 sqlite)后
  找到第二个独立缺陷:提交环节放行,但 worker 把**原始文本**直接交驱动执行,以分号结尾
  的多行 SQL(终端手癖;终端前端会剥尾分号,导出页不剥)在部分驱动上被拒——sqlite 报
  "not an error (21)",pg 扩展协议同样严格。修复:produceExport 执行前用 SplitStatements
  归一化取单条语句(尾分号/尾注释自然剥离;提交时已证明恰一条)。回归:
  export_multiline_test.go 五种形态(多行/CRLF/尾分号/分号后注释/行内注释)在真实库上
  全部 done。CTE 修复(3668ede)是同一报告下的另一真实缺陷,两者都成立。
