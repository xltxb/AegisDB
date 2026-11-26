package bootstrap

// 业务方数据查询同样受敏感字段管控:与终端、导出读的是同一张【设置 · 敏感字段】规则表。
//
// 打码(maskResultSet)按选择列表的文本比对,兜得住改名(t AS x)与包函数,兜不住经子查询 /
// UNION / CTE 把敏感字段改名后带出来。终端上这是"顺手看到",导出因此要审批;业务查询没有审批
// 这条路,而业务方拿到的又是可以导出的生产数据 —— 所以这种形态直接拒绝。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"aegisdb/internal/model"
)

func TestBizQuery_SensitiveColumns(t *testing.T) {
	b := newBizEnv(t, 0)
	b.app.addSensitiveColumn(b.admin, "notes", "t", "full")

	type maskedResp struct {
		Rows          [][]string `json:"rows"`
		MaskedColumns []string   `json:"maskedColumns"`
	}
	run := func(q string) (apiResp, maskedResp) {
		r, _ := b.exec(q)
		var out maskedResp
		_ = json.Unmarshal(r.Data, &out)
		return r, out
	}
	plainIn := func(rows [][]string, col int) bool {
		for _, row := range rows {
			if row[col] == "plain" {
				return true
			}
		}
		return false
	}

	// 直接查、改名、包函数:都打码,不拒绝
	for _, q := range []string{
		"SELECT id, t FROM notes",
		"SELECT id, t AS x FROM notes",
		"SELECT id, UPPER(t) AS x FROM notes",
	} {
		r, out := run(q)
		if r.Code != 0 {
			t.Fatalf("%s:应放行并打码,实际 code=%d msg=%q", q, r.Code, r.Msg)
		}
		if len(out.Rows) != 2 || plainIn(out.Rows, 1) || strings.EqualFold(out.Rows[1][1], "PLAIN") {
			t.Errorf("%s:敏感列应打码,实际 %v", q, out.Rows)
		}
		if len(out.MaskedColumns) != 1 {
			t.Errorf("%s:应报告打码的列,实际 %v", q, out.MaskedColumns)
		}
	}

	// 经子查询 / CTE / UNION 改名:打码兜不住,拒绝,并写审计
	var before int64
	b.app.repo.DB().Model(&model.AuditLog{}).Where("result = ?", model.ResultRejected).Count(&before)
	evasions := []string{
		"SELECT x FROM (SELECT t AS x FROM notes) s",
		"WITH n AS (SELECT t AS x FROM notes) SELECT x FROM n",
		"SELECT 'a' AS x UNION ALL SELECT t FROM notes",
	}
	for _, q := range evasions {
		r, out := run(q)
		var ref struct {
			ReasonRef struct {
				Code string            `json:"code"`
				Args map[string]string `json:"args"`
			} `json:"reasonRef"`
		}
		_ = json.Unmarshal(r.Data, &ref)
		if r.Code != 40300 || ref.ReasonRef.Code != model.BizSensitiveEvasion || ref.ReasonRef.Args["column"] != "t" {
			t.Errorf("%s:应以 40300 / %s 拒绝,实际 code=%d ref=%+v msg=%q rows=%v",
				q, model.BizSensitiveEvasion, r.Code, ref.ReasonRef, r.Msg, out.Rows)
		}
	}
	var after int64
	b.app.repo.DB().Model(&model.AuditLog{}).Where("result = ?", model.ResultRejected).Count(&after)
	eq(t, after-before, int64(len(evasions)), "被拒的改名查询都写审计")

	// 导出走同一道判定
	eq(t, b.app.do(http.MethodPut, "/api/v1/settings", b.admin, map[string]any{"bizQuery.exportEnabled": true}).Code, 0, "开导出")
	g := b.body("biz@corp.io", "OA-SENS")
	g["allowExport"] = true
	grantOf(t, b.put(g))
	if _, _, body := b.export(evasions[0]); !strings.Contains(string(body), model.BizSensitiveEvasion) {
		t.Errorf("导出改名查询应同样拒绝,实际 %s", body)
	}
	if _, ct, body := b.export("SELECT id, t FROM notes"); !strings.HasPrefix(ct, "text/csv") || strings.Contains(string(body), "plain") {
		t.Errorf("导出直接查询应放行且打码,实际 %s %s", ct, body)
	}

	// 没提到敏感字段的 CTE 照常执行 —— 这道闸只拦"可能把敏感字段改名带出"的那一种
	if r, out := run("WITH o AS (SELECT id FROM orders WHERE id <= 3) SELECT id FROM o"); r.Code != 0 || len(out.Rows) != 3 {
		t.Errorf("与敏感字段无关的 CTE 不该拒绝,实际 code=%d msg=%q rows=%v", r.Code, r.Msg, out.Rows)
	}
}

