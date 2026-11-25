package gateway

import (
	"context"
	"strings"
	"testing"
	"time"

	"velagateway/internal/model"
)

// 数据查询里的 EXPLAIN。MySQL / PG 系的 EXPLAIN 在只读事务里本来就能跑;Oracle 的 EXPLAIN PLAN
// 要写 PLAN_TABLE(只读事务里 ORA-01456),看计划又要调 DBMS_XPLAN(判定层不放行 DBMS_ 包),
// 所以由执行层代为完成 —— 判定层只认 EXPLAIN PLAN FOR <只读查询> 这一种写法。
func TestBizReadOnlyCheck_Explain(t *testing.T) {
	ok := []struct{ eng, q string }{
		{"MySQL 8.0", "EXPLAIN SELECT * FROM orders"},
		{"MySQL 8.0", "EXPLAIN FORMAT=TREE SELECT * FROM orders"},
		{"PostgreSQL 16", "EXPLAIN (FORMAT JSON) SELECT * FROM orders"},
		{"Oracle 19c", "EXPLAIN PLAN FOR SELECT * FROM orders WHERE id = 1"},
		{"Oracle 19c", "explain plan for\n  select o.id from orders o join items i on i.oid = o.id;"},
		{"Oracle 19c", "EXPLAIN PLAN FOR WITH t AS (SELECT id FROM orders) SELECT * FROM t"},
	}
	for _, c := range ok {
		if r := BizReadOnlyCheck(c.eng, c.q, "APP", []string{"APP"}); r != nil {
			t.Errorf("%s %q:应放行,实际拒绝 %q", c.eng, c.q, model.RenderBizReason(r))
		}
	}
	deny := []struct{ q, code string }{
		{"EXPLAIN PLAN SET STATEMENT_ID = 'x' FOR SELECT * FROM orders", model.BizExplainForm},
		{"EXPLAIN PLAN FOR DELETE FROM orders", model.BizExplainForm},
		{"EXPLAIN PLAN FOR UPDATE orders SET a = 1", model.BizExplainForm},
		{"EXPLAIN PLAN INTO my_plan FOR SELECT * FROM orders", model.BizExplainForm},
		{"EXPLAIN PLAN FOR", model.BizExplainForm},
		{"EXPLAIN PLAN FOR SELECT * FROM orders FOR UPDATE", model.BizLock},
		{"EXPLAIN PLAN FOR SELECT * FROM sys.user$", model.BizSystemDB},
	}
	for _, c := range deny {
		r := BizReadOnlyCheck("Oracle 19c", c.q, "APP", []string{"APP"})
		if r == nil || r.Code != c.code {
			got := "放行"
			if r != nil {
				got = r.Code
			}
			t.Errorf("%q:应以 %s 拒绝,实际 %s", c.q, c.code, got)
		}
	}
	if in, ok := OracleExplainInner("-- 看计划\nEXPLAIN PLAN FOR SELECT 1 FROM dual ;"); !ok || in != "SELECT 1 FROM dual" {
		t.Errorf("应取出被解释的语句,实际 %q %v", in, ok)
	}
}

func TestOracleBizQuery_ExplainPlan(t *testing.T) {
	conn := testOracle(t)
	res, err := RealBizQuery(context.Background(), conn, "EXPLAIN PLAN FOR SELECT * FROM dual WHERE dummy = 'X'", 100, 20*time.Second)
	if err != nil {
		t.Fatalf("EXPLAIN PLAN 应返回计划,实际报错 %v", err)
	}
	text := make([]string, 0, len(res.Data))
	for _, r := range res.Data {
		text = append(text, r[0])
	}
	all := strings.Join(text, "\n")
	if len(res.Columns) != 1 || !strings.Contains(all, "Plan hash value") || !strings.Contains(all, "DUAL") {
		t.Fatalf("应是 DBMS_XPLAN 的输出,实际 %v\n%s", res.Columns, all)
	}
	// 回滚之后 PLAN_TABLE 里不留网关写的计划
	after, err := RealBizQuery(context.Background(), conn, "SELECT COUNT(*) FROM plan_table WHERE statement_id LIKE 'VELA%'", 1, 10*time.Second)
	if err != nil || len(after.Data) != 1 || after.Data[0][0] != "0" {
		t.Errorf("PLAN_TABLE 里不该留下计划,实际 %v %v", after.Data, err)
	}
}
