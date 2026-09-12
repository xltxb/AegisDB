package model

import (
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// 承载 code 的列,宽度不能小于允许写进去的长度。
//
// 分层代码与环境代码由 service.codeRe 约束:`^[a-z0-9][a-z0-9-]{0,31}$` —— 最长 32 个
// 字符。而这些列里有一半是 VARCHAR(16)。
//
// SQLite(开发/测试)对 VARCHAR 的长度**不做约束**,所以这件事在本地永远不会暴露:
// 建一个 20 字符的分层、把实例挂上去,一切正常。到了 MySQL 上:
//
//   · STRICT 模式 → `Data too long for column` (1406),写不进去
//   · 非 STRICT   → **静默截断**,而截断后的 code 解析不到任何分层,那台实例的每一条
//                   命令都被拒(fail-closed 是对的,但没人知道为什么)
//
// 这条用例按 codeRe 的上限去量每一列。它挡的是"改了正则忘了改列"和"改了列忘了改
// 正则"两个方向 —— 0034 那次收窄(把 tbl_connection.env 从 32 改回 16)正是后者。

// codeMaxLen 是 service.codeRe 允许的最长 code。写死在这里而不是 import service:
// model 不该依赖 service,而这个数字变了就该有人同时看这两处。
const codeMaxLen = 32

func gormSize(t *testing.T, v any, field string) int {
	t.Helper()
	f, ok := reflect.TypeOf(v).FieldByName(field)
	if !ok {
		t.Fatalf("%T 上没有字段 %s", v, field)
	}
	for _, part := range strings.Split(f.Tag.Get("gorm"), ";") {
		if s, found := strings.CutPrefix(strings.TrimSpace(part), "size:"); found {
			n, err := strconv.Atoi(s)
			if err != nil {
				t.Fatalf("%T.%s 的 size 不是数字: %q", v, field, s)
			}
			return n
		}
	}
	t.Fatalf("%T.%s 没有声明 size", v, field)
	return 0
}

func TestCodeColumnsFitWhatTheRegexAllows(t *testing.T) {
	for _, c := range []struct {
		model any
		field string
		why   string
	}{
		{EnvTier{}, "Code", "分层代码本身"},
		{Environment{}, "Code", "环境代码本身"},
		{Environment{}, "TierCode", "环境指向的分层"},
		{Connection{}, "Env", "实例挂在哪个环境 —— 0034 把它从 32 收窄回了 16"},
		{RoleCapability{}, "TierCode", "能力矩阵按分层存"},
		{RiskCommand{}, "TierCode", "高危字典按分层存"},
		{Approval{}, "TierCode", "工单的分层快照"},
		{AuditLog{}, "TierCode", "审计行的分层快照"},
		{Pipeline{}, "TierCode", "流程模板绑定的分层"},
		{Release{}, "TierCode", "发布单的分层快照"},
	} {
		if n := gormSize(t, c.model, c.field); n < codeMaxLen {
			t.Errorf("%T.%s 只有 %d,而 codeRe 允许 %d(%s)—— MySQL 上要么 1406 写不进去,"+
				"要么静默截断成一个解析不到分层的 code", c.model, c.field, n, codeMaxLen, c.why)
		}
	}
}

// 顺带把 codeRe 的上限本身钉住 —— 它和上面那个常量必须是同一个数。
func TestCodeRegexUpperBoundIsWhatWeSized(t *testing.T) {
	re := regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	if !re.MatchString(strings.Repeat("a", codeMaxLen)) {
		t.Errorf("codeRe 应当接受 %d 个字符", codeMaxLen)
	}
	if re.MatchString(strings.Repeat("a", codeMaxLen+1)) {
		t.Errorf("codeRe 不该接受 %d 个字符 —— 列宽是按 %d 定的", codeMaxLen+1, codeMaxLen)
	}
}
