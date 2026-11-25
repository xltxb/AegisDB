package bootstrap

// 审计行记下**这一下从哪台机器发出来的**(迁移 0041),以及审计页新加的那几个筛选。
//
// actor 回答"谁",client_ip 回答"人在哪"。同一个账号昨夜从两个网段各删过一次数据,只看
// actor 是一样的两行 —— 账号被盗用、凭据被拿去别处跑,都只在这一列上露头。所以这一组测
// 的是:它真的被记下来了(包括**被拒的登录** —— 那一条最该留下来源)、它能被搜到、
// 导出带得上、而且搜索框里的通配符不会把"全部"当成搜索结果给出去。
//
// 测试的 httptest 服务器走的是真 TCP,所以 gin 的 ClientIP() 拿到的是 127.0.0.1。

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

const testClientIP = "127.0.0.1"

type auditRowIP struct {
	Actor      string `json:"actor"`
	ClientIP   string `json:"clientIp"`
	Command    string `json:"command"`
	Instance   string `json:"instance"`
	EventType  string `json:"eventType"`
	DataAction string `json:"dataAction"`
	Result     string `json:"result"`
	ApprovalNo string `json:"approvalNo"`
	Source     string `json:"source"`
}

type auditPageIP struct {
	Items []auditRowIP `json:"items"`
	Total int64        `json:"total"`
}

// auditQ 按任意一组查询参数拉审计页。
func (a *testApp) auditQ(token string, params map[string]string) auditPageIP {
	a.t.Helper()
	q := url.Values{"page": {"1"}, "pageSize": {"200"}}
	for k, v := range params {
		q.Set(k, v)
	}
	r := a.do(http.MethodGet, "/api/v1/audit?"+q.Encode(), token, nil)
	if r.Code != 0 {
		a.t.Fatalf("查审计 %v: code=%d msg=%s", params, r.Code, r.Msg)
	}
	var p auditPageIP
	if err := json.Unmarshal(r.Data, &p); err != nil {
		a.t.Fatalf("解码: %v", err)
	}
	return p
}

func rowWithCommand(items []auditRowIP, cmd string) *auditRowIP {
	for i := range items {
		if items[i].Command == cmd {
			return &items[i]
		}
	}
	return nil
}

// 登录、被拒的登录、真执行 —— 三条路径写下的行都带着来源地址。
func TestAuditClientIP_RecordedOnLoginFailureAndExec(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")

	// 一次密码错误。它不该带来一个 token,但**必须**留下一行,而且带着来源。
	if r := app.do(http.MethodPost, "/api/v1/auth/login", "", map[string]any{
		"email": "linwei@vela.io", "password": "wrong-password",
	}); r.Code == 0 {
		t.Fatal("错误的密码登录成功了")
	}

	dev := app.connIDByEnv(admin, "dev")
	const sql = "SELECT 1"
	eq(t, app.do(http.MethodPost, "/api/v1/terminal/exec", admin, map[string]any{
		"connectionId": dev, "sql": sql, "reason": "带 IP 的执行",
	}).Code, 0, "在 dev 上执行")

	items := app.auditQ(admin, nil).Items
	for _, want := range []string{"login", "login failed", sql} {
		row := rowWithCommand(items, want)
		if row == nil {
			t.Fatalf("审计里没有 %q 这一行", want)
		}
		if row.ClientIP != testClientIP {
			t.Fatalf("%q 这一行的来源地址是 %q,要的是 %q —— 一条被拒的登录最该留下的就是它从哪儿来",
				want, row.ClientIP, testClientIP)
		}
	}
}

// 操作人与来源 IP 共用一个模糊搜索框:两边任意一边命中都算。
func TestAuditFilter_KeywordMatchesActorOrIP(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	dev := app.connIDByEnv(admin, "dev")
	const sql = "SELECT 'kw-probe'"
	eq(t, app.do(http.MethodPost, "/api/v1/terminal/exec", admin, map[string]any{
		"connectionId": dev, "sql": sql, "reason": "搜索用例",
	}).Code, 0, "执行")

	// 按人名的一截
	if rowWithCommand(app.auditQ(admin, map[string]string{"q": "Lin"}).Items, sql) == nil {
		t.Fatal("按操作人搜不到自己刚跑的那条")
	}
	// 按 IP 的一截
	if rowWithCommand(app.auditQ(admin, map[string]string{"q": "127.0.0"}).Items, sql) == nil {
		t.Fatal("按来源 IP 搜不到刚跑的那条")
	}
	// 谁也不是
	if n := app.auditQ(admin, map[string]string{"q": "不存在的人"}).Total; n != 0 {
		t.Fatalf("搜一个不存在的人,却搜出了 %d 行", n)
	}
	// 通配符按字面量处理:一个 % 不能等于"全部"。搜索框里打一个百分号,得到的是全部行
	// 的话,人会以为自己搜到了什么。
	if n := app.auditQ(admin, map[string]string{"q": "%"}).Total; n != 0 {
		t.Fatalf("一个 %% 匹配到了 %d 行 —— LIKE 的通配符没有被转义", n)
	}
}

