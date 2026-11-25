package service

import (
	"testing"

	"aegisdb/internal/gateway"
)

func TestApprovalRows_UnknownIsStoredAsMinusOne(t *testing.T) {
	if got := approvalRows(gateway.ExecResult{Rows: 0, RowsUnknown: true}); got != -1 {
		t.Errorf("数据库没报行数时应记 -1,实际 %d", got)
	}
	if got := approvalRows(gateway.ExecResult{Rows: 7}); got != 7 {
		t.Errorf("报了行数照实记,实际 %d", got)
	}
	if got := approvalRows(gateway.ExecResult{Rows: 0}); got != 0 {
		t.Errorf("真的影响 0 行就记 0,实际 %d", got)
	}
}
