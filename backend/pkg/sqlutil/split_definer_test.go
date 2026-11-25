package sqlutil

import "testing"

// 2026-10-26 讨论 12:没有块体的存储程序(单语句的 MySQL 触发器 / 事件 / 函数)在第一个分号就结束了。
// 151d04a 起,只要全文以 END; / END 结尾(常见的是 CASE … END;),它和后面的语句就被整段并成一条,
// 下发即 1064,后面那条也不再单独判定。
func TestSplit_BodylessRoutineDoesNotSwallowNextStatement(t *testing.T) {
	for _, c := range []struct {
		sql  string
		want int
	}{
		{"CREATE EVENT ev ON SCHEDULE EVERY 1 DAY DO DELETE FROM log WHERE ts < NOW();\nUPDATE t SET a = CASE WHEN b = 1 THEN 2 ELSE 3 END;", 2},
		{"CREATE DEFINER=`root`@`%` TRIGGER trg BEFORE INSERT ON t FOR EACH ROW SET NEW.c = NOW();\nUPDATE t SET a = CASE WHEN b = 1 THEN 2 ELSE 3 END;", 2},
		{"INSERT INTO t VALUES (1);\nCREATE DEFINER=`root`@`%` EVENT ev ON SCHEDULE EVERY 1 DAY DO DELETE FROM log;\nUPDATE t SET a = CASE WHEN b = 1 THEN 2 END;", 3},
		{"CREATE TRIGGER trg BEFORE INSERT ON t FOR EACH ROW SET NEW.c = NOW();\nSELECT CASE WHEN 1 = 1 THEN 1 END", 2},
		{"CREATE FUNCTION f(a INT) RETURNS INT DETERMINISTIC RETURN a + 1;\nSELECT CASE WHEN 1 = 1 THEN 1 END", 2},
	} {
		if got := SplitStatements(c.sql); len(got) != c.want {
			t.Errorf("应拆成 %d 条,实际 %d: %q", c.want, len(got), got)
		}
	}
}

// 回归:有块体的存储程序照旧整块交回(151d04a 修的 DELIMITER 二次分句、Oracle 包 / 过程、DEFINER 过程)。
func TestSplit_RoutinesWithBodiesStayWhole(t *testing.T) {
	for _, sql := range []string{
		// DELIMITER 拆出来的一条,END 后没有分号
		"CREATE DEFINER=`root`@`%` FUNCTION f(d INT) RETURNS INT DETERMINISTIC BEGIN DECLARE x INT; SET x = d + 1; RETURN x; END",
		"CREATE PROCEDURE p() BEGIN UPDATE t SET a = CASE WHEN b = 1 THEN 2 END; DELETE FROM t2; END;",
		"CREATE OR REPLACE PACKAGE pk IS\n  PROCEDURE p;\n  c CONSTANT NUMBER := 1;\nEND pk;",
		"CREATE OR REPLACE PROCEDURE p AS\n  v NUMBER;\nBEGIN\n  v := 1;\n  UPDATE t SET a = v;\nEND;",
		"CREATE OR REPLACE TRIGGER trg BEFORE INSERT ON t FOR EACH ROW\nBEGIN\n  :NEW.id := 1;\nEND;",
		"CREATE EVENT ev ON SCHEDULE EVERY 1 DAY DO BEGIN DELETE FROM log; DELETE FROM log2; END",
	} {
		if got := SplitStatements(sql); len(got) != 1 {
			t.Errorf("有块体的存储程序应整块,实际拆成 %d 条: %q", len(got), got)
		}
	}
	// '/' 结尾的 Oracle 脚本照旧按 '/' 切
	if got := SplitStatements("CREATE OR REPLACE PACKAGE pk IS\n  PROCEDURE p;\nEND pk;\n/\nCREATE OR REPLACE PACKAGE BODY pk IS\n  PROCEDURE p IS BEGIN NULL; END;\nEND pk;\n/\n"); len(got) != 2 {
		t.Errorf("包头 + 包体应为 2 条,实际 %d: %q", len(got), got)
	}
}
