package bootstrap

// 终端 Tab 补全用的"一张表有哪些列"(GET /connections/:id/columns)。
//
// 实时查、不走元数据缓存(缓存默认关,开了也可能是昨天的);按建表顺序、保留原始大小写
// (补全插进去的就是它);按库过滤 —— 同名表在别的库里的列不能混进来。

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	_ "github.com/glebarez/go-sqlite"
)

func (a *testApp) columnsOf(token string, conn int64, database, table string) ([]string, apiResp) {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/connections/"+itoa(conn)+"/columns?database="+urlEscape(database)+"&table="+urlEscape(table), token, nil)
	var d struct {
		Columns []string `json:"columns"`
	}
	_ = json.Unmarshal(r.Data, &d)
	return d.Columns, r
}

func TestColumns_LiveInTableOrderWithOriginalCase(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	file := filepath.Join(t.TempDir(), "cols.db")
	db, err := sql.Open("sqlite", file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE Orders (id INTEGER, CustomerName TEXT, amount REAL)"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	r := app.do(http.MethodPost, "/api/v1/connections", admin, map[string]any{
		"name": "cols-sqlite", "engine": "SQLite", "host": "localhost:0", "env": "dev", "policy": "audit-only", "database": file,
	})
	var c struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(r.Data, &c)

	cols, r := app.columnsOf(admin, c.ID, "", "orders")
	eq(t, r.Code, 0, "取列")
	if len(cols) != 3 || cols[0] != "id" || cols[1] != "CustomerName" || cols[2] != "amount" {
		t.Errorf("应按建表顺序、保留大小写:[id CustomerName amount],实际 %v", cols)
	}
	if cols, r := app.columnsOf(admin, c.ID, "", "no_such_table"); r.Code != 0 || len(cols) != 0 {
		t.Errorf("没有这张表:应是空列表而不是报错,实际 %d %v", r.Code, cols)
	}
	if cols, _ := app.columnsOf(admin, c.ID, "", "main.Orders"); len(cols) != 3 {
		t.Errorf("带限定名(main.Orders)也应取得到,实际 %v", cols)
	}
	if _, r := app.columnsOf(admin, c.ID, "", "orders; DROP TABLE Orders"); r.Code == 0 {
		t.Error("不是合法标识符的表名应当拒绝")
	}

	// 访问不到这台实例的人拿不到它的列(新建的实例不带 l2 角色的标签)
	other := app.login("chenhao@vela.io", "vela123")
	if cols, r := app.columnsOf(other, c.ID, "", "orders"); r.Code == 0 && len(cols) > 0 {
		t.Errorf("无权访问的实例不该返回列,实际 %v", cols)
	}
}
