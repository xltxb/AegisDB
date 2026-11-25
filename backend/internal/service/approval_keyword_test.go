package service

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// 审批单 keyword 列宽 32:无论脚本怎么开头，取出来的都不能超宽(否则 Error 1406 建单失败)。
func TestApprovalKeyword(t *testing.T) {
	long := strings.Repeat("x", 200)
	cases := []struct{ in, want string }{
		// 原有行为不变
		{"update t set a=1 where id=1", "UPDATE"},
		{"  SELECT 1", "SELECT"},
		{"", ""},
		// 注释开头:跳过注释取真正的动词
		{"--==========================================\nALTER TABLE t ADD c INT;", "ALTER"},
		{"--本次变更说明：订单表新增渠道字段配合十一月渠道结算需求上线\ncreate table t(id int);", "CREATE"},
		{"/* ****************************************** */ DELETE FROM t WHERE id=1", "DELETE"},
		{"# 说明说明说明说明说明说明说明说明说明说明说明说明说明说明说明说明说明\nINSERT INTO t VALUES(1)", "INSERT"},
		// 第一个词本身就超长:截断到列宽
		{long + " x", strings.ToUpper(long[:31]) + "…"},
	}
	for _, c := range cases {
		got := approvalKeyword(c.in)
		if got != c.want {
			t.Errorf("approvalKeyword(%q) = %q, want %q", c.in, got, c.want)
		}
		if n := utf8.RuneCountInString(got); n > approvalKeywordWidth {
			t.Errorf("approvalKeyword(%q) 有 %d 字符，超过列宽", c.in, n)
		}
	}
	// 只有注释:不能超宽，也不能是非法 UTF-8
	for _, in := range []string{
		"--" + strings.Repeat("中", 100),
		"/*" + strings.Repeat("=", 100) + "*/",
		strings.Repeat("-- 注释\n", 20000) + "UPDATE t SET a=1", // 超过 64KB 的开头
	} {
		got := approvalKeyword(in)
		if n := utf8.RuneCountInString(got); n > approvalKeywordWidth || !utf8.ValidString(got) {
			t.Errorf("only-comment input → %q (%d 字符)", got, n)
		}
	}
}
