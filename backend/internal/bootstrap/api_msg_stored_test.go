package bootstrap

// 接口返回文案多语言第二批(.scratch/api-msg-i18n/PRD-batch2.md,迁移 0060):存进库的提示文本
// (站内通知、任务错误、审批执行结果)另存 ref,界面按读者语言显示。
//
// 三件事:中文原列一字不变;带参数的句子写入时带上 ref;固定句的老数据(没有 ref 列)读出时按原句补上。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"aegisdb/internal/apimsg"
	"aegisdb/internal/model"
)

type notifRefItem struct {
	Title    string `json:"title"`
	Body     string `json:"body"`
	TitleRef string `json:"titleRef"`
	BodyRef  string `json:"bodyRef"`
	RefNo    string `json:"refNo"`
}

func refCode(t *testing.T, js string) string {
	t.Helper()
	if js == "" {
		return ""
	}
	var r model.RuleRef
	if err := json.Unmarshal([]byte(js), &r); err != nil {
		t.Fatalf("ref 不是合法 JSON:%v(%s)", err, js)
	}
	return r.Code
}

func (a *testApp) notificationRefs(token string) []notifRefItem {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/notifications", token, nil)
	var out struct {
		Items []notifRefItem `json:"items"`
	}
	if r.Code != 0 || json.Unmarshal(r.Data, &out) != nil {
		a.t.Fatalf("list notifications: code=%d msg=%s", r.Code, r.Msg)
	}
	return out.Items
}

// 驳回通知:标题、正文都还是原来的中文,同时带上 ref。
func TestStoredRef_NotificationCarriesRefs(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@aegisdb.io", "aegisdb123")
	owner := app.login("zhangwei@aegisdb.io", "aegisdb123")
	ap := app.submitProdHighRisk(token)
	eq(t, app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/reject", owner, nil).Code, 0, "reject")

	var hit *notifRefItem
	for _, n := range app.notificationRefs(token) {
		if n.RefNo == ap.ApNo {
			n := n
			hit = &n
		}
	}
	if hit == nil {
		t.Fatal("没有驳回通知")
	}
	eq(t, hit.Title, "审批被拒绝", "中文标题不变")
	eq(t, refCode(t, hit.TitleRef), apimsg.Code("审批被拒绝"), "标题 ref")
	eq(t, refCode(t, hit.BodyRef), apimsg.Code("%s 驳回了你的命令：%s"), "正文 ref")
	var body model.RuleRef
	_ = json.Unmarshal([]byte(hit.BodyRef), &body)
	if body.Args["0"] == "" || body.Args["1"] == "" {
		t.Errorf("正文 ref 应带操作人与命令:%+v", body)
	}
}

// 0060 之前写入的老通知:没有 body_ref。固定标题照样能翻译,带参数的正文没有 ref(界面显示中文原句)。
func TestStoredRef_OldNotificationFallsBack(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@aegisdb.io", "aegisdb123")
	u, err := app.repo.GetUserByEmail("linwei@aegisdb.io")
	if err != nil {
		t.Fatal(err)
	}
	if err := app.repo.CreateNotification(&model.Notification{UserID: u.ID, Type: model.NotifExportFailed,
		Title: "导出失败", Body: "订单导出 · 某个旧错误", RefNo: "old-1"}); err != nil {
		t.Fatal(err)
	}
	for _, n := range app.notificationRefs(token) {
		if n.RefNo != "old-1" {
			continue
		}
		eq(t, refCode(t, n.TitleRef), apimsg.Code("导出失败"), "老通知的固定标题按原句补 ref")
		eq(t, n.BodyRef, "", "老通知带参数的正文没有 ref")
		eq(t, n.Body, "订单导出 · 某个旧错误", "正文原样")
		return
	}
	t.Fatal("没找到老通知")
}

// 任务错误:固定句的老行读出时补 ref;带参数的写入时存 ref。
func TestStoredRef_JobErrors(t *testing.T) {
	app := newTestApp(t)
	u, _ := app.repo.GetUserByEmail("linwei@aegisdb.io")

	ej := &model.ExportJob{UserID: u.ID, Status: model.ExportFailed, Error: "导出队列已满,请稍后重试"}
	if err := app.repo.CreateExportJob(ej); err != nil {
		t.Fatal(err)
	}
	got, err := app.repo.GetExportJob(ej.ID)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, got.Error, "导出队列已满,请稍后重试", "导出错误原文")
	eq(t, refCode(t, got.ErrorRef), apimsg.Code("导出队列已满,请稍后重试"), "老行固定句按原句补 ref")

	aj := &model.AsyncJob{UserID: u.ID, Status: model.AsyncRunning}
	if err := app.repo.CreateAsyncJob(aj); err != nil {
		t.Fatal(err)
	}
	m := apimsg.Sprintf("异步执行异常: %v", "boom")
	if err := app.repo.FinishAsyncJob(aj.ID, model.AsyncFailed, "· "+m.S+"\n", "", m.S, apimsg.RefJSON(m.Ref), 0, time.Now()); err != nil {
		t.Fatal(err)
	}
	ga, _ := app.repo.GetAsyncJob(aj.ID)
	eq(t, ga.Error, "异步执行异常: boom", "异步错误原文")
	eq(t, refCode(t, ga.ErrorRef), apimsg.Code("异步执行异常: %v"), "带参数的错误存了 ref")

	// 驱动原文之类认不出的错误:没有 ref,界面显示原文
	ej2 := &model.ExportJob{UserID: u.ID, Status: model.ExportFailed, Error: "Error 1045: Access denied"}
	_ = app.repo.CreateExportJob(ej2)
	g2, _ := app.repo.GetExportJob(ej2.ID)
	eq(t, g2.ErrorRef, "", "认不出的错误不给 ref")
}

// 收尾批(迁移 0061):异步执行日志里网关自己写的行带身份,NOTICE 等数据库原话不在其中。
func TestStoredRef_AsyncLogLines(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@aegisdb.io", "aegisdb123")
	dev := app.connIDByEnv(token, "dev")
	r := app.do(http.MethodPost, "/api/v1/terminal/exec-async", token, map[string]any{
		"connectionId": dev, "sql": "SELECT run_long_proc()", "reason": "nightly job",
	})
	eq(t, r.Code, 0, "submit async")
	var sub struct {
		JobID int64 `json:"jobId"`
	}
	_ = json.Unmarshal(r.Data, &sub)

	var job struct {
		Status  string `json:"status"`
		Log     string `json:"log"`
		LogRefs string `json:"logRefs"`
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		jr := app.do(http.MethodGet, "/api/v1/async-jobs/"+itoa(sub.JobID), token, nil)
		_ = json.Unmarshal(jr.Data, &job)
		if job.Status == "done" || job.Status == "failed" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	eq(t, job.Status, "done", "async job completes")
	var lines []apimsg.LineRef
	if err := json.Unmarshal([]byte(job.LogRefs), &lines); err != nil {
		t.Fatalf("logRefs 不是合法 JSON:%v(%s)", err, job.LogRefs)
	}
	codes := map[string]bool{}
	for _, l := range lines {
		if !strings.Contains(job.Log, l.T+"\n") {
			t.Errorf("logRefs 里的行在日志原文里找不到:%q", l.T)
		}
		codes[l.R.Code] = true
	}
	for _, f := range []string{"· 开始执行 @ %s", "· 执行完成 @ %s · 影响 %d 行"} {
		if !codes[apimsg.Code(f)] {
			t.Errorf("日志缺少 %q 那一行的身份:%s", f, job.LogRefs)
		}
	}
}
