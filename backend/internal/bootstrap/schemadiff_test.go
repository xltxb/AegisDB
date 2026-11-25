package bootstrap

// 跨环境表结构对比的接口(.scratch/schema-diff/issues/03)。两台 SQLite 走完整条链路:
// 可对比的实例清单 → 对比 → 审计;以及拒绝的几种情形。

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"aegisdb/internal/model"
)

type diffResp struct {
	Left, Right struct {
		Instance string `json:"instance"`
		Database string `json:"database"`
		Tables   int    `json:"tables"`
	}
	Diff *struct {
		Summary   map[string]int `json:"summary"`
		OnlyLeft  []string       `json:"onlyLeft"`
		OnlyRight []string       `json:"onlyRight"`
		Changed   []struct {
			Table   string `json:"table"`
			Columns []struct {
				Column string `json:"column"`
				Status string `json:"status"`
			} `json:"columns"`
		} `json:"changed"`
	} `json:"diff"`
	Error     string `json:"error"`
	ErrorSide string `json:"errorSide"`
	ErrorRef  *struct {
		Code string            `json:"code"`
		Args map[string]string `json:"args"`
	} `json:"errorRef"`
	EmptySide string `json:"emptySide"`
}

func sqliteDB(t *testing.T, stmts ...string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "d.db")
	raw, err := sql.Open("sqlite", file)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	for _, s := range stmts {
		if _, err := raw.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	return file
}

func (a *testApp) addConn(token, name, engine, env, host, db, user, pw string) int64 {
	a.t.Helper()
	r := a.do(http.MethodPost, "/api/v1/connections", token, map[string]any{
		"name": name, "engine": engine, "host": host, "env": env, "policy": "audit-only",
		"database": db, "username": user, "password": pw,
	})
	if r.Code != 0 {
		a.t.Fatalf("create conn %s: %s", name, r.Msg)
	}
	var c struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(r.Data, &c)
	return c.ID
}

