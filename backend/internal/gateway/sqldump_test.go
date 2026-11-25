package gateway

import (
	"strings"
	"testing"
)

// 导出成 SQL:值要按目标库的规则写成字面量,否则导回去就不是原来的数据。
//
// NULL 写成 '' 会把空值变成空串;数字加引号在 Oracle 上要做隐式转换;MySQL 默认把
// 反斜杠当转义符,原样写出去 'C:\new' 会变成 'C:<换行>ew';PostgreSQL 则相反。

func TestSQLLiteral_PerEngine(t *testing.T) {
	cases := []struct {
		engine string
		cell   Cell
		want   string
	}{
		{"MySQL 8.0", Cell{Null: true}, "NULL"},
		{"MySQL 8.0", Cell{Text: ""}, "''"},
		{"MySQL 8.0", Cell{Text: "42", Kind: KindNumber}, "42"},
		{"MySQL 8.0", Cell{Text: "-3.5e2", Kind: KindNumber}, "-3.5e2"},
		{"MySQL 8.0", Cell{Text: "******", Kind: KindNumber}, "'******'"}, // 打码后的数字列不再是数字
		{"MySQL 8.0", Cell{Text: `O'Brien C:\new`}, `'O''Brien C:\\new'`},
		{"PostgreSQL 15", Cell{Text: `O'Brien`}, `'O''Brien'`},
		{"PostgreSQL 15", Cell{Text: `C:\new`}, `E'C:\\new'`},
		{"Oracle 19c", Cell{Text: `C:\new`}, `'C:\new'`},
		{"Oracle 19c", Cell{Text: "2026-10-08 09:30:00", Kind: KindTime}, "TIMESTAMP '2026-10-08 09:30:00'"},
		{"MySQL 8.0", Cell{Text: "2026-10-08 09:30:00", Kind: KindTime}, "'2026-10-08 09:30:00'"},
		{"MySQL 8.0", Cell{Text: "\x01\xff", Kind: KindBinary}, "X'01ff'"},
		{"PostgreSQL 15", Cell{Text: "\x01\xff", Kind: KindBinary}, `'\x01ff'::bytea`},
		{"Oracle 19c", Cell{Text: "\x01\xff", Kind: KindBinary}, "HEXTORAW('01ff')"},
		{"PostgreSQL 15", Cell{Text: "true", Kind: KindBool}, "TRUE"},
	}
	for _, c := range cases {
		if got := SQLLiteral(c.engine, c.cell); got != c.want {
			t.Errorf("SQLLiteral(%s, %+v) = %s, want %s", c.engine, c.cell, got, c.want)
		}
	}
}

func TestQuoteIdent_PerEngine(t *testing.T) {
	if got := QuoteIdent("MySQL 8.0", "shop.t_order"); got != "`shop`.`t_order`" {
		t.Errorf("MySQL: %s", got)
	}
	if got := QuoteIdent("PostgreSQL 15", `public.my"t`); got != `"public"."my""t"` {
		t.Errorf("PG: %s", got)
	}
}

// MySQL / PG 一条 INSERT 带多行;Oracle 不支持这种写法,每行一条。
func TestInsertStatements_BatchesExceptOracle(t *testing.T) {
	cols := []string{"id", "name"}
	rows := [][]Cell{
		{{Text: "1", Kind: KindNumber}, {Text: "a"}},
		{{Text: "2", Kind: KindNumber}, {Null: true}},
	}
	my := InsertStatements("MySQL 8.0", "t", cols, rows)
	if want := "INSERT INTO `t` (`id`, `name`) VALUES\n(1, 'a'),\n(2, NULL);\n"; my != want {
		t.Errorf("MySQL:\n%s\nwant\n%s", my, want)
	}
	ora := InsertStatements("Oracle 19c", "T", cols, rows)
	if strings.Count(ora, "INSERT INTO") != 2 || !strings.Contains(ora, `INSERT INTO "T" ("id", "name") VALUES (2, NULL);`) {
		t.Errorf("Oracle 应每行一条:\n%s", ora)
	}
}
