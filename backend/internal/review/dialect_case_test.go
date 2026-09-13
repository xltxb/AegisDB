package review

// 方言名的大小写不该决定规则跑不跑。
//
// 开放接口的 /open/sql-review 允许调用方直接给方言(不指定实例时),那个值一路原样
// 送到 Check。而 AppliesTo 把规则上写的方言小写之后,拿去和这个**没归一过的**值比。
//
// 于是 dialect=MySQL(大写 M)审出来的报告里,一条 MySQL 规则都没跑 —— 只剩 all 那些。
// 报告照样回"通过",还把 MySQL 这个方言名回显给调用方,而他看到的是一份少了一半规则
// 的结论。这类失败最坏的地方在于它看起来完全正常。

import "testing"

func mysqlOnlyRule() []Rule {
	return rulesFor(Rule{
		Code: "mysql.only", Name: "只在 MySQL 上生效", Dialect: DialectMySQL,
		Kind: "regex", Level: LevelError, Params: `{"pattern":"DROP\\s+TABLE"}`,
	})
}

func TestCheck_DialectNameIsCaseInsensitive(t *testing.T) {
	const sql = "DROP TABLE tbl_legacy;"
	base := len(Check(DialectMySQL, sql, mysqlOnlyRule()).Findings)
	if base != 1 {
		t.Fatalf("前置条件不成立:小写方言下该命中 1 条,实际 %d 条", base)
	}
	for _, d := range []string{"MySQL", "MYSQL", " mysql", "mysql "} {
		if n := len(Check(d, sql, mysqlOnlyRule()).Findings); n != 1 {
			t.Errorf("方言写作 %q 时命中 %d 条,want 1 —— 这一批规则被静默跳过了", d, n)
		}
	}
}

// 认不出的方言名不该被悄悄当成 generic 审完了事 —— 调用方以为自己按那个方言审过了。
func TestCheckSQL_RefusesAnUnknownDialect(t *testing.T) {
	for _, d := range []string{"mysql", "MySQL", "tidb", "dws", "oracle", "generic", "all", ""} {
		if !KnownDialect(d) {
			t.Errorf("%q 该是认得的方言", d)
		}
	}
	for _, d := range []string{"postgres", "pg", "sqlserver", "mysql8", "我猜是MySQL"} {
		if KnownDialect(d) {
			t.Errorf("%q 不该被当成认得的方言", d)
		}
	}
}
