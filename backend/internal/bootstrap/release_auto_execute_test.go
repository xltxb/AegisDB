package bootstrap

// 2026-10-30:发布流程新增「自动执行」阶段(auto_execute)。
//
// 与「执行变更」做同一件事,只少一道「确认执行」的人工闸 —— 前序阶段通过就下发。只能挂在打开了
// 「允许自动执行」的分层上(迁移 0058,默认 dev / uat 开,prod / staging / gli 关),保存流程时校验;
// 执行时按实例此刻所在分层再查一次,被关掉就退回等人工确认。复判与逐条审计照旧。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestAutoExecute_TierDefaultsAndBackfillRunsOnce(t *testing.T) {
	app := newTestApp(t)
	for code, want := range map[string]bool{"prod": false, "staging": false, "gli": false, "dev": true, "uat": true} {
		if got := tierOfCode(t, app, code).AutoExecute; got != want {
			t.Errorf("%s:默认 允许自动执行=%v,应为 %v", code, got, want)
		}
	}
	// 运维把 uat 关掉之后,回填再跑一次(重启)不能把它打开回来
	admin := app.login("linwei@vela.io", "vela123")
	app.setTierSwitch(admin, "uat", "autoExecute", false)
	if err := backfillTierAutoExecute(app.repo.DB()); err != nil {
		t.Fatal(err)
	}
	if tierOfCode(t, app, "uat").AutoExecute {
		t.Error("回填只能跑一次:重跑把运维关掉的 uat 自动执行又打开了")
	}
	// 局部更新:只改这一个开关,其它开关不动
	if tierOfCode(t, app, "uat").StrictNoWhere != true {
		t.Error("只改了自动执行,无 WHERE 拦截不该跟着变")
	}
}

func TestAutoExecute_NewTierInheritsTemplateUnlessGiven(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	create := func(code, tmpl string, extra map[string]any) {
		body := map[string]any{"code": code, "displayName": code, "templateCode": tmpl, "strictNoWhere": true}
		for k, v := range extra {
			body[k] = v
		}
		if r := app.do(http.MethodPost, "/api/v1/env-tiers", admin, body); r.Code != 0 {
			t.Fatalf("建分层 %s: %d %s", code, r.Code, r.Msg)
		}
	}
	create("prod-hk", "prod", nil)
	create("fat", "dev", nil)
	create("fat-2", "dev", map[string]any{"autoExecute": false})
	for code, want := range map[string]bool{"prod-hk": false, "fat": true, "fat-2": false} {
		if got := tierOfCode(t, app, code).AutoExecute; got != want {
			t.Errorf("%s:允许自动执行=%v,应为 %v", code, got, want)
		}
	}
}

// 生产类分层、以及「适用全部分层」的流程,都不能保存自动执行阶段;后端拒,不靠前端。
func TestAutoExecute_RejectedOnTiersThatDoNotAllowIt(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	stages := []map[string]any{
		{"type": "review", "config": `{"failOn":"error"}`},
		{"type": "auto_execute"},
	}
	save := func(name, tier string) apiResp {
		return app.do(http.MethodPost, "/api/v1/pipelines", admin, map[string]any{
			"name": name, "tierCode": tier, "enabled": true, "stages": stages,
		})
	}
	for _, tier := range []string{"prod", "staging", "gli", ""} {
		if r := save("自动-"+tier, tier); r.Code == 0 {
			t.Errorf("分层 %q 的流程不该能保存自动执行阶段", tier)
		} else if !strings.Contains(r.Msg, "自动执行") {
			t.Errorf("分层 %q:拒绝理由应提到自动执行,得到 %q", tier, r.Msg)
		}
	}
	if r := save("自动-dev", "dev"); r.Code != 0 {
		t.Fatalf("dev 分层应能保存自动执行阶段: %d %s", r.Code, r.Msg)
	}

	// 已保存的 dev 流程改挂到 prod:同样拒
	pid := app.createPipeline(admin, "自动-改挂", "dev", stages)
	r := app.do(http.MethodPut, "/api/v1/pipelines/"+itoa(pid), admin, map[string]any{
		"name": "自动-改挂", "tierCode": "prod", "enabled": true, "stages": stages,
	})
	if r.Code == 0 {
		t.Error("自动执行流程改挂到 prod 不该保存成功")
	}

	// 分层把开关关掉之后,dev 也不能再保存
	app.setTierSwitch(admin, "dev", "autoExecute", false)
	if r := save("自动-dev-关", "dev"); r.Code == 0 {
		t.Error("dev 关掉允许自动执行后,不该还能保存自动执行阶段")
	}
}

