package gateway

// 客户端命令翻译:psql 反斜杠、mysql 的 \G、SQL*Plus 的 EXEC。
//
// 它们都不是 SQL —— 是说给客户端听的话。网关承担客户端那一半职责,但有两条不能破:
// 翻译只发生在下发前(判定看到的仍是原文),以及翻不了的绝不猜。

import (
	"strings"
	"testing"
)

// ——— psql 反斜杠 ———

func TestPsql_ListingCommandsBecomeCatalogQueries(t *testing.T) {
	cases := map[string]string{
		`\dt`: "pg_class",
		`\dv`: "pg_class",
		`\di`: "pg_class",
		`\ds`: "pg_class",
		`\l`:  "pg_database",
		`\dn`: "pg_namespace",
		`\du`: "pg_roles",
		`\d`:  "pg_class",
	}
	for in, want := range cases {
		got, ok := PostgresPsqlMeta(in)
		if !ok {
			t.Errorf("%q 应当被翻译 —— 服务端从来没见过反斜杠", in)
			continue
		}
		if !strings.Contains(got, want) {
			t.Errorf("%q → %q,期望里带 %s", in, got, want)
		}
	}
}

// \dt \dv \di \ds 只差一个 relkind,不能都翻成同一个东西。
func TestPsql_EachListingLooksAtItsOwnKind(t *testing.T) {
	tbl, _ := PostgresPsqlMeta(`\dt`)
	idx, _ := PostgresPsqlMeta(`\di`)
	if tbl == idx {
		t.Fatal(`\dt 和 \di 翻成了同一条查询 —— 它们看的是不同的对象`)
	}
	if !strings.Contains(tbl, "'r','p'") {
		t.Errorf(`\dt 应当看普通表与分区表,实际: %q`, tbl)
	}
	if !strings.Contains(idx, "'i'") {
		t.Errorf(`\di 应当看索引,实际: %q`, idx)
	}
}

func TestPsql_DescribeATableListsItsColumns(t *testing.T) {
	got, ok := PostgresPsqlMeta(`\d t_user`)
	if !ok {
		t.Fatal(`\d <表> 应当被翻译`)
	}
	if !strings.Contains(got, "information_schema.columns") || !strings.Contains(got, "'t_user'") {
		t.Errorf("应当查这张表的列,实际: %q", got)
	}
}

func TestPsql_DescribeAcceptsSchemaQualifiedNames(t *testing.T) {
	got, _ := PostgresPsqlMeta(`\d app.t_user`)
	if !strings.Contains(got, "'t_user'") || !strings.Contains(got, "'app'") {
		t.Errorf("schema 与表名都要用上,实际: %q", got)
	}
}

// 系统 schema 不该出现在列表里 —— psql 也不列它们。
func TestPsql_ListingsHideSystemSchemas(t *testing.T) {
	got, _ := PostgresPsqlMeta(`\dt`)
	if !strings.Contains(got, "pg_catalog") || !strings.Contains(got, "information_schema") {
		t.Errorf("应当把系统 schema 排除掉,实际: %q", got)
	}
}

// 依赖 psql 自身状态/文件系统的命令不翻译 —— 那些东西在网关这边根本不存在,
// 硬造一个答案等于让人以为自己在跟一个有状态的 psql 会话说话。
func TestPsql_LeavesClientStateCommandsAlone(t *testing.T) {
	for _, in := range []string{`\x`, `\timing`, `\c mydb`, `\i f.sql`, `\copy t TO 'f'`, `\q`, `\e`} {
		if got, ok := PostgresPsqlMeta(in); ok {
			t.Errorf("%q 不该被翻译,却变成了 %q", in, got)
		}
	}
}

// 名字会拼进字符串字面量,所以那条正则是安全边界。形状怪的一律不翻译。
func TestPsql_RefusesAnythingItCannotSafelyQuote(t *testing.T) {
	for _, in := range []string{
		`\d t_user; DROP TABLE t_x`,
		`\d t' OR '1'='1`,
		`\d "Weird Name"`,
		`\dt (SELECT 1)`,
	} {
		if got, ok := PostgresPsqlMeta(in); ok {
			t.Errorf("%q 不该被翻译,却变成了 %q", in, got)
		}
	}
}

