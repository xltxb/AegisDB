package service

// 每个 recordAudit / recordAuditBy 调用点传的事件类型,必须是认得出的那四类之一。
//
// 这条守卫的成因很具体:事件类型是**一个字符串字面量**,散在三十多个调用点上,而拼错
// 它不会有任何报错 —— 那一行照样写进审计表,只是:
//
//   - webhook 的订阅匹配 `strings.Contains(","+cfg.Events+",", ","+eventType+",")` 永远
//     不命中,于是这一类事件**永远不推**,没有任何人会注意到;
//   - 审计页按类型筛也永远筛不到它,而界面上的下拉里根本没有这个选项,所以连"咦怎么
//     少了一类"都看不出来。
//
// 两个后果都是**静默的**,而且都发生在事后追查的那条路上 —— 正是最不能出静默错误的地方。
// 编译器帮不上忙(它只看到 string),所以在这里静态扫一遍源码。
//
// 它不检查动态取值(变量、函数返回):那种写法这里认不出来,如实跳过并计数,而不是假装
// 检查过 —— 见文件末尾的自检。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"velagateway/internal/model"
)

func TestRecordAuditEventTypesAreKnown(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	fset := token.NewFileSet()
	literals, dynamic := 0, 0

	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		src, rerr := os.ReadFile(path)
		if rerr != nil {
			t.Fatalf("read %s: %v", path, rerr)
		}
		f, perr := parser.ParseFile(fset, path, src, 0)
		if perr != nil {
			t.Fatalf("parse %s: %v", path, perr)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (sel.Sel.Name != "recordAudit" && sel.Sel.Name != "recordAuditBy") {
				return true
			}
			if len(call.Args) == 0 {
				return true
			}
			// 事件类型是最后一个参数(见 recordAudit / recordAuditBy 的签名)。
			last := call.Args[len(call.Args)-1]
			lit, ok := last.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				dynamic++
				return true
			}
			v, uerr := strconv.Unquote(lit.Value)
			if uerr != nil {
				return true
			}
			literals++
			// 空串是允许的:它明确表示"这一行不推 webhook"(recordAuditBy 里就是这么判的)。
			if v == "" || model.IsAuditEventType(v) {
				return true
			}
			pos := fset.Position(lit.Pos())
			t.Errorf("%s:%d 传了一个认不出的事件类型 %q。\n"+
				"    已知的是 %v(见 model.AuditEventTypes)。\n"+
				"    拼错它不会报错:这一类事件从此永远不推 webhook,审计页也永远筛不到 ——\n"+
				"    两个后果都是静默的,而且都发生在事后追查的那条路上。",
				pos.Filename, pos.Line, v, model.AuditEventTypes)
			return true
		})
	}

	// 自检:一个字面量都没扫到,说明这条用例检查的是空集、永远绿着 —— 那比没有更糟。
	// 当前实际有三十多处,门槛压低到 10 只为了挡住"整段失配"这一种退化。
	if literals < 10 {
		t.Fatalf("只扫到 %d 个字面量事件类型,这条用例基本等于没生效(动态取值 %d 处)", literals, dynamic)
	}
	t.Logf("扫过 %d 处字面量事件类型,%d 处是动态取值(未检查)", literals, dynamic)
}

// 审计事件的四类与 webhook 的默认订阅必须用同一组字符串。订阅一个写不出来的类型,
// 表现是"这类事件永远不推",而不是报错。
func TestAuditEventTypesAreDistinctAndNonEmpty(t *testing.T) {
	seen := map[string]bool{}
	for _, k := range model.AuditEventTypes {
		if strings.TrimSpace(k) == "" {
			t.Error("事件类型不能是空串 —— 空串在 recordAuditBy 里表示「不推」")
		}
		if seen[k] {
			t.Errorf("事件类型 %q 重复", k)
		}
		seen[k] = true
		if !model.IsAuditEventType(k) {
			t.Errorf("IsAuditEventType 不认得自己列出来的 %q", k)
		}
	}
	if model.IsAuditEventType("") || model.IsAuditEventType("nope") {
		t.Error("IsAuditEventType 应当只认那四类")
	}
}
