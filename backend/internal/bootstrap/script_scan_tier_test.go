package bootstrap

import (
	"encoding/json"
	"net/http"
	"testing"

	"velagateway/internal/model"
)

// scanScript runs the script scanner against one connection.
func (a *testApp) scanScript(token string, connID int64, content string) scriptScanResult {
	a.t.Helper()
	r := a.do(http.MethodPost, "/api/v1/scripts/scan", token, map[string]any{
		"connectionId": connID, "content": content, "filename": "t.sql",
	})
	if r.Code != 0 {
		a.t.Fatalf("scripts/scan: code=%d msg=%s", r.Code, r.Msg)
	}
	var out scriptScanResult
	if err := json.Unmarshal(r.Data, &out); err != nil {
		a.t.Fatalf("decode scan: %v", err)
	}
	return out
}

type scriptScanResult struct {
	Total    int  `json:"total"`
	High     int  `json:"high"`
	Mid      int  `json:"mid"`
	Safe     int  `json:"safe"`
	HasRisky bool `json:"hasRisky"`
}

// 脚本扫描按**目标实例的分层**判定,不再一律按某个固定的基准分层。
//
// 从前基准固定是 prod,于是一个只准备在 dev 跑的脚本也被按生产的尺子量:一份菜单
// 初始化脚本因为 perms 串里含 delete 这个词,被判成高危 P1,而它的目标是 UAT。
// 报告说的和将要发生的事对不上 —— 执行时每条语句本来就按目标分层重判(见
// ExecuteSafeScript → execJudged),那道固定镜头没拦住任何东西,只制造了假警报。
func TestScriptScan_JudgedByTargetTier(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	prod := app.connIDByEnv(token, "prod")
	dev := app.connIDByEnv(token, "dev")

	const script = "DROP TABLE orders_2024_q3;\nDELETE FROM audit_tmp;\n"

	// PROD:字典把 DROP / DELETE 判为 high。
	onProd := app.scanScript(token, prod, script)
	eq(t, onProd.Total, 2, "prod 扫描语句数")
	if onProd.High == 0 || !onProd.HasRisky {
		t.Errorf("PROD 上这段脚本必须报高危,实际 high=%d hasRisky=%v", onProd.High, onProd.HasRisky)
	}

	// DEV:同一份脚本,dev 分层的字典全是 off,应当扫成安全。
	onDev := app.scanScript(token, dev, script)
	eq(t, onDev.Total, 2, "dev 扫描语句数")
	if onDev.High != 0 || onDev.HasRisky {
		t.Errorf("DEV 的字典是 off,同一脚本不该报高危,实际 high=%d mid=%d hasRisky=%v",
			onDev.High, onDev.Mid, onDev.HasRisky)
	}
	eq(t, onDev.Safe, 2, "dev 上两条都算安全")
}

// 目标实例解析不出来时必须拒绝扫描,而不是退回某个分层去判 —— 拿不到分层的扫描
// 会把每条语句(DROP TABLE 也包括)都报成安全,且没有任何地方会失败。
func TestScriptScan_RefusesWithoutAResolvableTarget(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")

	r := app.do(http.MethodPost, "/api/v1/scripts/scan", token, map[string]any{
		"connectionId": 999999, "content": "DROP TABLE x;", "filename": "t.sql",
	})
	if r.Code == 0 {
		t.Fatal("目标实例不存在时扫描必须失败,不能给出'一切正常'的报告")
	}
}

// 镜头是活的:改目标分层的字典,同一份脚本的扫描结论跟着变。这条同时守住"扫描读的
// 是运行时规则而不是编译期常量"。
func TestScriptScan_FollowsTheTargetTierDictionary(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	prod := app.connIDByEnv(admin, "prod")

	const script = "DROP TABLE orders;"
	if got := app.scanScript(admin, prod, script); got.High == 0 {
		t.Fatalf("DROP 在 prod 应扫成高危,实际 high=%d", got.High)
	}

	// 把 DROP 在 prod 上调成 off,同一份脚本、同一台实例,结论应当跟着变。
	eq(t, app.do(http.MethodPatch, "/api/v1/risk-commands/DROP", admin,
		map[string]any{"tier": "prod", "level": "off"}).Code, 0, "把 DROP 在 prod 设为 off")

	after := app.scanScript(admin, prod, script)
	eq(t, after.High, 0, "字典改了之后不再报高危")
	eq(t, after.Safe, 1, "这条语句现在算安全")
}

// 反面:目标实例的分层解析不出来时,扫描和执行都必须拒绝。
//
// 拿不到分层的扫描会把每条语句(DROP TABLE 也包括)都报成安全,而且没有任何地方会
// 失败 —— 扫描只是"找不到风险"了。而一份全安全的扫描结果正是脚本免审直接执行的
// 依据,所以这里只能拒绝(ED3,与不可读规则层同一立场)。
func TestScriptScan_RefusesWhenTheTargetTierCannotBeResolved(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	prod := app.connIDByEnv(admin, "prod")

	// API 不会让你把实例指到一个不存在的环境上 —— 这正是它守的不变量。直接改库,
	// 证明读取路径不会默默容忍这种状态。
	if err := app.repo.DB().Model(&model.Connection{}).
		Where("id = ?", prod).Update("env", "no-such-env").Error; err != nil {
		t.Fatalf("break the tier link: %v", err)
	}

	r := app.do(http.MethodPost, "/api/v1/scripts/scan", admin, map[string]any{
		"connectionId": prod, "filename": "t.sql", "content": "DROP TABLE orders;",
	})
	if r.Code == 0 {
		t.Fatal("分层解析不出来时,扫描必须失败,而不是给出一份干净的报告")
	}

	// 执行同理:它先扫描,而一份全安全的扫描就是脚本免审执行的通行证。
	er := app.do(http.MethodPost, "/api/v1/scripts/execute", admin, map[string]any{
		"connectionId": prod, "filename": "t.sql", "content": "DROP TABLE orders;",
	})
	if er.Code == 0 {
		t.Fatal("分层解析不出来时,脚本执行必须失败")
	}
}
