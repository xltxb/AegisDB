package bootstrap

// 模拟执行不能在审计链上记成「已执行」。
//
// 审计链要回答的是"谁在什么时候对哪台库执行了什么"。一条**从未接触任何数据库**的
// 命令如果记成 executed,就是在这条链上写下一句假话 —— 而这条链的全部价值就在于它
// 不说假话。
//
// 它也不该记成 warn:没有任何东西出错。所以是单独一个值 simulated。
//
// 生产环境不会出现它(模拟一律被拒,见 TestProductionServesNoSimulatedData),
// 这条守的是开发/演示环境里那份记录仍然诚实。

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestAudit_SimulatedRunIsNotRecordedAsExecuted(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	// 没有凭据的实例 = 模拟路径(夹具默认允许模拟,模拟的是开发环境)。
	conn := app.newOracleConn(admin, "audit-sim")

	if r := app.execSQL(admin, conn, "SELECT 1"); r.Code != 0 {
		t.Fatalf("开发环境下模拟执行应当成功: code=%d msg=%s", r.Code, r.Msg)
	}

	r := app.do(http.MethodGet, "/api/v1/audit?page=1&pageSize=20&risk=all", admin, nil)
	if r.Code != 0 {
		t.Fatalf("读审计失败: code=%d msg=%s", r.Code, r.Msg)
	}
	var page struct {
		Items []struct {
			Command string `json:"command"`
			Result  string `json:"result"`
		} `json:"items"`
	}
	if err := json.Unmarshal(r.Data, &page); err != nil {
		t.Fatalf("decode audit: %v", err)
	}
	found := false
	for _, it := range page.Items {
		if it.Command != "SELECT 1" {
			continue
		}
		found = true
		if it.Result == "executed" {
			t.Error("模拟执行被记成了「已执行」—— 审计链上多了一条从未发生的执行")
		}
		if it.Result != "simulated" {
			t.Errorf("模拟执行应当记成 simulated,实际 %q", it.Result)
		}
	}
	if !found {
		t.Fatal("审计里找不到那条命令")
	}
}

// 真实执行仍然记成 executed —— 否则上面那条用"永远不记 executed"就能作弊通过。
func TestAudit_RealRunStillRecordedAsExecuted(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	dev, _ := app.sameFileConns(admin, "audit-real") // 真实 SQLite 文件

	if r := app.execSQL(admin, dev, "SELECT 1"); r.Code != 0 {
		t.Fatalf("真实执行失败: %s", r.Msg)
	}
	r := app.do(http.MethodGet, "/api/v1/audit?page=1&pageSize=20&risk=all", admin, nil)
	var page struct {
		Items []struct {
			Command string `json:"command"`
			Result  string `json:"result"`
		} `json:"items"`
	}
	_ = json.Unmarshal(r.Data, &page)
	for _, it := range page.Items {
		if it.Command == "SELECT 1" && it.Result != "executed" {
			t.Errorf("真实执行应当仍然记成 executed,实际 %q", it.Result)
		}
	}
}
