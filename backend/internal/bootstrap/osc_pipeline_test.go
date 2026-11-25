package bootstrap

// 发布流水线里的在线变更(.scratch/osc-product 工单 04),走真实接口、对着真 MySQL。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"aegisdb/internal/model"
)

func (a *testApp) oscRelease(token string, prod int64, stages []map[string]any, sql string) releaseView {
	a.t.Helper()
	pid := a.createPipeline(token, "在线变更流程", "", stages)
	r := a.submitRelease(token, map[string]any{"title": "大表加索引", "pipelineId": pid, "connectionId": prod, "sql": sql, "reason": "在线变更"})
	if r.Code != 0 {
		a.t.Fatalf("提交发布单: code=%d msg=%s", r.Code, r.Msg)
	}
	var rel releaseView
	_ = json.Unmarshal(r.Data, &rel)
	return rel
}

// 发布单停在审批阶段:通过它,让流水线继续
func (a *testApp) approveReleaseStage(admin string, rel releaseView) releaseView {
	a.t.Helper()
	w := a.waitRelease(admin, rel.ID, "waiting", "failed", "success")
	ap := stageByType(w, "approve")
	if ap == nil || ap.ApprovalID == 0 {
		a.t.Fatalf("应停在审批阶段: %+v", w)
	}
	approver := a.login("zhangwei@aegisdb.io", "aegisdb123")
	eq(a.t, a.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ApprovalID)+"/approve", approver, nil).Code, 0, "审批通过")
	a.svc.ResumeReleaseApprovals()
	return a.waitRelease(admin, rel.ID, "waiting", "failed", "success")
}

func TestOSCPipeline_PrecheckAndExecute(t *testing.T) {
	app := newTestApp(t)
	admin, prod, schema := app.oscTermSetup()
	rel := app.oscRelease(admin, prod, []map[string]any{
		{"name": "人工审批", "type": "approve"},
		{"name": "执行变更", "type": "execute"},
	}, "ALTER TABLE big ADD INDEX idx_a (a);")

	// 收单预检:说明这条会走在线变更
	raw := app.do(http.MethodGet, "/api/v1/releases/"+itoa(rel.ID), admin, nil)
	if !strings.Contains(string(raw.Data), "preOscOnline") {
		t.Errorf("预检应说明将以在线变更执行: %s", raw.Data)
	}

	w := app.approveReleaseStage(admin, rel)
	ex := stageByType(w, "execute")
	eq(t, app.confirmStage(admin, rel.ID, ex.ID).Code, 0, "确认执行")
	done := app.waitReleaseLong(admin, rel.ID, "success", "failed")
	if done.Status != "success" {
		t.Fatalf("发布应成功: %+v", done)
	}
	log := stageByType(done, "execute").Log
	if !strings.Contains(log, "以在线变更执行") || !strings.Contains(log, "在线变更完成") || !strings.Contains(log, "_big_del") {
		t.Errorf("执行日志应说明在线变更与保留的旧表: %s", log)
	}
	if !oscHasIndex(t, oscRaw(t, schema), "big", "idx_a") {
		t.Error("索引应已加上")
	}
}

func TestOSCPipeline_NeedsAnApproval(t *testing.T) {
	app := newTestApp(t)
	admin, prod, _ := app.oscTermSetup()
	// 能力矩阵把生产 DDL 设成放行、也不命中高危字典:在线变更照样要审批 —— 没有审批阶段的流程收不了单
	if err := app.repo.DB().Exec("UPDATE tbl_role_capability SET level = 'allow' WHERE capability IN ('ddl', 'release')").Error; err != nil {
		t.Fatal(err)
	}
	app.repo.DB().Exec("DELETE FROM tbl_risk_command") // 引擎每次判定实时读库
	pid := app.createPipeline(admin, "无审批流程", "", []map[string]any{{"name": "执行变更", "type": "execute"}})
	r := app.submitRelease(admin, map[string]any{"title": "大表加索引", "pipelineId": pid, "connectionId": prod,
		"sql": "ALTER TABLE big ADD INDEX idx_a (a);", "reason": "在线变更"})
	if r.Code == 0 || !strings.Contains(r.Msg, "在线变更") {
		t.Fatalf("没有审批阶段的流程不该收在线变更的单: code=%d msg=%s", r.Code, r.Msg)
	}
}

func TestOSCPipeline_AbortStopsTheMigration(t *testing.T) {
	app := newTestApp(t)
	admin, prod, schema := app.oscTermSetup()
	eq(t, app.do(http.MethodPut, "/api/v1/settings", admin, map[string]any{"osc.chunkSize": 1}).Code, 0, "一行一块")
	rel := app.oscRelease(admin, prod, []map[string]any{
		{"name": "人工审批", "type": "approve"},
		{"name": "执行变更", "type": "execute"},
	}, "ALTER TABLE big ADD INDEX idx_a (a);")
	w := app.approveReleaseStage(admin, rel)
	eq(t, app.confirmStage(admin, rel.ID, stageByType(w, "execute").ID).Code, 0, "确认执行")

	var job *model.OSCJob
	deadline := time.Now().Add(30 * time.Second)
	for job == nil || job.Phase != "copying" {
		job, _ = app.repo.RunningOSCJobForRelease(rel.ID)
		if time.Now().After(deadline) {
			t.Fatalf("迁移没有开始拷贝: %+v", job)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if r := app.do(http.MethodPost, "/api/v1/releases/"+itoa(rel.ID)+"/abort", admin, nil); r.Code != 0 {
		t.Fatalf("中止发布: code=%d msg=%s", r.Code, r.Msg)
	}
	done := app.waitReleaseLong(admin, rel.ID, "failed", "aborted", "success")
	if done.Status == "success" {
		t.Fatalf("中止后发布不该成功: %+v", done)
	}
	if j, _ := app.repo.GetOSCJob(job.ID); j.Status != model.OSCAborted {
		t.Errorf("迁移应已中止: %+v", j)
	}
	if oscHasIndex(t, oscRaw(t, schema), "big", "idx_a") {
		t.Error("中止后原表不动")
	}
}

// waitReleaseLong 同 waitRelease,但给在线变更留足时间。
func (a *testApp) waitReleaseLong(token string, id int64, want ...string) releaseView {
	a.t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	var last releaseView
	for time.Now().Before(deadline) {
		last = a.getRelease(token, id)
		for _, w := range want {
			if last.Status == w {
				return last
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	a.t.Fatalf("发布单 %d 一直是 %q(err=%q)", id, last.Status, last.Error)
	return last
}