// 执行变更与自动执行合起来只能有一个:两个会把同一份 SQL 跑两遍。
func TestAutoExecute_CountsAsTheExecuteStage(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	r := app.do(http.MethodPost, "/api/v1/pipelines", admin, map[string]any{
		"name": "双执行", "tierCode": "dev", "enabled": true, "stages": []map[string]any{
			{"type": "execute"}, {"type": "auto_execute"},
		},
	})
	if r.Code == 0 {
		t.Error("执行变更 + 自动执行同在一个流程里不该保存成功")
	}
	// 只有自动执行(没有执行变更)是合法的完整流程;失败策略被强制为中止,名称由类型派生
	pid := app.createPipeline(admin, "仅自动", "dev", []map[string]any{
		{"name": "随便写", "type": "auto_execute", "onFailure": "continue"},
	})
	g := app.do(http.MethodGet, "/api/v1/pipelines/"+itoa(pid), admin, nil)
	var p struct {
		Stages []struct {
			Name      string `json:"name"`
			Type      string `json:"type"`
			OnFailure string `json:"onFailure"`
		} `json:"stages"`
	}
	_ = json.Unmarshal(g.Data, &p)
	if len(p.Stages) != 1 || p.Stages[0].Name != "自动执行" || p.Stages[0].OnFailure != "abort" {
		t.Errorf("自动执行阶段应名为「自动执行」、失败策略 abort,得到 %+v", p.Stages)
	}
}

// 核心场景:dev 上 规范审查 → 自动执行,不点任何按钮就跑完,且逐条入审计。
func TestAutoExecute_RunsWithoutConfirmationAndAudits(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	dev := app.connIDByEnv(admin, "dev")
	pid := app.createPipeline(admin, "fat", "dev", []map[string]any{
		{"type": "review", "config": `{"failOn":"error"}`},
		{"type": "auto_execute"},
	})
	r := app.submitRelease(admin, map[string]any{
		"title": "自动执行", "pipelineId": pid, "connectionId": dev,
		"sql": "SELECT 4242 AS auto_exec_marker;", "reason": "自动执行回归",
	})
	eq(t, r.Code, 0, "submit")
	var rel releaseView
	_ = json.Unmarshal(r.Data, &rel)

	done := app.waitRelease(admin, rel.ID, "success", "failed", "waiting")
	eq(t, done.Status, "success", "auto_execute runs without a click")
	ex := stageByType(done, "auto_execute")
	if ex == nil {
		t.Fatal("no auto_execute stage on the run")
	}
	eq(t, ex.Status, "success", "auto_execute stage")
	if !strings.Contains(ex.Log, "自动执行") || !strings.Contains(ex.Log, "执行完成") {
		t.Errorf("stage log should say it auto-executed, got %q", ex.Log)
	}

	var rows []struct {
		Command string `json:"command"`
		Actor   string `json:"actor"`
		Result  string `json:"result"`
	}
	_ = json.Unmarshal(app.auditItemsRaw(admin, "?range=today&pageSize=200"), &rows)
	executed, confirm := false, false
	for _, row := range rows {
		if strings.Contains(row.Command, "auto_exec_marker") && row.Result == "executed" {
			executed = true
			eq(t, row.Actor, "Lin Wei", "actor is whose change it is")
		}
		if strings.Contains(row.Command, "RELEASE-EXECUTE-CONFIRM") {
			confirm = true
		}
	}
	if !executed {
		t.Error("the auto-executed statement has no executed audit row")
	}
	if confirm {
		t.Error("no human confirmed, yet a RELEASE-EXECUTE-CONFIRM row exists")
	}
}

// 流程保存之后分层把开关关了:走到这一步退回等人工确认,点了才跑。
func TestAutoExecute_FallsBackToConfirmWhenTierSwitchedOff(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	dev := app.connIDByEnv(admin, "dev")
	pid := app.createPipeline(admin, "fat", "dev", []map[string]any{
		{"type": "auto_execute"},
	})
	app.setTierSwitch(admin, "dev", "autoExecute", false)

	r := app.submitRelease(admin, map[string]any{
		"title": "自动执行退回", "pipelineId": pid, "connectionId": dev,
		"sql": "SELECT 1;", "reason": "自动执行退回回归",
	})
	eq(t, r.Code, 0, "submit")
	var rel releaseView
	_ = json.Unmarshal(r.Data, &rel)

	waiting := app.waitRelease(admin, rel.ID, "waiting", "success", "failed")
	eq(t, waiting.Status, "waiting", "parks at the gate when the tier no longer allows auto")
	ex := stageByType(waiting, "auto_execute")
	eq(t, ex.Status, "waiting", "auto_execute waits")
	if !strings.Contains(ex.Log, "未开启") {
		t.Errorf("stage log should say why it waits, got %q", ex.Log)
	}

	// 待办里出现这一条(和执行变更同一类「等我确认」)
	td := app.do(http.MethodGet, "/api/v1/approvals/todo", admin, nil)
	if td.Code == 0 && !strings.Contains(string(td.Data), "auto_execute") {
		t.Errorf("the waiting auto_execute gate should show up in the todo list, got %s", td.Data)
	}

	c := app.confirmStage(admin, rel.ID, ex.ID)
	eq(t, c.Code, 0, "creator confirms")
	done := app.waitRelease(admin, rel.ID, "success", "failed")
	eq(t, done.Status, "success", "runs after the click")
}
