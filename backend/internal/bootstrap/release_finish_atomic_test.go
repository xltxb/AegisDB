package bootstrap

// issue #107:发布单写成终态、与把没跑的阶段标成 skipped,必须是同一个事务。
//
// 从前是先写终态、再逐条改阶段。中间读到的人看见「已失败,后面的阶段还在待执行」,而发布页看到
// 没有进行中的单就不再轮询,那一眼就成了它最后的样子。
//
// 竞态靠反复跑碰运气才撞得上;这里让「跳过」那一步失败,把"中间状态"固定下来:不许出现一张
// 已经终结、却还有待执行阶段的单。

import (
	"net/http"
	"testing"
)

// refuseUpdateTo 在存储库上挂一个触发器:把 table 的 status 改成 status 的写入一律失败。
func refuseUpdateTo(t *testing.T, app *testApp, table, status string) {
	t.Helper()
	db := app.repo.DB()
	fn := "refuse_" + table + "_" + status
	if err := db.Exec(`CREATE FUNCTION ` + fn + `() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION '` + status + ` refused'; END $$`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TRIGGER ` + fn + ` BEFORE UPDATE ON ` + table + `
		FOR EACH ROW WHEN (NEW.status = '` + status + `') EXECUTE FUNCTION ` + fn + `()`).Error; err != nil {
		t.Fatal(err)
	}
}

// breakStageSkipping 让任何把阶段改成 skipped 的写入失败。
func breakStageSkipping(t *testing.T, app *testApp) {
	t.Helper()
	refuseUpdateTo(t, app, "tbl_release_stage", "skipped")
}

func assertNotHalfFinished(t *testing.T, v releaseView) {
	t.Helper()
	for _, st := range v.Stages {
		if st.Status == "pending" && (v.Status == "failed" || v.Status == "aborted") {
			t.Fatalf("发布单已经 %s,阶段 %s 却还是 pending", v.Status, st.Type)
		}
	}
}

func TestRejectedReleaseIsNeverHalfFinished(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	approver := app.login("zhangwei@aegisdb.io", "aegisdb123")
	waiting := submitParkedRelease(t, app, admin, "驳回半途")
	ap := stageByType(waiting, "approve")
	dec := app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ApprovalID)+"/reject", approver, map[string]any{"reason": "不行"})
	eq(t, dec.Code, 0, "reject")

	breakStageSkipping(t, app)
	app.svc.ResumeReleaseApprovals()
	assertNotHalfFinished(t, app.getRelease(admin, waiting.ID))
}

func TestAbortedReleaseIsNeverHalfFinished(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	waiting := submitParkedRelease(t, app, admin, "终止半途")

	breakStageSkipping(t, app)
	r := app.do(http.MethodPost, "/api/v1/releases/"+itoa(waiting.ID)+"/abort", admin, nil)
	if r.Code == 0 {
		t.Error("没能终止,不该回成功")
	}
	got := app.getRelease(admin, waiting.ID)
	assertNotHalfFinished(t, got)
	eq(t, got.Status, "waiting", "终止没成,单子原样")
}

// issue #111:终止与作废审批单同一个事务 —— 作废不了就不算终止成,不许留下"已终止、单还待审批"。
func TestAbortNeverLeavesItsTicketPending(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	waiting := submitParkedRelease(t, app, admin, "作废半途")
	ap := stageByType(waiting, "approve")
	refuseUpdateTo(t, app, "tbl_approval", "expired")
	app.do(http.MethodPost, "/api/v1/releases/"+itoa(waiting.ID)+"/abort", admin, nil)
	row, err := app.repo.GetApproval(ap.ApprovalID)
	if err != nil {
		t.Fatal(err)
	}
	if got := app.getRelease(admin, waiting.ID); got.Status == "aborted" && row.Status == "pending" {
		t.Fatal("发布单已终止,审批单却还待审批")
	}
}
