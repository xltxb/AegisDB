package sqlutil

// `BEGIN … END;` —— Oracle 的匿名块,和 MySQL 的"开事务"共用一个关键字。
//
// 一个 DBA 往终端里粘的就是这个形状,而且不会顺手加 SQL*Plus 的 '/'。此前它被按
// 块里的分号切成碎片,每一片 Oracle 都单独拒绝 —— 匿名块从控制台根本跑不起来。
//
// 判别式不需要知道引擎:MySQL 的 `BEGIN;` 里 BEGIN 就是整条语句,后面紧跟分隔符;
// Oracle 的 BEGIN 后面跟的是块体,而且整段以 END; 收尾。

import "testing"

func joinedOnce(t *testing.T, sql string) bool {
	t.Helper()
	return len(SplitStatements(sql)) == 1
}

// 匿名块要整块拿走,否则每一片都执行不了。
func TestSplit_AnonymousBeginBlockIsTakenWhole(t *testing.T) {
	blocks := []string{
		"BEGIN p1(); p2(); END;",
		"BEGIN\n  my_proc();\nEND;",
		"BEGIN\n  UPDATE t SET x = 1 WHERE id = 2;\n  COMMIT;\nEND;",
		"begin\n  null;\nend;",
		"BEGIN\n  FOR r IN (SELECT 1 FROM dual) LOOP\n    NULL;\n  END LOOP;\nEND;",
	}
	for _, b := range blocks {
		if !joinedOnce(t, b) {
			t.Errorf("匿名块被切碎了(%d 段):%q", len(SplitStatements(b)), b)
		}
	}
}

// 但 MySQL/PostgreSQL 的开事务不能被当成块 —— 那会把后面真正的语句一起吞掉,
// 而吞掉就是判定被绕过(总纲:过切安全,合并危险)。
func TestSplit_TransactionOpenerIsNotABlock(t *testing.T) {
	cases := []struct {
		sql  string
		want int
	}{
		{"BEGIN; UPDATE t SET x = 1; COMMIT;", 3},
		{"BEGIN WORK; DELETE FROM t; COMMIT;", 3},
		{"begin; drop table x; end;", 3},
	}
	for _, c := range cases {
		if got := len(SplitStatements(c.sql)); got != c.want {
			t.Errorf("SplitStatements(%q) 切成 %d 段, want %d —— 开事务不是 PL/SQL 块,合并会吞掉后面的语句",
				c.sql, got, c.want)
		}
	}
}

// 合并只在"整段输入就是这一个块"时发生:块后面还有别的语句时,宁可过切,
// 也绝不能把后面那条吞进来 —— 那条就没人判过了。
func TestSplit_BeginBlockNeverSwallowsWhatFollows(t *testing.T) {
	sql := "BEGIN p1(); END;\nSELECT 1 FROM dual;"
	got := SplitStatements(sql)
	for _, g := range got {
		if len(g) > 40 {
			g = g[:40]
		}
		t.Logf("  %s", g)
	}
	if len(got) < 2 {
		t.Fatalf("块后面还有语句时不该合并成一条:%v", got)
	}
	// 带 '/' 终结符时才允许整块 + 后续语句并存,这是 Oracle 部署脚本的写法。
	withSlash := "BEGIN p1(); END;\n/\nSELECT 1 FROM dual;"
	if got := SplitStatements(withSlash); len(got) != 2 {
		t.Errorf("带 '/' 的脚本应为 2 条(块 + SELECT),实际 %d: %v", len(got), got)
	}
}
