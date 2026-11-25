package bootstrap

// 审计行记下它自己是**哪一类事件**(迁移 0039)。
//
// 此前类型只是一个传给 recordAudit 的参数,用来决定 webhook 要不要发,之后就丢掉了。
// 而它推不出来:(risk, result) 区分不开这几类 —— intercept 会配 rejected 也会配 pending,
// approve 同样两者都配。这里钉四条:
//
//   1. 写进去了,而且是对的那一类:一条被拦下来的命令是 intercept,审完那一下是 approve,
//      登录是 login;
//   2. 按类型筛真的筛得动,而且拼错的取值**报错**,不是回一份空列表;
//   3. 导出的 CSV 也带这一列(人是看着筛过的列表按下导出的)。
//
// "它进了哈希载荷"那一条由 service 包的单元测试逐字段验(audit_hash_payload_test.go):
// 在那里能把同一份输入只改一个字段再算一遍,黑盒里做不到这么准。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

type auditRowTyped struct {
	ID        int64  `json:"id"`
	Command   string `json:"command"`
	Risk      string `json:"risk"`
	Result    string `json:"result"`
	EventType string `json:"eventType"`
	Hash      string `json:"hash"`
	PrevHash  string `json:"prevHash"`
}

type auditPageTyped struct {
	Items []auditRowTyped `json:"items"`
	Total int64           `json:"total"`
}

func (a *testApp) auditByEvent(token, event string) auditPageTyped {
	a.t.Helper()
	q := "/api/v1/audit?page=1&pageSize=200"
	if event != "" {
		q += "&event=" + event
	}
	r := a.do(http.MethodGet, q, token, nil)
	if r.Code != 0 {
		a.t.Fatalf("按类型 %q 查审计: code=%d msg=%s", event, r.Code, r.Msg)
	}
	var p auditPageTyped
	if err := json.Unmarshal(r.Data, &p); err != nil {
		a.t.Fatalf("解码: %v", err)
	}
	return p
}

// withCommand 收下**所有**命令里含 substr 的行。刻意不是"找第一条":同一条命令会在审计里
// 留下不止一行(被拦下来一行、审批那一下又一行),而这条用例要看的正是它们。
func (p auditPageTyped) withCommand(substr string) []auditRowTyped {
	var out []auditRowTyped
	for i := range p.Items {
		if strings.Contains(p.Items[i].Command, substr) {
			out = append(out, p.Items[i])
		}
	}
	return out
}

func (p auditPageTyped) firstOfType(ev string) *auditRowTyped {
	for i := range p.Items {
		if p.Items[i].EventType == ev {
			return &p.Items[i]
		}
	}
	return nil
}

// 拦截、审批、登录各写下自己的类型。
func TestAuditEventType_IsRecordedOnTheRowItself(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123") // 这一下本身就是一条 login
	approver := app.login("zhangwei@aegisdb.io", "aegisdb123")

	ap := app.submitProdHighRisk(admin) // DROP TABLE orders; → intercept
	eq(t, app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/approve", approver, nil).Code, 0, "审批通过")

	all := app.auditByEvent(admin, "")

	// 同一条命令在审计里留下了**两行**:被拦下来那一次,和审批那一次。这正是这一列存在的
	// 理由 —— 两行的命令文本一模一样,而它们是完全不同的两件事。
	rows := all.withCommand("DROP TABLE orders")
	if len(rows) < 2 {
		t.Fatalf("同一条命令应至少留下两行(拦截 + 审批),实际 %d 行", len(rows))
	}
	kinds := map[string]auditRowTyped{}
	for _, r := range rows {
		kinds[r.EventType] = r
	}
	icp, okI := kinds["intercept"]
	apv, okA := kinds["approve"]
	if !okI {
		t.Errorf("应有一行记为 intercept,实际这几行是 %v", kindsOf(rows))
	}
	if !okA {
		t.Errorf("应有一行记为 approve,实际这几行是 %v", kindsOf(rows))
	}
	// 而 (risk, result) 区分不开它们 —— 这一条是"为什么不能在读取时倒推"的证据。
	if okI && okA && icp.Risk == apv.Risk && icp.Result == apv.Result {
		t.Logf("两行的 (risk,result) 完全相同(%s/%s):类型确实推不出来,只能存下来",
			icp.Risk, icp.Result)
	}

	if all.firstOfType("login") == nil {
		t.Error("登录应留下一条 login 类型的审计行")
	}
}

func kindsOf(rows []auditRowTyped) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.EventType+"/"+r.Result)
	}
	return out
}

// 按类型筛真的筛得动;拼错的取值报错,不是回空列表。
func TestAuditEventType_FiltersAndRefusesAnUnknownValue(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	approver := app.login("zhangwei@aegisdb.io", "aegisdb123")
	ap := app.submitProdHighRisk(admin)
	eq(t, app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/approve", approver, nil).Code, 0, "审批通过")

	all := app.auditByEvent(admin, "")
	for _, ev := range []string{"intercept", "approve", "login"} {
		got := app.auditByEvent(admin, ev)
		if got.Total == 0 {
			t.Errorf("按 %q 筛应至少有一行", ev)
		}
		for _, it := range got.Items {
			if it.EventType != ev {
				t.Errorf("按 %q 筛,却筛出一行 eventType=%q", ev, it.EventType)
			}
		}
		if got.Total > all.Total {
			t.Errorf("按 %q 筛(%d)不该多于不筛(%d)", ev, got.Total, all.Total)
		}
	}

	if r := app.do(http.MethodGet, "/api/v1/audit?event=intercepted", admin, nil); r.Code == 0 {
		t.Error("拼错的类型应被拒绝,而不是回一份空列表让人以为真的没有")
	}
	// all 与 "all" 都表示不筛。
	if got := app.auditByEvent(admin, "all"); got.Total != all.Total {
		t.Errorf("event=all 应与不筛一致:%d vs %d", got.Total, all.Total)
	}
}

// 导出的 CSV 带这一列,而且认同一组筛选条件。
func TestAuditEventType_ShowsUpInTheCSVExport(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	app.submitProdHighRisk(admin)

	r := string(app.raw(http.MethodGet, "/api/v1/audit/export?event=intercept", admin))
	head, rest, _ := strings.Cut(r, "\n")
	if !strings.Contains(head, "event_type") {
		t.Fatalf("CSV 表头应有 event_type 一列,实际 %q", head)
	}
	cols := strings.Split(head, ",")
	idx := -1
	for i, c := range cols {
		if strings.TrimSpace(c) == "event_type" {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatal("找不到 event_type 列")
	}
	lines := strings.Split(strings.TrimSpace(rest), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) == "" {
		t.Fatal("按 intercept 导出应至少有一行")
	}
	if !strings.Contains(rest, "intercept") {
		t.Errorf("按 intercept 导出的内容里应出现该类型,实际:%s", clipStr(rest, 300))
	}
	// 拼错的类型在导出这一路同样要被拒。
	if r := app.do(http.MethodGet, "/api/v1/audit/export?event=nope", admin, nil); r.Code == 0 {
		t.Error("导出也应拒绝拼错的类型 —— 两边不一致会导出一份人没在看的东西")
	}
}

func clipStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
