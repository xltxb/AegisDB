package bootstrap

// 开放接口的人工确认:外部平台(「待人工确认」卡片)替确认人放行发布单上的「人工确认」。
// 「执行变更」那一步不归它:只能在网关上由人点(2026-11-10 用户要求)。
//
// 与外部审批同一组门:凭据不是确认人(必须点名一个人,按控制台「继续」同一条规则核对)、
// 发起人借外部平台也放行不了自己的单、够不到的实例碰不了、没勾 release:approve 用不了;
// 控制台和外部两边先到先得,同一道闸不会被放行两次。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

type openConfirm struct {
	RelNo            string `json:"relNo"`
	Stage            string `json:"stage"`
	AlreadyConfirmed bool   `json:"alreadyConfirmed"`
	ReleaseStatus    string `json:"releaseStatus"`
	ExecutePending   bool   `json:"executePending"`
}

func (a *testApp) openConfirm(token, relNo, confirmer string) (apiResp, openConfirm) {
	a.t.Helper()
	return a.openConfirmBody(token, relNo, map[string]any{"confirmer": confirmer, "comment": "卡片上点了确认"})
}

func (a *testApp) openConfirmBody(token, relNo string, body map[string]any) (apiResp, openConfirm) {
	a.t.Helper()
	r := a.openDo(http.MethodPost, "/api/v1/open/releases/"+relNo+"/confirm", token, body)
	var v openConfirm
	if r.Code == 0 {
		if err := json.Unmarshal(r.Data, &v); err != nil {
			a.t.Fatalf("decode confirm: %v", err)
		}
	}
	return r, v
}

// consoleReleaseAtManual 由管理员在控制台提一张单,停在人工确认上(确认角色 l2)。
func (a *testApp) consoleReleaseAtManual(admin, title string) releaseView {
	a.t.Helper()
	pid := a.createPipeline(admin, title+"-流程", "", []map[string]any{
		{"name": "人工确认", "type": "manual", "config": `{"confirmRole":"l2"}`},
		{"name": "执行变更", "type": "execute"},
	})
	r := a.submitRelease(admin, map[string]any{
		"title": title, "pipelineId": pid, "connectionId": a.connIDByEnv(admin, "staging"),
		"sql": "SELECT 1;", "reason": "外部确认回归",
	})
	if r.Code != 0 {
		a.t.Fatalf("submit: code=%d msg=%s", r.Code, r.Msg)
	}
	var rel releaseView
	_ = json.Unmarshal(r.Data, &rel)
	rel = a.waitRelease(admin, rel.ID, "waiting", "failed", "success")
	if g := stageByType(rel, "manual"); g == nil || g.Status != "waiting" {
		a.t.Fatalf("should park on the manual gate, got %+v", g)
	}
	return rel
}

// 主路径:确认角色里的人经外部平台放行,流水线越过人工确认往下走;审计记下是谁、经由哪个系统。
func TestOpenConfirmPassesManualGate(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	token := app.approverCredential(admin, "im-card", nil, []string{"release:approve"}, 0)
	rel := app.consoleReleaseAtManual(admin, "外部确认")

	r, c := app.openConfirm(token, rel.RelNo, "ChenHao@aegisdb.io") // 邮箱不分大小写
	eq(t, r.Code, 0, "a member of the confirm role passes the gate through the open API")
	eq(t, c.Stage, "人工确认", "stage name reported")
	eq(t, c.AlreadyConfirmed, false, "this call passed it")

	passed := false
	for i := 0; i < 300 && !passed; i++ {
		if g := stageByType(app.getRelease(admin, rel.ID), "manual"); g != nil && g.Status == "success" {
			passed = true
		} else {
			time.Sleep(20 * time.Millisecond)
		}
	}
	if !passed {
		t.Fatal("the manual stage should be success once confirmed")
	}

	// 重试安全:再来一次拿到的是"已放行",不报错 —— 也不会顺手把紧跟着的执行闸放了。
	r, c = app.openConfirm(token, rel.RelNo, "chenhao@aegisdb.io")
	eq(t, r.Code, 0, "a retry is answered, not refused")
	eq(t, c.AlreadyConfirmed, true, "reported as already confirmed")
	after := app.waitRelease(admin, rel.ID, "waiting", "failed", "success")
	if ex := stageByType(after, "execute"); ex != nil && ex.Status == "waiting" {
		// 流水线停在执行变更:重试不碰它,只告诉平台下一步要去网关点。
		eq(t, ex.ConfirmedBy, "", "a retry must not confirm the execute gate")
		eq(t, c.ExecutePending, true, "the caller is told execution waits for a human in the gateway")
	}

	var rows []struct {
		Operator string `json:"operator"`
		Command  string `json:"command"`
	}
	_ = json.Unmarshal(app.auditItemsRaw(admin, "?range=today&pageSize=200"), &rows)
	via, decision := false, false
	for _, row := range rows {
		if strings.Contains(row.Command, "open.release.confirm") && strings.Contains(row.Command, rel.RelNo) {
			via = true
			eq(t, row.Operator, "API:im-card-凭据", "the asking system is named")
			if !strings.Contains(row.Command, "chenhao@aegisdb.io") {
				t.Errorf("the audit line should name the confirmer, got %q", row.Command)
			}
		}
		if strings.Contains(row.Command, "RELEASE-CONTINUE "+rel.RelNo) {
			decision = true
		}
	}
	if !via {
		t.Error("no audit row for the external confirmation")
	}
	if !decision {
		t.Error("the gate decision itself should be on the chain, as from the console")
	}
}

