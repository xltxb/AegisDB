package bootstrap

// 点下执行 ≠ 执行完了。
//
// 从前工单在点下执行的那一刻写上 executed_at,命令还在库上跑,界面就只能把它显示成"已执行";
// 跑得久的语句(前端请求先超时)更是一直停在"已执行",看不出它到底成了没有。现在:
//   1. 占位与"执行中"同一条 UPDATE 落下,跑的期间工单说自己在执行中;
//   2. 执行中的单不能再点一次执行,理由说清是"正在执行";
//   3. 网关在执行途中重启的,启动时判失败并写明结果未知,不重跑、不说成功。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"velagateway/internal/model"
	"velagateway/internal/repository"
)

func (a *testApp) ticketByNo(token, apNo string) apOutcome {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/approvals?ap="+apNo, token, nil)
	var page struct {
		Items []apOutcome `json:"items"`
	}
	if err := json.Unmarshal(r.Data, &page); err != nil || len(page.Items) == 0 {
		a.t.Fatalf("取回工单 %s 失败: err=%v", apNo, err)
	}
	return page.Items[0]
}

func TestApprovalExec_RunningWhileTheCommandIsOnTheDatabase(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	dev, prod := app.sameFileConns(admin, "exec-running")
	if r := app.execSQL(admin, dev, `CREATE TABLE t_slow (id INTEGER PRIMARY KEY)`); r.Code != 0 {
		t.Fatalf("建表: %s", r.Msg)
	}
	// DROP 让这一批被拦下走审批;后面那条递归查询要在 sqlite 上跑一两秒
	slow := `DROP TABLE t_slow; SELECT count(*) FROM (WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c WHERE x < 3000000) SELECT x FROM c)`
	ap := app.interceptedTicket(admin, prod, slow)
	approver := app.login("zhangwei@vela.io", "vela123")
	if r := app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/approve", approver, nil); r.Code != 0 {
		t.Fatalf("审批通过: %s", r.Msg)
	}

	done := make(chan apiResp, 1)
	go func() { done <- app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/execute", admin, nil) }()

	// 跑的期间:执行中,有执行时间,不能再执行一次
	sawRunning := false
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && !sawRunning {
		select {
		case <-done:
			t.Fatal("前提不成立:命令结束得太快,没能在执行期间看到工单")
		default:
		}
		cur := app.ticketByNo(admin, ap.ApNo)
		if cur.ExecStatus == "running" {
			sawRunning = true
			if cur.ExecutedAt == nil {
				t.Error("执行中的单应当已经有执行时间(占位)")
			}
			again := app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/execute", admin, nil)
			if again.Code == 0 || !strings.Contains(again.Msg, "正在执行") {
				t.Errorf("执行中的单不能再执行,理由应说正在执行: code=%d msg=%q", again.Code, again.Msg)
			}
		} else if cur.ExecStatus != "" {
			t.Fatalf("还没看到执行中就已经有结论了: %q", cur.ExecStatus)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !sawRunning {
		t.Fatal("命令在库上跑的期间,工单应当显示执行中")
	}
	if r := <-done; r.Code != 0 {
		t.Fatalf("执行: code=%d msg=%s", r.Code, r.Msg)
	}
	if got := app.ticketByNo(admin, ap.ApNo).ExecStatus; got != "success" {
		t.Errorf("跑完之后应以实际结果为准(success),实际 %q", got)
	}
}

func TestApprovalExec_InterruptedByRestartIsFailedNotRerun(t *testing.T) {
	app := newTestApp(t)
	db := app.repo.DB()
	mk := func(no, execStatus string) {
		now := time.Now()
		if err := db.Create(&model.Approval{ApNo: no, ConnectionID: 1, Env: "prod", Instance: "x", Command: "DROP TABLE t",
			InitiatorID: 1, RiskLevel: "high", Status: model.StatusApproved, ExecStatus: execStatus, ExecutedAt: &now,
			Result: "原样"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	mk("AP-RUN-1", model.ExecStatusRunning)
	mk("AP-OK-1", model.ExecStatusSuccess)

	n, err := app.repo.FailStuckApprovalExecutions()
	if err != nil || n != 1 {
		t.Fatalf("应收尾 1 张执行中的单: n=%d err=%v", n, err)
	}
	var run, ok model.Approval
	db.Where("ap_no = ?", "AP-RUN-1").First(&run)
	db.Where("ap_no = ?", "AP-OK-1").First(&ok)
	if run.ExecStatus != model.ExecStatusFailed || run.Result != repository.ApprovalInterrupted || run.ExecutedAt == nil {
		t.Errorf("中断的单应判失败、写明结果未知、保留执行时间(不重跑): %+v", run)
	}
	if ok.ExecStatus != model.ExecStatusSuccess || ok.Result != "原样" {
		t.Errorf("已有结论的单不该被动: %+v", ok)
	}
}
