package gateway

// Oracle 无效对象批量重编译的逻辑测试。
//
// 仓库里没有真实 Oracle,所以能钉住的是三件不依赖数据库的事,而它们恰好是这个功能里
// 最容易悄悄退化的三件:
//
//   - **多轮的规则**。退化之后它照样返回一份看起来正常的报告,只是有些本来能修好的
//     对象留在了 INVALID。
//   - **对象类型的映射**。少认一个类型,那类对象就永远不出现在清单里 —— 表现是
//     "系统说没有无效对象",而不是报错。
//   - **编译语句的拼装**。视图和物化视图是这次新加的,它们的 ALTER 写法与存储程序不同。

import (
	"fmt"
	"strings"
	"testing"
)

func obj(name, kind string) InvalidObject {
	return InvalidObject{Owner: "APP", Name: name, Kind: kind, Units: []string{strings.ToUpper(kind)}}
}

func okReport() *CompileReport {
	return &CompileReport{Targets: []CompileTarget{{Type: "PACKAGE", Status: "VALID"}}}
}

func badReport() *CompileReport {
	return &CompileReport{
		Targets: []CompileTarget{{Type: "PACKAGE", Status: "INVALID"}},
		Errors:  []CompileDiag{{Type: "PACKAGE BODY", Line: 12, Position: 3, Text: "PLS-00201"}},
	}
}

// 依赖顺序:B 依赖 A,先编 B 会失败,A 编好之后第二轮 B 才过。
// 这正是"多轮"存在的理由 —— 只编一轮的话 B 会被报成编不过。
func TestRecompile_SecondPassFixesDependencyOrder(t *testing.T) {
	targets := []InvalidObject{obj("B", "package"), obj("A", "package")}
	aFixed := false
	rep := recompileWith(targets, func(o InvalidObject) (*CompileReport, error) {
		switch o.Name {
		case "A":
			aFixed = true
			return okReport(), nil
		default: // B 只有在 A 好了之后才能编过
			if aFixed {
				return okReport(), nil
			}
			return badReport(), nil
		}
	}, nil)

	if rep.Passes != 2 {
		t.Errorf("应当跑两轮,实际 %d", rep.Passes)
	}
	if rep.Fixed != 2 || rep.Failed != 0 {
		t.Errorf("两个都该修好:fixed=%d failed=%d", rep.Fixed, rep.Failed)
	}
	for _, it := range rep.Items {
		if it.Status != "VALID" {
			t.Errorf("%s 应为 VALID,实际 %s", it.Name, it.Status)
		}
	}
}

// 一轮下来一个都没修好,就不该再来一轮 —— 剩下的是真编不过,不是顺序问题。
// 少了这条,一个编不过的 schema 会被白编三遍,审计里也多两倍的噪音。
func TestRecompile_StopsWhenAPassFixesNothing(t *testing.T) {
	calls := 0
	rep := recompileWith([]InvalidObject{obj("A", "package"), obj("B", "view")},
		func(o InvalidObject) (*CompileReport, error) { calls++; return badReport(), nil }, nil)

	if rep.Passes != 1 {
		t.Errorf("第一轮零进展就该停,实际跑了 %d 轮", rep.Passes)
	}
	if calls != 2 {
		t.Errorf("应当只编了 2 次(每个对象一次),实际 %d", calls)
	}
	if rep.Failed != 2 || rep.Fixed != 0 {
		t.Errorf("两个都该算失败:fixed=%d failed=%d", rep.Fixed, rep.Failed)
	}
	if len(rep.Items[0].Errors) == 0 {
		t.Error("编不过的对象要带上 all_errors 的诊断,否则界面只能说'失败了'")
	}
}

// 单个对象编译报错(名字非法、对象没了、权限不够)不该让整批停下。
func TestRecompile_OneErrorDoesNotAbortTheBatch(t *testing.T) {
	rep := recompileWith([]InvalidObject{obj("BAD", "package"), obj("GOOD", "package")},
		func(o InvalidObject) (*CompileReport, error) {
			if o.Name == "BAD" {
				return nil, fmt.Errorf("ORA-01031: 权限不足")
			}
			return okReport(), nil
		}, nil)

	if rep.Fixed != 1 || rep.Failed != 1 {
		t.Errorf("一个成功一个出错:fixed=%d failed=%d", rep.Fixed, rep.Failed)
	}
	var bad *RecompileItem
	for i := range rep.Items {
		if rep.Items[i].Name == "BAD" {
			bad = &rep.Items[i]
		}
	}
	if bad == nil || bad.Status != "ERROR" || !strings.Contains(bad.Err, "ORA-01031") {
		t.Errorf("出错的对象要带上原因:%+v", bad)
	}
}

