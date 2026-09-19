package bootstrap

import (
	"encoding/json"
	"net/http"
	"testing"

	"velagateway/pkg/resp"
)

// 在线变更(ADR 0011)的接口。
//
// 这套东西**默认关闭**,而且不是随口一说:它会在生产库上建影子表、拷数据、原子改名,
// 而它还欠一次对着有从库的实例的演练。限流本身已经接上(心跳表 + 主库自报从库),
// 但**装不装得起来取决于目标实例**:装不起来时迁移照跑,只是不限流 —— 而一次没有
// 限流的在线变更比原生 DDL 更危险,因为原生 DDL 至少是 MySQL 自己在控制节奏。
//
// 所以关着的时候必须**明确地拒绝**:给一个自己的业务码,而不是 404(看起来像路由写错)
// 或 500(看起来像坏了)。界面要据此说人话。

func TestOscAPI_RefusesWhenTheFeatureIsOff(t *testing.T) {
	app := newTestApp(t) // 默认配置 = 特性关闭
	token := app.login("linwei@vela.io", "vela123")

	r := app.do(http.MethodPost, "/api/v1/osc/jobs", token, map[string]any{
		"connectionId": 1, "schema": "app", "table": "t_order",
		"alter": "ADD INDEX idx_memo (memo)",
	})
	if r.Code != resp.CodeOscDisabled {
		t.Fatalf("特性关闭时应当返回 %d,实际 code=%d msg=%q", resp.CodeOscDisabled, r.Code, r.Msg)
	}
	// 拒绝要说清为什么 —— 一句"功能未启用"会让人去翻配置,而真正的原因是它还没做完。
	if r.Msg == "" {
		t.Error("拒绝时没有给出任何理由")
	}
}

