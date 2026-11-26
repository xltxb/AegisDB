package repository

import (
	"testing"
	"time"

	"aegisdb/internal/model"
)

// #143:决定和随后的几笔写库在同一个事务里。

func newSettleFixture(t *testing.T, withWindowTable bool) (*Repo, *model.Approval, *model.ExecWindow) {
	t.Helper()
	r := newApprovalRepo(t)
	ap := &model.Approval{ApNo: "AP-W", Status: model.StatusPending, InitiatorID: 9}
	if err := r.CreateApproval(ap, []model.ApprovalStep{{StepOrder: 1, ApproverID: 11, Approver: "Zhang Wei", Status: "active"}}); err != nil {
		t.Fatal(err)
	}
	if !withWindowTable {
		return r, ap, nil
	}
	if err := r.db.AutoMigrate(&model.ExecWindow{}); err != nil {
		t.Fatal(err)
	}
	w := &model.ExecWindow{Name: "nightly", Status: model.WindowPending, ApprovalID: ap.ID}
	if err := r.db.Create(w).Error; err != nil {
		t.Fatal(err)
	}
	ap.WindowID = w.ID
	if err := r.db.Model(ap).Update("window_id", w.ID).Error; err != nil {
		t.Fatal(err)
	}
	return r, ap, w
}

func reloadWindow(t *testing.T, r *Repo, id int64) model.ExecWindow {
	t.Helper()
	var w model.ExecWindow
	if err := r.db.First(&w, id).Error; err != nil {
		t.Fatal(err)
	}
	return w
}

func TestDecideApprovalAtomic_WritesEverything(t *testing.T) {
	r, ap, w := newSettleFixture(t, true)
	now := time.Now()
	ok, err := r.DecideApprovalAtomic(ap.ID, model.StatusApproved, "lark", "", ApprovalOutcome{
		ApproverID: 11, StepStatus: model.StatusApproved, Output: "· 已批准", At: now,
		WindowID: w.ID, WindowStatus: model.WindowApproved,
	})
	if err != nil || !ok {
		t.Fatalf("应抢到: ok=%v err=%v", ok, err)
	}
	got, _ := r.GetApproval(ap.ID)
	if got.Status != model.StatusApproved || got.DecidedVia != "lark" || got.Result != "· 已批准" || got.DecidedAt == nil {
		t.Errorf("单子没有完整落库: %+v", got)
	}
	steps, _ := r.StepsOf(ap.ID)
	if steps[0].Status != model.StatusApproved || steps[0].ActedAt == nil {
		t.Errorf("审批步骤没记: %+v", steps[0])
	}
	if gw := reloadWindow(t, r, w.ID); gw.Status != model.WindowApproved || gw.DecidedAt == nil {
		t.Errorf("窗口应随单子生效: %+v", gw)
	}
}

// 后面任何一笔写失败(这里是窗口表不存在),整张单回滚:还是待审批,步骤还 active。
// 修之前:单子已是 approved、通知说窗口已生效,窗口却还停在待审批。
func TestDecideApprovalAtomic_RollsBackWhenALaterWriteFails(t *testing.T) {
	r, ap, _ := newSettleFixture(t, false)
	ok, err := r.DecideApprovalAtomic(ap.ID, model.StatusApproved, "console", "", ApprovalOutcome{
		ApproverID: 11, StepStatus: model.StatusApproved, Output: "· 已批准", At: time.Now(),
		WindowID: 42, WindowStatus: model.WindowApproved,
	})
	if err == nil || ok {
		t.Fatalf("窗口写不进去应整体失败: ok=%v err=%v", ok, err)
	}
	got, _ := r.GetApproval(ap.ID)
	if got.Status != model.StatusPending || got.DecidedVia != "" || got.Result != "" || got.DecidedAt != nil {
		t.Errorf("失败后单子应原样留在待审批: %+v", got)
	}
	steps, _ := r.StepsOf(ap.ID)
	if steps[0].Status != "active" || steps[0].ActedAt != nil {
		t.Errorf("失败后审批步骤应原样: %+v", steps[0])
	}
	// 回滚之后可以再决定一次。
	if ok, err := r.DecideApprovalAtomic(ap.ID, model.StatusRejected, "console", "", ApprovalOutcome{StepStatus: model.StatusRejected, At: time.Now()}); err != nil || !ok {
		t.Errorf("回滚后应能重试: ok=%v err=%v", ok, err)
	}
}

