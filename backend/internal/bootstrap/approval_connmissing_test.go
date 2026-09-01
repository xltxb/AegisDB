package bootstrap

import (
	"net/http"
	"strings"
	"testing"

	"velagateway/internal/model"
)

// 目标连接被删掉的工单,任何一步都不能报成"已执行" —— 什么都没跑(V2 回归)。
//
// 这个保证的落点随"通过不再代执行"搬了家:通过那一刻本来就不执行任何命令,所以
// 连接在不在已经与审批无关;真正会撞上它的是发起人后来去执行的那一下,那里必须
// 说清"库没了、没执行",而不是让人对着一条查不到结果的成功发呆。
func TestApproval_DeletedTargetConnReportedNotExecuted(t *testing.T) {
	app := newTestApp(t)
	initiator := app.login("linwei@vela.io", "vela123") // admin: not in the owner chain
	prod := app.connIDByEnv(initiator, "prod")
	ap := app.submitProdHighRisk(initiator)

	// the target connection disappears before the approver acts
	if err := app.svc.Repo.DB().Where("id = ?", prod).Delete(&model.Connection{}).Error; err != nil {
		t.Fatalf("delete connection: %v", err)
	}

	approver := app.login("zhangwei@vela.io", "vela123") // owner: chain member
	r := app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/approve", approver, nil)
	eq(t, r.Code, 0, "decision is recorded")
	// 通过这一步既不执行,也不该声称执行过。
	if strings.Contains(string(r.Data), "已执行") {
		t.Errorf("通过不执行任何命令,不该报成已执行,got: %s", r.Data)
	}

	// 发起人去执行时才撞上"库没了",这里要如实说,而不是含糊地失败。
	x := app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/execute", initiator, nil)
	if x.Code == 0 {
		t.Fatalf("目标连接已删除,执行应当失败,got: %s", x.Data)
	}
	if !strings.Contains(x.Msg, "连接") {
		t.Errorf("拒绝的理由要说清是连接没了,got: %q", x.Msg)
	}
}
