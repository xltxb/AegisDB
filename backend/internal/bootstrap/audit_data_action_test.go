package bootstrap

// 审计行记下它对**数据**做了什么:增 / 删 / 改 / 查(迁移 0040)。
//
// 与 0039 的 event_type 正交:那个说"这一步在流程里是什么",这个说"它动了数据没有"。
// 一条 approve 放行的可能是 DROP,也可能是 SELECT —— 只有事件类型的话,"上个月谁删过东西"
// 这句话答不出来。
//
// 归类本身在 gateway 包里逐例测过(data_action_test.go:CTE、EXPLAIN ANALYZE、多语句取最重
// 的一条、认不出不兜底成"查")。这里测的是它周围那一圈:**真的存进行里了**、筛得动、
// 导出带得上、以及推给 webhook 的是行上存着的那个结论而不是现场重算的。

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type auditRowAction struct {
	Command    string `json:"command"`
	EventType  string `json:"eventType"`
	DataAction string `json:"dataAction"`
}

type auditPageAction struct {
	Items []auditRowAction `json:"items"`
	Total int64            `json:"total"`
}

func (a *testApp) auditByAction(token, action string) auditPageAction {
	a.t.Helper()
	q := "/api/v1/audit?page=1&pageSize=200"
	if action != "" {
		q += "&action=" + action
	}
	r := a.do(http.MethodGet, q, token, nil)
	if r.Code != 0 {
		a.t.Fatalf("按 action=%q 查审计: code=%d msg=%s", action, r.Code, r.Msg)
	}
	var p auditPageAction
	if err := json.Unmarshal(r.Data, &p); err != nil {
		a.t.Fatalf("解码: %v", err)
	}
	return p
}

// 结论真的写进了行里,而且登录那一行**不是"查"**。
func TestAuditDataAction_IsRecordedAndLoginIsNotARead(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123") // 这一下是一条 login
	app.submitProdHighRisk(admin)                   // DROP TABLE orders;

	all := app.auditByAction(admin, "")
	var login, drop *auditRowAction
	for i := range all.Items {
		switch {
		case all.Items[i].EventType == "login" && login == nil:
			login = &all.Items[i]
		case strings.Contains(all.Items[i].Command, "DROP TABLE orders") && drop == nil:
			drop = &all.Items[i]
		}
	}
	if drop == nil {
		t.Fatal("前置:审计里应有那条 DROP")
	}
	if drop.DataAction != "delete" {
		t.Errorf("DROP 应记为 delete,实际 %q", drop.DataAction)
	}
	if login == nil {
		t.Fatal("前置:应有一条登录审计")
	}
	if login.DataAction == "read" {
		t.Error("登录被记成了「查」—— 一次登录不是一次查询,而原来那段 eventAction 正是这么错的")
	}
	if login.DataAction != "other" {
		t.Errorf("登录应记为 other,实际 %q", login.DataAction)
	}
}

// 按增删改查筛得动;拼错的取值报错,不是回空列表。
func TestAuditDataAction_FiltersAndRefusesAnUnknownValue(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	app.submitProdHighRisk(admin)

	all := app.auditByAction(admin, "")
	for _, act := range []string{"delete", "other"} {
		got := app.auditByAction(admin, act)
		if got.Total == 0 {
			t.Errorf("按 %q 筛应至少有一行", act)
		}
		for _, it := range got.Items {
			if it.DataAction != act {
				t.Errorf("按 %q 筛,却筛出一行 dataAction=%q(命令 %q)", act, it.DataAction, clipStr(it.Command, 40))
			}
		}
		if got.Total > all.Total {
			t.Errorf("按 %q 筛(%d)不该多于不筛(%d)", act, got.Total, all.Total)
		}
	}

	if r := app.do(http.MethodGet, "/api/v1/audit?action=deleted", admin, nil); r.Code == 0 {
		t.Error("拼错的取值应被拒绝,而不是回一份空列表让人以为真的没有")
	}
	if got := app.auditByAction(admin, "all"); got.Total != all.Total {
		t.Errorf("action=all 应与不筛一致:%d vs %d", got.Total, all.Total)
	}
}

