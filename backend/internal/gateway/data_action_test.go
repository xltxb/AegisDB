package gateway

// 增 / 删 / 改 / 查 的归类。
//
// 这里钉的重点不是"INSERT 是增"那种一眼可见的映射,而是原来那段 eventAction 错掉的三处 ——
// 它们都有同一个形状:**看起来像查,其实在动数据**,而事后追查时"查"正是最不会被细看的
// 那一类。

import "testing"

func TestDataAction_PlainVerbs(t *testing.T) {
	cases := map[string]string{
		"INSERT INTO orders VALUES (1)":       ActionCreate,
		"create table t (id int)":             ActionCreate,
		"DELETE FROM orders WHERE id = 1":     ActionDelete,
		"DROP TABLE orders":                   ActionDelete,
		"TRUNCATE TABLE orders":               ActionDelete,
		"UPDATE orders SET x = 1":             ActionUpdate,
		"ALTER TABLE orders ADD COLUMN m int": ActionUpdate,
		"GRANT SELECT ON db.* TO reporter":    ActionUpdate,
		"REVOKE SELECT ON db.* FROM reporter": ActionUpdate,
		"SELECT * FROM orders":                ActionRead,
		"SHOW TABLES":                         ActionRead,
	}
	for sql, want := range cases {
		if got := DataAction(sql); got != want {
			t.Errorf("DataAction(%q) = %q, want %q", sql, got, want)
		}
	}
}

// 认不出的**不兜底成"查"**。这是原来那段最大的毛病:登录、配置变更、流程标签全被报成
// 查询,于是"查"这一类里混着一大堆根本不是查询的东西,它也就不再说明任何事情。
func TestDataAction_UnrecognisedIsOtherNotRead(t *testing.T) {
	notData := []string{
		"login",
		"admin.role.capabilities role=dba_l2 value={...}",
		"开启执行窗口「九月发版班车」· 2026-09-01 ~ 2026-09-30",
		"RELEASE-CONTINUE REL-11 :: 人工确认「执行变更」放行",
		"COMPILE PACKAGE APP.PKG_ORDERS",
		"",
		"   ",
	}
	for _, cmd := range notData {
		got := DataAction(cmd)
		if got == ActionRead {
			t.Errorf("DataAction(%q) = %q —— 它不是一次查询,把它算成查会让"+
				"「查」这一类不再说明任何事情", cmd, got)
		}
		if got != ActionOther {
			t.Errorf("DataAction(%q) = %q, want %q", cmd, got, ActionOther)
		}
	}
}

// 多语句脚本按**最重的那一条**算。首条是 SELECT、后面藏着 DROP 的脚本,按第一条算会被
// 报成"查" —— 而那正是事后追查最不能出的错。
func TestDataAction_BatchTakesTheHeaviestStatement(t *testing.T) {
	cases := map[string]string{
		"SELECT 1; DROP TABLE orders;":                      ActionDelete,
		"SELECT 1; UPDATE orders SET x=1;":                  ActionUpdate,
		"SELECT 1; INSERT INTO orders VALUES (1);":          ActionCreate,
		"SELECT 1; SELECT 2;":                               ActionRead,
		"INSERT INTO t VALUES (1); DELETE FROM t WHERE 1=1": ActionDelete,
		// 一条真实形态:建表、灌数、再把临时表删掉。最重的是删。
		"create table if not exists t_x (id integer); insert into t_x values (1); drop table t_x": ActionDelete,
	}
	for sql, want := range cases {
		if got := DataAction(sql); got != want {
			t.Errorf("DataAction(%q) = %q, want %q", sql, got, want)
		}
	}
}

// CTE 里藏着的写。ParseVerb 早就处理了它(见 cte_verb_test.go),这里确认归类跟着它走 ——
// `WITH d AS (DELETE … RETURNING …) SELECT …` 在 PostgreSQL 上真的删。
func TestDataAction_CTECarryingAMutation(t *testing.T) {
	cases := map[string]string{
		"WITH t AS (SELECT id FROM users) SELECT * FROM t":                   ActionRead,
		"WITH d AS (DELETE FROM orders RETURNING id) SELECT count(*) FROM d": ActionDelete,
		"WITH t AS (SELECT 1) UPDATE orders SET x=1":                         ActionUpdate,
		"WITH t AS (SELECT 1) INSERT INTO orders SELECT * FROM t":            ActionCreate,
		// 引号里的 delete 不算:它是文本,不是动词。
		"WITH t AS (SELECT 'delete me' AS note FROM users) SELECT note FROM t": ActionRead,
	}
	for sql, want := range cases {
		if got := DataAction(sql); got != want {
			t.Errorf("DataAction(%q) = %q, want %q", sql, got, want)
		}
	}
}

// EXPLAIN 只出计划,什么都不动 —— 是查;EXPLAIN ANALYZE 真的跑,里面是删就是删。
// 只看第一个词的话两者都会被看成 EXPLAIN,而后者会把一次真实的删除记成查询。
func TestDataAction_ExplainVersusExplainAnalyze(t *testing.T) {
	if got := DataAction("EXPLAIN DELETE FROM orders WHERE id = 1"); got != ActionRead {
		t.Errorf("EXPLAIN(不带 ANALYZE)只出计划,应算查,实际 %q", got)
	}
	for _, sql := range []string{
		"EXPLAIN ANALYZE DELETE FROM orders WHERE id = 1",
		"EXPLAIN (ANALYZE) DELETE FROM orders WHERE id = 1",
		"EXPLAIN (ANALYZE, BUFFERS) DELETE FROM orders WHERE id = 1",
	} {
		if got := DataAction(sql); got != ActionDelete {
			t.Errorf("%q 真的会删,应算删,实际 %q —— 记成查等于把一次删除藏起来", sql, got)
		}
	}
}

// 白名单与下拉用的那份清单要自洽。
func TestDataActions_AreSelfConsistent(t *testing.T) {
	seen := map[string]bool{}
	for _, a := range DataActions {
		if seen[a] {
			t.Errorf("%q 重复", a)
		}
		seen[a] = true
		if !IsDataAction(a) {
			t.Errorf("IsDataAction 不认得自己列出来的 %q", a)
		}
	}
	if len(DataActions) != 5 {
		t.Errorf("应当是五类(增删改查 + 其它),实际 %d", len(DataActions))
	}
	if IsDataAction("") || IsDataAction("nope") {
		t.Error("IsDataAction 只该认那五类 —— 空串表示「迁移之前没有这一列」,不是一个结论")
	}
}