// 凭据不是确认人:不在确认角色里的、发起人本人、不存在 / 没写的账号都放行不了。
func TestOpenConfirmRefusesWhoCannotConfirm(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	token := app.approverCredential(admin, "im-refuse", nil, []string{"release:approve"}, 0)
	rel := app.consoleReleaseAtManual(admin, "外部确认拒绝")

	r, _ := app.openConfirm(token, rel.RelNo, "zhangwei@aegisdb.io") // 有审批权,但不在 l2
	eq(t, r.Code, 40300, "someone outside the confirm role cannot pass the gate")
	r, _ = app.openConfirm(token, rel.RelNo, "linwei@aegisdb.io")
	eq(t, r.Code, 40300, "the initiator cannot pass their own release's gate")
	if !strings.Contains(r.Msg, "确认人无权") {
		t.Errorf("should say the confirmer may not pass it, got %q", r.Msg)
	}
	r, _ = app.openConfirm(token, rel.RelNo, "nobody@aegisdb.io")
	eq(t, r.Code, 40001, "unknown confirmer")
	if !strings.Contains(r.Msg, "nobody@aegisdb.io") {
		t.Errorf("should name the unknown account, got %q", r.Msg)
	}
	r, _ = app.openConfirm(token, rel.RelNo, "")
	eq(t, r.Code, 40001, "confirmer is required")
	r, _ = app.openConfirm(token, "REL-999999", "chenhao@aegisdb.io")
	eq(t, r.Code, 40001, "unknown release")

	g := stageByType(app.getRelease(admin, rel.ID), "manual")
	eq(t, g.Status, "waiting", "nothing above passed the gate")
}

// release:approve 必须显式勾选;范围外实例上的单碰不了。
func TestOpenConfirmNeedsScopeAndInstance(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	rel := app.consoleReleaseAtManual(admin, "外部确认范围")

	def := app.approverCredential(admin, "ci-default", nil, nil, 0) // 默认集合
	r, _ := app.openConfirm(def, rel.RelNo, "chenhao@aegisdb.io")
	if r.Code == 0 {
		t.Fatal("a credential without release:approve must not pass any gate")
	}
	sandbox := app.approverCredential(admin, "im-sandbox", []string{"sandbox"}, []string{"release:approve"}, 0)
	r, _ = app.openConfirm(sandbox, rel.RelNo, "chenhao@aegisdb.io")
	eq(t, r.Code, 40300, "a release outside the service account's instances is off limits")

	g := stageByType(app.getRelease(admin, rel.ID), "manual")
	eq(t, g.Status, "waiting", "still waiting")
}

// 两边都能点,先到先得:控制台先放行了,外部再来拿到 alreadyConfirmed。
func TestOpenConfirmFirstWins(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	l2 := app.login("chenhao@aegisdb.io", "aegisdb123")
	token := app.approverCredential(admin, "im-race", nil, []string{"release:approve"}, 0)
	rel := app.consoleReleaseAtManual(admin, "外部确认先到先得")

	eq(t, app.confirmStage(l2, rel.ID, stageByType(rel, "manual").ID).Code, 0, "console continues first")
	r, c := app.openConfirm(token, rel.RelNo, "chenhao@aegisdb.io")
	eq(t, r.Code, 0, "a late call is answered, not refused")
	eq(t, c.AlreadyConfirmed, true, "reported as already confirmed")
}

// 停在审批上(不是人工闸):说清楚,不是放行。
func TestOpenConfirmWithoutWaitingGate(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	token := app.approverCredential(admin, "im-nogate", nil, []string{"release:approve"}, 0)
	rel := app.consoleReleaseAwaitingL2(admin, "停在审批")
	r, _ := app.openConfirm(token, rel.RelNo, "chenhao@aegisdb.io")
	eq(t, r.Code, 40001, "no manual gate waiting")
	if !strings.Contains(r.Msg, "人工确认") {
		t.Errorf("should say there is no gate waiting, got %q", r.Msg)
	}
}

// 执行变更不能经开放接口放行:只能在网关上由人点。连 gate=execute 这种字段也不认。
func TestOpenConfirmNeverPassesExecuteGate(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	token := app.approverCredential(admin, "im-exec", nil, []string{"release:approve"}, 0)
	pid := app.createPipeline(admin, "执行确认-流程", "", []map[string]any{
		{"name": "执行变更", "type": "execute", "config": `{"confirmRole":"l2"}`},
	})
	r := app.submitRelease(admin, map[string]any{
		"title": "外部不能确认执行", "pipelineId": pid, "connectionId": app.connIDByEnv(admin, "staging"),
		"sql": "SELECT 1;", "reason": "外部确认回归",
	})
	eq(t, r.Code, 0, "submit")
	var rel releaseView
	_ = json.Unmarshal(r.Data, &rel)
	rel = app.waitRelease(admin, rel.ID, "waiting", "failed", "success")
	if ex := stageByType(rel, "execute"); ex == nil || ex.Status != "waiting" {
		t.Fatalf("execute gate should be waiting, got %+v", ex)
	}

	for _, body := range []map[string]any{
		{"confirmer": "chenhao@aegisdb.io"},
		{"confirmer": "chenhao@aegisdb.io", "gate": "execute"},
		{"confirmer": "linwei@aegisdb.io", "gate": "execute"}, // 发起人在控制台能点执行,经接口也不行
	} {
		rr, _ := app.openConfirmBody(token, rel.RelNo, body)
		eq(t, rr.Code, 40001, "the execute gate is never passed through the open API")
		if !strings.Contains(rr.Msg, "执行变更") {
			t.Errorf("should say execution must be confirmed in the gateway, got %q", rr.Msg)
		}
	}
	ex := stageByType(app.getRelease(admin, rel.ID), "execute")
	eq(t, ex.Status, "waiting", "execute gate untouched")
	eq(t, ex.ConfirmedBy, "", "nobody confirmed execution")
}