// 两个维度**互不干扰**:同时筛才是交集,而不是谁盖掉谁。
func TestAuditDataAction_CombinesWithTheEventTypeFilter(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	app.submitProdHighRisk(admin)

	r := app.do(http.MethodGet, "/api/v1/audit?page=1&pageSize=200&event=intercept&action=delete", admin, nil)
	eq(t, r.Code, 0, "两个筛选一起用")
	var p auditPageAction
	_ = json.Unmarshal(r.Data, &p)
	if p.Total == 0 {
		t.Fatal("被拦下来的那条 DROP 应同时满足 event=intercept 与 action=delete")
	}
	for _, it := range p.Items {
		if it.EventType != "intercept" || it.DataAction != "delete" {
			t.Errorf("交集里混进一行 event=%q action=%q", it.EventType, it.DataAction)
		}
	}
	// 反向:登录是 other,和 intercept 没有交集。
	r2 := app.do(http.MethodGet, "/api/v1/audit?page=1&pageSize=200&event=login&action=delete", admin, nil)
	eq(t, r2.Code, 0, "空交集也要是一次正常响应")
	var p2 auditPageAction
	_ = json.Unmarshal(r2.Data, &p2)
	if p2.Total != 0 {
		t.Errorf("登录不可能是删除,交集应为空,实际 %d 行", p2.Total)
	}
}

// 导出的 CSV 带这一列。
func TestAuditDataAction_ShowsUpInTheCSVExport(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	app.submitProdHighRisk(admin)

	csv := string(app.raw(http.MethodGet, "/api/v1/audit/export?action=delete", admin))
	head, rest, _ := strings.Cut(csv, "\n")
	if !strings.Contains(head, "data_action") {
		t.Fatalf("CSV 表头应有 data_action 一列,实际 %q", head)
	}
	if !strings.Contains(rest, "delete") {
		t.Errorf("按 delete 导出的内容里应出现该结论,实际:%s", clipStr(rest, 300))
	}
	if r := app.do(http.MethodGet, "/api/v1/audit/export?action=nope", admin, nil); r.Code == 0 {
		t.Error("导出也应拒绝拼错的取值")
	}
}

// 推给 webhook 的 action 用的是**行上存着的**结论,不是现场按命令文本重算的。
//
// 这一条要紧:重算等于让推出去的和存下来的各说各话,而接收端拿到的那个数字会被当成事实
// 写进报表。顺带钉住有损投影 —— 对方枚举里没有 other,只能落到 read,而真实结论照样在
// raw_payload 里送得出去。
func TestAuditDataAction_WebhookSendsTheStoredConclusion(t *testing.T) {
	var mu sync.Mutex
	var body map[string]any
	received := make(chan struct{}, 1)
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		_ = json.Unmarshal(raw, &body)
		mu.Unlock()
		select {
		case received <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer stub.Close()

	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	// 订阅按**增删改查**匹配(见 service.Dispatch):这条 DROP 是一次「删」。
	app.configureWebhook(token, stub.URL, "s3cr3t", "delete", 3)

	prodConn := app.connIDByEnv(token, "prod")
	eq(t, app.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
		"connectionId": prodConn, "sql": "DROP TABLE orders;", "reason": "增删改查回归",
	}).Code, 42200, "PROD DROP 应被拦截并触发 webhook")

	select {
	case <-received:
	case <-time.After(5 * time.Second):
		t.Fatal("桩没收到事件")
	}
	mu.Lock()
	defer mu.Unlock()

	if got, _ := body["action"].(string); got != "delete" {
		t.Errorf("顶层 action 应是 delete,实际 %q", got)
	}
	rp, ok := body["raw_payload"].(map[string]any)
	if !ok {
		t.Fatalf("raw_payload 应是一个对象,实际 %#v", body["raw_payload"])
	}
	if got, _ := rp["dataAction"].(string); got != "delete" {
		t.Errorf("raw_payload 里的审计行也应带着这个结论,实际 %q", got)
	}
}