func TestSchemaDiff_API(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	prod := app.addConn(admin, "orders-prod", "SQLite", "prod", "localhost:0", sqliteDB(t,
		"CREATE TABLE t_order (id INTEGER PRIMARY KEY, code TEXT NOT NULL, note TEXT)",
		"CREATE TABLE t_user (id INTEGER PRIMARY KEY)",
		"CREATE TABLE t_legacy (id INTEGER)"), "", "")
	uat := app.addConn(admin, "orders-uat", "SQLite", "dev", "localhost:0", sqliteDB(t,
		"CREATE TABLE t_order (id INTEGER PRIMARY KEY, code TEXT NOT NULL, note TEXT, remark TEXT)",
		"CREATE TABLE t_user (id INTEGER PRIMARY KEY)",
		"CREATE TABLE t_new (id INTEGER)"), "", "")
	empty := app.addConn(admin, "empty", "SQLite", "dev", "localhost:0", sqliteDB(t), "", "")
	mysqlDown := app.addConn(admin, "mysql-down", "MySQL 8.0", "prod", "127.0.0.1:1", "orders", "u", "p")
	mysqlDown2 := app.addConn(admin, "mysql-down-2", "MySQL 8.0", "dev", "127.0.0.1:2", "orders", "u", "p")

	// 可对比的实例:带引擎族,前端据此只让同一族互选
	var conns []struct {
		ID     int64  `json:"id"`
		Family string `json:"family"`
	}
	_ = json.Unmarshal(app.do(http.MethodGet, "/api/v1/schema-diff/connections", admin, nil).Data, &conns)
	fam := map[int64]string{}
	for _, c := range conns {
		fam[c.ID] = c.Family
	}
	if fam[prod] != "sqlite" || fam[mysqlDown] != "mysql" {
		t.Fatalf("清单应带引擎族: %+v", conns)
	}

	compare := func(l, r int64) (apiResp, diffResp) {
		res := app.do(http.MethodPost, "/api/v1/schema-diff", admin, map[string]any{
			"left": map[string]any{"connectionId": l}, "right": map[string]any{"connectionId": r},
		})
		var out diffResp
		_ = json.Unmarshal(res.Data, &out)
		return res, out
	}

	// 正常对比
	r, out := compare(prod, uat)
	eq(t, r.Code, 0, "对比")
	if out.Diff == nil || strings.Join(out.Diff.OnlyLeft, ",") != "t_legacy" || strings.Join(out.Diff.OnlyRight, ",") != "t_new" ||
		len(out.Diff.Changed) != 1 || out.Diff.Changed[0].Table != "t_order" || out.Diff.Summary["same"] != 1 {
		t.Fatalf("差异不对: %+v", out.Diff)
	}
	if c := out.Diff.Changed[0].Columns; len(c) != 1 || c[0].Column != "remark" || c[0].Status != "onlyRight" {
		t.Errorf("t_order 应只差右边多一列 remark: %+v", c)
	}
	if out.Left.Instance != "orders-prod" || out.Left.Tables != 3 || out.Right.Database != "main" {
		t.Errorf("两边说明: %+v %+v", out.Left, out.Right)
	}
	// 审计:左右两个实例各一条(按实例筛都查得到);operator 留空(没有别人授权);差异计数在命令里
	var rows []model.AuditLog
	app.repo.DB().Where("command LIKE ?", "SCHEMA-DIFF %").Order("id").Find(&rows)
	if len(rows) != 2 || rows[0].ConnectionID == rows[1].ConnectionID {
		t.Fatalf("左右两个实例应各记一条审计: %+v", rows)
	}
	for _, a := range rows {
		if a.Command != "SCHEMA-DIFF orders-prod/main <-> orders-uat/main · 仅左 1 · 仅右 1 · 不同 1 · 一致 1" ||
			a.Result != model.ResultExecuted || a.DataAction != "read" || a.Operator != "" {
			t.Errorf("审计: cmd=%q result=%s action=%s op=%q", a.Command, a.Result, a.DataAction, a.Operator)
		}
	}

	// 引擎不同
	if r, _ := compare(prod, mysqlDown); r.Code != 40001 || !strings.Contains(r.Msg, "引擎不同") {
		t.Errorf("引擎不同应 40001: %d %q", r.Code, r.Msg)
	} else {
		// 拒绝也带原因代码,前端按读者语言显示
		var d struct {
			ReasonRef struct {
				Code string            `json:"code"`
				Args map[string]string `json:"args"`
			} `json:"reasonRef"`
		}
		_ = json.Unmarshal(r.Data, &d)
		if d.ReasonRef.Code != "sdEngineMismatch" || d.ReasonRef.Args["right"] != "MySQL 8.0" {
			t.Errorf("引擎不同应带 reasonRef: %+v", d)
		}
	}
	// 一边读不到:回 0,说清是哪一边、带原因代码,不出半份报告;审计只记失败归类,不记驱动原文
	r, out = compare(mysqlDown, mysqlDown2)
	if r.Code != 0 || out.Diff != nil || out.ErrorSide == "" || out.ErrorRef == nil || out.ErrorRef.Code != "sdReadFailed" || out.ErrorRef.Args["err"] == "" {
		t.Errorf("读失败应回 0 + error + errorRef: %d %+v", r.Code, out)
	}
	var fail model.AuditLog
	app.repo.DB().Where("command LIKE ?", "SCHEMA-DIFF mysql-down%").Order("id DESC").First(&fail)
	if fail.Result != model.ResultWarn || !strings.Contains(fail.Command, "读取失败(连接或读取出错)") || strings.Contains(fail.Command, "127.0.0.1") || fail.Operator != "" {
		t.Errorf("读失败的审计只记归类: cmd=%q op=%q", fail.Command, fail.Operator)
	}
	// 一边一张表都没有:不是错误(新环境可能就是空库),照常出报告,另标出是哪一边
	if r, out := compare(prod, empty); r.Code != 0 || out.Diff == nil || out.EmptySide != "right" || len(out.Diff.OnlyLeft) != 3 {
		t.Errorf("空库应照常出报告并标出: %d %+v", r.Code, out)
	}

	// 没有菜单的角色进不来
	roID := app.roleIDByCode(admin, "ro")
	eq(t, app.do(http.MethodPost, "/api/v1/users", admin, map[string]any{
		"email": "ro@aegisdb.io", "name": "只读", "password": "initpass9", "roleIds": []int64{roID}}).Code, 0, "建只读账号")
	ro := app.login("ro@aegisdb.io", "initpass9")
	if r := app.do(http.MethodPost, "/api/v1/schema-diff", ro, map[string]any{
		"left": map[string]any{"connectionId": prod}, "right": map[string]any{"connectionId": uat}}); r.Code != 40300 {
		t.Errorf("没有 schemadiff 菜单应 40300,实际 %d", r.Code)
	}
}

