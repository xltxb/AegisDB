package bootstrap

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// 2026-10-22 生产反馈:DWS 实例上开着班车,执行仍要审批。PG 系(含 GaussDB(DWS))的窗口放宽
// 走同一套判定:目标库与窗口的库一致、语句不越库(PG 系只拦 dblink)就免审批。别的用户执行同样放行
// —— 窗口是大家共用的门,不限申请人。
func TestExecWindow_DWSRelaxesForEveryone(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	cr := app.do(http.MethodPost, "/api/v1/connections", admin, map[string]any{
		"name": "dws-digiplus-prod-b", "engine": "GaussDB(DWS)", "host": "10.9.0.9:8000", "env": "prod", "policy": "strict", "database": "gaussdb",
	})
	eq(t, cr.Code, 0, "建 DWS 实例")
	var conn struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(cr.Data, &conn)
	eq(t, app.do(http.MethodPatch, "/api/v1/connections/"+itoa(conn.ID), admin, map[string]any{"tags": "orders"}).Code, 0, "打上 L2 够得到的标签")
	const sql = "DROP TABLE dw.tmp_orders_2024"

	before := app.riskCheckIn(admin, conn.ID, sql, "gaussdb")
	eq(t, before.RequiresApproval, true, "开窗前要审批")

	app.openWindow(admin, conn.ID, "gaussdb", time.Now().Add(-time.Minute), time.Now().Add(time.Hour))
	eq(t, app.riskCheckIn(admin, conn.ID, sql, "gaussdb").RequiresApproval, false, "窗口内:申请人免审批")
	l2 := app.login("chenhao@aegisdb.io", "aegisdb123")
	eq(t, app.riskCheckIn(l2, conn.ID, sql, "gaussdb").RequiresApproval, false, "窗口内:别的用户同样免审批")
	// 不带库名(终端没选库)时以连接配置的默认库为准
	eq(t, app.riskCheckIn(l2, conn.ID, sql, "").RequiresApproval, false, "没选库时按连接默认库 gaussdb")
	// 选了别的库:窗口不覆盖,照常审批
	eq(t, app.riskCheckIn(l2, conn.ID, sql, "postgres").RequiresApproval, true, "别的库不在窗口内")
}

// 2026-10-22:一张窗口可以勾同一台实例上的多个库。勾到的每个库都放行,没勾的照常审批;
// 审批单上写清覆盖了哪几个库;回看按全部勾选的库捞语句。
func TestExecWindow_MultipleDatabases(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	cr := app.do(http.MethodPost, "/api/v1/connections", admin, map[string]any{
		"name": "dws-digiplus-prod-b", "engine": "GaussDB(DWS)", "host": "10.9.0.9:8000", "env": "prod", "policy": "strict", "database": "gaussdb",
	})
	eq(t, cr.Code, 0, "建 DWS 实例")
	var conn struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(cr.Data, &conn)
	const sql = "DROP TABLE dw.tmp_orders_2024"

	r := app.do(http.MethodPost, "/api/v1/exec-windows", admin, map[string]any{
		"name": "dws-b", "enabled": true, "connectionId": conn.ID, "databases": []string{"gaussdb", "prod_c66_b", " gaussdb "},
		"kind": "once", "startsAt": time.Now().Add(-time.Minute).Format(time.RFC3339), "endsAt": time.Now().Add(time.Hour).Format(time.RFC3339),
		"reason": "多库发车",
	})
	eq(t, r.Code, 0, "建多库窗口")
	var w struct {
		ID        int64    `json:"id"`
		ApNo      string   `json:"apNo"`
		Database  string   `json:"database"`
		Databases []string `json:"databases"`
	}
	_ = json.Unmarshal(r.Data, &w)
	eq(t, len(w.Databases), 2, "去重后两个库")
	eq(t, w.Database, "gaussdb", "第一个库")

	ap, err := app.repo.GetApprovalByApNo(w.ApNo)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ap.Command, "gaussdb") || !strings.Contains(ap.Command, "prod_c66_b") {
		t.Errorf("审批单上要写清覆盖的全部库,实际 %q", ap.Command)
	}

	// 批准前一个库都不放行
	eq(t, app.riskCheckIn(admin, conn.ID, sql, "prod_c66_b").RequiresApproval, true, "批准前不放行")
	app.decideWindowTicket(w.ApNo, true)
	eq(t, app.riskCheckIn(admin, conn.ID, sql, "gaussdb").RequiresApproval, false, "勾选的库 1 放行")
	eq(t, app.riskCheckIn(admin, conn.ID, sql, "prod_c66_b").RequiresApproval, false, "勾选的库 2 放行")
	eq(t, app.riskCheckIn(admin, conn.ID, sql, "postgres").RequiresApproval, true, "没勾的库照常审批")

	// 列表与详情都带出全部库
	var list []struct {
		ID        int64    `json:"id"`
		Databases []string `json:"databases"`
	}
	_ = json.Unmarshal(app.do(http.MethodGet, "/api/v1/exec-windows", admin, nil).Data, &list)
	found := false
	for _, x := range list {
		if x.ID == w.ID {
			found = true
			eq(t, strings.Join(x.Databases, ","), "gaussdb,prod_c66_b", "列表里的库")
		}
	}
	if !found {
		t.Fatal("列表里应有这张窗口")
	}

	// 改成只勾一个库:另一个立刻不再覆盖(改完回到待审批,批准后按新定义生效)
	up := app.do(http.MethodPut, "/api/v1/exec-windows/"+itoa(w.ID), admin, map[string]any{
		"name": "dws-b", "enabled": true, "connectionId": conn.ID, "databases": []string{"prod_c66_b"},
		"kind": "once", "startsAt": time.Now().Add(-time.Minute).Format(time.RFC3339), "endsAt": time.Now().Add(time.Hour).Format(time.RFC3339),
	})
	eq(t, up.Code, 0, "改窗口")
	_ = json.Unmarshal(up.Data, &w)
	app.decideWindowTicket(w.ApNo, true)
	eq(t, app.riskCheckIn(admin, conn.ID, sql, "prod_c66_b").RequiresApproval, false, "仍勾着的库放行")
	eq(t, app.riskCheckIn(admin, conn.ID, sql, "gaussdb").RequiresApproval, true, "去掉的库不再放行")
}

func TestExecWindow_DatabaseListValidation(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	prod := app.connIDByEnv(admin, "prod")
	mk := func(dbs []string) apiResp {
		return app.do(http.MethodPost, "/api/v1/exec-windows", admin, map[string]any{
			"name": "v", "enabled": true, "connectionId": prod, "databases": dbs, "kind": "once",
			"startsAt": time.Now().Format(time.RFC3339), "endsAt": time.Now().Add(time.Hour).Format(time.RFC3339),
		})
	}
	if mk([]string{" ", ""}).Code == 0 {
		t.Error("一个库都没勾不应建成")
	}
	many := make([]string, 21)
	for i := range many {
		many[i] = "db" + itoa(int64(i))
	}
	if r := mk(many); r.Code == 0 || !strings.Contains(r.Msg, "20") {
		t.Errorf("超过 20 个库应被拒并说明上限,实际 code=%d msg=%s", r.Code, r.Msg)
	}
	// 老客户端只传 database 也认
	r := app.do(http.MethodPost, "/api/v1/exec-windows", admin, map[string]any{
		"name": "old", "enabled": true, "connectionId": prod, "database": "orders_db", "kind": "once",
		"startsAt": time.Now().Format(time.RFC3339), "endsAt": time.Now().Add(time.Hour).Format(time.RFC3339),
	})
	eq(t, r.Code, 0, "只传 database 的老写法")
}
