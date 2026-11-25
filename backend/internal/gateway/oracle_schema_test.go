package gateway

import (
	"testing"

	"velagateway/internal/model"
)

// 树上点一个 Oracle schema,要真的切过去。
//
// Oracle 的 Connection.Database 装的是**服务名**,不是 schema,所以 applyTargetDatabase
// 不能像别的引擎那样覆盖它(会连错实例)。选中的 owner 改走 TargetSchema,由执行层在
// 会话上 ALTER SESSION SET CURRENT_SCHEMA。这里钉住"什么样的值会被采纳"。
func TestOracleTargetSchema_AcceptsOnlyPlainIdentifiers(t *testing.T) {
	ok := []string{"SCOTT", "hr", "APP_DATA", "X$Y", "A#B", "u1"}
	for _, s := range ok {
		c := &model.Connection{Engine: "oracle", TargetSchema: s}
		if got := oracleTargetSchema(c); got != s {
			t.Errorf("%q 应被采纳,实际 %q", s, got)
		}
	}

	// 拼进 ALTER SESSION 的东西必须是干净标识符。校验不过就当没选 —— 在登录用户
	// 自己的 schema 里跑,而不是把可疑文本拼进 SQL。
	bad := []string{
		`SCOTT"; DROP TABLE t; --`,
		"SCOTT'",
		"a b",
		"a;b",
		"a.b",   // 带库名前缀的对象名不是 schema 名
		"a-b",
		"1abc",  // 不能数字开头
		"",
		"   ",
	}
	for _, s := range bad {
		c := &model.Connection{Engine: "oracle", TargetSchema: s}
		if got := oracleTargetSchema(c); got != "" {
			t.Errorf("%q 不该被采纳,实际 %q", s, got)
		}
	}
}

// 只对 Oracle 生效:别的引擎换库就是换 Database 字段,不该走这条路。
func TestOracleTargetSchema_OnlyAppliesToOracle(t *testing.T) {
	for _, engine := range []string{"mysql", "postgresql", "sqlite", "tidb", "dws"} {
		c := &model.Connection{Engine: engine, TargetSchema: "SCOTT"}
		if got := oracleTargetSchema(c); got != "" {
			t.Errorf("%s 不该走 Oracle 的 schema 切换,实际 %q", engine, got)
		}
	}
}
