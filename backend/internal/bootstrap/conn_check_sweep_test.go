package bootstrap

// 定时连通性巡检的边界。
//
// 这是网关里第二个**主动去连生产库**的后台任务(第一个是元数据同步),所以它的边界
// 不是性能问题,是"这台网关会在你不知道的时候连哪些库"的问题。四条各挡一种:
//
//	默认关       升级一个版本不该让网关自己开始拨号所有生产实例。
//	跳维护态     status=maint 是人为标的"别碰",定时任务不该是那个例外。
//	跳没凭据的   没有远端可拨。报"连不上"会把"我们没配凭据"说成"那台库坏了"。
//	跳认证失败的 防账号被锁 —— Oracle 默认失败 10 次就锁,10 分钟一轮的话不到两小时
//	             就能锁掉网关的账号。这一条挡的是一次真实的事故,不是省事。

import (
	"net/http"
	"testing"
)

// 默认关着:什么都不动时跑一轮,不该给任何实例留下巡检记录。
func TestConnSweep_DisabledByDefault(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	conn := app.realSQLiteConnIn(admin, "dev", "sweep-off")

	app.svc.SweepConnChecks()

	if v := app.connView(admin, conn); v.LastCheckOK != nil {
		t.Errorf("默认关着的时候不该拨号:%+v", v)
	}
}

// 打开之后会巡检 —— 证明上一条测的是"关着",不是"这功能根本不工作"。
func TestConnSweep_ChecksWhenEnabled(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	conn := app.realSQLiteConnIn(admin, "dev", "sweep-on")
	app.setSettings(admin, map[string]any{"conn.check.enabled": true})

	app.svc.SweepConnChecks()

	v := app.connView(admin, conn)
	if v.LastCheckOK == nil {
		t.Fatalf("开了却没巡检:%+v", v)
	}
	if !*v.LastCheckOK {
		t.Errorf("这台是真连得上的,却记成了连不上:%+v", v)
	}
}

// 维护态跳过:人标的"别碰",定时任务不该是那个例外。
func TestConnSweep_SkipsMaintenance(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	conn := app.realSQLiteConnIn(admin, "dev", "sweep-maint")
	if r := app.do(http.MethodPatch, "/api/v1/connections/"+itoa(conn), admin, map[string]any{"status": "maint"}); r.Code != 0 {
		t.Fatalf("标维护态: %s", r.Msg)
	}
	app.setSettings(admin, map[string]any{"conn.check.enabled": true})

	app.svc.SweepConnChecks()

	if v := app.connView(admin, conn); v.LastCheckOK != nil {
		t.Errorf("维护态的实例不该被拨号:%+v", v)
	}
}

// 没配凭据的跳过:没有远端可拨,记一条"连不上"是把我们的配置问题说成远端的状况。
func TestConnSweep_SkipsInstancesWithoutCredentials(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	stg := app.connIDByEnv(admin, "staging") // 种子实例,没有连接凭据
	app.setSettings(admin, map[string]any{"conn.check.enabled": true})

	app.svc.SweepConnChecks()

	if v := app.connView(admin, stg); v.LastCheckOK != nil {
		t.Errorf("没凭据的实例不该被拨号:%+v", v)
	}
}

// 上次因**认证**失败的跳过 —— 这一条是防账号被锁。
//
// 同时验清楚它跳的是认证失败,不是所有失败:一台 i/o timeout 的实例照样要继续巡检,
// 否则它一旦恢复就没人知道。
func TestConnSweep_SkipsAuthFailuresButKeepsRetryingNetworkFailures(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	authBad := app.realSQLiteConnIn(admin, "dev", "sweep-auth")
	netBad := app.realSQLiteConnIn(admin, "dev", "sweep-net")

	// 预置两条不同成因的失败。两台底下都是真连得上的 SQLite,所以只要巡检真的跑了,
	// 结论就会翻成"通" —— 这正好用来区分"跑了"和"跳过了"。
	if err := app.svc.Repo.SaveConnCheck(authBad, "auth", false, "Error 1045: Access denied for user 'app'@'10.0.0.1'"); err != nil {
		t.Fatalf("预置认证失败: %v", err)
	}
	if err := app.svc.Repo.SaveConnCheck(netBad, "unreachable", false, "dial tcp 10.0.0.2:3306: i/o timeout"); err != nil {
		t.Fatalf("预置网络失败: %v", err)
	}
	app.setSettings(admin, map[string]any{"conn.check.enabled": true})

	app.svc.SweepConnChecks()

	if v := app.connView(admin, authBad); v.LastCheckOK == nil || *v.LastCheckOK {
		t.Errorf("认证失败的实例不该被自动重试(会锁账号),结论却变了:%+v", v)
	}
	if v := app.connView(admin, netBad); v.LastCheckOK == nil || !*v.LastCheckOK {
		t.Errorf("网络失败的实例应当继续巡检,否则恢复了也没人知道:%+v", v)
	}
}

// 手动巡检不受那条限制:人点下去,就是人认为凭据已经改好了。
func TestConnSweep_ManualCheckStillRunsAfterAuthFailure(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	conn := app.realSQLiteConnIn(admin, "dev", "manual-after-auth")
	if err := app.svc.Repo.SaveConnCheck(conn, "auth", false, "ORA-01017: invalid username/password"); err != nil {
		t.Fatalf("预置: %v", err)
	}

	if r := app.do(http.MethodPost, "/api/v1/connections/"+itoa(conn)+"/test", admin, nil); r.Code != 0 {
		t.Fatalf("手动巡检: %s", r.Msg)
	}

	v := app.connView(admin, conn)
	if v.LastCheckOK == nil || !*v.LastCheckOK || v.LastCheckErr != "" {
		t.Errorf("手动巡检应当照跑并恢复,实际 %+v", v)
	}
}

// 防锁账号那条不只属于连通性巡检:元数据同步同样是定时拨号者。
// 它按小时重拨一台凭据错误的 Oracle,不到半天就把网关的账号锁掉。
func TestConnSweep_MetadataSweepAlsoSkipsAuthFailures(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	conn := app.realSQLiteConnIn(admin, "dev", "meta-skips-auth")
	if r := app.execSQL(admin, conn, `CREATE TABLE t_probe (id INTEGER)`); r.Code != 0 {
		t.Fatalf("建表: %s", r.Msg)
	}
	if err := app.svc.Repo.SaveConnCheck(conn, "auth", false, "ORA-01017: invalid username/password"); err != nil {
		t.Fatalf("预置: %v", err)
	}
	app.setSettings(admin, map[string]any{"meta.sync.enabled": true})

	app.svc.SweepMetadata()

	// 底下是真连得上的 SQLite:只要同步真的跑了,就会留下同步记录。
	if got := app.readMeta(admin, conn); got.Sync != nil {
		t.Errorf("上次认证失败的实例不该被元数据同步重拨(会锁账号),却同步了:%+v", got.Sync)
	}
}
