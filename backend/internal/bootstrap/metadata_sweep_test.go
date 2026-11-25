package bootstrap

// 定时全量同步的边界。
//
// 这是网关里少数几个**主动去连生产库**的东西,所以它的边界不是性能问题,是"这台网关
// 会在你不知道的时候连哪些库"的问题。三条各自挡一种:
//
//   默认关   —— 升级一个版本不该让网关自己开始扫所有生产实例。
//   跳维护态 —— status=maint 是人为标的"别碰",定时任务不该是那个例外。
//   跳模拟连接 —— 没有凭据就没有远端,写一份空清单进去等于宣布这台库是空的。

import (
	"net/http"
	"testing"
)

// 默认关着:什么设置都不动时,跑一轮 sweep 不该给任何实例留下同步记录。
func TestMetaSweep_DisabledByDefault(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	dev, _ := app.sameFileConns(admin, "sweep-off")

	app.svc.SweepMetadata()

	if got := app.readMeta(admin, dev); got.Sync != nil {
		t.Errorf("默认关着的时候不该同步任何东西:%+v", got.Sync)
	}
}

// 打开之后会同步 —— 证明上一条测的是"关着",不是"这功能根本不工作"。
func TestMetaSweep_SyncsWhenEnabled(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	dev, _ := app.sameFileConns(admin, "sweep-on")
	if r := app.execSQL(admin, dev, `CREATE TABLE t_sweep_on (id INTEGER)`); r.Code != 0 {
		t.Fatalf("建表: %s", r.Msg)
	}
	app.setMetaSync(admin, true)

	app.svc.SweepMetadata()

	got := app.readMeta(admin, dev)
	if got.Sync == nil {
		t.Fatal("打开之后应当同步")
	}
	if !hasTable(got, "t_sweep_on") {
		t.Errorf("缓存里没有 t_sweep_on:%+v", got.Tables)
	}
}

// 维护态的实例跳过。它是被人为标成"别碰"的,定时任务不该是那个例外。
func TestMetaSweep_SkipsMaintenanceInstances(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	dev, _ := app.sameFileConns(admin, "sweep-maint")
	if r := app.execSQL(admin, dev, `CREATE TABLE t_sweep_maint (id INTEGER)`); r.Code != 0 {
		t.Fatalf("建表: %s", r.Msg)
	}
	eq(t, app.do(http.MethodPatch, "/api/v1/connections/"+itoa(dev), admin,
		map[string]any{"status": "maint"}).Code, 0, "标成维护态")
	app.setMetaSync(admin, true)

	app.svc.SweepMetadata()

	if got := app.readMeta(admin, dev); got.Sync != nil {
		t.Errorf("维护态的实例不该被定时任务碰:%+v", got.Sync)
	}
}

// setMetaSync 通过设置接口开关定时同步 —— 走真实那条路,而不是直接改仓储:
// 设置项的键名写错了,直接改仓储的测试照样绿。
func (a *testApp) setMetaSync(token string, on bool) {
	a.t.Helper()
	r := a.do(http.MethodPut, "/api/v1/settings", token, map[string]any{"meta.sync.enabled": on})
	if r.Code != 0 {
		a.t.Fatalf("开关定时同步失败: code=%d msg=%s", r.Code, r.Msg)
	}
}
