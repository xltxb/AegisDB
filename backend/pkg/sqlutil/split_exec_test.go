package sqlutil

import (
	"strings"
	"testing"
)

// 2026-10 生产:TiDB 建库脚本里的 `/*T![auto_rand] AUTO_RANDOM(5) */` 在建好的表上丢了 ——
// 多条语句逐条下发时发的是分句器剥掉注释后的文本,TiDB 的特性注释被当成普通注释整段删了
// (`/*T![clustered_index] CLUSTERED */` 同样丢,表变成非聚簇)。下发文本必须原样保留可执行
// 注释;判定文本照旧去掉标记、保留注释体。
func TestSplitStatementsExec_KeepsExecutableCommentsVerbatim(t *testing.T) {
	src := "/* ilotto_office_staff */\n" +
		"CREATE TABLE `t_a` (\n" +
		"  `id` BIGINT NOT NULL /*T![auto_rand] AUTO_RANDOM(5) */ COMMENT '主键', -- 行尾注释\n" +
		"  PRIMARY KEY (`id`) /*T![clustered_index] CLUSTERED */\n" +
		") /*!50100 ENGINE=InnoDB */ COMMENT='a;b';\n" +
		"-- 收尾注释\n" +
		"CREATE TABLE t_b (id INT);\n" +
		"-- GRANT SELECT ON x TO y;\n"
	judged := SplitStatements(src)
	exec := SplitStatementsExec(src)
	if len(judged) != 2 || len(exec) != 2 {
		t.Fatalf("应拆成 2 条且两份对齐:judged=%d exec=%d\n%q\n%q", len(judged), len(exec), judged, exec)
	}
	for _, keep := range []string{"/*T![auto_rand] AUTO_RANDOM(5) */", "/*T![clustered_index] CLUSTERED */", "/*!50100 ENGINE=InnoDB */", "COMMENT='a;b'"} {
		if !strings.Contains(exec[0], keep) {
			t.Errorf("下发文本应原样保留 %q:\n%s", keep, exec[0])
		}
	}
	for _, gone := range []string{"ilotto_office_staff", "行尾注释"} {
		if strings.Contains(exec[0], gone) {
			t.Errorf("普通注释不必下发 %q:\n%s", gone, exec[0])
		}
	}
	// 判定文本:注释体要看得见(TiDB / MySQL 会执行它),标记去掉
	for _, keep := range []string{"AUTO_RANDOM(5)", "CLUSTERED", "ENGINE=InnoDB"} {
		if !strings.Contains(judged[0], keep) {
			t.Errorf("判定文本应保留注释体 %q:\n%s", keep, judged[0])
		}
	}
	if strings.Contains(judged[0], "/*") {
		t.Errorf("判定文本不应带注释标记:\n%s", judged[0])
	}
	if exec[1] != "CREATE TABLE t_b (id INT)" {
		t.Errorf("第二条:%q", exec[1])
	}
}

// 可执行注释里的分号照旧是分隔符(判定要逐条看);下发时每一段各自补齐标记,单独拿出来仍是
// 一条合法的、版本语义不变的可执行注释。
func TestSplitStatementsExec_SemicolonInsideExecutableComment(t *testing.T) {
	src := "/*!50000 SET @a = 1; SET @b = 2 */;\nSELECT /*T! 1 + */ 1"
	judged := SplitStatements(src)
	exec := SplitStatementsExec(src)
	want := []string{"/*!50000 SET @a = 1 */", "/*!50000 SET @b = 2 */", "SELECT /*T! 1 + */ 1"}
	if len(exec) != len(want) || len(judged) != len(want) {
		t.Fatalf("应拆成 %d 条:judged=%q exec=%q", len(want), judged, exec)
	}
	for i := range want {
		if strings.Join(strings.Fields(exec[i]), " ") != want[i] {
			t.Errorf("第 %d 条下发文本 %q,应为 %q", i+1, exec[i], want[i])
		}
	}
}

// 没有可执行注释时,两份文本完全一致 —— 这次改动不许碰其它语句的下发内容。
func TestSplitStatementsExec_IdenticalWithoutExecutableComments(t *testing.T) {
	for _, src := range []string{
		"SELECT 1; /* x */ UPDATE t SET v = 'a;b' WHERE id = 1 -- c\n; DELETE FROM t WHERE id = 2",
		"DELIMITER $$\nCREATE PROCEDURE p() BEGIN SELECT 1; END$$\nDELIMITER ;\nCALL p()",
		"SELECT $$a;b$$; SELECT E'x\\'y'",
	} {
		judged, exec := SplitStatements(src), SplitStatementsExec(src)
		if strings.Join(judged, "\x00") != strings.Join(exec, "\x00") {
			t.Errorf("%q:\njudged=%q\nexec  =%q", src, judged, exec)
		}
	}
}