// 数据查询页的"表"下拉:只列授权库里的表;没有授权的库连有哪些表都不给看。
func TestBizQuery_Tables(t *testing.T) {
	b := newBizEnv(t, 0)
	var out struct {
		Tables    []string `json:"tables"`
		Truncated bool     `json:"truncated"`
		Error     string   `json:"error"`
	}
	r := b.app.do(http.MethodGet, "/api/v1/bizquery/tables?sourceId="+itoa(b.source)+"&database="+b.db, b.user, nil)
	eq(t, r.Code, 0, "有授权的库列得出表")
	_ = json.Unmarshal(r.Data, &out)
	if strings.Join(out.Tables, ",") != "notes,orders" || out.Truncated || out.Error != "" {
		t.Errorf("应列出 notes、orders,实际 %+v", out)
	}

	r = b.app.do(http.MethodGet, "/api/v1/bizquery/tables?sourceId="+itoa(b.source)+"&database=other_db", b.user, nil)
	var ref struct {
		ReasonRef struct {
			Code string `json:"code"`
		} `json:"reasonRef"`
	}
	_ = json.Unmarshal(r.Data, &ref)
	if r.Code != 40300 || ref.ReasonRef.Code != model.BizNoGrant {
		t.Errorf("没有授权的库应以 40300 / bizNoGrant 拒绝,实际 code=%d ref=%s", r.Code, ref.ReasonRef.Code)
	}

	// 授权撤销后立即看不到
	g, _ := grantOf(t, b.put(b.body("biz@corp.io", "OA-100")))
	eq(t, b.app.do(http.MethodPost, "/api/v1/query-grants/"+itoa(g.ID)+"/revoke", b.admin, map[string]any{"reason": "测试"}).Code, 0, "撤销授权")
	if r := b.app.do(http.MethodGet, "/api/v1/bizquery/tables?sourceId="+itoa(b.source)+"&database="+b.db, b.user, nil); r.Code != 40300 {
		t.Errorf("撤销后应拒绝,实际 %d", r.Code)
	}
}

// 2026-10-26:数据查询页自动补全取列。判定与"表"下拉相同;限定名不许指向授权库以外的库 / 系统库。
func TestBizQuery_Columns(t *testing.T) {
	b := newBizEnv(t, 0)
	get := func(db, table string) (apiResp, []string) {
		r := b.app.do(http.MethodGet, "/api/v1/bizquery/columns?sourceId="+itoa(b.source)+"&database="+db+"&table="+table, b.user, nil)
		var out struct {
			Columns []string `json:"columns"`
		}
		_ = json.Unmarshal(r.Data, &out)
		return r, out.Columns
	}
	r, cols := get(b.db, "orders")
	eq(t, r.Code, 0, "有授权的库取得到列")
	if len(cols) == 0 || cols[0] != "id" {
		t.Errorf("应按建表顺序返回 orders 的列,实际 %v", cols)
	}
	if r, _ := get("other_db", "orders"); r.Code != 40300 {
		t.Errorf("没有授权的库应 40300,实际 %d", r.Code)
	}
	// PostgreSQL 的限定名是库里面的 schema,不是"别的库"(跨库检查只做 MySQL 系 / Oracle):
	// 指向一个不存在的 schema 不拒绝,但也不能读出别处的列。
	if r, cols := get(b.db, "other_db.orders"); r.Code != 0 || len(cols) != 0 {
		t.Errorf("限定名 other_db 在 PG 上只是个不存在的 schema:应 0 且无列,实际 %d %v", r.Code, cols)
	}
	if r, _ := get(b.db, "pg_catalog.pg_class"); r.Code != 40300 {
		t.Errorf("系统 schema 应 40300,实际 %d", r.Code)
	}
	if r, _ := get(b.db, "information_schema.columns"); r.Code != 40300 {
		t.Errorf("系统库应 40300,实际 %d", r.Code)
	}
	if r, _ := get(b.db, "a;b"); r.Code == 0 {
		t.Error("不合法的表名不该放行")
	}
	// 回归:表下拉的响应照旧,只多一个 quote
	var tl struct {
		Tables []string `json:"tables"`
		Quote  string   `json:"quote"`
	}
	_ = json.Unmarshal(b.app.do(http.MethodGet, "/api/v1/bizquery/tables?sourceId="+itoa(b.source)+"&database="+b.db, b.user, nil).Data, &tl)
	if strings.Join(tl.Tables, ",") != "notes,orders" || tl.Quote == "" {
		t.Errorf("表下拉应照旧并带 quote,实际 %+v", tl)
	}
}

// MySQL 系 / Oracle:限定名必须就是授权的那个库 —— 别的库连列名都不该看到。这条判定在
// 连目标库之前,所以一个连不上的 MySQL 查询源(缓存里有库名,可授权)就够钉住它。
func TestBizQuery_Columns_QualifierMustBeTheGrantedDatabaseOnMySQL(t *testing.T) {
	b := newBizEnv(t, 0)
	src := b.newRealSource(t, "mysql-down", map[string]any{
		"engine": "MySQL 8.0", "host": "127.0.0.1", "port": 1, "username": "u", "password": "p",
	})
	b.seedMeta(t, src, "granted_db")
	body := b.body("biz@corp.io", "OA-MYSQL-1")
	body["sourceId"], body["database"] = src, "granted_db"
	eq(t, b.put(body).Code, 0, "缓存里的库能授权(实例连不上时退回缓存)")

	get := func(table string) apiResp {
		return b.app.do(http.MethodGet, "/api/v1/bizquery/columns?sourceId="+itoa(src)+"&database=granted_db&table="+table, b.user, nil)
	}
	if r := get("other_db.orders"); r.Code != 40300 {
		t.Errorf("限定名指向别的库应 40300,实际 %d %s", r.Code, r.Msg)
	}
	if r := get("information_schema.columns"); r.Code != 40300 {
		t.Errorf("系统库应 40300,实际 %d", r.Code)
	}
	// 限定名就是授权的库:不被拒绝(连不上只回 0 + Error,不是 40300)
	if r := get("granted_db.orders"); r.Code == 40300 {
		t.Errorf("限定名等于授权库不该被拒,实际 %d", r.Code)
	}
}
