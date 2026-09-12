package gateway

// Oracle 切 schema:加引号就等于要求**字面匹配**。
//
//	ALTER SESSION SET CURRENT_SCHEMA = "hr"
//
// Oracle 把未加引号的标识符大写化后存进数据字典,所以那里的名字是 HR。加上引号之后
// 它按字面去找 `hr` —— 找不到,切换失败。而用户在对象树上选的正是界面显示给他的那个
// 小写名字。
//
// 不加引号是安全的:schemaIdentRe 已经把 schema 名限制成 `^[A-Za-z][A-Za-z0-9_$#]*$`,
// 引号、空格、分号一个都进不来。它同时也是**用户在 SQL*Plus 里敲的那一行**,行为一致。
//
// 取舍写在这里:真的用引号建出来的混合大小写 schema(`CREATE USER "MySchema"`)从此切不
// 过去。那是 Oracle 上极少见的用法,而它的代价是每一个正常的小写 schema 都切不过去 ——
// 两害相权。

import (
	"testing"

	"velagateway/internal/model"
)

func TestOracleSetSchemaSQL_DoesNotQuoteTheIdentifier(t *testing.T) {
	for _, c := range []struct{ schema, want string }{
		{"hr", `ALTER SESSION SET CURRENT_SCHEMA = hr`},
		{"HR", `ALTER SESSION SET CURRENT_SCHEMA = HR`},
		{"app_owner", `ALTER SESSION SET CURRENT_SCHEMA = app_owner`},
		{"t$x#1", `ALTER SESSION SET CURRENT_SCHEMA = t$x#1`},
	} {
		if got := oracleSetSchemaSQL(c.schema); got != c.want {
			t.Errorf("oracleSetSchemaSQL(%q) = %q,期望 %q", c.schema, got, c.want)
		}
	}
}

// 能走到拼 SQL 那一步的 schema 名,一定先过了 schemaIdentRe —— 这条钉住那个前提,
// 因为不加引号之后,它就是唯一挡住注入的东西。
func TestOracleTargetSchema_RejectsAnythingButAPlainIdentifier(t *testing.T) {
	for _, bad := range []string{
		`hr"; DROP TABLE t --`,
		`hr owner`,
		`"hr"`,
		`1hr`,
		`hr;`,
		``,
	} {
		conn := &model.Connection{Engine: "Oracle 19c", TargetSchema: bad}
		if got := oracleTargetSchema(conn); got != "" {
			t.Errorf("schema %q 不该被接受,实际拿到 %q —— 不加引号之后这里就是唯一的闸", bad, got)
		}
	}
	ok := &model.Connection{Engine: "Oracle 19c", TargetSchema: "app_owner"}
	if got := oracleTargetSchema(ok); got != "app_owner" {
		t.Errorf("正常标识符应当通过,实际 %q", got)
	}
}
