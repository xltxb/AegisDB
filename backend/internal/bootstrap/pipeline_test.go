package bootstrap

// Black-box coverage for the two features that ship together: the 规范审查规则库
// and the 发布流水线 (CI/CD). Everything here drives the real HTTP seam, so a
// route, a guard or a runner transition that breaks shows up as a failing
// assertion rather than as a green suite over dead code.

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"time"
)

// ---------------------------------------------------------------- helpers

type stageView struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	Status     string `json:"status"`
	Log        string `json:"log"`
	Findings   string `json:"findings"`
	ApprovalNo string `json:"approvalNo"`
	ApprovalID int64  `json:"approvalId"`
}

type releaseView struct {
	ID     int64       `json:"id"`
	RelNo  string      `json:"relNo"`
	Title  string      `json:"title"`
	Status string      `json:"status"`
	Risk   string      `json:"risk"`
	Error  string      `json:"error"`
	Stages []stageView `json:"stages"`
}

// createPipeline saves a flow template and returns its id.
func (a *testApp) createPipeline(token, name, tier string, stages []map[string]any) int64 {
	a.t.Helper()
	r := a.do(http.MethodPost, "/api/v1/pipelines", token, map[string]any{
		"name": name, "tierCode": tier, "enabled": true, "stages": stages,
	})
	if r.Code != 0 {
		a.t.Fatalf("create pipeline: code=%d msg=%s", r.Code, r.Msg)
	}
	var out struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(r.Data, &out); err != nil {
		a.t.Fatalf("pipeline decode: %v", err)
	}
	return out.ID
}

func (a *testApp) submitRelease(token string, body map[string]any) apiResp {
	a.t.Helper()
	return a.do(http.MethodPost, "/api/v1/releases", token, body)
}

func (a *testApp) getRelease(token string, id int64) releaseView {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/releases/"+itoa(id), token, nil)
	if r.Code != 0 {
		a.t.Fatalf("get release: code=%d msg=%s", r.Code, r.Msg)
	}
	var v releaseView
	if err := json.Unmarshal(r.Data, &v); err != nil {
		a.t.Fatalf("release decode: %v", err)
	}
	return v
}

// waitRelease polls until the run reaches one of the wanted states. The runner
// is asynchronous by design, so a test that read the row once would be asserting
// on whichever moment it happened to catch.
func (a *testApp) waitRelease(token string, id int64, want ...string) releaseView {
	a.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var last releaseView
	for time.Now().Before(deadline) {
		last = a.getRelease(token, id)
		if slices.Contains(want, last.Status) {
			return last
		}
		time.Sleep(40 * time.Millisecond)
	}
	a.t.Fatalf("release %d stayed %q, wanted one of %v (err=%q)", id, last.Status, want, last.Error)
	return last
}

func stageByType(v releaseView, typ string) *stageView {
	for i := range v.Stages {
		if v.Stages[i].Type == typ {
			return &v.Stages[i]
		}
	}
	return nil
}

// ---------------------------------------------------------------- 规范审查

