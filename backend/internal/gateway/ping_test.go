package gateway

// 连通性探测:必须**真的连过去**。
//
// 这组用例是被一次线上误判换来的:批量巡检把 88 台实例全报成"可连",而其中有的连
// dial 都超时 —— 因为它走的 Executor.Test 是一个 `sleep(120ms); return true` 的桩。
//
// 一个永远说好话的巡检比没有巡检更糟:没有巡检时人会自己去试,有一个说"可连"的巡检
// 时人不会。所以下面三条各自钉住一种"说好话"的方式:
//
//   连不上的要报连不上;
//   没配凭据的要报**不可连**,而不是"已接入网关";
//   连得上的当然要报可连 —— 否则上面两条可以靠"永远返回 false"作弊通过。

import (
	"path/filepath"
	"strings"
	"testing"

	"velagateway/internal/model"
)

// 真的能连:一个真实存在的 SQLite 文件。
func TestRealPing_ReachableTargetSaysOK(t *testing.T) {
	conn := &model.Connection{Engine: "SQLite", Database: filepath.Join(t.TempDir(), "ping.db")}
	ok, msg := RealPing(conn)
	if !ok {
		t.Fatalf("可连的目标应当报可连:%s", msg)
	}
}

// 连不上:指向一个没人监听的本地端口。
//
// 用 127.0.0.1 的关闭端口而不是一个不可路由的地址:前者立刻被拒,用例是秒级的;
// 后者要等满 dial 超时,把一条本该快的用例拖成 8 秒。
func TestRealPing_UnreachableTargetSaysSo(t *testing.T) {
	conn := &model.Connection{
		Engine: "MySQL 8.0", Host: "127.0.0.1", Port: 1,
		Username: "u", Password: "p", Database: "d",
	}
	ok, msg := RealPing(conn)
	if ok {
		t.Fatal("连不上的目标被报成了可连 —— 这正是巡检当初撒的那个谎")
	}
	if strings.TrimSpace(msg) == "" {
		t.Error("连不上要说出原因,否则人不知道该查网络还是查账号")
	}
}

// 没配凭据的模拟连接:报**不可连**,并说清是为什么。
//
// 从前它返回 "已接入网关" —— 把"我们没配凭据"说成了"那台库是好的"。
func TestRealPing_SimulatedConnectionIsNotReportedReachable(t *testing.T) {
	conn := &model.Connection{Engine: "MySQL 8.0", Host: "10.0.0.9", Port: 3306} // 无账号
	ok, msg := RealPing(conn)
	if ok {
		t.Fatal("没有凭据时不该报可连")
	}
	if !strings.Contains(msg, "凭据") {
		t.Errorf("要指出是凭据没配,而不是含糊的失败:%q", msg)
	}
	if strings.Contains(msg, "已接入") {
		t.Errorf("不能说成已接入网关:%q", msg)
	}
}

// Executor.Test 就是这条路 —— 它是巡检真正调用的入口,不能绕过 RealPing 另走一套。
func TestExecutorTest_UsesTheRealProbe(t *testing.T) {
	x := &Executor{}
	bad := &model.Connection{Engine: "MySQL 8.0", Host: "127.0.0.1", Port: 1, Username: "u", Password: "p", Database: "d"}
	if ok, _ := x.Test(bad); ok {
		t.Error("Executor.Test 仍然在说好话")
	}
	good := &model.Connection{Engine: "SQLite", Database: filepath.Join(t.TempDir(), "ok.db")}
	if ok, msg := x.Test(good); !ok {
		t.Errorf("可连的目标应当报可连:%s", msg)
	}
}
