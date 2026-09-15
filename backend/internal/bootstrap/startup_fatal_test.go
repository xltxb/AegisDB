package bootstrap

// 启动时建库/播种失败,进程必须**退出**,不能只打一行日志接着监听。
//
// 一台没有角色、没有管理员的网关照样能绑上端口:`/healthz` 返回 ok(它不碰数据库),
// 于是编排系统认为这个副本是健康的,把流量切过来 —— 而每一次登录都 40100。运维看到的
// 是「服务是活的、只是所有人都登不上」,而真正的原因在启动日志里滚过去了一次。
//
// 这条闸是对着 main.go **自己写下的那句话**立的:
//
//	「失败必须 os.Exit。一台没有表的网关照样能监听端口 —— 每个请求 500,而进程
//	  看上去是活的」
//
// 那句话对 Migrate 兑现了,对 25 行之下的 Seed 没有。prod 的 seed 本来就关着,所以
// 它咬的是 dev / 演示那条路 —— 而那正是「探活绿、登录全挂」最容易被当成别的毛病的地方。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestFatalStartupStepsExit(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "../../cmd/server/main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}

	var mainFn *ast.FuncDecl
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil && fd.Name.Name == "main" {
			mainFn = fd
		}
	}
	if mainFn == nil {
		t.Fatal("cmd/server/main.go 里找不到 main() —— 这条闸解析错了东西")
	}

	// callName 取 `pkg.Fn(...)` 的 "pkg.Fn"。
	callName := func(n ast.Node) string {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return ""
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return ""
		}
		id, ok := sel.X.(*ast.Ident)
		if !ok {
			return ""
		}
		return id.Name + "." + sel.Sel.Name
	}

	// 对每一个致命步骤:找到 `if err := <step>; err != nil { … }`,断言它的 body 里
	// 真的有一次 os.Exit。
	want := map[string]string{
		"bootstrap.Migrate": "表都没建全的网关会对每个请求返 500,而进程看上去是活的",
		"bootstrap.Seed":    "播种失败 = 没有角色、没有管理员;/healthz 照样绿,而每次登录都 40100",
		// --no-migrate 的那一支。它跳过 Migrate,所以必须自己验证 schema 已是最新 ——
		// 验证不过还继续起,就是对着旧表结构服务,和上面第一条同一个下场,只是这次
		// 是**自找的**:开关的本意是把迁移交给别人跑,而「别人忘了」正是它要能查出来的。
		"bootstrap.VerifySchemaCurrent": "跳过迁移却不验证,等于自愿回到「表不全照样监听」那个形状",
	}
	found := map[string]bool{}

	ast.Inspect(mainFn.Body, func(n ast.Node) bool {
		ifs, ok := n.(*ast.IfStmt)
		if !ok || ifs.Init == nil {
			return true
		}
		assign, ok := ifs.Init.(*ast.AssignStmt)
		if !ok || len(assign.Rhs) != 1 {
			return true
		}
		step := callName(assign.Rhs[0])
		why, guarded := want[step]
		if !guarded {
			return true
		}
		found[step] = true
		exits := false
		ast.Inspect(ifs.Body, func(m ast.Node) bool {
			if callName(m) == "os.Exit" {
				exits = true
			}
			return true
		})
		if !exits {
			t.Errorf("main(): %s 失败之后没有 os.Exit —— %s.\n"+
				"    探活探到的会是一个「跑着的坏进程」,而那比启动失败难查得多。", step, why)
		}
		return true
	})

	for step := range want {
		if !found[step] {
			t.Errorf("在 main() 里没找到 `if err := %s(…); err != nil` 这个形状 —— "+
				"这条闸此刻守着 0 个对象,等于没跑", step)
		}
	}
}
