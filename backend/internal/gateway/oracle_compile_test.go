package gateway

import (
	"reflect"
	"strings"
	"testing"
)

// 编译语句里的对象名是**拼进 SQL 文本**的 —— Oracle 不允许把标识符做成绑定变量。
// 所以名字必须先过校验再加引号,否则对象浏览器里的一个名字就是一条注入通道。
func TestOracleCompileRejectsUnsafeIdentifiers(t *testing.T) {
	bad := []string{
		`APP_PKG" COMPILE; DROP TABLE users --`, // 闭合引号后夹带
		"app_pkg; DROP TABLE users",
		`pkg"name`,
		"1pkg",  // 不能以数字开头
		"",      // 空
		"a b",   // 空格
		"包名",    // 非 ASCII 标识符不走这条路
		strings.Repeat("x", 200),
	}
	for _, name := range bad {
		if _, err := oracleCompileSQL("APP", "package", name); err == nil {
			t.Errorf("unsafe identifier %q must be refused, not quoted into the statement", name)
		}
	}
	good := []string{"APP_PKG", "app_pkg", "P$1", "A#B", "Pkg_2"}
	for _, name := range good {
		if _, err := oracleCompileSQL("APP", "package", name); err != nil {
			t.Errorf("legitimate identifier %q refused: %v", name, err)
		}
	}
	// owner 同样是拼进去的
	if _, err := oracleCompileSQL(`APP" --`, "package", "APP_PKG"); err == nil {
		t.Error("an unsafe owner must be refused too")
	}
}

// 包要编译两个单元:规格与包体。编译规格会让包体失效,所以"编译这个包"只做一半
// 是错的 —— 用户点一次,两半都要重新有效。
func TestOracleCompileTargetsCoverSpecAndBody(t *testing.T) {
	cases := []struct {
		typ  string
		want []string
	}{
		{"package", []string{
			`ALTER PACKAGE "APP"."APP_PKG" COMPILE PACKAGE`,
			`ALTER PACKAGE "APP"."APP_PKG" COMPILE BODY`,
		}},
		{"procedure", []string{`ALTER PROCEDURE "APP"."APP_PKG" COMPILE`}},
		{"function", []string{`ALTER FUNCTION "APP"."APP_PKG" COMPILE`}},
		{"trigger", []string{`ALTER TRIGGER "APP"."APP_PKG" COMPILE`}},
		{"type", []string{
			`ALTER TYPE "APP"."APP_PKG" COMPILE TYPE`,
			`ALTER TYPE "APP"."APP_PKG" COMPILE BODY`,
		}},
	}
	for _, tc := range cases {
		got, err := oracleCompileSQL("APP", tc.typ, "APP_PKG")
		if err != nil {
			t.Fatalf("%s: %v", tc.typ, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s → %#v, want %#v", tc.typ, got, tc.want)
		}
	}
	if _, err := oracleCompileSQL("APP", "table", "T"); err == nil {
		t.Error("a table has nothing to compile — that must be an error, not a no-op")
	}
}

// 没有 owner 时不拼 owner 前缀(用当前 schema),但仍然要加引号。
func TestOracleCompileWithoutOwner(t *testing.T) {
	got, err := oracleCompileSQL("", "procedure", "P")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{`ALTER PROCEDURE "P" COMPILE`}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

// CompileReport.OK 的语义:只要有一个单元不是 VALID,就不是成功。
//
// 这是这个功能最容易撒谎的地方 —— ALTER ... COMPILE 即使编译出错也返回成功,
// 对象只是被留成 INVALID。以驱动没报错为准去显示"编译成功",就是在骗人。
func TestCompileReportOKRequiresEveryUnitValid(t *testing.T) {
	valid := &CompileReport{Targets: []CompileTarget{{Type: "PACKAGE", Status: "VALID"}, {Type: "PACKAGE BODY", Status: "VALID"}}}
	if !valid.OK() {
		t.Error("all units VALID should read as success")
	}
	half := &CompileReport{Targets: []CompileTarget{{Type: "PACKAGE", Status: "VALID"}, {Type: "PACKAGE BODY", Status: "INVALID"}}}
	if half.OK() {
		t.Error("an INVALID body must not report success")
	}
	withErrs := &CompileReport{
		Targets: []CompileTarget{{Type: "PACKAGE", Status: "VALID"}},
		Errors:  []CompileDiag{{Type: "PACKAGE BODY", Line: 4, Text: "PLS-00103"}},
	}
	if withErrs.OK() {
		t.Error("compilation diagnostics must defeat success even if status reads VALID")
	}
	none := &CompileReport{}
	if none.OK() {
		t.Error("no targets at all is not a success — nothing was proven compiled")
	}
}
