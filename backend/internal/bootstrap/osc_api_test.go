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
// 而目前还缺一件要紧的东西 —— 真实从库延迟的读取,也就是说**限流形同虚设**。一次
// 没有限流的在线变更比原生 DDL 更危险,因为原生 DDL 至少是 MySQL 自己在控制节奏。
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
	// 缺限流这件事必须从接口就说出来 —— 写在文档里的警告,发起的人看不到。
	if len(st.Caveats) == 0 {
		t.Error("没有列出任何注意事项 —— 缺少从库限流这件事必须让发起的人看见")
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
