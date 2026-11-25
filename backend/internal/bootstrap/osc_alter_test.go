package bootstrap

// 任意 ALTER 走在线变更的终端入口(.scratch/osc-product 工单 08),走真实接口、对着真 MySQL。

import (
	"net/http"
	"strings"
	"testing"

	"velagateway/internal/model"
)

func TestOSCAlter_ModifyColumnRunsAsOnlineChange(t *testing.T) {
	app := newTestApp(t)
	admin, prod, schema := app.oscTermSetup()
	out := app.termExec(admin, prod, "ALTER TABLE big MODIFY b VARCHAR(40) NULL, ADD UNIQUE KEY uk_a (a)")
	if !out.Intercepted || out.OSC == nil {
		t.Fatalf("改列定义 + 加唯一键应生成在线变更工单: %+v", out)
	}
	ap := app.approvalByNoFull(admin, out.ApprovalNo)
	approver := app.login("zhangwei@vela.io", "vela123")
	eq(t, app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/approve", approver, nil).Code, 0, "审批通过")
	eq(t, app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/execute", admin, nil).Code, 0, "执行")
	done := app.waitApprovalExec(ap.ApNo)
	if done.ExecStatus != model.ExecStatusSuccess {
		t.Fatalf("应执行成功: %+v", done)
	}
	raw := oscRaw(t, schema)
	var typ string
	if err := raw.QueryRow("SELECT COLUMN_TYPE FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'big' AND COLUMN_NAME = 'b'").Scan(&typ); err != nil {
		t.Fatal(err)
	}
	if typ != "varchar(40)" || !oscHasIndex(t, raw, "big", "uk_a") {
		t.Errorf("列类型应已变为 varchar(40) 且有 uk_a,实际 %s", typ)
	}
}

// 生成工单之前就在空的探测表上试跑:删了主键又没有替代的唯一键,无从定位行 —— 当场拒绝,不出工单。
func TestOSCAlter_ProbeRefusesBeforeTheTicket(t *testing.T) {
	app := newTestApp(t)
	admin, prod, _ := app.oscTermSetup()
	out := app.termExec(admin, prod, "ALTER TABLE big DROP PRIMARY KEY")
	if out.ApprovalNo != "" || len(out.OSCBlockers) == 0 || out.OSCBlockers[0].Code != "no_shared_key" {
		t.Fatalf("应被前置检查拦下(no_shared_key)且不生成工单: %+v", out)
	}
	if !strings.Contains(out.OSCBlockers[0].Reason, "唯一键") {
		t.Errorf("要说清为什么: %+v", out.OSCBlockers[0])
	}
}