func TestSQLReviewLibraryIsSeededAndChecks(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")

	r := app.do(http.MethodGet, "/api/v1/sql-review/rules", token, nil)
	eq(t, r.Code, 0, "rules code")
	var rules []struct {
		Code    string `json:"code"`
		Level   string `json:"level"`
		Dialect string `json:"dialect"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.Unmarshal(r.Data, &rules); err != nil {
		t.Fatalf("rules decode: %v", err)
	}
	if len(rules) < 20 {
		t.Fatalf("expected the shipped library to be seeded, got %d rules", len(rules))
	}

	// A statement that violates several standards is reported per rule, with the
	// dialect deciding which rules could apply at all.
	res := app.reviewCheck(token, 0, "mysql", "CREATE TABLE Orders (id INT, amount FLOAT) ENGINE=MyISAM;")
	if res.Passed {
		t.Error("a table with no primary key must not pass")
	}
	hit := map[string]bool{}
	for _, f := range res.Findings {
		hit[f.Code] = true
	}
	for _, want := range []string{"ddl.require.primary.key", "mysql.require.innodb", "naming.table.pattern"} {
		if !hit[want] {
			t.Errorf("expected rule %s to fire", want)
		}
	}
	// The same statement judged as Oracle must not carry MySQL-only rules.
	res = app.reviewCheck(token, 0, "oracle", "CREATE TABLE Orders (id INT, amount FLOAT) ENGINE=MyISAM;")
	for _, f := range res.Findings {
		if f.Code == "mysql.require.innodb" {
			t.Error("a MySQL-only rule fired on the Oracle dialect")
		}
	}
}

type checkResult struct {
	Passed   bool `json:"passed"`
	Errors   int  `json:"errors"`
	Findings []struct {
		Code  string `json:"code"`
		Level string `json:"level"`
	} `json:"findings"`
}

func (a *testApp) reviewCheck(token string, connID int64, dialect, sql string) checkResult {
	a.t.Helper()
	r := a.do(http.MethodPost, "/api/v1/sql-review/check", token, map[string]any{
		"connectionId": connID, "dialect": dialect, "sql": sql,
	})
	if r.Code != 0 {
		a.t.Fatalf("review check: code=%d msg=%s", r.Code, r.Msg)
	}
	var out struct {
		Result checkResult `json:"result"`
	}
	if err := json.Unmarshal(r.Data, &out); err != nil {
		a.t.Fatalf("check decode: %v", err)
	}
	return out.Result
}

func TestSQLReviewRuleEditsAreAdminOnly(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	dba := app.login("chenhao@vela.io", "vela123") // l2: may run the check, may not edit

	// A DBA can self-check a change…
	app.reviewCheck(dba, 0, "mysql", "SELECT 1;")
	// …but cannot change what the standards ARE.
	r := app.do(http.MethodPost, "/api/v1/sql-review/rules", dba, map[string]any{
		"code": "no.select.star", "name": "x", "level": "warn", "enabled": true,
	})
	if r.Code == 0 {
		t.Fatal("a non-admin must not be able to create review rules")
	}

	// An admin can, and the code is namespaced so it can never collide with a
	// builtin shipped later.
	r = app.do(http.MethodPost, "/api/v1/sql-review/rules", admin, map[string]any{
		"code": "no.hint", "name": "禁止 SQL_NO_CACHE", "level": "warn", "enabled": true,
		"dialect": "mysql", "category": "perf", "params": `{"pattern":"SQL_NO_CACHE"}`,
	})
	eq(t, r.Code, 0, "create custom rule")
	var created struct {
		ID   int64  `json:"id"`
		Code string `json:"code"`
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(r.Data, &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	eq(t, created.Code, "custom.no.hint", "custom code namespaced")
	eq(t, created.Kind, "regex", "operator-created rules are pattern rules")

	// The custom rule now fires on a real check…
	res := app.reviewCheck(admin, 0, "mysql", "SELECT SQL_NO_CACHE * FROM t;")
	found := false
	for _, f := range res.Findings {
		if f.Code == "custom.no.hint" {
			found = true
		}
	}
	if !found {
		t.Error("the custom rule should fire")
	}
	// …and can be deleted, while a builtin can only be disabled.
	r = app.do(http.MethodDelete, "/api/v1/sql-review/rules/"+itoa(created.ID), admin, nil)
	eq(t, r.Code, 0, "delete custom rule")

	builtinID := app.ruleIDByCode(admin, "ddl.require.primary.key")
	r = app.do(http.MethodDelete, "/api/v1/sql-review/rules/"+itoa(builtinID), admin, nil)
	if r.Code == 0 {
		t.Error("a builtin rule must not be deletable")
	}
	// Disabling it is the supported way, and it takes effect immediately.
	r = app.do(http.MethodPut, "/api/v1/sql-review/rules/"+itoa(builtinID), admin, map[string]any{
		"level": "error", "enabled": false,
	})
	eq(t, r.Code, 0, "disable builtin")
	res = app.reviewCheck(admin, 0, "mysql", "CREATE TABLE t_x (id INT);")
	for _, f := range res.Findings {
		if f.Code == "ddl.require.primary.key" {
			t.Error("a disabled rule must not fire")
		}
	}
}

func (a *testApp) ruleIDByCode(token, code string) int64 {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/sql-review/rules", token, nil)
	var rules []struct {
		ID   int64  `json:"id"`
		Code string `json:"code"`
	}
	_ = json.Unmarshal(r.Data, &rules)
	for _, rule := range rules {
		if rule.Code == code {
			return rule.ID
		}
	}
	a.t.Fatalf("rule %s not found", code)
	return 0
}

// ---------------------------------------------------------------- 发布流水线

func TestReleaseRunsPipelineToSuccess(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	dev := app.connIDByEnv(token, "dev")

	pid := app.createPipeline(token, "测试流程", "", []map[string]any{
		{"name": "规范审查", "type": "review", "config": `{"failOn":"error"}`},
		{"name": "执行变更", "type": "execute"},
		{"name": "结果通知", "type": "notify"},
	})
	r := app.submitRelease(token, map[string]any{
		"title": "新增订单表", "pipelineId": pid, "connectionId": dev,
		"sql": "CREATE TABLE tbl_order_new (\n id BIGINT NOT NULL AUTO_INCREMENT COMMENT '主键',\n" +
			" amount DECIMAL(12,2) NOT NULL DEFAULT 0 COMMENT '金额',\n PRIMARY KEY (id)\n" +
			") ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='订单';",
		"reason": "回归测试",
	})
	eq(t, r.Code, 0, "submit release")
	var rel releaseView
	if err := json.Unmarshal(r.Data, &rel); err != nil {
		t.Fatalf("release decode: %v", err)
	}
	if rel.RelNo == "" {
		t.Fatal("a release must carry a number")
	}

	done := app.waitRelease(token, rel.ID, "success", "failed")
	eq(t, done.Status, "success", "release status")
	for _, st := range done.Stages {
		if st.Status != "success" && st.Status != "skipped" {
			t.Errorf("stage %s ended %s: %s", st.Name, st.Status, st.Log)
		}
	}
	// The review stage keeps its result so a run can be explained later.
	if rv := stageByType(done, "review"); rv == nil || rv.Findings == "" {
		t.Error("the review stage should record its findings")
	}
	// Execution is audited under the release number — a change applied through
	// the pipeline must be as traceable as one typed into the terminal.
	items := app.auditItemsRaw(token, "?range=today&pageSize=100")
	var rows []struct {
		Command    string `json:"command"`
		Result     string `json:"result"`
		ApprovalNo string `json:"approvalNo"`
	}
	_ = json.Unmarshal(items, &rows)
	executed := false
	for _, row := range rows {
		if row.ApprovalNo == done.RelNo && row.Result == "executed" {
			executed = true
		}
	}
	if !executed {
		t.Error("the executed statement should be audited against the release number")
	}
}

func TestReleaseBlockedByReviewError(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	dev := app.connIDByEnv(token, "dev")

	pid := app.createPipeline(token, "严格审查", "", []map[string]any{
		{"name": "规范审查", "type": "review", "config": `{"failOn":"error"}`},
		{"name": "执行变更", "type": "execute"},
	})
	r := app.submitRelease(token, map[string]any{
		"title": "无主键建表", "pipelineId": pid, "connectionId": dev,
		"sql": "CREATE TABLE tbl_no_pk (id INT);",
	})
	eq(t, r.Code, 0, "submit release")
	var rel releaseView
	_ = json.Unmarshal(r.Data, &rel)

	done := app.waitRelease(token, rel.ID, "success", "failed")
	eq(t, done.Status, "failed", "review must stop the release")
	// The execute stage must never have started: that is the whole point.
	if ex := stageByType(done, "execute"); ex == nil || ex.Status != "pending" {
		t.Errorf("execute stage should not have run, got %+v", ex)
	}
}

func TestReleaseWaitsForApprovalThenExecutes(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	approver := app.login("zhangwei@vela.io", "vela123") // DBA 负责人, on the default chain
	stg := app.connIDByEnv(admin, "staging")

	pid := app.createPipeline(admin, "预发布流程", "", []map[string]any{
		{"name": "规范审查", "type": "review", "config": `{"failOn":"none"}`},
		{"name": "人工审批", "type": "approve"},
		{"name": "执行变更", "type": "execute"},
	})
	r := app.submitRelease(admin, map[string]any{
		"title": "订单表加字段", "pipelineId": pid, "connectionId": stg,
		"sql":    "ALTER TABLE tbl_order ADD COLUMN memo VARCHAR(64) NOT NULL DEFAULT '' COMMENT '备注';",
		"reason": "需求 #123",
	})
	eq(t, r.Code, 0, "submit release")
	var rel releaseView
	_ = json.Unmarshal(r.Data, &rel)

	waiting := app.waitRelease(admin, rel.ID, "waiting", "failed", "success")
	eq(t, waiting.Status, "waiting", "release parks on the approval")
	ap := stageByType(waiting, "approve")
	if ap == nil || ap.ApprovalNo == "" {
		t.Fatalf("approve stage should carry a ticket, got %+v", ap)
	}
	if ex := stageByType(waiting, "execute"); ex.Status != "pending" {
		t.Fatal("nothing may execute while the approval is open")
	}

	// Approve through the ordinary approval channel — the pipeline has no
	// separate decision path, which is what keeps one approval queue.
	dec := app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ApprovalID)+"/approve", approver, nil)
	eq(t, dec.Code, 0, "approve")

	// The sweeper is what notices the decision (see ResumeReleaseApprovals).
	app.svc.ResumeReleaseApprovals()
	done := app.waitRelease(admin, rel.ID, "success", "failed")
	eq(t, done.Status, "success", "release resumes after approval")
	eq(t, stageByType(done, "execute").Status, "success", "execute stage")
}

func TestReleaseRefusedWhenCapabilityDenies(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	dba := app.login("chenhao@vela.io", "vela123") // l2: grant = deny everywhere
	dev := app.connIDByEnv(admin, "dev")

	pid := app.createPipeline(admin, "直接执行", "", []map[string]any{
		{"name": "执行变更", "type": "execute"},
	})
	r := app.submitRelease(dba, map[string]any{
		"title": "授权", "pipelineId": pid, "connectionId": dev,
		"sql": "GRANT SELECT ON sandbox.* TO reporter;",
	})
	if r.Code == 0 {
		t.Fatal("a release must not be a way around the capability matrix")
	}
}

func TestReleaseRequiresApprovalStageWhenVerdictDemandsOne(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	stg := app.connIDByEnv(admin, "staging")

	// No approve stage in the flow, but the change needs one on staging.
	pid := app.createPipeline(admin, "无审批流程", "", []map[string]any{
		{"name": "执行变更", "type": "execute"},
	})
	r := app.submitRelease(admin, map[string]any{
		"title": "加字段", "pipelineId": pid, "connectionId": stg,
		"sql": "ALTER TABLE tbl_order ADD COLUMN memo2 VARCHAR(64) NOT NULL DEFAULT '' COMMENT '备注';",
	})
	if r.Code == 0 {
		t.Fatal("a change that needs approval must not be accepted into a flow that never asks")
	}
}

func TestPipelineTemplatesAreAdminOnly(t *testing.T) {
	app := newTestApp(t)
	dba := app.login("chenhao@vela.io", "vela123")

	// Reading the flows is part of raising a release…
	r := app.do(http.MethodGet, "/api/v1/pipelines", dba, nil)
	eq(t, r.Code, 0, "list pipelines")
	var flows []struct {
		Name   string `json:"name"`
		Stages []struct {
			Type string `json:"type"`
		} `json:"stages"`
	}
	if err := json.Unmarshal(r.Data, &flows); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(flows) < 2 {
		t.Fatalf("the two starter templates should be seeded, got %d", len(flows))
	}
	// …but defining them is configuration.
	r = app.do(http.MethodPost, "/api/v1/pipelines", dba, map[string]any{
		"name": "x", "stages": []map[string]any{{"type": "execute"}},
	})
	if r.Code == 0 {
		t.Fatal("a non-admin must not define release flows")
	}
}

func TestPipelineRejectsUnknownStageType(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	r := app.do(http.MethodPost, "/api/v1/pipelines", admin, map[string]any{
		"name": "坏流程", "enabled": true,
		"stages": []map[string]any{{"name": "?", "type": "deploy-magic"}},
	})
	if r.Code == 0 {
		t.Fatal("an unknown stage type must be refused at save time, not skipped at run time")
	}
}
