package review

// 一条规则的**出厂参数**只该有一个值。
//
// `Params` 那个字段的注释写着「"" = built-in defaults」—— 也就是说种子给的那份 JSON 和
// 检查器里写死的兜底值,说的应该是同一件事。而它们各写了一份,并且已经分叉:
//
//   · `dws.max.join.tables` 的标题写着「不超过 8 张」、种子给 `{"max":8}`,而检查器在
//     params 为空时用 **16**。同一条规则,两个阈值 —— 而界面上显示的是标题里那个 8。
//   · `dws.forbid.volatile.in.subquery` 的两份函数列表**互有对方没有的条目**:种子有
//     `gen_random_uuid`(PG 13+ 的标准写法)、`sys_guid`、`clock_timestamp`、`uuid`,
//     检查器默认有 `uuid_generate_v4`、`currval`、`lastval`。params 一空,这条规则查的
//     就是另一组函数了。
//
// params 什么时候会空?管理员在界面上把它清掉、或者按这条内置规则手工新建一条 —— 而
// 那时他期待的显然是「照标题说的那样」。
//
// 这一组用例不比对常量(那样改完就是重言),它比对**结论**:同一条 SQL,在种子参数下和在
// 空参数下必须报出同一件事。

import (
	"strings"
	"testing"
)

// rulesWithoutParams 是内置规则集,但每条的 params 都清空 —— 模拟"管理员清掉了参数"。
func rulesWithoutParams() []Rule {
	out := allRules()
	for i := range out {
		out[i].Params = ""
	}
	return out
}

func TestRuleDefaults_JoinCountAgreesWithTheSeed(t *testing.T) {
	// 九张表参与关联(八个 JOIN),超过标题写的 8 张上限。
	var b strings.Builder
	b.WriteString("SELECT * FROM t0")
	for i := 1; i <= 8; i++ {
		b.WriteString(" JOIN t")
		b.WriteByte(byte('0' + i))
		b.WriteString(" ON 1=1")
	}
	sql := b.String()

	if !hasCode(Check(DialectDWS, sql, allRules()), "dws.max.join.tables") {
		t.Fatal("前置条件不成立:种子参数下九张表应当超限")
	}
	if !hasCode(Check(DialectDWS, sql, rulesWithoutParams()), "dws.max.join.tables") {
		t.Error("params 一清空,阈值就从标题写的 8 变成了检查器里的 16 —— 同一条规则两个答案," +
			"而界面上显示的是标题里那个 8")
	}
}

func TestRuleDefaults_VolatileFunctionsAgreeWithTheSeed(t *testing.T) {
	// gen_random_uuid 是 PG 13+ 生成 UUID 的标准写法,种子列了它,检查器默认没有。
	const sql = `SELECT * FROM t WHERE id IN (SELECT gen_random_uuid() FROM u)`

	if !hasCode(Check(DialectDWS, sql, allRules()), "dws.forbid.volatile.in.subquery") {
		t.Fatal("前置条件不成立:种子参数下 gen_random_uuid 应当被拦")
	}
	if !hasCode(Check(DialectDWS, sql, rulesWithoutParams()), "dws.forbid.volatile.in.subquery") {
		t.Error("params 一清空,这条规则查的就是另一组函数了 —— gen_random_uuid 从此不在其中")
	}
}
