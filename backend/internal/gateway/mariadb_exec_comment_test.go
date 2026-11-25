package gateway

import (
	"context"
	"testing"
	"time"

	"aegisdb/internal/model"
	"aegisdb/pkg/sqlutil"
)

// 2026-11 白盒审计 V-01:MariaDB 的 /*M!…*/ 是可执行注释,从前被当普通注释整段剥掉 —— 判定看到的
// 是空语句,按只读放行,而下发的原文在 MariaDB 上真的执行了注释体。
func TestV01_MariaDBExecComment_BodyIsJudged(t *testing.T) {
	if got := sqlutil.SplitStatements("/*M! DROP TABLE t */"); len(got) != 1 || got[0] != "DROP TABLE t" {
		t.Fatalf("注释体必须进判定文本,实际 %q", got)
	}
	if got := sqlutil.SplitStatementsExec("/*M!100100 DROP TABLE t */"); len(got) != 1 || got[0] != "/*M!100100 DROP TABLE t */" {
		t.Fatalf("下发文本必须保留标记,实际 %q", got)
	}
	if v := ParseVerb("/*M!50000 DROP TABLE t */"); v != "DROP" {
		t.Errorf("动词应为 DROP,实际 %q", v)
	}
	if !IsRead("SELECT 1") {
		t.Error("普通 SELECT 应仍是读")
	}
	if r := ReadSideEffect("SELECT 1 /*M! , SLEEP(5) */"); r == nil {
		t.Error("注释体里的函数要看得见")
	}
	if !WritesServerFile("SELECT 1 /*M! INTO OUTFILE '/tmp/a' */") {
		t.Error("注释体里的 INTO OUTFILE 要看得见")
	}
	if ReadOnlyStatements("MariaDB", "/*M! DROP TABLE t */") {
		t.Error("注释体是写时不能算只读")
	}
	if r := BizReadOnlyCheck("MariaDB", "SELECT 1 /*M! , LOAD_FILE('/etc/passwd') */", "orders", nil); r == nil {
		t.Error("数据查询也要看见注释体里的函数")
	}

	e := NewRiskEngine(&fakeStore{caps: map[string]string{"select|prod": "allow", "ddl|prod": "deny", "write|prod": "deny"}})
	for _, q := range []string{"/*M! DROP TABLE t */", "/*M!50000 DROP TABLE t */", "/* x */ /*M!100100 DELETE FROM t */"} {
		for _, st := range SplitForJudge("MariaDB", q) {
			if v := e.EvaluateFor([]int64{1}, "MariaDB", "prod", st); v.Action != ActionDeny {
				t.Errorf("%q 应被拒,实际 %s", q, v.Action)
			}
		}
		if len(SplitForJudge("MariaDB", q)) == 0 {
			t.Errorf("%q 不能拆成空", q)
		}
	}
}

// 原有行为:普通注释照旧是注释,/*! 与 /*T! 不变;字面量里的 /*M! 是数据。
func TestV01_OrdinaryCommentsUnchanged(t *testing.T) {
	cases := map[string]string{
		"/* M! 这不是可执行注释 */ SELECT 1": "SELECT 1",
		"/*M 没有感叹号 */ SELECT 1":        "SELECT 1",
		"SELECT '/*M! DROP TABLE t */'":     "SELECT '/*M! DROP TABLE t */'",
		"/*!50000 SELECT 1 */":              "SELECT 1",
		"/*T![clustered_index] SELECT 1 */": "SELECT 1",
	}
	for in, want := range cases {
		got := sqlutil.SplitStatements(in)
		if len(got) != 1 || got[0] != want {
			t.Errorf("%q → %q,应为 %q", in, got, want)
		}
	}
}

// 兜底:判定层拆不出语句的内容到不了服务器,不管它是哪种"注释"。
func TestV01_CommentOnlyIsNeverDispatched(t *testing.T) {
	AllowSimulation = true
	defer func() { AllowSimulation = false }()
	ex := NewExecutor()
	for _, q := range []string{"-- 只有注释", "/* 只有注释 */", "/*X! 没见过的写法 */", " ;; "} {
		conn := &model.Connection{Engine: "MariaDB", Name: "t"}
		res := ex.Run(context.Background(), conn, q, time.Second)
		if res.OutputRef == nil || res.OutputRef.Code != model.OutNothingToRun || res.Err != nil {
			t.Errorf("%q 应不下发并回 nothingToRun,实际 %+v", q, res)
		}
	}
}
