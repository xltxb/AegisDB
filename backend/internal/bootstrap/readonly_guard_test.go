package bootstrap

// 2026-10-29:只读用户在终端里 SELECT 一个会写表的函数,函数里的写真的落了库(判定层按文本判它是读)。
// 直接执行时只读用户的会话设成数据库层面只读;有写权限的人照旧。

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
)

func TestTerminal_ReadOnlyUserCannotWriteThroughFunction_RealMySQL(t *testing.T) {
	v := os.Getenv("AEGISDB_TEST_MYSQL")
	if v == "" {
		t.Skip("未设置 AEGISDB_TEST_MYSQL")
	}
	p := strings.SplitN(v, "/", 3)
	raw, err := sql.Open("mysql", fmt.Sprintf("%s:%s@tcp(%s)/", p[1], p[2], p[0]))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	for _, s := range []string{"CREATE DATABASE IF NOT EXISTS vz_roguard", "DROP TABLE IF EXISTS vz_roguard.lg", "CREATE TABLE vz_roguard.lg (n INT)",
		"DROP FUNCTION IF EXISTS vz_roguard.f",
		"CREATE FUNCTION vz_roguard.f() RETURNS INT DETERMINISTIC MODIFIES SQL DATA BEGIN INSERT INTO vz_roguard.lg VALUES (1); RETURN 1; END"} {
		if _, err := raw.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	defer raw.Exec("DROP DATABASE vz_roguard")
	count := func() int {
		var n int
		_ = raw.QueryRow("SELECT COUNT(*) FROM vz_roguard.lg").Scan(&n)
		return n
	}

	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	prod := app.addConn(admin, "roguard-prod", "MySQL 8.0", "prod", p[0], "vz_roguard", p[1], p[2])
	eq(t, app.do(http.MethodPatch, "/api/v1/connections/"+itoa(prod), admin, map[string]any{"tags": "analytics"}).Code, 0, "贴上只读用户够得到的标签")

	ro := app.login("zhaolei@aegisdb.io", "aegisdb123") // 研发只读
	type out struct {
		Output string     `json:"output"`
		Failed bool       `json:"failed"`
		Data   [][]string `json:"data"`
	}
	exec := func(token string, conn int64, q string) (apiResp, out) {
		r := app.execSQL(token, conn, q)
		var o out
		_ = json.Unmarshal(r.Data, &o)
		return r, o
	}
	// 判定层仍然判它是读、放行 —— 拦它的是数据库层面的只读会话
	r, o := exec(ro, prod, "SELECT f()")
	if r.Code != 0 || !o.Failed || !strings.Contains(o.Output, "只读执行") {
		t.Fatalf("只读用户经函数写库应被数据库拒绝并说明,实际 code=%d msg=%q %+v", r.Code, r.Msg, o)
	}
	if n := count(); n != 0 {
		t.Fatalf("一行都不该写进去,实际 %d 行", n)
	}
	// 只读用户的日常查询照常
	if r, o := exec(ro, prod, "SELECT COUNT(*) FROM lg"); r.Code != 0 || o.Failed || len(o.Data) != 1 {
		t.Fatalf("只读用户的普通查询应照常,实际 code=%d msg=%q %+v", r.Code, r.Msg, o)
	}

	// 有写权限的人(管理员在 dev 上):照旧,函数照写
	dev := app.addConn(admin, "roguard-dev", "MySQL 8.0", "dev", p[0], "vz_roguard", p[1], p[2])
	if r, o := exec(admin, dev, "SELECT f()"); r.Code != 0 || o.Failed {
		t.Fatalf("有写权限的人应照常执行,实际 code=%d msg=%q %+v", r.Code, r.Msg, o)
	}
	if n := count(); n != 1 {
		t.Fatalf("有写权限时函数应写进 1 行,实际 %d", n)
	}
	// 管理员随后在 dev 上的写照常(同一台实例的连接没有被留成只读)
	if r, o := exec(admin, dev, "INSERT INTO lg VALUES (9)"); r.Code != 0 || o.Failed {
		t.Fatalf("之后的普通写应照常,实际 code=%d msg=%q %+v", r.Code, r.Msg, o)
	}
}
