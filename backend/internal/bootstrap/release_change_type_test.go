package bootstrap

// 升级单的变更类型(DML / DDL)。
//
// 一张单要么是数据订正(DML)要么是结构变更(DDL),不能两者混装:审批人按
// 类型评估风险(DDL 锁表、DML 影响行数),一张混合单让两种评估都失效。三条
// 规则:声明的类型必须与内容一致;两种类型的语句不得同单;未声明时由内容
// 推断并落到单据上。
//
// 分类口径:SELECT 归入 DML(MySQL 官方文档即把 SELECT 列为 DML —— 变更单
// 常带 SELECT 自查);GRANT/REVOKE 归 DDL(权限是结构不是数据);事务/会话
// 控制语句(BEGIN/COMMIT/SET/USE)是中性的,不参与定类 —— 迁移脚本几乎总
// 带着它们。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func submitTyped(t *testing.T, app *testApp, token string, connID int64, pid int64, title, changeType, sql string) apiResp {
	t.Helper()
	body := map[string]any{
		"title": title, "pipelineId": pid, "connectionId": connID,
		"sql": sql, "reason": "变更类型回归",
	}
	if changeType != "" {
		body["changeType"] = changeType
	}
	return app.submitRelease(token, body)
}

func TestReleaseRefusesMixedChangeTypes(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	stg := app.connIDByEnv(admin, "staging")
	pid := app.createPipeline(admin, "类型混装流程", "", []map[string]any{
		{"name": "执行变更", "type": "execute"},
	})

	// 规则3:一张单不能同时有 DML 和 DDL —— 无论声明什么类型。
	mixed := "UPDATE tbl_order SET memo='x' WHERE id=1; ALTER TABLE tbl_order ADD COLUMN c2 INT;"
	for _, declared := range []string{"", "dml", "ddl"} {
		r := submitTyped(t, app, admin, stg, pid, "混装单", declared, mixed)
		if r.Code == 0 {
			t.Errorf("a mixed DML+DDL ticket must be refused (declared=%q)", declared)
			continue
		}
		if !strings.Contains(r.Msg, "DML") || !strings.Contains(r.Msg, "DDL") {
			t.Errorf("the refusal should name both types, got %q", r.Msg)
		}
	}
}

func TestReleaseRefusesContentOutsideDeclaredType(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	stg := app.connIDByEnv(admin, "staging")
	pid := app.createPipeline(admin, "类型校验流程", "", []map[string]any{
		{"name": "人工审批", "type": "approve"},
		{"name": "执行变更", "type": "execute"},
	})

	// 规则2:DML 单里出现 DDL → 拒,并点名动词。
	r := submitTyped(t, app, admin, stg, pid, "DML单装DDL", "dml",
		"ALTER TABLE tbl_order ADD COLUMN c3 INT;")
	if r.Code == 0 {
		t.Fatal("DDL content in a DML ticket must be refused")
	}
	if !strings.Contains(r.Msg, "ALTER") {
		t.Errorf("refusal should name the offending verb, got %q", r.Msg)
	}
	// 反向同理。
	r = submitTyped(t, app, admin, stg, pid, "DDL单装DML", "ddl",
		"UPDATE tbl_order SET memo='y' WHERE id=1;")
	if r.Code == 0 {
		t.Fatal("DML content in a DDL ticket must be refused")
	}
	if !strings.Contains(r.Msg, "UPDATE") {
		t.Errorf("refusal should name the offending verb, got %q", r.Msg)
	}
	// 声明了类型、内容匹配 → 通过,类型落到单据上。
	r = submitTyped(t, app, admin, stg, pid, "正经DDL单", "ddl",
		"ALTER TABLE tbl_order ADD COLUMN c4 INT;")
	eq(t, r.Code, 0, "a matching declaration passes")
	var rel struct {
		ChangeType string `json:"changeType"`
	}
	_ = json.Unmarshal(r.Data, &rel)
	eq(t, rel.ChangeType, "ddl", "declared type recorded on the ticket")
}

func TestReleaseInfersChangeTypeWhenUndeclared(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	stg := app.connIDByEnv(admin, "staging")
	pid := app.createPipeline(admin, "类型推断流程", "", []map[string]any{
		{"name": "人工审批", "type": "approve"},
		{"name": "执行变更", "type": "execute"},
	})

	cases := []struct{ title, sql, want string }{
		// 事务控制是中性的,不影响定类;SELECT 归 DML(自查语句)。
		{"数据订正", "BEGIN; UPDATE tbl_order SET memo='z' WHERE id=1; SELECT 1; COMMIT;", "dml"},
		{"结构变更", "ALTER TABLE tbl_order ADD COLUMN c5 INT;", "ddl"},
	}
	for _, c := range cases {
		r := submitTyped(t, app, admin, stg, pid, c.title, "", c.sql)
		eq(t, r.Code, 0, c.title+" submits")
		var rel struct {
			ChangeType string `json:"changeType"`
		}
		_ = json.Unmarshal(r.Data, &rel)
		eq(t, rel.ChangeType, c.want, c.title+" inferred type")
	}
}

// TestOpenAPIEnforcesChangeType — 外部升级单系统走同一套规则:开放接口是门,
// 不是第二条流水线。
func TestOpenAPIEnforcesChangeType(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	ctPid := app.createPipeline(admin, "外部类型流程", "", []map[string]any{
		{"name": "执行变更", "type": "execute"},
	})
	token := app.issueClientPiped(admin, "类型校验平台", "zhangwei@vela.io", nil, ctPid)

	r := app.openDo(http.MethodPost, "/api/v1/open/releases", token, map[string]any{
		"title": "外部DML单装DDL", "instance": "sandbox-dev",
		"changeType": "dml", "sql": "DROP TABLE tbl_order;", "externalRef": "CT-1",
	})
	if r.Code == 0 {
		t.Fatal("the open API must enforce the same type rules")
	}
	if !strings.Contains(r.Msg, "DROP") {
		t.Errorf("refusal should name the verb, got %q", r.Msg)
	}

	ok := app.openDo(http.MethodPost, "/api/v1/open/releases", token, map[string]any{
		"title": "外部DDL单", "instance": "sandbox-dev",
		"changeType": "ddl", "sql": "ALTER TABLE tbl_order ADD COLUMN ext1 INT;", "externalRef": "CT-2",
	})
	eq(t, ok.Code, 0, "matching declaration passes through the open API")
	var created struct {
		ChangeType string `json:"changeType"`
	}
	_ = json.Unmarshal(ok.Data, &created)
	eq(t, created.ChangeType, "ddl", "type visible in the external contract")
}
