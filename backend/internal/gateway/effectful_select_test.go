package gateway

import (
	"testing"

	"aegisdb/internal/model"
)

// 动词是 SELECT,不等于这条语句只读。
//
// 按首动词判,`SELECT pg_terminate_backend(…)` 是一次读 —— 只读角色就能踢掉生产库上
// 别人的连接;`SELECT dblink_exec('…', 'DROP TABLE t')` 能借另一条连接执行任意 SQL;
// `SELECT setval(…)` 改序列。这些都按**写**判,交给能力矩阵决定。
func TestEvaluate_SelectCallingAnEffectfulFunctionIsAWrite(t *testing.T) {
	e := NewRiskEngine(&fakeStore{caps: map[string]string{"write|prod": model.LevelDeny}})
	for _, sql := range []string{
		"SELECT pg_terminate_backend(12345)",
		"select pg_catalog.pg_cancel_backend(pid) from pg_stat_activity",
		"SELECT dblink_exec('host=x', 'DROP TABLE t')",
		`SELECT "dblink_exec"('host=x', 'DROP TABLE t')`,
		"SELECT setval('seq_order', 1)",
		"SELECT lo_export(12, '/tmp/x')",
		"SELECT pg_reload_conf()",
		"SELECT load_file('/etc/passwd')",
		"SELECT utl_http.request('http://x') FROM dual",
		"SELECT * FROM OPENROWSET(BULK 'c:/x.txt', SINGLE_CLOB) AS t",
	} {
		v := e.EvaluateFor([]int64{1}, "PostgreSQL 15", "prod", sql)
		if v.Action != ActionDeny {
			t.Errorf("EvaluateFor(%q) = %s;调用了有副作用的函数,应按写判(write=deny)", sql, v.Action)
		}
	}
	// 同名的列、字符串里的函数名都不算调用。
	for _, sql := range []string{
		"SELECT setval_count FROM t",
		"SELECT 'pg_terminate_backend(1)' AS s",
		"SELECT id FROM t WHERE note = 'dblink_exec(x)'",
	} {
		if v := e.EvaluateFor([]int64{1}, "PostgreSQL 15", "prod", sql); v.Action != ActionAllow {
			t.Errorf("EvaluateFor(%q) = %s;没有调用任何有副作用的函数", sql, v.Action)
		}
	}
}

// 业务查询有自己更严的函数黑名单,但它在抹掉引号内容的文本上匹配:`"dblink_exec"(…)`
// 里的函数名被抹成空格,黑名单就看不见了。PG 里引号包着的函数名照样能调用。
func TestBizReadOnlyCheck_QuotedFunctionNameIsStillAFunction(t *testing.T) {
	for _, q := range []string{
		`SELECT "dblink_exec"('host=x', 'DROP TABLE t')`,
		`SELECT "pg_terminate_backend"(1)`,
	} {
		if BizReadOnlyCheck("PostgreSQL 15", q, "", nil) == nil {
			t.Errorf("业务查询放行了 %q", q)
		}
	}
}
