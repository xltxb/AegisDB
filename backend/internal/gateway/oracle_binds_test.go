package gateway

import (
	"context"
	"strings"
	"testing"

	"aegisdb/internal/model"
)

// SQL*Plus 绑定变量(sqlplus-compat 06)。

func TestReferencedBinds(t *testing.T) {
	binds := []Bind{{Name: "N"}, {Name: "S"}, {Name: "NEW"}}
	names := func(q string) string {
		var out []string
		for _, r := range referencedBinds(q, binds) {
			out = append(out, binds[r.idx].Name)
		}
		return strings.Join(out, ",")
	}
	cases := map[string]string{
		"SELECT :n + 1 FROM dual":                    "N",
		"BEGIN :s := :n || :s; END;":                 "S,N",
		"SELECT ':n' FROM dual":                      "",  // 字符串里
		"SELECT 1 FROM dual -- :n":                   "",  // 注释里
		"SELECT /* :s */ :N FROM dual":               "N", // 不分大小写
		`SELECT ":n" FROM dual`:                      "",  // 带引号的标识符
		"SELECT :undeclared FROM dual":               "",
		"SELECT a:n FROM t":                          "",  // 前面是标识符
		"SELECT x::int FROM t":                       "",
		"SELECT TO_CHAR(SYSDATE, 'HH24:MI') FROM dual": "",
	}
	for q, want := range cases {
		if got := names(q); got != want {
			t.Errorf("%q: got %q, want %q", q, got, want)
		}
	}
	// CREATE 语句(触发器体里的 :NEW)一律不绑定
	ctx := WithBinds(context.Background(), binds)
	if p := planOracleBinds(ctx, ora, "CREATE OR REPLACE TRIGGER t BEFORE INSERT ON x FOR EACH ROW BEGIN :NEW.id := 1; END;"); p != nil {
		t.Error("CREATE 语句不该绑定")
	}
	if p := planOracleBinds(ctx, "MySQL 8.0", "SELECT :n"); p != nil {
		t.Error("非 Oracle 不该绑定")
	}
	if p := planOracleBinds(ctx, ora, "BEGIN :n := 1; END;"); p == nil || !p.plsql {
		t.Error("PL/SQL 块应按 IN OUT 绑定")
	}
}

func TestParseVariable(t *testing.T) {
	ok := map[string]Bind{
		"n NUMBER":                 {Name: "N", Type: "NUMBER", Null: true},
		"s varchar2(30)":           {Name: "S", Type: "VARCHAR2(30)", Null: true},
		"s VARCHAR2 ( 30 CHAR )":   {Name: "S", Type: "VARCHAR2(30CHAR)", Null: true},
		"c clob":                   {Name: "C", Type: "CLOB", Null: true},
		"n NUMBER = 5":             {Name: "N", Type: "NUMBER", Value: "5"},
		"s VARCHAR2(10) = 'it''s'": {Name: "S", Type: "VARCHAR2(10)", Value: "it's"},
		"ch char":                  {Name: "CH", Type: "CHAR", Null: true},
	}
	for in, want := range ok {
		got, good := ParseVariable(in)
		if !good || got != want {
			t.Errorf("%q: got %+v ok=%v, want %+v", in, got, good, want)
		}
	}
	for _, in := range []string{"rc REFCURSOR", "b BLOB", "x", "1x NUMBER", "d DATE"} {
		if _, good := ParseVariable(in); good {
			t.Errorf("%q 应不支持", in)
		}
	}
	if bindSize("VARCHAR2(30)") != 120 || bindSize("NUMBER") != 100 || bindSize("CLOB") != 32767 || bindSize("VARCHAR2(32767)") != 32767 {
		t.Error("bindSize 不对")
	}
}

func TestPrintBindsAndFlow(t *testing.T) {
	binds := []Bind{{Name: "N", Value: "5"}, {Name: "S", Null: true}}
	cols, row, bad := printBinds(binds, nil)
	if strings.Join(cols, ",") != "N,S" || strings.Join(row, ",") != "5," || bad != "" {
		t.Fatalf("PRINT 全部: %v %v %q", cols, row, bad)
	}
	if _, _, bad := printBinds(binds, []string{"n", "zz"}); bad != "zz" {
		t.Fatalf("没声明的名字应报出来: %q", bad)
	}

	f := NewScriptFlowCtx(WithBinds(context.Background(), []Bind{{Name: "A", Type: "NUMBER", Value: "1"}}), ora)
	f.Observe("VARIABLE b VARCHAR2(20)")
	f.Observe("var a NUMBER") // 重新声明:清空
	got := f.Binds()
	if len(got) != 2 || got[0].Name != "B" || got[1].Name != "A" || !got[1].Null {
		t.Fatalf("声明后的变量不对: %+v", got)
	}
	if len(bindsFrom(f.Context(context.Background()))) != 2 {
		t.Fatal("Context 应带上变量")
	}

	// 执行器:PRINT 从 ctx 取值;VARIABLE 认类型
	x := NewExecutor()
	ctx := WithBinds(context.Background(), []Bind{{Name: "N", Type: "NUMBER", Value: "42"}})
	res := x.Run(ctx, &modelConnOra, "print n", 0)
	if strings.Join(res.Columns, ",") != "N" || len(res.Data) != 1 || res.Data[0][0] != "42" {
		t.Fatalf("PRINT n: %+v", res)
	}
	if res := x.Run(ctx, &modelConnOra, "print zz", 0); res.Err == nil {
		t.Error("PRINT 没声明的变量应失败")
	}
	if res := x.Run(ctx, &modelConnOra, "variable rc refcursor", 0); res.Err == nil {
		t.Error("REFCURSOR 应说不支持")
	}
	if q, ok := OracleSQLPlus("exec :n := :n + 1"); !ok || q != "BEGIN :n := :n + 1; END;" {
		t.Errorf("EXEC 赋值: %q %v", q, ok)
	}
}

var modelConnOra = model.Connection{Engine: ora}
