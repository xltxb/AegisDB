package model

import "testing"

// 类型判定与它的 SQL 形态出自同一张表;命令单是"哪个外键都没挂"。
func TestApprovalKinds_TypeAndWhere(t *testing.T) {
	cases := []struct {
		a    Approval
		want string
	}{
		{Approval{}, ApTypeCommand},
		{Approval{ReleaseID: 1}, ApTypeRelease},
		{Approval{WindowID: 1}, ApTypeWindow},
		{Approval{ExportJobID: 1}, ApTypeExport},
		{Approval{OSCJobID: 1}, ApTypeOSC},
		{Approval{QueryGrantReqID: 1}, ApTypeGrant},
	}
	for _, c := range cases {
		if got := ApprovalType(&c.a); got != c.want {
			t.Errorf("%+v 判成了 %q,应为 %q", c.a, got, c.want)
		}
	}
	if w, _ := ApprovalTypeWhere(ApTypeCommand); w != "release_id = 0 AND window_id = 0 AND export_job_id = 0 AND osc_job_id = 0 AND query_grant_req_id = 0" {
		t.Errorf("命令单的条件不对: %s", w)
	}
	if w, _ := ApprovalTypeWhere(ApTypeGrant); w != "query_grant_req_id > 0" {
		t.Errorf("权限申请单的条件不对: %s", w)
	}
	if _, ok := ApprovalTypeWhere("bogus"); ok || IsApprovalType("bogus") {
		t.Error("认不出的类型不能给条件")
	}
	// 待执行只留命令单与在线变更单
	if w := ManualExecWhere(); w != "release_id = 0 AND window_id = 0 AND export_job_id = 0 AND query_grant_req_id = 0" {
		t.Errorf("待执行的条件不对: %s", w)
	}
}
