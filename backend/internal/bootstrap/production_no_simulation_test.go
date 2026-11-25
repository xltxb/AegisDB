package bootstrap

// 生产环境不返回任何模拟数据。
//
// 规矩来自一句要求:**模拟数据可以在本地开发调试环境保留并生效,但打包部署生产时,
// 所有已实现的功能都必须是真实现。**
//
// 这条测试是那句话的可执行形式,也是 build.sh 打包前跑的那一道闸(见脚本里的
// "verify no simulated data in prod")。它逐条走 docs/simulated-paths.md 里登记的
// 每一条路径,在**关掉模拟**之后确认它们给的是明确的拒绝,而不是编出来的数据。
//
// 为什么值得单独一条:这些路径的触发条件是"连接没配凭据",而那在生产上是**会发生**
// 的 —— 有人新建实例忘了填密码。那一刻的行为如果是"返回一份看似合理的假结果",
// 没有任何东西会报错,而人会照着它做判断。

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"velagateway/internal/gateway"
)

// withSimulationOff 在一个用例内关掉模拟,结束后恢复 —— 夹具默认是开的(它模拟开发
// 环境),这里要的是生产那一侧。
func withSimulationOff(t *testing.T) {
	t.Helper()
	prev := gateway.AllowSimulation
	gateway.AllowSimulation = false
	t.Cleanup(func() { gateway.AllowSimulation = prev })
}

// 默认值必须是"不允许" —— 漏设开关的新入口得到的是拒绝,不是造假。
func TestSimulationDefaultsToOff(t *testing.T) {
	// 断言的是**源码里的默认值**,不是运行时的值:夹具早把它打开了,读变量只会读到
	// 被改过的那个。这条守的是"下一个人不要顺手把默认改成 true"。
	b, err := os.ReadFile(filepath.Join("..", "gateway", "simulation.go"))
	if err != nil {
		t.Fatalf("读 simulation.go: %v", err)
	}
	if !strings.Contains(string(b), "var AllowSimulation = false") {
		t.Error("AllowSimulation 的默认值必须是 false —— 漏设的后果得是拒绝,不是造假")
	}
}

// 关掉模拟之后,四条路径逐一确认:给的是拒绝,不是数据。
func TestProductionServesNoSimulatedData(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	// 一台没有凭据的实例 —— 生产上"新建了实例但忘了填密码"就是这个状态。
	conn := app.newOracleConn(admin, "prod-no-sim")
	withSimulationOff(t)

	t.Run("exec-run:执行不返回编出来的行数", func(t *testing.T) {
		r := app.execSQL(admin, conn, "SELECT 1")
		// 要么整个请求被拒,要么回执里说清是拒绝 —— 唯独不能是一份像样的结果。
		if r.Code == 0 {
			var out struct {
				Output string `json:"output"`
				Rows   int    `json:"rows"`
			}
			_ = json.Unmarshal(r.Data, &out)
			if !strings.Contains(out.Output, "凭据") {
				t.Errorf("生产环境不该返回模拟执行结果:output=%q rows=%d", out.Output, out.Rows)
			}
		}
	})

	t.Run("schema-tree:库表树不返回种子数据", func(t *testing.T) {
		r := app.do(http.MethodGet, "/api/v1/connections/"+itoa(conn)+"/schema", admin, nil)
		var out struct {
			Databases []struct {
				Name string `json:"name"`
			} `json:"databases"`
			Error string `json:"error"`
		}
		_ = json.Unmarshal(r.Data, &out)
		if len(out.Databases) > 0 {
			t.Errorf("生产环境不该把演示种子树当成这台实例的库表:%+v", out.Databases)
		}
		if !strings.Contains(out.Error, "凭据") {
			t.Errorf("要说清是凭据没配,而不是回一棵空树:%q", out.Error)
		}
	})

	t.Run("objects-list:对象列表不返回演示集", func(t *testing.T) {
		r := app.do(http.MethodGet, "/api/v1/connections/"+itoa(conn)+"/objects?scope=APP", admin, nil)
		var out struct {
			Functions []string `json:"functions"`
			Packages  []string `json:"packages"`
			Error     string   `json:"error"`
		}
		_ = json.Unmarshal(r.Data, &out)
		if len(out.Functions) > 0 || len(out.Packages) > 0 {
			t.Errorf("生产环境不该返回演示对象集:%+v", out)
		}
		if !strings.Contains(out.Error, "凭据") {
			t.Errorf("要说清是凭据没配:%q", out.Error)
		}
	})

	t.Run("object-source:对象源码不返回编出来的正文", func(t *testing.T) {
		r := app.do(http.MethodGet,
			"/api/v1/connections/"+itoa(conn)+"/object-source?type=package&name=APP_PKG", admin, nil)
		if r.Code == 0 {
			t.Errorf("生产环境不该编造一段对象源码:%s", string(r.Data))
		}
		if !strings.Contains(r.Msg, "凭据") {
			t.Errorf("要说清是凭据没配:%q", r.Msg)
		}
	})
}

// 反过来:开发环境仍然要能用。
//
// 少了这一条,上面那些用"永远拒绝"就能作弊通过,而演示数据集会在没人发现的情况下死掉。
func TestDevelopmentStillServesSimulatedData(t *testing.T) {
	app := newTestApp(t) // 夹具默认打开模拟 = 开发环境
	admin := app.login("linwei@vela.io", "vela123")
	conn := app.newOracleConn(admin, "dev-sim-ok")

	r := app.do(http.MethodGet, "/api/v1/connections/"+itoa(conn)+"/objects?scope=APP", admin, nil)
	var out struct {
		Packages []string `json:"packages"`
		Error    string   `json:"error"`
	}
	_ = json.Unmarshal(r.Data, &out)
	if out.Error != "" || len(out.Packages) == 0 {
		t.Errorf("开发环境应当照旧返回演示对象集:err=%q packages=%v", out.Error, out.Packages)
	}
}