// 库名下拉:列出实例上的库;读不到回 0 + error(页面退回手填);没有菜单进不来。
func TestSchemaDiff_Databases(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	lite := app.addConn(admin, "lite", "SQLite", "dev", "localhost:0", sqliteDB(t, "CREATE TABLE t (id INTEGER)"), "", "")
	down := app.addConn(admin, "mysql-down", "MySQL 8.0", "prod", "127.0.0.1:1", "orders", "u", "p")

	var out struct {
		Databases []string `json:"databases"`
		Error     string   `json:"error"`
	}
	r := app.do(http.MethodGet, "/api/v1/schema-diff/connections/"+model.Itoa(int(lite))+"/databases", admin, nil)
	_ = json.Unmarshal(r.Data, &out)
	if r.Code != 0 || strings.Join(out.Databases, ",") != "main" || out.Error != "" {
		t.Fatalf("SQLite 只有 main: %d %+v", r.Code, out)
	}
	out.Databases, out.Error = nil, ""
	r = app.do(http.MethodGet, "/api/v1/schema-diff/connections/"+model.Itoa(int(down))+"/databases", admin, nil)
	_ = json.Unmarshal(r.Data, &out)
	if r.Code != 0 || len(out.Databases) != 0 || out.Error == "" {
		t.Errorf("连不上应回 0 + error,页面退回手填: %d %+v", r.Code, out)
	}
	// schema 下拉:只有 PG 系有这一层,别的引擎回空列表
	var sch struct {
		Schemas []string `json:"schemas"`
	}
	r = app.do(http.MethodGet, "/api/v1/schema-diff/connections/"+model.Itoa(int(lite))+"/schemas?database=main", admin, nil)
	_ = json.Unmarshal(r.Data, &sch)
	if r.Code != 0 || sch.Schemas == nil || len(sch.Schemas) != 0 {
		t.Errorf("非 PG 应回空的 schemas: %d %s", r.Code, r.Data)
	}
	if r := app.do(http.MethodGet, "/api/v1/schema-diff/connections/99999/databases", admin, nil); r.Code != 40001 || r.Msg != "实例不存在或无权访问" {
		t.Errorf("实例不存在应 40001: %d %q", r.Code, r.Msg)
	}

	roID := app.roleIDByCode(admin, "ro")
	eq(t, app.do(http.MethodPost, "/api/v1/users", admin, map[string]any{
		"email": "ro2@aegisdb.io", "name": "只读", "password": "initpass9", "roleIds": []int64{roID}}).Code, 0, "建只读账号")
	ro := app.login("ro2@aegisdb.io", "initpass9")
	if r := app.do(http.MethodGet, "/api/v1/schema-diff/connections/"+model.Itoa(int(lite))+"/databases", ro, nil); r.Code != 40300 {
		t.Errorf("没有 schemadiff 菜单应 40300,实际 %d", r.Code)
	}
}
