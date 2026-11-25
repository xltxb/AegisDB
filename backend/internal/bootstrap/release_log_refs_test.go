package bootstrap

// 发布阶段日志 / 错误条的机器可读身份(迁移 0059,前端 rlLog.)。
//
// 用户在英文界面看 REL-1,日志与错误条整段是中文:它们是服务端拼好的中文。现在同一段话另以
// RuleRef 下发,界面按读者语言渲染。这里钉住三件事:
//   1. refs 渲染出的中文与 log / error 列一字不差 —— 两份对不上,英文界面说的就不是记录里那件事;
//   2. 执行器那句话的 code 跟着走(outText.execAffected),英文界面才翻得出"N 行受影响";
//   3. 列表接口不带 refs(与不带日志同一个理由:体积)。

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"aegisdb/internal/model"
)

func parseRefs(t *testing.T, raw string) []model.RuleRef {
	t.Helper()
	var refs []model.RuleRef
	if err := json.Unmarshal([]byte(raw), &refs); err != nil {
		t.Fatalf("logRefs is not a RuleRef array: %v (%q)", err, raw)
	}
	return refs
}

func TestReleaseStageLogRefsRenderToTheStoredLog(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	conn := realConnForRelease(t, app, admin, func(db *sql.DB) {
		_, _ = db.Exec(`CREATE TABLE t_bond (bondseq_id INTEGER, bond_type INTEGER)`)
		_, _ = db.Exec(`INSERT INTO t_bond VALUES (1, 0), (2, 0)`)
	})
	pid := app.createPipeline(admin, "日志多语言", "", []map[string]any{
		{"name": "规范审查", "type": "review", "config": `{"failOn":"none"}`},
		{"name": "执行变更", "type": "execute"},
		{"name": "执行后校验", "type": "verify", "config": `{"sql":"select * from t_bond"}`},
		{"name": "结果通知", "type": "notify"},
	})
	r := app.submitRelease(admin, map[string]any{
		"title": "日志多语言", "pipelineId": pid, "connectionId": conn, "reason": "rlLog",
		"sql": "update t_bond set bond_type=1 where bondseq_id=1;\nselect * from t_bond;",
	})
	eq(t, r.Code, 0, "submit release")
	var rel releaseView
	_ = json.Unmarshal(r.Data, &rel)
	done := app.confirmExecutionAndWait(admin, rel.ID)
	eq(t, done.Status, "success", "release runs ("+done.Error+")")
	if done.ErrorRef != "" {
		t.Errorf("a successful release carries no error ref, got %q", done.ErrorRef)
	}

	for _, st := range done.Stages {
		if st.LogRefs == "" {
			t.Errorf("stage %s: no logRefs", st.Type)
			continue
		}
		refs := parseRefs(t, st.LogRefs)
		if got := model.RenderRelLogLines(refs); got != st.Log {
			t.Errorf("stage %s: refs render\n%q\nbut log is\n%q", st.Type, got, st.Log)
		}
	}

	ex := parseRefs(t, stageByType(done, "execute").LogRefs)
	var affected, rows bool
	for _, ref := range ex {
		if ref.Code == model.RelLogStmtOk && len(ref.Parts) == 1 {
			affected = affected || (ref.Args["ns"] == "outText" && ref.Parts[0].Code == model.OutExecAffected)
			rows = rows || (ref.Args["ns"] == "rlLog" && ref.Parts[0].Code == model.RelLogOutRows)
		}
	}
	if !affected || !rows {
		t.Errorf("execute refs should carry the executor's sentence by code (affected=%v rows=%v): %s", affected, rows, stageByType(done, "execute").LogRefs)
	}

	lr := app.do(http.MethodGet, "/api/v1/releases?scope=all&pageSize=100", admin, nil)
	var page struct {
		Items []releaseView `json:"items"`
	}
	_ = json.Unmarshal(lr.Data, &page)
	for _, it := range page.Items {
		if it.ID != rel.ID {
			continue
		}
		for _, st := range it.Stages {
			if st.LogRefs != "" {
				t.Errorf("list should not carry stage logRefs (stage %s)", st.Type)
			}
		}
		return
	}
	t.Fatal("release not in list")
}

func TestReleaseErrorRefRendersToTheStoredError(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	conn := realConnForRelease(t, app, admin, func(db *sql.DB) {
		_, _ = db.Exec(`CREATE TABLE t_ok (n INTEGER)`)
	})
	pid := app.createPipeline(admin, "错误条多语言", "", []map[string]any{{"name": "执行变更", "type": "execute"}})
	r := app.submitRelease(admin, map[string]any{
		"title": "错误条多语言", "pipelineId": pid, "connectionId": conn, "reason": "rlLog",
		"sql": "insert into t_ok values (1);\nupdate t_missing set n=2 where n=1;",
	})
	eq(t, r.Code, 0, "submit release")
	var rel releaseView
	_ = json.Unmarshal(r.Data, &rel)
	done := app.confirmExecutionAndWait(admin, rel.ID)
	eq(t, done.Status, "failed", "second statement fails")

	var ref model.RuleRef
	if err := json.Unmarshal([]byte(done.ErrorRef), &ref); err != nil {
		t.Fatalf("errorRef is not a RuleRef: %v (%q)", err, done.ErrorRef)
	}
	eq(t, ref.Code, model.RelErrStageFailed, "error ref code")
	eq(t, ref.Args["type"], "execute", "error ref names the stage by type")
	if got := model.RenderRelLog(&ref); got != done.Error {
		t.Errorf("error ref renders %q but error is %q", got, done.Error)
	}
	if len(ref.Parts) == 0 || ref.Parts[0].Code != model.RelLogHaltedFail {
		t.Errorf("error ref should carry the log's first line by code, got %+v", ref.Parts)
	}
	ex := stageByType(done, "execute")
	if got := model.RenderRelLogLines(parseRefs(t, ex.LogRefs)); got != ex.Log {
		t.Errorf("failed execute: refs render\n%q\nbut log is\n%q", got, ex.Log)
	}
	if !strings.Contains(ex.Log, "no such table") {
		t.Errorf("driver error should stay in the log verbatim, got %q", ex.Log)
	}
}
