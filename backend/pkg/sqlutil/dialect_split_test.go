package sqlutil

import "testing"

// 嵌套块注释只有 PostgreSQL 支持,所以它必须按方言开关 —— 一刀切会开一个绕过判定的口子。
//
// MySQL 读 `/* /* */ DROP TABLE t; */` 的方式是:注释到第一个 `*/` 就结束,于是
// **DROP TABLE t 是一条真语句**,最后那个 `*/` 才是语法错。若按嵌套去拆,整段都成了
// 注释 —— 判定层看不见那个 DROP,而服务端照样会执行它。那不是语法适配问题,是把危险
// 语句藏进注释的办法。
//
// 所以默认(未知方言)保持不嵌套:与 MySQL/Oracle 一致,也是更保守的那一侧。
func TestSplitFor_NestedBlockCommentsArePostgresOnly(t *testing.T) {
	const nested = "/* 外 /* 内 */ 仍在注释里 */ SELECT 1;"

	t.Run("postgres 按嵌套读", func(t *testing.T) {
		got := SplitStatementsFor("postgres", nested)
		if len(got) != 1 || got[0] != "SELECT 1" {
			t.Errorf("PG 下应当只剩 SELECT 1,实得 %q", got)
		}
	})

	t.Run("mysql 不嵌套", func(t *testing.T) {
		got := SplitStatementsFor("mysql", nested)
		if len(got) != 1 || got[0] == "SELECT 1" {
			t.Errorf("MySQL 下注释在第一个 */ 就结束,不该只剩 SELECT 1,实得 %q", got)
		}
	})

	t.Run("藏在嵌套注释里的 DROP 在 mysql 下必须可见", func(t *testing.T) {
		got := SplitStatementsFor("mysql", "/* /* */ DROP TABLE t; */")
		var seen bool
		for _, s := range got {
			if s == "DROP TABLE t" {
				seen = true
			}
		}
		if !seen {
			t.Errorf("MySQL 会执行这条 DROP,拆分却把它藏进了注释:%q", got)
		}
	})

	t.Run("默认入口与未知方言一样保守", func(t *testing.T) {
		a := SplitStatements(nested)
		b := SplitStatementsFor("", nested)
		if len(a) != len(b) || (len(a) > 0 && a[0] != b[0]) {
			t.Errorf("默认入口与未知方言不一致:%q vs %q", a, b)
		}
	})
}

// \G 是 mysql 客户端的「执行并竖排输出」,不是 SQL。粘进来时既要当分隔符,也要剥掉 ——
// 原样发给驱动是语法错。
func TestSplitFor_MySQLBackslashG(t *testing.T) {
	got := SplitStatementsFor("mysql", `SELECT 1\G SELECT 2\G`)
	if len(got) != 2 || got[0] != "SELECT 1" || got[1] != "SELECT 2" {
		t.Errorf("实得 %q, 想要 [SELECT 1, SELECT 2]", got)
	}

	// 别的方言里 \G 不是分隔符 —— PG 的字符串里出现它是普通两个字节。
	if pg := SplitStatementsFor("postgres", `SELECT 1\G SELECT 2\G`); len(pg) != 1 {
		t.Errorf("PG 下不该按 \\G 拆,实得 %q", pg)
	}
}
