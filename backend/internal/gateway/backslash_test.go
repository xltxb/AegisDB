package gateway

import (
	"context"
	"testing"
	"time"

	"aegisdb/internal/model"
	"aegisdb/pkg/sqlutil"
)

// backslashReading 把 MySQL 对 \' 的理解改写成标准写法,且长度不变。
func TestBackslashReading(t *testing.T) {
	cases := []struct{ in, want string }{
		{`SELECT 'a\'', x AS 'z'`, `SELECT 'a''', x AS 'z'`},
		{`SELECT "a\"", x`, `SELECT "a""", x`},
		{`SELECT 'a\\', x`, `SELECT 'a\\', x`},       // \\ 是一个反斜杠,之后的 ' 照常结束
		{`SELECT 'a\\\'', x`, `SELECT 'a\\''', x`},   // \\ 之后的 \' 仍是转义
		{"SELECT `a\\`, x", "SELECT `a\\`, x"},       // 反引号标识符里没有反斜杠转义
		{`SELECT 'a\_%'`, `SELECT 'a\_%'`},            // 不在引号前的反斜杠不动
		{`SELECT 1 -- 'a\'`, `SELECT 1 -- 'a\'`},      // 注释里的不算
	}
	for _, c := range cases {
		got := backslashReading(c.in)
		if got != c.want {
			t.Errorf("backslashReading(%q)\n got %q\nwant %q", c.in, got, c.want)
		}
		if len(got) != len(c.in) {
			t.Errorf("长度必须不变:%q → %q", c.in, got)
		}
	}
}

// 切分器按 SQL 标准不认 \',于是 MySQL 上 `'a\''` 之后的分号被当成字符串内容,几条语句被
// 并成"一条"交给判定。这不是绕过,靠的是驱动的 multiStatements 保持关闭:服务端会整条拒绝,
// 藏在后面的 DROP 不会执行。这组用例把这个前提钉住 —— 哪天有人打开了多语句,它会先失败。
func TestSplitStatements_BackslashMergeIsRejectedByServer(t *testing.T) {
	merged := `SELECT 'a\''; DROP TABLE biztest.bs_victim; SELECT ''`
	if n := len(sqlutil.SplitStatements(merged)); n != 1 {
		t.Fatalf("前提变了:切分器现在切出 %d 条(本用例描述的是并成 1 条的情形)", n)
	}
	db, done := bizSetup(t)
	defer done()
	if _, err := db.Exec("CREATE TABLE IF NOT EXISTS biztest.bs_victim (n INT)"); err != nil {
		t.Fatal(err)
	}
	conn := testMySQL(t)
	if _, err := RealRun(context.Background(), conn, merged, 5*time.Second); err == nil {
		t.Error("并成一条的多语句应被服务端拒绝(multiStatements 关闭)")
	}
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = 'biztest' AND table_name = 'bs_victim'").Scan(&n); err != nil || n != 1 {
		t.Fatalf("藏在后面的 DROP 不该执行:表还在吗?count=%d err=%v", n, err)
	}
}

// 终端:\' 把表名藏进"字符串"时,按表限定的打码规则照样要生效。
func TestSensitiveMask_BackslashCannotHideTable(t *testing.T) {
	rules := []SensitiveRule{{Table: "t_user", Column: "id_card"}}
	sql := `SELECT 'a\'', id_card AS 'z' FROM t_user WHERE 'q' = 'q'`
	got := SensitiveMaskTargets(sql, []string{"a'", "z"}, rules)
	if len(got) != 1 || got[0] != 1 {
		t.Fatalf("第 2 列(id_card 改名为 z)应当打码,实际 %v", got)
	}
}

// 终端:\' 把高危命令藏进"字符串"时,字典照样要命中。
func TestRiskDictionary_BackslashCannotHideCommand(t *testing.T) {
	e := NewRiskEngine(&fakeStore{cmds: []model.RiskCommand{{TierCode: "prod", Command: "SLEEP", Level: model.RiskHigh}}})
	cmd, lvl, err := e.matchCommand(`SELECT 'a\'', SLEEP(600) AS 'z'`, "prod")
	if err != nil || cmd != "SLEEP" || lvl != model.RiskHigh {
		t.Fatalf("应命中 SLEEP(high),实际 %q %q %v", cmd, lvl, err)
	}
}

// 终端:CTE 里被 \' 藏起来的写操作,不能被当成读。
func TestIsRead_BackslashCannotHideMutation(t *testing.T) {
	sql := `WITH a AS (SELECT 'x\'' AS c), d AS (DELETE FROM t RETURNING 1) SELECT ' ' FROM a`
	if IsRead(sql) {
		t.Fatal("CTE 里有 DELETE,不能判为读")
	}
}
