// Command exportrules 把网关的内置默认规则集导出为 JSON，供 AegisDB 官网的
// 判定链演示使用，并用**真的 RiskEngine** 跑出一份期望表。
//
// 它不连数据库：规则集显式写在这里，与 internal/bootstrap/seed.go 的种子对齐。
// 官网那边有一份判定引擎的 TypeScript 移植，靠这份期望表证明两边算出同样的结论 ——
// 后端判定逻辑改了而移植没跟上时，官网的测试会失败，而不是等谁发现官网在骗人。
//
// 用法：go run ./cmd/exportrules -out /path/to/AegisDB-Web/src/data
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"velagateway/internal/gateway"
	"velagateway/internal/model"
	"velagateway/pkg/sqlutil"
)

// ---- 规则集（与 internal/bootstrap/seed.go 对齐）----

var tiers = []string{"prod", "gli", "staging", "uat", "dev"}

var roles = []string{"admin", "owner", "l2", "ro", "audit"}

// 行序 = model.Capabilities，列序 = [prod, staging, dev]。
// gli / uat 在下面按 prod / staging 克隆。
var matrices = map[string][][]string{
	"admin": {{"allow", "allow", "allow"}, {"approve", "allow", "allow"}, {"approve", "approve", "allow"}, {"approve", "approve", "approve"}, {"allow", "allow", "allow"}, {"allow", "allow", "deny"}, {"allow", "allow", "allow"}, {"approve", "allow", "allow"}},
	"owner": {{"allow", "allow", "allow"}, {"approve", "allow", "allow"}, {"approve", "approve", "allow"}, {"approve", "approve", "approve"}, {"allow", "allow", "allow"}, {"allow", "allow", "allow"}, {"allow", "allow", "allow"}, {"approve", "allow", "allow"}},
	"l2":    {{"allow", "allow", "allow"}, {"approve", "approve", "allow"}, {"approve", "approve", "allow"}, {"deny", "deny", "deny"}, {"deny", "deny", "deny"}, {"deny", "deny", "deny"}, {"allow", "allow", "allow"}, {"approve", "approve", "allow"}},
	"ro":    {{"allow", "allow", "allow"}, {"deny", "deny", "allow"}, {"deny", "deny", "allow"}, {"deny", "deny", "deny"}, {"deny", "deny", "deny"}, {"deny", "deny", "deny"}, {"allow", "allow", "allow"}, {"deny", "deny", "allow"}},
	"audit": {{"allow", "allow", "allow"}, {"deny", "deny", "deny"}, {"deny", "deny", "deny"}, {"deny", "deny", "deny"}, {"deny", "deny", "deny"}, {"deny", "deny", "deny"}, {"allow", "allow", "allow"}, {"deny", "deny", "deny"}},
}

// 菜单开关。列序见下面的 menuKeys。
var menuKeys = []string{"terminal", "approve", "db", "rules", "envtier", "perms", "audit", "settings", "pipeline", "execwindow"}

var menuMatrix = map[string][]bool{
	"admin": {true, true, true, true, true, true, true, true, true, true},
	"owner": {true, true, false, false, false, false, true, false, true, true},
	"l2":    {true, true, false, false, false, false, true, false, true, true},
	"ro":    {true, false, false, false, false, false, true, false, false, false},
	"audit": {false, false, false, false, false, false, true, false, false, false},
}

type dictRow struct{ cmd, prod, staging, dev string }

// 种子字典只有 7 条命令 × 3 个分层（见 internal/bootstrap/seed.go:673-687，prod/staging/dev）。
// gli / uat 两行下面用 cloneFrom 显式补上——种子里没有这两层的字典行。
var dict = []dictRow{
	{"DROP", "high", "mid", "off"}, {"TRUNCATE", "high", "mid", "off"}, {"DELETE", "high", "mid", "off"},
	{"ALTER", "high", "mid", "off"}, {"RENAME", "high", "mid", "off"},
	{"GRANT", "mid", "mid", "off"}, {"REVOKE", "mid", "mid", "off"},
}

