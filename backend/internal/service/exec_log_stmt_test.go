package service

import (
	"strings"
	"testing"
)

// 执行日志回显语句时要先脱敏再截断:截断点落在口令字面量中间时,引号不闭合,
// 读出时的 RedactSecrets 认不出它,口令前半截就留在日志里了。
func TestExecLogStmtRedactsBeforeClipping(t *testing.T) {
	pad := strings.Repeat("x", 465) // 截断点(500 字)正好落在口令里
	stmt := "CREATE USER u_" + pad + " IDENTIFIED BY 'S3cretPassw0rd-very-long-value';"
	got := execLogStmt(stmt)
	if strings.Contains(got, "S3cr") {
		t.Fatalf("password leaked into the execute log echo: %q", got[len(got)-80:])
	}
}

func TestExecLogStmtIndentsEveryLine(t *testing.T) {
	got := execLogStmt("update t\nset a=1\nwhere id=2;")
	want := "    update t\n    set a=1\n    where id=2;\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
