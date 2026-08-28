package gateway

// 结果集取不取,应该由**数据库**说了算,不该由平台按动词表猜。
//
// 起因是一句质疑,而且质疑得对:Web 命令行是直连数据库的,语法本来就由数据库校验,
// 平台凭什么让一条合法命令"没有结果"?
//
// 答案是:平台从没做语法校验,那条 DWS 的 EXPLAIN PERFORMANCE 也确实合法、确实执行
// 了 —— 平台只是**没去取它的结果集**。因为动词表里没有 PERFORMANCE,就按写处理,
// 走了 Exec,结果被丢掉了。
//
// 真正该改的不是"再往动词表里补几个词"(那是没有尽头的:每个引擎都有自己的方言,
// 补漏永远慢一步),而是**认不出来的时候不要猜**。判定仍然需要分类(那是产品存在
// 的理由),但"要不要取结果集"这件事,试一次就知道了。

import (
	"path/filepath"
	"testing"

	"velagateway/internal/model"
)

func sqliteConn(t *testing.T) *model.Connection {
	t.Helper()
	return &model.Connection{Engine: "sqlite", Database: filepath.Join(t.TempDir(), "t.db")}
}

// KnownVerb 划的是"平台认不认得这个动词"的界。认得的照旧按分类走,认不得的才去问
// 数据库 —— 猜错只在认不得的那一格里发生,修就修在那一格。
func TestKnownVerb_MarksWhatTheClassifierActuallyKnows(t *testing.T) {
	known := []string{"SELECT", "INSERT", "UPDATE", "DELETE", "CREATE", "DROP", "ALTER",
		"TRUNCATE", "RENAME", "GRANT", "REVOKE", "SHOW", "DESC", "EXPLAIN", "TABLE", "VALUES"}
	for _, v := range known {
		if !KnownVerb(v) {
			t.Errorf("KnownVerb(%q) = false —— 它在分类表里,不该走问数据库这条路", v)
		}
	}
	// 这些是分类表接不住、只能落进默认档的:正是猜错会发生的地方。
	unknown := []string{"PERFORMANCE", "CALL", "PRAGMA", "VACUUM", "REFRESH", "CHECKSUM", "LISTAGG_WHATEVER"}
	for _, v := range unknown {
		if KnownVerb(v) {
			t.Errorf("KnownVerb(%q) = true,但分类表里并没有它", v)
		}
	}
}

// 平台不认识的动词,只要数据库认、且真的返回了行,就必须把行显示出来。
// 这条用 PRAGMA 做例子:SQLite 认它、它返回结果集,而平台的动词表里没有。
func TestRealRun_UnknownVerbThatReturnsRowsStillShowsThem(t *testing.T) {
	conn := sqliteConn(t)
	if _, err := RealRun(conn, `CREATE TABLE t_demo (id INTEGER PRIMARY KEY, name TEXT)`, 0); err != nil {
		t.Fatalf("建表: %v", err)
	}

	// PRAGMA 不在动词表里 —— 此前会被按写处理,走 Exec,结果被丢掉。
	if KnownVerb(ParseVerb(`PRAGMA table_info(t_demo)`)) {
		t.Skip("PRAGMA 已进入动词表,这条用例需要换一个平台不认识的动词")
	}
	res, err := RealRun(conn, `PRAGMA table_info(t_demo)`, 0)
	if err != nil {
		t.Fatalf("PRAGMA: %v", err)
	}
	if len(res.Columns) == 0 || res.Rows == 0 {
		t.Errorf("平台不认识的动词也该把结果集取回来,实际 columns=%v rows=%d —— 数据库明明返回了行",
			res.Columns, res.Rows)
	}
}

// 反过来:平台不认识、数据库也不返回行的语句,不能因此报错,要如实说"执行成功"。
func TestRealRun_UnknownVerbWithNoRowsReportsSuccess(t *testing.T) {
	conn := sqliteConn(t)
	if _, err := RealRun(conn, `CREATE TABLE t_demo (id INTEGER PRIMARY KEY)`, 0); err != nil {
		t.Fatalf("建表: %v", err)
	}
	res, err := RealRun(conn, `PRAGMA foreign_keys = ON`, 0)
	if err != nil {
		t.Fatalf("PRAGMA 赋值不该报错: %v", err)
	}
	if res.Output == "" {
		t.Error("没有结果集时也要给一句话,否则用户又是对着空白发呆")
	}
}

// 已知的写操作仍然走 Exec —— 那条路能拿到"影响行数",而这是写操作最该看到的东西。
// 认不出来才去问数据库,不是把所有语句都改成问。
func TestRealRun_KnownWritesStillReportRowsAffected(t *testing.T) {
	conn := sqliteConn(t)
	if _, err := RealRun(conn, `CREATE TABLE t_demo (id INTEGER PRIMARY KEY, name TEXT)`, 0); err != nil {
		t.Fatalf("建表: %v", err)
	}
	if _, err := RealRun(conn, `INSERT INTO t_demo (name) VALUES ('a'), ('b')`, 0); err != nil {
		t.Fatalf("插入: %v", err)
	}
	res, err := RealRun(conn, `UPDATE t_demo SET name = 'c'`, 0)
	if err != nil {
		t.Fatalf("更新: %v", err)
	}
	if res.Rows != 2 {
		t.Errorf("已知写操作应报影响行数,实际 %d", res.Rows)
	}
}

// 读操作原样不变。
func TestRealRun_ReadsAreUnchanged(t *testing.T) {
	conn := sqliteConn(t)
	if _, err := RealRun(conn, `CREATE TABLE t_demo (id INTEGER PRIMARY KEY, name TEXT)`, 0); err != nil {
		t.Fatalf("建表: %v", err)
	}
	if _, err := RealRun(conn, `INSERT INTO t_demo (name) VALUES ('a')`, 0); err != nil {
		t.Fatalf("插入: %v", err)
	}
	res, err := RealRun(conn, `SELECT id, name FROM t_demo`, 0)
	if err != nil {
		t.Fatalf("查询: %v", err)
	}
	if len(res.Columns) != 2 || res.Rows != 1 {
		t.Errorf("读操作应返回 2 列 1 行,实际 columns=%v rows=%d", res.Columns, res.Rows)
	}
}