// 分层属性位。与 internal/bootstrap/seed.go 的 builtinTiers 逐字段对齐。
//
// 导出的不只是 strictNoWhere：官网的分层徽章要能说出「强制 MFA」「终端红色警告」
// 这些话，而它们和无 WHERE 拦截一样长在分层上，不是判定链里另一个地方的东西。
// 少导一个，官网就只能把它硬写进文案 —— 那正是这个程序存在的理由的反面。
type tierProp struct {
	DisplayName     string `json:"displayName"`
	SortOrder       int    `json:"sortOrder"`
	RequireMFA      bool   `json:"requireMfa"`
	DangerBanner    bool   `json:"dangerBanner"`
	CountsInPending bool   `json:"countsInPending"`
	ScanBaseline    bool   `json:"scanBaseline"`
	StrictNoWhere   bool   `json:"strictNoWhere"`
}

var tierProps = map[string]tierProp{
	"prod":    {DisplayName: "生产环境 · PROD", SortOrder: 0, RequireMFA: true, DangerBanner: true, CountsInPending: true, ScanBaseline: true, StrictNoWhere: true},
	"gli":     {DisplayName: "法务环境 · GLI", SortOrder: 1, StrictNoWhere: true},
	"staging": {DisplayName: "预发布环境 · STAGING", SortOrder: 2, StrictNoWhere: true},
	"uat":     {DisplayName: "演练环境 · UAT", SortOrder: 3, StrictNoWhere: true},
	"dev":     {DisplayName: "开发环境 · DEV", SortOrder: 4, StrictNoWhere: false},
}

// 无 WHERE 拦截的分层开关 —— 从 tierProps 派生，不另写一份。内置五分层里只有 dev
// 关掉它。两份手写的表迟早分叉，而分叉的那一天没人会发现。
var strictNoWhere = func() map[string]bool {
	m := make(map[string]bool, len(tierProps))
	for code, p := range tierProps {
		m[code] = p.StrictNoWhere
	}
	return m
}()

// 分层克隆：gli 按 prod 判（法务），uat 按 staging 判（演练）。
// 种子只种了 prod/staging/dev 三层，这两层是导出时补的 —— 与产品「新分层必须从
// 现有分层克隆规则」的语义一致，但它是本程序的选择，不是种子里就有的。
var cloneFrom = map[string]string{"gli": "prod", "uat": "staging"}

// levelIndex 把分层映射到 matrices / dict 的列下标。
func levelIndex(tier string) int {
	if src, ok := cloneFrom[tier]; ok {
		tier = src
	}
	switch tier {
	case "prod":
		return 0
	case "staging":
		return 1
	case "dev":
		return 2
	}
	return 0
}

// ---- 内存 Store：把上面的规则集喂给真 RiskEngine ----

type memStore struct{}

func (memStore) CapabilityLevel(roleID int64, capability, tier string) (string, error) {
	role := roles[roleID-1]
	rows := matrices[role]
	for ci, c := range model.Capabilities {
		if c == capability {
			return rows[ci][levelIndex(tier)], nil
		}
	}
	// 矩阵里没有这一维 = 不存在的行 = allow（与 Store 契约一致）。
	return model.LevelAllow, nil
}

func (memStore) RiskCommands() ([]model.RiskCommand, error) {
	out := make([]model.RiskCommand, 0, len(dict)*len(tiers))
	for _, d := range dict {
		lv := []string{d.prod, d.staging, d.dev}
		for _, t := range tiers {
			out = append(out, model.RiskCommand{Command: d.cmd, TierCode: t, Level: lv[levelIndex(t)]})
		}
	}
	return out, nil
}

func (memStore) StrictNoWhere(tier string) (bool, error) { return strictNoWhere[tier], nil }

// ---- 要跑的用例 ----

type caseIn struct {
	Engine string `json:"engine"`
	Tier   string `json:"tier"`
	Role   string `json:"role"`
	SQL    string `json:"sql"`
}