func TestPsql_OrdinarySQLIsUntouched(t *testing.T) {
	in := `SELECT * FROM t_user WHERE name LIKE 'a\%'`
	if got, ok := PostgresPsqlMeta(in); ok || got != in {
		t.Errorf("普通 SQL 不该被碰,%q → %q (ok=%v)", in, got, ok)
	}
}

// ——— mysql 的 \G ———

func TestMySQL_TrailingVerticalTerminatorIsStripped(t *testing.T) {
	for _, in := range []string{
		`SELECT * FROM t\G`,
		`SELECT * FROM t \G`,
		`SELECT * FROM t\G;`,
		`SELECT * FROM t\g`,
	} {
		got, ok := MySQLStripVertical(in)
		if !ok {
			t.Errorf("%q 结尾的 \\G 应当被剥掉 —— 那是客户端的显示指令,服务端不认得", in)
			continue
		}
		if strings.Contains(got, `\G`) || strings.Contains(got, `\g`) {
			t.Errorf("%q → %q,还留着终结符", in, got)
		}
		if !strings.HasPrefix(got, "SELECT * FROM t") {
			t.Errorf("%q → %q,语句本体被改坏了", in, got)
		}
	}
}

// 只认结尾。字符串字面量里的反斜杠不能碰。
func TestMySQL_DoesNotTouchBackslashesInsideTheStatement(t *testing.T) {
	for _, in := range []string{
		`SELECT 'a\Gb' FROM t`,
		`SELECT * FROM t WHERE c = 'x\Gy' ORDER BY id`,
		`SELECT 1`,
	} {
		if got, ok := MySQLStripVertical(in); ok {
			t.Errorf("%q 不该被改,却变成了 %q", in, got)
		}
	}
}

// 光一个 \G 不是语句 —— 剥完就空了,原样退回去让服务端说话。
func TestMySQL_ABareTerminatorIsNotAStatement(t *testing.T) {
	if got, ok := MySQLStripVertical(`\G`); ok {
		t.Errorf(`光一个 \G 不该被当成语句,却变成了 %q`, got)
	}
}

// ——— SQL*Plus 的 EXEC ———

func TestOracle_ExecBecomesAnAnonymousBlock(t *testing.T) {
	cases := map[string]string{
		`EXEC my_proc`:            "BEGIN my_proc; END;",
		`EXECUTE my_proc`:         "BEGIN my_proc; END;",
		`exec pkg.my_proc(1, 'a')`: "BEGIN pkg.my_proc(1, 'a'); END;",
		`EXEC my_proc;`:           "BEGIN my_proc; END;",
	}
	for in, want := range cases {
		got, ok := OracleSQLPlus(in)
		if !ok {
			t.Errorf("%q 应当被翻译 —— EXEC 是 SQL*Plus 对 BEGIN…END 的简写", in)
			continue
		}
		if got != want {
			t.Errorf("%q → %q,期望 %q", in, got, want)
		}
	}
}

func TestOracle_ExecRefusesShapesItCannotParse(t *testing.T) {
	for _, in := range []string{
		`EXEC :x := 1`,
		`EXEC DBMS_OUTPUT.PUT_LINE('a'); DROP TABLE t`,
		`EXEC`,
	} {
		if got, ok := OracleSQLPlus(in); ok {
			t.Errorf("%q 不该被翻译,却变成了 %q", in, got)
		}
	}
}

// ——— 分派 ———

// 每个家族只认自己的客户端命令。psql 的反斜杠不该在 MySQL 上被翻译。
func TestClientCommands_AreDispatchedByEngineFamily(t *testing.T) {
	if out := clientCommandSQL("mysql", `\dt`); out != `\dt` {
		t.Errorf(`psql 的反斜杠不该在 MySQL 上被翻译,得到 %q`, out)
	}
	if out := clientCommandSQL("postgres", "SHOW USER"); out != "SHOW USER" {
		t.Errorf("SQL*Plus 的 SHOW 不该在 PostgreSQL 上被翻译,得到 %q", out)
	}
	if out := clientCommandSQL("postgres", `\dt`); out == `\dt` {
		t.Error(`PostgreSQL 上的 \dt 应当被翻译`)
	}
	if out := clientCommandSQL("oracle", "SHOW USER"); out == "SHOW USER" {
		t.Error("Oracle 上的 SHOW USER 应当被翻译")
	}
}
