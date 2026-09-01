package bootstrap

// 敏感字段脱敏的端到端验证。
//
// 这一组要证的只有一件事,但必须证死:**接口回传的数据本身就是打码后的**。
// 不是前端不显示,不是界面上盖一层 —— 是网关回给浏览器的那个 JSON 里,身份证号
// 已经不在了。让前端去打码等于把明文发到浏览器再请它别显示,抓个包就绕过了,
// 而且它已经躺在浏览器缓存和沿途任何代理的日志里。
//
// 所以下面每条断言都直接查原始响应体:**明文一个字节都不能出现在里面**。

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

type sensitiveRuleView struct {
	ID         int64  `json:"id"`
	TableName  string `json:"tableName"`
	ColumnName string `json:"columnName"`
	MaskStyle  string `json:"maskStyle"`
	Enabled    bool   `json:"enabled"`
}

func (a *testApp) addSensitiveColumn(token, table, column, style string) sensitiveRuleView {
	a.t.Helper()
	r := a.do(http.MethodPost, "/api/v1/sensitive-columns", token, map[string]any{
		"tableName": table, "columnName": column, "maskStyle": style, "enabled": true,
		"note": "端到端用例",
	})
	if r.Code != 0 {
		a.t.Fatalf("新增敏感字段: code=%d msg=%s", r.Code, r.Msg)
	}
	var v sensitiveRuleView
	_ = json.Unmarshal(r.Data, &v)
	return v
}

// execSQL 走终端的执行通道 —— 与人在 Web 命令行里敲下回车走的是同一条路,
// 所以它拿到的响应体就是浏览器会拿到的那一份。
func (a *testApp) execSQL(token string, connID int64, sql string) apiResp {
	a.t.Helper()
	return a.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
		"connectionId": connID, "sql": sql, "reason": "sensitive 用例",
	})
}

// realSQLiteConn 建一个**真的能执行**的连接(种子里的实例都是模拟的,不会真去查库)。
//
// 脱敏发生在结果从数据库读出来的那一刻,所以这组用例必须打到一个真库上 ——
// 拿模拟执行器测脱敏,测的是一段永远不会跑到的代码。
func (a *testApp) realSQLiteConn(token string) int64 {
	return a.realSQLiteConnIn(token, "dev", "sensitive-probe")
}

// sameFileConns 把**同一个库文件**挂成两条连接:一条在 dev(准备数据用,不被拦),
// 一条在 prod(分层决定规则,一条 DROP 就会被拦成审批工单)。
//
// 需要两条是因为生产分层上连 CREATE TABLE 都要审批 —— 用一条 prod 连接就没法
// 先把被测的表建出来。
func (a *testApp) sameFileConns(token, name string) (dev, prod int64) {
	a.t.Helper()
	path := filepath.Join(a.t.TempDir(), name+".db")
	return a.sqliteConnAt(token, "dev", name+"-dev", path), a.sqliteConnAt(token, "prod", name+"-prod", path)
}

func (a *testApp) realSQLiteConnIn(token, env, name string) int64 {
	return a.sqliteConnAt(token, env, name, filepath.Join(a.t.TempDir(), name+".db"))
}

func (a *testApp) sqliteConnAt(token, env, name, path string) int64 {
	a.t.Helper()
	r := a.do(http.MethodPost, "/api/v1/connections", token, map[string]any{
		"name": name, "engine": "sqlite", "host": "local", "port": 0,
		"env": env, "policy": "audit-only", "defaultRole": "dba_l2",
		"database": path,
	})
	if r.Code != 0 {
		a.t.Fatalf("建连接: code=%d msg=%s", r.Code, r.Msg)
	}
	var c struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(r.Data, &c)
	if c.ID == 0 {
		a.t.Fatal("建连接后没拿到 id")
	}
	return c.ID
}

func (a *testApp) sensitiveColumns(token string) []sensitiveRuleView {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/sensitive-columns", token, nil)
	var out []sensitiveRuleView
	_ = json.Unmarshal(r.Data, &out)
	return out
}

func TestSensitive_CRUD(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")

	v := app.addSensitiveColumn(admin, "t_user", "id_card", "partial")
	if v.ID == 0 {
		t.Fatal("新增后应带回 id")
	}
	// 同一张表的同一个字段只有一条规则:再加一次要拒,而不是悄悄多出一条
	// —— 两条规则各自生效时,改了一条会以为改完了。
	if r := app.do(http.MethodPost, "/api/v1/sensitive-columns", admin, map[string]any{
		"tableName": "T_USER", "columnName": "ID_CARD",
	}); r.Code == 0 {
		t.Error("同一 表.字段 重复添加应被拒(且大小写不该绕过)")
	}

	// 编辑:换成全打码并停用
	if r := app.do(http.MethodPut, "/api/v1/sensitive-columns/"+itoa(v.ID), admin, map[string]any{
		"tableName": "t_user", "columnName": "id_card", "maskStyle": "full", "enabled": false,
	}); r.Code != 0 {
		t.Fatalf("编辑失败: %s", r.Msg)
	}
	got := app.sensitiveColumns(admin)
	found := false
	for _, x := range got {
		if x.ID == v.ID {
			found = true
			if x.MaskStyle != "full" || x.Enabled {
				t.Errorf("编辑没生效: %+v", x)
			}
		}
	}
	if !found {
		t.Fatal("编辑后的规则不在列表里")
	}

	// 非法的脱敏方式要拒 —— 存进去会变成"看起来配了、其实按默认打"。
	if r := app.do(http.MethodPost, "/api/v1/sensitive-columns", admin, map[string]any{
		"tableName": "t_x", "columnName": "c", "maskStyle": "reverse",
	}); r.Code == 0 {
		t.Error("非法的脱敏方式应被拒")
	}

	if r := app.do(http.MethodDelete, "/api/v1/sensitive-columns/"+itoa(v.ID), admin, nil); r.Code != 0 {
		t.Fatalf("删除失败: %s", r.Msg)
	}
	for _, x := range app.sensitiveColumns(admin) {
		if x.ID == v.ID {
			t.Error("删除后仍在列表里")
		}
	}
}