var cases = []caseIn{
	// 同一条 DROP，两种结局 —— 演示首页那三秒靠这两条
	{"mysql", "prod", "l2", "DROP TABLE orders"},
	{"mysql", "dev", "l2", "DROP TABLE orders"},
	// 常规读
	{"mysql", "prod", "ro", "SELECT * FROM orders LIMIT 10"},
	{"mysql", "prod", "audit", "SELECT 1"},
	// 无 WHERE
	{"mysql", "prod", "l2", "DELETE FROM orders"},
	{"mysql", "prod", "l2", "DELETE FROM orders WHERE id = 1"},
	{"mysql", "dev", "l2", "DELETE FROM orders"},
	{"postgresql", "prod", "l2", "UPDATE t SET a = 1"},
	// 引擎差异：同一串字节，MySQL 无 WHERE、PostgreSQL 有
	{"mysql", "prod", "l2", `UPDATE t SET a='x\' WHERE 1=1 --'`},
	{"postgresql", "prod", "l2", `UPDATE t SET a='x\' WHERE 1=1 --'`},
	// EXPLAIN 五种形态
	{"postgresql", "prod", "l2", "EXPLAIN DELETE FROM orders"},
	{"postgresql", "prod", "l2", "EXPLAIN ANALYZE DELETE FROM orders"},
	{"postgresql", "prod", "l2", "EXPLAIN ANALYSE DELETE FROM orders"},
	{"postgresql", "prod", "l2", "EXPLAIN (ANALYZE, BUFFERS) DELETE FROM orders"},
	{"dws", "prod", "l2", "EXPLAIN PERFORMANCE DELETE FROM orders"},
	{"mysql", "prod", "l2", "EXPLAIN FORMAT=TREE DELETE FROM orders"},
	{"oracle", "prod", "l2", "EXPLAIN PLAN FOR DELETE FROM orders"},
	{"oracle", "prod", "l2", "EXPLAIN PLAN SET STATEMENT_ID = 'plan for q3' FOR DELETE FROM orders"},
	// 会话级设置短路
	{"postgresql", "prod", "ro", "SET search_path TO dwd"},
	{"mysql", "prod", "ro", "SET GLOBAL read_only = 0"},
	{"mysql", "prod", "ro", "SET @@GLOBAL.read_only = 0"},
	{"oracle", "prod", "ro", "ALTER SESSION SET NLS_DATE_FORMAT = 'YYYY-MM-DD'"},
	{"oracle", "prod", "ro", "ALTER SYSTEM SET processes = 300"},
	// 字典多命中取最严
	{"mysql", "prod", "l2", "ALTER TABLE t DROP PARTITION p"},
	// 字面量里的关键词不是语法
	{"mysql", "prod", "l2", "INSERT INTO menu(perm) VALUES ('system:menu:delete')"},
	// 动态 SQL 的载荷要扫
	{"oracle", "prod", "l2", "EXECUTE IMMEDIATE 'DROP TABLE t'"},
	// CTE 与 DO 块
	{"postgresql", "prod", "l2", "WITH d AS (DELETE FROM t RETURNING *) SELECT * FROM d"},
	{"postgresql", "prod", "l2", "WITH t AS (SELECT 1) SELECT * FROM t"},
	{"postgresql", "prod", "ro", "DO $$ BEGIN DROP TABLE t; END $$"},
	// 能力矩阵三态
	{"mysql", "prod", "ro", "INSERT INTO t VALUES (1)"},    // ro 在 prod 上 write = deny
	{"mysql", "dev", "ro", "INSERT INTO t VALUES (1)"},     // ro 在 dev 上 write = allow
	{"mysql", "staging", "l2", "INSERT INTO t VALUES (1)"}, // l2 在 staging 上 write = approve
	// 注释绕过
	{"mysql", "prod", "l2", "/*!40001 DROP TABLE t */"},
	{"mysql", "prod", "l2", "DROP /* 注释 */ TABLE t"},
	// 授权
	{"mysql", "prod", "admin", "GRANT SELECT ON db.* TO u"},
	// 空语句
	{"mysql", "prod", "ro", "   "},
	// 跨分层：gli 克隆 prod，uat 克隆 staging
	{"mysql", "gli", "l2", "DROP TABLE orders"},
	{"mysql", "uat", "l2", "DROP TABLE orders"},
	{"mysql", "uat", "l2", "DELETE FROM orders"},
	// 只读角色读计划
	{"postgresql", "prod", "ro", "EXPLAIN SELECT * FROM t"},
	{"postgresql", "prod", "audit", "EXPLAIN SELECT * FROM t"},
}

