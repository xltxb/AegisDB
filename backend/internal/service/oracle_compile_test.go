package service

import (
	"testing"

	"velagateway/internal/gateway"
	"velagateway/internal/model"
)

// 2026-10-26:对象树上 Oracle 不传 database,编译请求的 TargetSchema 为空,窗口按目标 schema
// 一扇都找不到,班车时段里点「编译」照样被拒成"需审批"。判定改用被编译对象的 owner。
func TestCompileJudgeConn_UsesOwnerWhenNoSchema(t *testing.T) {
	conn := &model.Connection{ID: 7, Engine: "oracle", Database: "ORCLPDB1"}
	j := compileJudgeConn(conn, " G04 ")
	if got := effectiveDatabase(j); got != "G04" {
		t.Errorf("没带 schema 时应以 owner 判定,实际 %q", got)
	}
	if conn.TargetSchema != "" {
		t.Error("只用于判定:执行用的连接不能被改动")
	}
	// 编译语句本身落在 owner 里,判为不越库 —— 窗口能放行
	stmts, err := gateway.OracleCompileStatements("G04", "package", "PK_AGENT_WALLET")
	if err != nil {
		t.Fatal(err)
	}
	if !staysInDatabase(j, effectiveDatabase(j), stmts) {
		t.Errorf("编译 G04 下的包应判为不越库: %v", stmts)
	}
}

// 回归:请求带了 schema 时照旧以它为准;没有 owner 时不凭空造一个。
func TestCompileJudgeConn_KeepsExplicitSchema(t *testing.T) {
	conn := &model.Connection{Engine: "oracle", Database: "ORCLPDB1", TargetSchema: "SCOTT"}
	if got := effectiveDatabase(compileJudgeConn(conn, "G04")); got != "SCOTT" {
		t.Errorf("已选 schema 不应被 owner 覆盖,实际 %q", got)
	}
	bare := &model.Connection{Engine: "oracle", Database: "ORCLPDB1"}
	if got := effectiveDatabase(compileJudgeConn(bare, "")); got != "" {
		t.Errorf("没有 owner 时应保持为空(不放行),实际 %q", got)
	}
	// 编译别的 schema 的对象:目标是 SCOTT,编 HR 的包仍算越库
	stmts, _ := gateway.OracleCompileStatements("HR", "package", "PK")
	if staysInDatabase(conn, "SCOTT", stmts) {
		t.Error("在 SCOTT 下编译 HR 的包应判为越库")
	}
}