// 列表在关着的时候仍然可读 —— 否则一次半途失败留下的残局,在关掉开关之后就没人
// 看得见了,而那正是最需要看见它的时候。
func TestOscAPI_TheJobListStaysReadableWhenOff(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")

	r := app.do(http.MethodGet, "/api/v1/osc/jobs", token, nil)
	if r.Code != resp.CodeOK {
		t.Fatalf("关着的时候列表也该读得到,实际 code=%d msg=%q", r.Code, r.Msg)
	}
	var jobs []struct {
		ID     int64  `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(r.Data, &jobs); err != nil {
		t.Fatalf("解析任务列表: %v", err)
	}
}

// 状态里要带上「这套东西现在能不能用,以及为什么」,让界面不必自己猜。
func TestOscAPI_StatusTellsTheConsoleWhatItCanOffer(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")

	r := app.do(http.MethodGet, "/api/v1/osc/status", token, nil)
	if r.Code != resp.CodeOK {
		t.Fatalf("读状态失败: code=%d msg=%q", r.Code, r.Msg)
	}
	var st struct {
		Enabled bool     `json:"enabled"`
		Caveats []string `json:"caveats"`
	}
	if err := json.Unmarshal(r.Data, &st); err != nil {
		t.Fatalf("解析状态: %v", err)
	}
	if st.Enabled {
		t.Error("默认配置下这套东西应当是关着的")
	}
	// 这套东西做不到的事必须从接口就说出来 —— 写在文档里的警告,发起的人看不到。
	if len(st.Caveats) == 0 {
		t.Error("没有列出任何注意事项 —— 限流可能装不起来这件事必须让发起的人看见")
	}
}

// 只读账号不该碰得到它。这套东西会改生产表结构。
//
// 注意这条用例必须在**开关打开**的情况下跑:开关关着时所有人都被挡,那样它会
// 因为一个与权限无关的理由而变绿 —— 一条永远不会失败的用例。
func TestOscAPI_OnlyAdminsCanReachIt(t *testing.T) {
	app := newTestApp(t)
	app.cfg.OSC.Enabled = true
	ro := app.login("zhaolei@vela.io", "vela123")

	r := app.do(http.MethodPost, "/api/v1/osc/jobs", ro, map[string]any{
		"connectionId": 1, "schema": "app", "table": "t_order", "alter": "ADD INDEX i (c)",
	})
	// 点名 403,而不是只说"没成功"。
	//
	// 只说"没成功"的话这条用例是假绿:去掉管理员守卫之后,只读账号照样会被挡下 ——
	// 但挡它的是"实例凭据不完整"(40001),跟权限毫无关系。那时用例仍然是绿的,
	// 而任何人都能发起生产表结构变更。这是变异测试当场抓到的。
	if r.Code != resp.CodeForbidden {
		t.Fatalf("只读账号发起在线变更应当被权限挡下(%d),实际 code=%d msg=%q",
			resp.CodeForbidden, r.Code, r.Msg)
	}
}

// 急停开关:改配置文件加重启关不掉一个正在出事的特性。
//
// 方向是**不对称**的,这是有意的:打开它的前提是一次对着有从库的实例的演练
// (ADR 0011),那是人做的事,界面上点一下不构成那个前提;而关要快 —— 一次迁移正在
// 把从库拖垮时,人要挡住后续发起,而不是先去重启网关。
func TestOscAPI_TheKillSwitchClosesItWithoutTouchingTheConfigFile(t *testing.T) {
	app := newTestApp(t)
	app.cfg.OSC.Enabled = true // 配置里开着
	if err := app.repo.SetSetting("osc.enabled", "false"); err != nil {
		t.Fatalf("写设置失败: %v", err)
	}
	token := app.login("linwei@vela.io", "vela123")

	r := app.do(http.MethodPost, "/api/v1/osc/jobs", token, map[string]any{
		"connectionId": 1, "schema": "app", "table": "t_order",
		"alter": "ADD INDEX idx_memo (memo)",
	})

	if r.Code != resp.CodeOscDisabled {
		t.Fatalf("后台急停之后应当返回 %d,实际 code=%d msg=%q", resp.CodeOscDisabled, r.Code, r.Msg)
	}
}

// 反方向不成立:配置里关着时,后台这个开关怎么拨都打不开。
//
// 少了这条,"急停开关"会悄悄变成"启用开关" —— 任何平台管理员在界面上点一下就能
// 打开一个会在生产库上改表的功能,而 ADR 0011 要求的那次演练没有发生。
func TestOscAPI_TheKillSwitchCannotTurnItOn(t *testing.T) {
	app := newTestApp(t)
	app.cfg.OSC.Enabled = false // 配置里关着 —— 这是前提,不是偏好
	if err := app.repo.SetSetting("osc.enabled", "true"); err != nil {
		t.Fatalf("写设置失败: %v", err)
	}
	token := app.login("linwei@vela.io", "vela123")

	r := app.do(http.MethodPost, "/api/v1/osc/jobs", token, map[string]any{
		"connectionId": 1, "schema": "app", "table": "t_order",
		"alter": "ADD INDEX idx_memo (memo)",
	})

	if r.Code != resp.CodeOscDisabled {
		t.Fatalf("配置关着时后台开关不该能打开它,实际 code=%d msg=%q", r.Code, r.Msg)
	}
}

// 没写过这个设置的部署(绝大多数)行为不变:配置说了算。
func TestOscAPI_UnsetKillSwitchMeansTheConfigDecides(t *testing.T) {
	app := newTestApp(t)
	app.cfg.OSC.Enabled = true // 没有写过 osc.enabled 这个设置
	token := app.login("linwei@vela.io", "vela123")

	r := app.do(http.MethodPost, "/api/v1/osc/jobs", token, map[string]any{
		"connectionId": 1, "schema": "app", "table": "t_order",
		"alter": "ADD INDEX idx_memo (memo)",
	})

	if r.Code == resp.CodeOscDisabled {
		t.Fatal("没写过急停设置时不该被拦 —— 默认值把一个开着的部署关掉了")
	}
}