// ---- 多语句用例：取最严 ----
//
// 设计文档把「多语句取最严」列进了期望表必须覆盖的项目，而上面那批全是单语句 ——
// 于是官网那边整个 batch.ts 只有手写期望在守：后端把 maxBatchHits 从 8 改掉、或者
// 调一下排序，保险一声不吭。
//
// 聚合循环（下面的 rawVerdict）是从 internal/service/gateway.go 内联过来的，不是
// 调 service 层 —— 那要数据库。它依赖的只有 eng.EvaluateFor 的逐条结果，加上
// actionRank / riskRank / batchHitRef / batchRef 这几段纯逻辑。
var batchCases = []caseIn{
	// 取最严，不是取第一条
	{"mysql", "prod", "l2", "SELECT 1; DROP TABLE t"},
	// 同为 approve 时取风险更高的（GRANT 在 prod 是 mid，DROP 是 high）
	{"mysql", "prod", "admin", "GRANT SELECT ON db.* TO u; DROP TABLE t"},
	// deny 胜过 approve（ro 在 prod 上 write = deny）
	{"mysql", "prod", "ro", "SELECT 1; INSERT INTO t VALUES (1)"},
	// 两条命中 → batch 规则，两条 parts
	{"mysql", "prod", "l2", "DROP TABLE a; TRUNCATE TABLE b"},
	// 三条 DROP 报三条，不合并 —— 合并会把数量藏起来
	{"mysql", "prod", "l2", "DROP TABLE a; DROP TABLE b; DROP TABLE c"},
	// 超过 maxBatchHits：截断到 8 条 + batchMore{n:2}
	{"mysql", "prod", "l2", "DROP TABLE t0; DROP TABLE t1; DROP TABLE t2; DROP TABLE t3; DROP TABLE t4; DROP TABLE t5; DROP TABLE t6; DROP TABLE t7; DROP TABLE t8; DROP TABLE t9"},
	// 全放行：不带 batch 规则
	{"mysql", "prod", "l2", "SELECT 1; SELECT 2"},
	// 批量里嵌的是**那一条**自己的规则，含它的参数（这里是无 WHERE 提级，且分层是 UAT）
	{"mysql", "uat", "l2", "DELETE FROM orders; SELECT 1"},
}

// maxBatchHits：一批命令里最多点名几条。与 service/gateway.go 的同名常量一致。
const maxBatchHits = 8

// batchHitRef / batchRef / actionRank / riskRank 逐字抄自 internal/service/gateway.go。
// 抄而不是 import，因为它们在 service 包里不导出，而把它们提到公共包只为了喂一个
// 导出程序，是让产品代码迁就工具。抄一份的代价是要跟着改 —— 期望表本来就是为了
// 在没跟上时炸出来。

func batchHitRef(pos int, v gateway.Verdict) *model.RuleRef {
	code := model.RuleBatchHit
	if v.Command == "" {
		code = model.RuleBatchHitBare
	}
	r := model.NewRuleRef(code, "pos", model.Itoa(pos), "command", v.Command)
	r.Parts = []model.RuleRef{*v.Ref}
	return r
}

func batchRef(hits []model.RuleRef) *model.RuleRef {
	r := model.NewRuleRef(model.RuleBatch)
	if len(hits) <= maxBatchHits {
		r.Parts = hits
		return r
	}
	r.Parts = append(append([]model.RuleRef{}, hits[:maxBatchHits]...),
		*model.NewRuleRef(model.RuleBatchMore, "n", model.Itoa(len(hits)-maxBatchHits)))
	return r
}

func riskRank(r string) int {
	switch r {
	case model.RiskHigh:
		return 2
	case model.RiskMid:
		return 1
	default:
		return 0
	}
}

func actionRank(a string) int {
	switch a {
	case gateway.ActionDeny:
		return 2
	case gateway.ActionApprove:
		return 1
	default:
		return 0
	}
}

// rawVerdict 内联自 Services.rawVerdict：逐条判定，交回要求把关最多的那一条
// （deny > approve > allow，同一动作里取风险更高的），并在多语句且有命中时把规则
// 换成逐条点名的 batch。
func rawVerdict(eng *gateway.RiskEngine, roleIDs []int64, engine, tier, sql string) gateway.Verdict {
	stmts := sqlutil.SplitStatements(sql)
	strict := gateway.Verdict{Action: gateway.ActionAllow, Risk: model.RiskLow}
	var hits []model.RuleRef
	for i, st := range stmts {
		v := eng.EvaluateFor(roleIDs, engine, tier, st)
		if v.Action != gateway.ActionAllow && v.Ref != nil {
			hits = append(hits, *batchHitRef(i+1, v))
		}
		if actionRank(v.Action) > actionRank(strict.Action) ||
			(actionRank(v.Action) == actionRank(strict.Action) && riskRank(v.Risk) > riskRank(strict.Risk)) {
			strict = v
		}
	}
	if len(stmts) > 1 && len(hits) > 0 {
		strict.Ref = batchRef(hits)
		strict.Rule = model.RenderRule(strict.Ref)
	}
	return strict
}