// 每个对象只回调一次,而且带的是**最终**结果 —— 审计一个对象一行,与单个编译一致。
func TestRecompile_AuditCallbackOncePerObjectWithFinalResult(t *testing.T) {
	seen := map[string]int{}
	final := map[string]bool{}
	aFixed := false
	recompileWith([]InvalidObject{obj("B", "package"), obj("A", "package")},
		func(o InvalidObject) (*CompileReport, error) {
			if o.Name == "A" {
				aFixed = true
				return okReport(), nil
			}
			if aFixed {
				return okReport(), nil
			}
			return badReport(), nil
		},
		func(o InvalidObject, r *CompileReport, err error) {
			seen[o.Name]++
			final[o.Name] = err == nil && r.OK()
		})

	for _, n := range []string{"A", "B"} {
		if seen[n] != 1 {
			t.Errorf("%s 的审计回调应当只发生一次,实际 %d 次", n, seen[n])
		}
		if !final[n] {
			t.Errorf("%s 的回调应当带最终(成功)结果,而不是中间那一轮的失败", n)
		}
	}
}

// 空批次不该崩,也不该报成失败。
func TestRecompile_EmptyBatch(t *testing.T) {
	rep := recompileWith(nil, func(InvalidObject) (*CompileReport, error) {
		t.Fatal("空批次不该调用编译")
		return nil, nil
	}, nil)
	if rep.Total != 0 || rep.Fixed != 0 || rep.Failed != 0 || rep.Passes != 0 {
		t.Errorf("空批次的报告应当全零:%+v", rep)
	}
}

// object_type → 编译类型。少认一个,那类对象就永远不出现在清单里。
func TestOracleCompileKind(t *testing.T) {
	cases := map[string]string{
		"PACKAGE": "package", "PACKAGE BODY": "package",
		"TYPE": "type", "TYPE BODY": "type",
		"PROCEDURE": "procedure", "FUNCTION": "function", "TRIGGER": "trigger",
		"VIEW": "view", "MATERIALIZED VIEW": "materialized view",
		// ALTER … COMPILE 修不了的:列出来只会让人以为按一下就好了
		"SYNONYM": "", "JAVA CLASS": "", "TABLE": "", "": "",
	}
	for in, want := range cases {
		if got := oracleCompileKind(in); got != want {
			t.Errorf("oracleCompileKind(%q) = %q, want %q", in, got, want)
		}
	}
	// 清点用的类型清单必须和映射对得上,否则会查回一类然后又把它丢掉。
	for _, ot := range oracleInvalidTypes {
		if oracleCompileKind(ot) == "" {
			t.Errorf("清点了 %q 却没有对应的编译类型", ot)
		}
	}
}

// 视图与物化视图是这次新加的编译类型,ALTER 写法和存储程序不同。
func TestOracleCompileSQL_Views(t *testing.T) {
	cases := []struct{ kind, want string }{
		{"view", `ALTER VIEW "APP"."V_ORDER" COMPILE`},
		{"materialized view", `ALTER MATERIALIZED VIEW "APP"."MV_ORDER" COMPILE`},
	}
	names := map[string]string{"view": "V_ORDER", "materialized view": "MV_ORDER"}
	for _, c := range cases {
		got, err := oracleCompileSQL("APP", c.kind, names[c.kind])
		if err != nil {
			t.Fatalf("%s: %v", c.kind, err)
		}
		if len(got) != 1 || got[0] != c.want {
			t.Errorf("%s: got %v, want [%s]", c.kind, got, c.want)
		}
	}
	// 视图只有一个编译单元 —— 别照抄包那套"再编一次 BODY"。
	for _, k := range []string{"view", "materialized view"} {
		if u := oracleUnitTypes(k); len(u) != 1 {
			t.Errorf("%s 的编译单元应当只有一个,实际 %v", k, u)
		}
	}
}