func TestDecideApprovalAtomic_LoserWritesNothing(t *testing.T) {
	r, ap, w := newSettleFixture(t, true)
	if ok, err := r.CancelPendingApprovalAtomic(ap.ID, ApprovalOutcome{StepStatus: model.StatusCancelled, At: time.Now()}); err != nil || !ok {
		t.Fatalf("撤回应抢到: ok=%v err=%v", ok, err)
	}
	ok, err := r.DecideApprovalAtomic(ap.ID, model.StatusApproved, "lark", "", ApprovalOutcome{
		ApproverID: 11, StepStatus: model.StatusApproved, Output: "· 已批准", At: time.Now(),
		WindowID: w.ID, WindowStatus: model.WindowApproved,
	})
	if err != nil || ok {
		t.Fatalf("已撤回的单不该再被抢到: ok=%v err=%v", ok, err)
	}
	got, _ := r.GetApproval(ap.ID)
	if got.Status != model.StatusCancelled || got.DecidedVia != "" || got.Result != "" {
		t.Errorf("抢输的一方不该写任何东西: %+v", got)
	}
	steps, _ := r.StepsOf(ap.ID)
	if steps[0].Status != model.StatusCancelled {
		t.Errorf("步骤应停在撤回: %+v", steps[0])
	}
	if gw := reloadWindow(t, r, w.ID); gw.Status != model.WindowPending {
		t.Errorf("抢输的一方不该动窗口: %+v", gw)
	}
}

// 窗口已经挂了别的单(被改过):单子照常决定,窗口不跟着这张旧单变。
func TestDecideApprovalAtomic_LeavesAWindowOwnedByAnotherTicket(t *testing.T) {
	r, ap, w := newSettleFixture(t, true)
	if err := r.db.Model(w).Update("approval_id", ap.ID+100).Error; err != nil {
		t.Fatal(err)
	}
	ok, err := r.DecideApprovalAtomic(ap.ID, model.StatusApproved, "console", "", ApprovalOutcome{
		ApproverID: 11, StepStatus: model.StatusApproved, At: time.Now(),
		WindowID: w.ID, WindowStatus: model.WindowApproved,
	})
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if gw := reloadWindow(t, r, w.ID); gw.Status != model.WindowPending {
		t.Errorf("旧单不能打开改过的窗口: %+v", gw)
	}
}

// 撤回:待执行(已批准、未执行)的单也能撤,已执行的不行;窗口记成 cancelled。
func TestCancelApprovalAtomic(t *testing.T) {
	r, ap, w := newSettleFixture(t, true)
	o := ApprovalOutcome{StepStatus: model.StatusCancelled, At: time.Now(), WindowID: w.ID, WindowStatus: model.WindowCancelled}
	if ok, err := r.CancelApprovalAtomic(ap.ID, o); err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if got, _ := r.GetApproval(ap.ID); got.Status != model.StatusCancelled || got.DecidedAt == nil {
		t.Errorf("单子应撤回: %+v", got)
	}
	if gw := reloadWindow(t, r, w.ID); gw.Status != model.WindowCancelled {
		t.Errorf("窗口应记 cancelled: %+v", gw)
	}

	ran := &model.Approval{ApNo: "AP-R", Status: model.StatusApproved}
	if err := r.CreateApproval(ran, nil); err != nil {
		t.Fatal(err)
	}
	if err := r.db.Model(ran).Update("executed_at", time.Now()).Error; err != nil {
		t.Fatal(err)
	}
	if ok, err := r.CancelApprovalAtomic(ran.ID, ApprovalOutcome{StepStatus: model.StatusCancelled, At: time.Now()}); err != nil || ok {
		t.Errorf("已执行的单不能撤回: ok=%v err=%v", ok, err)
	}
}
