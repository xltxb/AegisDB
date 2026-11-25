package bootstrap

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// 一份脚本里只要有一条语句需要审批,整份脚本作为一张审批单提交,批准后一起执行。
//
// 现场(2026-10):上传 6 条 CREATE TABLE 的建表脚本到生产。扫描只看高危字典,CREATE 不在
// 字典里,整份被当成"安全脚本"逐条直接执行;第 1 条撞上能力矩阵的"需审批",就**只为它**
// 建了一张单,后面 5 条既没执行也没进审批。审批人批的那张单里只有第一条 CREATE TABLE。
func TestScript_OneStatementNeedingApprovalSendsTheWholeScript(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	dev, prod := app.sameFileConns(admin, "script-whole")
	script := "CREATE TABLE t_w1 (id INTEGER PRIMARY KEY);\nCREATE TABLE t_w2 (id INTEGER PRIMARY KEY);\nCREATE TABLE t_w3 (id INTEGER PRIMARY KEY);\n"

	// 前提:这条 CREATE 在 prod 上是能力矩阵要审批(不是字典)
	if !app.riskCheck(admin, prod, "CREATE TABLE t_x (id INTEGER)").RequiresApproval {
		t.Skip("种子数据下 admin 在 prod 上建表不需要审批,本用例前提不成立")
	}

	up, _ := app.uploadScript(admin, "ddl.sql", script)
	r := app.do(http.MethodPost, "/api/v1/scripts/execute", admin, map[string]any{"connectionId": prod, "uploadId": up})
	eq(t, r.Code, 42200, "整份脚本应进审批: "+r.Msg)
	for _, tb := range []string{"t_w1", "t_w2", "t_w3"} {
		if app.tableExists(admin, dev, tb) {
			t.Fatalf("提交审批时不该执行任何一条,%s 已经建了", tb)
		}
	}
	ap := lastApproval(t, app)
	if ap.ScriptUploadID != up {
		t.Fatalf("应是一张引用整份脚本的脚本工单,实际: %+v", ap)
	}

	approver := app.login("zhangwei@vela.io", "vela123")
	eq(t, app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/approve", approver, nil).Code, 0, "审批通过")
	eq(t, app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/execute", admin, nil).Code, 0, "执行")
	for _, tb := range []string{"t_w1", "t_w2", "t_w3"} {
		if !app.tableExists(admin, dev, tb) {
			t.Errorf("批准后应一起执行,%s 没建出来", tb)
		}
	}
}

// 扫描报告要和执行说同一句话:要审批的语句标出来,不能把整份显示成"脚本干净"。
func TestScriptScan_ReportsApprovalFromTheFullJudgement(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	_, prod := app.sameFileConns(admin, "script-scan-full")
	if !app.riskCheck(admin, prod, "CREATE TABLE t_x (id INTEGER)").RequiresApproval {
		t.Skip("前提不成立")
	}
	r := app.do(http.MethodPost, "/api/v1/scripts/scan", admin, map[string]any{
		"connectionId": prod, "filename": "ddl.sql", "content": "CREATE TABLE t_s1 (id INTEGER);\nSELECT 1;\n"})
	eq(t, r.Code, 0, "scan")
	var scan struct {
		HasRisky   bool `json:"hasRisky"`
		Statements []struct {
			Risk string `json:"risk"`
		} `json:"statements"`
	}
	_ = json.Unmarshal(r.Data, &scan)
	if !scan.HasRisky || scan.Statements[0].Risk == "safe" {
		t.Errorf("CREATE 在 prod 上要审批,扫描不该报安全: %+v", scan)
	}
}

// 有一条被禁止,整份脚本不执行 —— 前面那些"允许"的语句也不能先跑掉。
func TestScript_OneDeniedStatementRefusesTheWholeScript(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	dev, _ := app.sameFileConns(admin, "script-deny")
	if r := app.execSQL(admin, dev, `CREATE TABLE t_deny (id INTEGER)`); r.Code != 0 {
		t.Fatalf("建表: %s", r.Msg)
	}
	eq(t, app.do(http.MethodPatch, "/api/v1/connections/"+itoa(dev), admin, map[string]any{"tags": "analytics"}).Code, 0, "贴上 ro 够得到的标签")
	ro := app.login("zhaolei@vela.io", "vela123")
	if v := app.riskCheck(ro, dev, "GRANT SELECT ON t_deny TO someone"); v.Action != "deny" {
		t.Skipf("前提不成立:ro 在 dev 上 GRANT 不是 deny(%s)", v.Action)
	}
	app.setSettings(admin, map[string]any{"script.savePath": t.TempDir()}) // 只读账号改不了设置
	r := app.do(http.MethodPost, "/api/v1/scripts/execute", ro, map[string]any{
		"connectionId": dev, "filename": "mixed.sql", "content": "INSERT INTO t_deny VALUES (1);\nGRANT SELECT ON t_deny TO someone;\n"})
	if r.Code == 0 || r.Code == 42200 {
		t.Fatalf("含被禁止语句的脚本应整份拒绝: code=%d msg=%s", r.Code, r.Msg)
	}
	cnt := app.execSQL(admin, dev, `SELECT COUNT(*) AS n FROM t_deny`)
	if !strings.Contains(string(cnt.Data), `"0"`) && !strings.Contains(string(cnt.Data), `[0]`) {
		t.Errorf("被拒的脚本里那条 INSERT 不该执行: %s", cnt.Data)
	}
}
