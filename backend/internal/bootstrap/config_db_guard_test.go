package bootstrap

// 「生产必须显式给出 postgres_dsn」这条守卫,必须挂在**每一条会打开网关自身存储的路**
// 上,而不只是 serve。
//
// 空 DSN 不是「没配」。libpq 把它读成:走 unix socket、user = 当前 OS 用户、
// **dbname = OS 用户名**,而且连得上。服务账号叫 `vela`、主机上又恰好有个 `vela` 库时
// (`createuser --createdb vela` 之后顺手 `createdb` 是常见习惯),后果是:
//
//	./vela-gateway migrate   → 在**错的库**里建满 36 张表,打印 migration complete,退出码 0
//	./vela-gateway init      → 在那儿建出管理员
//	systemd 起 serve         → 连的是真正的 db.internal,prod 的 seed=false 不播种
//	                         → 那边一个管理员都没有,/healthz 绿,每次登录 40100
//
// 三步各自成功、各自退出码 0,没有任何一处指向真因。所以这条守卫必须在 migrate / init
// 打开数据库**之前**就拦下来。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

func TestValidateForDB_RejectsEmptyProdDSN(t *testing.T) {
	mk := func(env, dsn string) *Config {
		c := &Config{}
		c.Env = env
		c.Database.PostgresDSN = dsn
		return c
	}
	if err := mk("prod", "").ValidateForDB(); err == nil {
		t.Error("prod + 空 DSN 应当被拒 —— libpq 会连上本机默认库并在上面建表")
	}
	if err := mk("prod", "   ").ValidateForDB(); err == nil {
		t.Error("prod + 全空白 DSN 应当被拒")
	}
	if err := mk("prod", "host=db.internal port=5432 user=vela dbname=vela_gateway sslmode=require").ValidateForDB(); err != nil {
		t.Errorf("prod + 显式 DSN 不该被拒:%v", err)
	}
	// dev 不拦:本机随手跑一下不该被这条挡住,config.yaml 自带 DSN。
	if err := mk("dev", "").ValidateForDB(); err != nil {
		t.Errorf("dev 不该被这条拦住:%v", err)
	}
}

// serve / migrate / init 三条路都得调到校验。这条闸从**源码**这一侧看 —— 行为测试
// 够不着 main:那三个入口都以 os.Exit 收场,而它们正是漏掉校验的地方。
func TestEveryDBEntryPointValidatesTheConfig(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "../../cmd/server/main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}

	// 入口名 → 它调到的校验方法集合。
	calls := map[string]map[string]bool{}
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Recv != nil {
			continue
		}
		seen := map[string]bool{}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok &&
				strings.HasPrefix(sel.Sel.Name, "ValidateFor") {
				seen[sel.Sel.Name] = true
			}
			return true
		})
		calls[fd.Name.Name] = seen
	}

	// main 走 serve,ValidateForServe 内部含 ValidateForDB 那一段。
	for _, c := range []struct {
		fn       string
		accepted []string
	}{
		{"main", []string{"ValidateForServe", "ValidateForDB"}},
		{"runMigrate", []string{"ValidateForDB", "ValidateForServe"}},
		{"runInit", []string{"ValidateForDB", "ValidateForServe"}},
	} {
		seen, ok := calls[c.fn]
		if !ok {
			t.Fatalf("cmd/server/main.go 里找不到 %s —— 这条闸解析错了东西", c.fn)
		}
		hit := false
		for _, name := range c.accepted {
			if seen[name] {
				hit = true
				break
			}
		}
		if !hit {
			t.Errorf("%s 打开数据库之前一次配置校验都没做(期望调到 %s 之一)。\n"+
				"    prod 的空 postgres_dsn 于是不会报错:libpq 会连上本机的默认库\n"+
				"    (dbname = OS 用户名),在**错的库**里建满表并以退出码 0 结束。",
				c.fn, strings.Join(c.accepted, " / "))
		}
	}
}