// expectOut 是 Go 引擎对一条用例给出的答案。
//
// Ref 是**整个** RuleRef，不只是它的 code。只记 code 时，规则参数出的错一条都抓
// 不到：dictDeny 带的 tier 一度硬写成 PROD，于是 UAT 上被拦的人读到「PROD 禁止
// 直接执行」—— 那个 bug 全长在 args 里，而 args 被丢掉了。批量规则更是如此，
// 它的全部内容就是 parts。
//
// Ref 为 nil 时整个字段不出现在 JSON 里（omitempty），官网那边读成 undefined。
type expectOut struct {
	Action  string         `json:"action"`
	Risk    string         `json:"risk"`
	Command string         `json:"command"`
	Ref     *model.RuleRef `json:"ref,omitempty"`
}

type caseOut struct {
	caseIn
	Expect expectOut `json:"expect"`
}

func expectOf(v gateway.Verdict) expectOut {
	return expectOut{Action: v.Action, Risk: v.Risk, Command: v.Command, Ref: v.Ref}
}

func roleID(code string) int64 {
	for i, r := range roles {
		if r == code {
			return int64(i + 1)
		}
	}
	panic("unknown role " + code)
}

func main() {
	out := flag.String("out", "", "目标目录（官网的 src/data）")
	flag.Parse()
	if *out == "" {
		fmt.Fprintln(os.Stderr, "需要 -out")
		os.Exit(2)
	}

	// ---- rules.json ----
	capMatrix := map[string]map[string]map[string]string{}
	for _, role := range roles {
		capMatrix[role] = map[string]map[string]string{}
		for ci, c := range model.Capabilities {
			capMatrix[role][c] = map[string]string{}
			for _, t := range tiers {
				capMatrix[role][c][t] = matrices[role][ci][levelIndex(t)]
			}
		}
	}

	dictOut := map[string]map[string]string{}
	for _, d := range dict {
		dictOut[d.cmd] = map[string]string{}
		lv := []string{d.prod, d.staging, d.dev}
		for _, t := range tiers {
			dictOut[d.cmd][t] = lv[levelIndex(t)]
		}
	}

	menuOut := map[string]map[string]bool{}
	for _, role := range roles {
		menuOut[role] = map[string]bool{}
		for i, k := range menuKeys {
			menuOut[role][k] = menuMatrix[role][i]
		}
	}

	rules := map[string]any{
		"note":             "由 db-gateway/backend/cmd/exportrules 生成，勿手改。gli 按 prod 克隆、uat 按 staging 克隆。",
		"tiers":            tiers,
		"roles":            roles,
		"capabilities":     model.Capabilities,
		"capabilityMatrix": capMatrix,
		"riskDictionary":   dictOut,
		"strictNoWhere":    strictNoWhere,
		"tierProps":        tierProps,
		"menuKeys":         menuKeys,
		"menuMatrix":       menuOut,
	}
	writeJSON(filepath.Join(*out, "rules.json"), rules)

	// ---- fixtures.json：用真引擎跑 ----
	eng := gateway.NewRiskEngine(memStore{})

	results := make([]caseOut, 0, len(cases))
	for _, c := range cases {
		v := eng.EvaluateFor([]int64{roleID(c.Role)}, c.Engine, c.Tier, c.SQL)
		results = append(results, caseOut{caseIn: c, Expect: expectOf(v)})
	}

	batchResults := make([]caseOut, 0, len(batchCases))
	for _, c := range batchCases {
		v := rawVerdict(eng, []int64{roleID(c.Role)}, c.Engine, c.Tier, c.SQL)
		batchResults = append(batchResults, caseOut{caseIn: c, Expect: expectOf(v)})
	}

	writeJSON(filepath.Join(*out, "fixtures.json"), map[string]any{
		"note": "由 db-gateway/backend/cmd/exportrules 用真 RiskEngine 跑出，勿手改。" +
			"cases 是单语句（EvaluateFor），batchCases 是多语句取最严（rawVerdict）。" +
			"刻意不带时间戳：导出必须幂等，否则 `git diff --exit-code src/data` 这道漂移闸永远是红的，等于没有。",
		"cases":      results,
		"batchCases": batchResults,
	})

	fmt.Printf("wrote rules.json (%d commands) and fixtures.json (%d cases + %d batch cases) to %s\n",
		len(dict), len(results), len(batchResults), *out)
}

func writeJSON(path string, v any) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		panic(err)
	}
	b = append(b, '\n')
	if err := os.WriteFile(path, b, 0o644); err != nil {
		panic(err)
	}
}
