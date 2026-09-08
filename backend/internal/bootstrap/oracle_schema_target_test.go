package bootstrap

// Oracle 的 Database 字段装的是 **SERVICE NAME**,不是库名;对象树第二层是 OWNER
// (schema)。终端里点一个 schema、执行一条命令被拦下来之后,这张工单必须记住的是
// 那个 **schema**,而不是服务名 —— 否则批准后执行时会拿服务名去
// ALTER SESSION SET CURRENT_SCHEMA,切到一个根本不是 schema 的东西上。
//
// 这一组不需要真的连到 Oracle:判定与建单都发生在下发之前。

import (
	"encoding/json"
	"net/http"
	"testing"
)

// oracleConn 建一台 oracle 实例。它连不通,但判定、建单、审计都在下发之前发生。
func (a *testApp) oracleConn(token, env, name, serviceName string) int64 {
	a.t.Helper()
	r := a.do(http.MethodPost, "/api/v1/connections", token, map[string]any{
		"name": name, "engine": "oracle", "host": "127.0.0.1", "port": 1521,
		"env": env, "database": serviceName, "username": "app", "password": "x",
		"policy": "audit-only", "defaultRole": "dba_l2",
	})
	if r.Code != 0 {
		a.t.Fatalf("建 oracle 实例: code=%d msg=%s", r.Code, r.Msg)
	}
	var c struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(r.Data, &c); err != nil {
		a.t.Fatalf("decode connection: %v", err)
	}
	return c.ID
}

// 被拦下来的工单要记住选中的 schema,不是服务名。
func TestOracleApproval_RecordsSchemaNotServiceName(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")

	const service = "ORCLPDB1" // 实例配置里的服务名
	const schema = "G04"       // 树上点中的属主
	id := app.oracleConn(token, "prod", "ora-approval", service)

	r := app.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
		"connectionId": id, "sql": "CREATE TABLE t_ora_case (id NUMBER)",
		"reason": "建表", "database": schema,
	})
	var data struct {
		ApprovalNo string `json:"approvalNo"`
	}
	_ = json.Unmarshal(r.Data, &data)
	if data.ApprovalNo == "" {
		t.Fatalf("PROD 上的 CREATE TABLE 本该被拦成工单: code=%d msg=%s data=%s", r.Code, r.Msg, string(r.Data))
	}

	page := app.searchApprovals(token, data.ApprovalNo)
	if len(page.Items) != 1 {
		t.Fatalf("取不回工单 %s", data.ApprovalNo)
	}
	got := page.Items[0].Database
	if got == service {
		t.Fatalf("工单记成了服务名 %q —— 批准后执行会拿它去 ALTER SESSION SET CURRENT_SCHEMA", got)
	}
	if got != schema {
		t.Errorf("工单的目标库应当是选中的 schema %q,实际 %q", schema, got)
	}
}

// 审计记的也该是 schema。effectiveDatabase 的注释写着"审计里记的也是同一个值",
// 那句话要么是真的,要么就是一句会误导排查的话。
func TestOracleAudit_RecordsSchemaNotServiceName(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")

	const service = "ORCLPDB2"
	const schema = "G07"
	id := app.oracleConn(token, "dev", "ora-audit", service) // dev:不拦,直接走执行+审计

	app.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
		"connectionId": id, "sql": "SELECT 1 FROM dual", "reason": "", "database": schema,
	})

	r := app.do(http.MethodGet, "/api/v1/audit?risk=&page=1&pageSize=50", token, nil)
	var page struct {
		Items []struct {
			Instance string `json:"instance"`
			Database string `json:"database"`
			Command  string `json:"command"`
		} `json:"items"`
	}
	if err := json.Unmarshal(r.Data, &page); err != nil {
		t.Fatalf("decode audit: %v", err)
	}
	for _, it := range page.Items {
		if it.Instance != "ora-audit" {
			continue
		}
		if it.Database == service {
			t.Fatalf("审计把服务名 %q 记成了目标库 —— 事后没人看得出这条语句落在哪个 schema", service)
		}
		if it.Database != schema {
			t.Errorf("审计的目标库应当是 %q,实际 %q", schema, it.Database)
		}
		return
	}
	t.Fatal("审计里没有这台实例的记录")
}

// 后台执行同理:任务记下来的必须是 schema,否则 worker 起来时那个 schema 已经
// 找不回来了 —— 语句会悄悄落在登录用户自己的 schema 上,而没有任何报错。
func TestOracleAsyncJob_RecordsSchemaNotServiceName(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")

	const service = "ORCLPDB3"
	const schema = "G09"
	id := app.oracleConn(token, "dev", "ora-async", service)

	if r := app.do(http.MethodPost, "/api/v1/terminal/exec-async", token, map[string]any{
		"connectionId": id, "sql": "SELECT 1 FROM dual", "database": schema, "reason": "后台跑",
	}); r.Code != 0 {
		t.Fatalf("提交后台任务: code=%d msg=%s", r.Code, r.Msg)
	}

	r := app.do(http.MethodGet, "/api/v1/async-jobs", token, nil)
	var jobs []struct {
		Instance string `json:"instance"`
		Database string `json:"database"`
	}
	if err := json.Unmarshal(r.Data, &jobs); err != nil {
		t.Fatalf("decode jobs: %v", err)
	}
	for _, j := range jobs {
		if j.Instance != "dev-ora-async" {
			continue
		}
		if j.Database == service {
			t.Fatalf("后台任务把服务名 %q 记成了目标库", service)
		}
		if j.Database != schema {
			t.Errorf("后台任务的目标库应当是 %q,实际 %q", schema, j.Database)
		}
		return
	}
	t.Fatal("没有找到这台实例的后台任务")
}
