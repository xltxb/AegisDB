package bootstrap

// backup 阶段的动词白名单(审查修复 #2)。
//
// The backup stage runs template-configured SQL straight through the executor —
// the one execution channel that never met the risk engine. The guard is a verb
// allowlist: a backup COPIES data (CREATE … AS SELECT / INSERT … SELECT /
// SELECT INTO), so only copying verbs may appear there. A template whose
// "backup" says DROP is not a backup, whoever wrote it.

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBackupStageRefusesDestructiveVerbs(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	stg := app.connIDByEnv(admin, "staging")

	pid := app.createPipeline(admin, "危险备份", "", []map[string]any{
		{"name": "备份", "type": "backup", "config": `{"sql":"DROP TABLE tbl_order;"}`},
		{"name": "执行变更", "type": "execute"},
	})
	r := app.submitRelease(admin, map[string]any{
		"title": "带危险备份的发布", "pipelineId": pid, "connectionId": stg,
		"sql": "SELECT 1;", "reason": "回归 #2",
	})
	eq(t, r.Code, 0, "submit release")
	var rel releaseView
	_ = json.Unmarshal(r.Data, &rel)

	done := app.waitRelease(admin, rel.ID, "failed", "success")
	eq(t, done.Status, "failed", "a DROP in the backup slot fails the run")
	bk := stageByType(done, "backup")
	eq(t, bk.Status, "failed", "backup stage refused")
	if !strings.Contains(bk.Log, "DROP") {
		t.Errorf("the refusal should name the offending verb, got %q", bk.Log)
	}
	// Refused means REFUSED: the statement never reached the database.
	chk := app.riskCheck(admin, stg, "SELECT COUNT(*) FROM tbl_order")
	if chk.Action == "deny" {
		t.Fatalf("probe query unexpectedly denied")
	}
	eq(t, stageByType(done, "execute").Status, "skipped", "nothing after the refusal ran")
}

// TestBackupStageJudgesEveryStatementBeforeRunningAny — a config that hides the
// destructive verb behind a legitimate first statement must not get the first
// one executed before the refusal.
func TestBackupStageJudgesEveryStatementBeforeRunningAny(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	stg := app.connIDByEnv(admin, "staging")

	pid := app.createPipeline(admin, "夹带备份", "", []map[string]any{
		{"name": "备份", "type": "backup",
			"config": `{"sql":"CREATE TABLE tbl_bak_smuggle AS SELECT * FROM tbl_order; TRUNCATE TABLE tbl_order;"}`},
	})
	r := app.submitRelease(admin, map[string]any{
		"title": "夹带", "pipelineId": pid, "connectionId": stg,
		"sql": "SELECT 1;", "reason": "回归 #2",
	})
	eq(t, r.Code, 0, "submit release")
	var rel releaseView
	_ = json.Unmarshal(r.Data, &rel)

	done := app.waitRelease(admin, rel.ID, "failed", "success")
	eq(t, done.Status, "failed", "the smuggled TRUNCATE fails the whole stage")
	bk := stageByType(done, "backup")
	if !strings.Contains(bk.Log, "TRUNCATE") {
		t.Errorf("refusal should name TRUNCATE, got %q", bk.Log)
	}
	// The first (legitimate) statement must NOT have run: judge-all-then-run,
	// not run-until-refused.
	if strings.Contains(bk.Log, "已执行") || strings.Contains(bk.Log, "备份完成") {
		t.Errorf("no statement may execute when any statement is refused, log %q", bk.Log)
	}
}

func TestBackupStageAllowsCopyingSQL(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	stg := app.connIDByEnv(admin, "staging")

	pid := app.createPipeline(admin, "正经备份", "", []map[string]any{
		{"name": "备份", "type": "backup",
			"config": `{"sql":"CREATE TABLE tbl_order_bak_r2 AS SELECT * FROM tbl_order"}`},
		{"name": "执行变更", "type": "execute"},
	})
	r := app.submitRelease(admin, map[string]any{
		"title": "带真备份的发布", "pipelineId": pid, "connectionId": stg,
		"sql": "SELECT 1;", "reason": "回归 #2",
	})
	eq(t, r.Code, 0, "submit release")
	var rel releaseView
	_ = json.Unmarshal(r.Data, &rel)

	// 0024 执行闸:到点的人点击确认后才落库
	done := app.confirmExecutionAndWait(admin, rel.ID)
	eq(t, done.Status, "success", "a copying backup passes")
	eq(t, stageByType(done, "backup").Status, "success", "backup ran")
}
