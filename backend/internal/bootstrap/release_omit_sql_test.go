package bootstrap

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// 发布单脚本创建后不再改。执行中页面每 2.5 秒轮询详情,大脚本每次整段回传、全文脱敏、浏览器整段
// 解析(#163)。?omitSql=1 只回状态与阶段,不带脚本,并标明是故意省掉的(不是脚本为空)。
func TestReleaseDetail_OmitSQLLeavesTheScriptOut(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@aegisdb.io", "aegisdb123")
	dev := app.connIDByEnv(token, "dev")
	pid := app.createPipeline(token, "省脚本", "", []map[string]any{{"name": "执行变更", "type": "execute"}})
	script := "CREATE TABLE t_omit (id BIGINT PRIMARY KEY);\n-- password='s3cret'"
	r := app.submitRelease(token, map[string]any{"title": "omit", "pipelineId": pid, "connectionId": dev, "sql": script, "reason": "t"})
	eq(t, r.Code, 0, "submit release")
	var rel releaseView
	_ = json.Unmarshal(r.Data, &rel)

	type detail struct {
		ID         int64       `json:"id"`
		RelNo      string      `json:"relNo"`
		Status     string      `json:"status"`
		SQL        string      `json:"sql"`
		SQLOmitted bool        `json:"sqlOmitted"`
		Stages     []stageView `json:"stages"`
	}
	get := func(q string) detail {
		t.Helper()
		g := app.do(http.MethodGet, "/api/v1/releases/"+itoa(rel.ID)+q, token, nil)
		eq(t, g.Code, 0, "get release"+q)
		var d detail
		if err := json.Unmarshal(g.Data, &d); err != nil {
			t.Fatal(err)
		}
		return d
	}

	full := get("")
	if !strings.Contains(full.SQL, "CREATE TABLE t_omit") || full.SQLOmitted {
		t.Errorf("不带参数照旧返回脚本:sql=%q omitted=%v", full.SQL, full.SQLOmitted)
	}
	if strings.Contains(full.SQL, "s3cret") {
		t.Errorf("脚本里的口令照旧脱敏:%q", full.SQL)
	}

	lean := get("?omitSql=1")
	if lean.SQL != "" || !lean.SQLOmitted {
		t.Errorf("omitSql=1 不该带脚本,并标明省掉了:sql=%q omitted=%v", lean.SQL, lean.SQLOmitted)
	}
	if lean.RelNo != full.RelNo || lean.Status == "" || len(lean.Stages) != len(full.Stages) {
		t.Errorf("省掉脚本以外的字段照常:%+v vs %+v", lean, full)
	}
}
