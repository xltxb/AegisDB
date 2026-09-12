package gateway

// `EXPLAIN <DML>` 有结果集,不能按写操作下发。
//
// ParseVerb 把 `EXPLAIN DELETE FROM t WHERE id=1` 解包成 DELETE —— 那是**判定**要的
// 答案(问计划的人和真删的人要过同一道闸的说法不成立,所以判定层用 PlanOnly 短路把它
// 当读放行)。但**执行**这一路读的是同一个动词:IsRead 为 false、KnownVerb 为 true,
// 于是走 Exec。
//
// 结果:判定层按只读放行了它,执行层却把它当写。终端上显示「0 行受影响」,而计划 ——
// 那条语句唯一的产出 —— 被丢掉了。人看到的是「命令没有反应」。
//
// 这正是 ADR 0008 表一里「有结果集却走 Exec」那一行。

import "testing"

func TestIsRead_PlanOnlyExplainRoutesAsRead(t *testing.T) {
	for _, sql := range []string{
		`EXPLAIN DELETE FROM t_orders WHERE id = 1`,
		`EXPLAIN UPDATE t_orders SET flag = 1 WHERE id = 1`,
		`EXPLAIN INSERT INTO t_orders (id) VALUES (1)`,
		`explain  delete from t_orders`,
		`EXPLAIN (FORMAT JSON) DELETE FROM t_orders WHERE id = 1`,
	} {
		if !PlanOnly(sql) {
			t.Fatalf("前置条件不成立:%q 应当是 plan-only", sql)
		}
		if !IsRead(sql) {
			t.Errorf("%q 会被当成写操作下发(走 Exec)—— 计划是它唯一的产出,却被丢掉,"+
				"终端上只剩一句「0 行受影响」", sql)
		}
	}
}

// EXPLAIN ANALYZE 真的会执行被包的那条语句 —— 它不是 plan-only,也就不该按读路由。
// 把它误判成读的后果比丢结果集严重得多:一条真的会删数据的语句被当成查询。
func TestIsRead_ExplainAnalyzeIsNotAPlanOnlyRead(t *testing.T) {
	for _, sql := range []string{
		`EXPLAIN ANALYZE DELETE FROM t_orders WHERE id = 1`,
		`EXPLAIN (ANALYZE) UPDATE t_orders SET flag = 1`,
	} {
		if PlanOnly(sql) {
			t.Errorf("%q 真的会执行,不该被当成 plan-only", sql)
		}
		if IsRead(sql) {
			t.Errorf("%q 真的会删/改数据,不该按只读路由", sql)
		}
	}
}

// MySQL 8 的 `FORMAT=TREE` 只是输出格式,却把动词解析整个带偏了。
//
//	EXPLAIN ANALYZE FORMAT=TREE DELETE FROM t_orders
//
// firstWord 读到 `FORMAT` 就停了(`=` 不是词字符),于是动词是 FORMAT —— 而 ANALYZE
// 意味着这条 DELETE **真的会执行**。verb 不是 delete,无 WHERE 拦截整层跳过:一条
// 真会清空表的语句,既没被严格模式拦下,工单上写的动词还是个 FORMAT。
func TestParseVerb_MySQLExplainFormatOption(t *testing.T) {
	for _, c := range []struct {
		sql       string
		wantVerb  string
		wantPlan  bool
		wantNoWhr bool
	}{
		{`EXPLAIN ANALYZE FORMAT=TREE DELETE FROM t_orders`, "DELETE", false, true},
		{`EXPLAIN ANALYZE FORMAT=JSON UPDATE t_orders SET a=1`, "UPDATE", false, true},
		{`EXPLAIN FORMAT=TREE DELETE FROM t_orders`, "DELETE", true, false}, // 只看计划,不执行
		{`EXPLAIN FORMAT = TRADITIONAL SELECT 1`, "SELECT", true, false},
		{`EXPLAIN ANALYZE FORMAT=TREE DELETE FROM t_orders WHERE id=1`, "DELETE", false, false},
	} {
		if got := ParseVerb(c.sql); got != c.wantVerb {
			t.Errorf("ParseVerb(%q) = %q,期望 %q", c.sql, got, c.wantVerb)
		}
		if got := PlanOnly(c.sql); got != c.wantPlan {
			t.Errorf("PlanOnly(%q) = %v,期望 %v", c.sql, got, c.wantPlan)
		}
		if got := NoWhere(c.sql); got != c.wantNoWhr {
			t.Errorf("NoWhere(%q) = %v,期望 %v —— ANALYZE 那条是真会执行的整表删除",
				c.sql, got, c.wantNoWhr)
		}
	}
}