// 按实例筛;下拉的可选值来自 /audit/instances,两者说的是同一批实例。
func TestAuditFilter_ByInstanceAndInstanceList(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	dev := app.connIDByEnv(admin, "dev")
	prod := app.connIDByEnv(admin, "prod")
	const devSQL = "SELECT 'on-dev'"
	const prodSQL = "SELECT 'on-prod'"
	eq(t, app.do(http.MethodPost, "/api/v1/terminal/exec", admin, map[string]any{
		"connectionId": dev, "sql": devSQL, "reason": "dev",
	}).Code, 0, "dev 执行")
	eq(t, app.do(http.MethodPost, "/api/v1/terminal/exec", admin, map[string]any{
		"connectionId": prod, "sql": prodSQL, "reason": "prod",
	}).Code, 0, "prod 执行")

	// 实例清单里两台都在
	r := app.do(http.MethodGet, "/api/v1/audit/instances", admin, nil)
	eq(t, r.Code, 0, "取实例清单")
	var names []string
	if err := json.Unmarshal(r.Data, &names); err != nil {
		t.Fatalf("解码实例清单: %v", err)
	}
	if len(names) < 2 {
		t.Fatalf("实例清单只有 %v —— 刚在两台实例上各跑过一条", names)
	}

	// 挑出 dev 那条所在的实例名,按它筛
	all := app.auditQ(admin, nil).Items
	devRow := rowWithCommand(all, devSQL)
	if devRow == nil {
		t.Fatal("dev 上那条没进审计")
	}
	got := app.auditQ(admin, map[string]string{"instance": devRow.Instance}).Items
	if rowWithCommand(got, devSQL) == nil {
		t.Fatalf("按实例 %q 筛,筛掉了它自己那条", devRow.Instance)
	}
	if rowWithCommand(got, prodSQL) != nil {
		t.Fatalf("按实例 %q 筛,另一台实例的语句串进来了", devRow.Instance)
	}
}

// 事件类型与增删改查都支持多选;任何一个取值认不出就整条报错。
func TestAuditFilter_MultiSelectAndRejectsUnknownValue(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	dev := app.connIDByEnv(admin, "dev")
	eq(t, app.do(http.MethodPost, "/api/v1/terminal/exec", admin, map[string]any{
		"connectionId": dev, "sql": "SELECT 'multi'", "reason": "多选用例",
	}).Code, 0, "执行")

	// login 与 exec 一起筛:两类都要在,而且总数不少于任何单独一类。
	both := app.auditQ(admin, map[string]string{"event": "login,exec"})
	onlyLogin := app.auditQ(admin, map[string]string{"event": "login"})
	onlyExec := app.auditQ(admin, map[string]string{"event": "exec"})
	if both.Total != onlyLogin.Total+onlyExec.Total {
		t.Fatalf("多选 login,exec 得到 %d 行,而单独两类分别是 %d + %d", both.Total, onlyLogin.Total, onlyExec.Total)
	}
	seen := map[string]bool{}
	for _, it := range both.Items {
		seen[it.EventType] = true
	}
	if !seen["login"] || !seen["exec"] {
		t.Fatalf("多选的结果里只有 %v", seen)
	}

	// 一个认得出、一个认不出 —— 整条报错,不是把认得出的那半拿去筛。
	if r := app.do(http.MethodGet, "/api/v1/audit?event=login,nope", admin, nil); r.Code == 0 {
		t.Fatal("event 里混进一个认不出的取值,却当成有效筛选执行了")
	}
	if r := app.do(http.MethodGet, "/api/v1/audit?action=delete,nope", admin, nil); r.Code == 0 {
		t.Fatal("action 里混进一个认不出的取值,却当成有效筛选执行了")
	}
}

// 导出跟着列表走:同一组筛选条件,而且表头多了 client_ip 那一列。
func TestAuditExport_CarriesClientIPAndHonoursFilters(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	dev := app.connIDByEnv(admin, "dev")
	const sql = "SELECT 'export-probe'"
	eq(t, app.do(http.MethodPost, "/api/v1/terminal/exec", admin, map[string]any{
		"connectionId": dev, "sql": sql, "reason": "导出用例",
	}).Code, 0, "执行")

	csv := string(app.raw(http.MethodGet, "/api/v1/audit/export", admin))
	head, _, _ := strings.Cut(csv, "\n")
	if !strings.Contains(head, "client_ip") {
		t.Fatalf("导出表头里没有 client_ip: %q", head)
	}
	if !strings.Contains(csv, testClientIP) {
		t.Fatal("导出内容里一个来源地址都没有")
	}

	// 筛选条件对导出一样生效 —— 人是看着筛过的列表按下导出的。
	filtered := string(app.raw(http.MethodGet, "/api/v1/audit/export?q=不存在的人", admin))
	if strings.Contains(filtered, sql) {
		t.Fatal("导出没跟着筛选走:搜一个不存在的人,导出里却有别人的语句")
	}
}