// 维护敏感字段是管理动作:配错一条,敏感数据就明文出去了。
func TestSensitive_MutationsAreAdminOnly(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	dba := app.login("chenhao@vela.io", "vela123") // l2

	v := app.addSensitiveColumn(admin, "t_user", "id_card", "partial")

	if r := app.do(http.MethodPost, "/api/v1/sensitive-columns", dba, map[string]any{
		"tableName": "t_user", "columnName": "phone",
	}); r.Code == 0 {
		t.Error("非管理员不该能新增敏感字段规则")
	}
	if r := app.do(http.MethodPut, "/api/v1/sensitive-columns/"+itoa(v.ID), dba, map[string]any{
		"tableName": "t_user", "columnName": "id_card", "enabled": false,
	}); r.Code == 0 {
		t.Error("非管理员不该能停用脱敏规则 —— 那等于自己给自己解除脱敏")
	}
	if r := app.do(http.MethodDelete, "/api/v1/sensitive-columns/"+itoa(v.ID), dba, nil); r.Code == 0 {
		t.Error("非管理员不该能删除脱敏规则")
	}
	// 但读得到:被脱敏的列在结果里是一串星号,"为什么这列看不到"要有个自己查得到
	// 的答案。
	if r := app.do(http.MethodGet, "/api/v1/sensitive-columns", dba, nil); r.Code != 0 {
		t.Errorf("非管理员应能查看规则列表: %s", r.Msg)
	}
}

// 这条是整个功能的核心断言:查出来的结果里,明文一个字节都不能有。
func TestSensitive_ResponseCarriesMaskedDataNotPlaintext(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	conn := app.realSQLiteConn(admin)

	const plaintext = "110101199003071234"
	// 造一张真表(dev 连接是 sqlite,能真的建表写数据)
	for _, sql := range []string{
		`CREATE TABLE t_person (id INTEGER PRIMARY KEY, name TEXT, id_card TEXT)`,
		`INSERT INTO t_person (name, id_card) VALUES ('张三', '` + plaintext + `')`,
	} {
		if r := app.execSQL(admin, conn, sql); r.Code != 0 {
			t.Fatalf("准备数据失败(%s): %s", sql, r.Msg)
		}
	}

	// 先确认没有规则时是明文 —— 否则后面的断言证明不了是脱敏起了作用。
	before := app.execSQL(admin, conn, `SELECT name, id_card FROM t_person`)
	if !strings.Contains(string(before.Data), plaintext) {
		t.Fatalf("前置条件不成立:没有规则时本应看到明文,实际:\n%s", string(before.Data))
	}

	app.addSensitiveColumn(admin, "t_person", "id_card", "partial")

	after := app.execSQL(admin, conn, `SELECT name, id_card FROM t_person`)
	body := string(after.Data)
	if strings.Contains(body, plaintext) {
		t.Fatalf("响应体里仍有明文身份证号 —— 脱敏必须在服务端完成:\n%s", body)
	}
	if !strings.Contains(body, "张三") {
		t.Errorf("非敏感字段不该被牵连,响应里应还有姓名:\n%s", body)
	}

	// 别名也不能绕过:同一份数据换个列名查出来,照样是打码的。
	aliased := app.execSQL(admin, conn, `SELECT id_card AS whatever FROM t_person`)
	if strings.Contains(string(aliased.Data), plaintext) {
		t.Errorf("用别名查询绕过了脱敏:\n%s", string(aliased.Data))
	}
}

// 停用一条规则,该字段就该恢复明文 —— 开关必须是真的开关,而不是一个不起作用的
// 摆设(那会让人以为已经解除了脱敏,实际没有,或者反过来)。
func TestSensitive_DisabledRuleStopsMasking(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	conn := app.realSQLiteConn(admin)

	const plaintext = "6222021234567890123"
	for _, sql := range []string{
		`CREATE TABLE t_acct (id INTEGER PRIMARY KEY, bank_card_no TEXT)`,
		`INSERT INTO t_acct (bank_card_no) VALUES ('` + plaintext + `')`,
	} {
		if r := app.execSQL(admin, conn, sql); r.Code != 0 {
			t.Fatalf("准备数据失败: %s", r.Msg)
		}
	}
	v := app.addSensitiveColumn(admin, "t_acct", "bank_card_no", "full")
	if strings.Contains(string(app.execSQL(admin, conn, `SELECT bank_card_no FROM t_acct`).Data), plaintext) {
		t.Fatal("规则启用时不该看到明文")
	}

	if r := app.do(http.MethodPut, "/api/v1/sensitive-columns/"+itoa(v.ID), admin, map[string]any{
		"tableName": "t_acct", "columnName": "bank_card_no", "maskStyle": "full", "enabled": false,
	}); r.Code != 0 {
		t.Fatalf("停用失败: %s", r.Msg)
	}
	if !strings.Contains(string(app.execSQL(admin, conn, `SELECT bank_card_no FROM t_acct`).Data), plaintext) {
		t.Error("停用后应恢复明文 —— 否则这个开关是个摆设")
	}
}
