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
	"time"

	"velagateway/internal/gateway"
	"velagateway/internal/model"
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

// 无 WHERE 拦截的分层开关。内置五分层里只有 dev 关掉它。
var strictNoWhere = map[string]bool{"prod": true, "gli": true, "staging": true, "uat": true, "dev": false}

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

type expectOut struct {
	Action   string `json:"action"`
	Risk     string `json:"risk"`
	Command  string `json:"command"`
	RuleCode string `json:"ruleCode,omitempty"`
}

type caseOut struct {
	caseIn
	Expect expectOut `json:"expect"`
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
		"menuKeys":         menuKeys,
		"menuMatrix":       menuOut,
	}
	writeJSON(filepath.Join(*out, "rules.json"), rules)

	// ---- fixtures.json：用真引擎跑 ----
	eng := gateway.NewRiskEngine(memStore{})
	results := make([]caseOut, 0, len(cases))
	for _, c := range cases {
		v := eng.EvaluateFor([]int64{roleID(c.Role)}, c.Engine, c.Tier, c.SQL)
		e := expectOut{Action: v.Action, Risk: v.Risk, Command: v.Command}
		if v.Ref != nil {
			e.RuleCode = v.Ref.Code
		}
		results = append(results, caseOut{caseIn: c, Expect: e})
	}
	writeJSON(filepath.Join(*out, "fixtures.json"), map[string]any{
		"note":        "由 db-gateway/backend/cmd/exportrules 用真 RiskEngine 跑出，勿手改。",
		"generatedAt": time.Now().UTC().Format(time.RFC3339),
		"cases":       results,
	})

	fmt.Printf("wrote rules.json (%d commands) and fixtures.json (%d cases) to %s\n",
		len(dict), len(results), *out)
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
