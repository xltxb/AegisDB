package service

import (
	"fmt"
	"strings"
	"testing"

	"velagateway/internal/dto"
)

// 审批人读到的摘要要列出**每一条**语句,不只是脚本的前 40 行。
//
// 一条建表语句就有几十行,前 40 行往往只装得下第一条 CREATE TABLE;后面的建表语句只剩
// "共 N 条 · 以下省略 X 行"一句 —— 审批人在批准他没看过的 DDL。
func TestScriptExcerpt_ListsEveryStatement(t *testing.T) {
	var b strings.Builder
	var stmts []dto.ScannedStmt
	for _, name := range []string{"t_order", "t_order_item", "t_payment"} {
		var cols []string
		for i := 0; i < 30; i++ {
			cols = append(cols, fmt.Sprintf("  c%d INT", i))
		}
		sql := "CREATE TABLE " + name + " (\n" + strings.Join(cols, ",\n") + "\n)"
		b.WriteString(sql + ";\n")
		stmts = append(stmts, dto.ScannedStmt{SQL: sql, Risk: "mid"})
	}
	ex := scriptExcerpt("ddl.sql", b.String(), stmts, 0, 3)
	for i, name := range []string{"t_order", "t_order_item", "t_payment"} {
		if want := fmt.Sprintf("%d. [需审批] CREATE TABLE %s (", i+1, name); !strings.Contains(ex, want) {
			t.Errorf("摘要应列出第 %d 条 %q:\n%s", i+1, want, ex)
		}
	}
}

// 清单不能把摘要撑破:摘要要进 MySQL TEXT(64KB)、列表与飞书卡片,上限 8KB。几千条语句的
// 脚本只列到预算为止,注明余数 —— 全文由"查看完整脚本"给。
func TestScriptExcerpt_IndexStaysWithinBudget(t *testing.T) {
	var b strings.Builder
	var stmts []dto.ScannedStmt
	for i := 0; i < 3000; i++ {
		sql := fmt.Sprintf("INSERT INTO t_audit_log_archive (a, b, c) VALUES (%d, 'padding padding padding', 'x')", i)
		b.WriteString(sql + ";\n")
		stmts = append(stmts, dto.ScannedStmt{SQL: sql, Risk: "safe"})
	}
	ex := scriptExcerpt("big.sql", b.String(), stmts, 0, 0)
	if len(ex) > 8*1024 {
		t.Errorf("摘要 %d 字节,超过 8KB", len(ex))
	}
	if !strings.Contains(ex, "另有") {
		t.Error("清单截断时要注明还剩多少条")
	}
}
