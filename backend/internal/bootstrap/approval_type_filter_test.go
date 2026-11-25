package bootstrap

// 审批列表按**工单类型**筛选。
//
// 四类工单混在一张列表里,而它们要人做的事完全不同:命令单批完要发起人自己去执行;
// 升级单的执行归流水线;窗口单批准即生效;导出单批完由后台任务跑。更麻烦的是后三类的
// "命令"其实是一句描述("开启执行窗口「…」"、"EXPORT [含敏感字段(原值)] …"),表名
// 都解析不出来 —— 不说类型,那一行看起来只是一张内容古怪的命令单。
//
// 类型不是表上的一列,是由 release_id / window_id / export_job_id 三个外键推出来的。
// 所以这里钉的是两件事:
//
//   1. 筛选**在服务端**做,并且筛出来的每一行都真的是那一类(带着 type 字段回来,
//      界面照它画标签 —— 两边各推一遍,迟早会出现"筛出来的行挂着别的标签");
//   2. 拼错的类型**报错**,不是静静返回空列表 —— 空列表和"确实没有"长得一模一样,
//      调用方看不出自己写错了。这与 status 的处理是同一条原则。

import (
	"encoding/json"
	"net/http"
	"testing"
)

type apTypedPage struct {
	Items []struct {
		ApNo string `json:"apNo"`
		Type string `json:"type"`
	} `json:"items"`
	Total int64 `json:"total"`
}

func (a *testApp) approvalsByType(token, typ string) apTypedPage {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/approvals?scope=all&page=1&pageSize=100&type="+typ, token, nil)
	if r.Code != 0 {
		a.t.Fatalf("按类型查 %q: code=%d msg=%s", typ, r.Code, r.Msg)
	}
	var p apTypedPage
	if err := json.Unmarshal(r.Data, &p); err != nil {
		a.t.Fatalf("解码: %v", err)
	}
	return p
}

// 每一行都带类型,而按类型筛出来的每一行都只属于那一类。
func TestApprovalTypeFilter_SeparatesCommandTicketsFromReleaseTickets(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")

	// 一张普通命令单(终端里被拦下来的那种)。
	cmd := app.submitProdHighRisk(admin)

	// 一张升级单的审批工单:带审批阶段的流水线会替发布单开一张。
	stg := app.connIDByEnv(admin, "staging")
	pid := app.createPipeline(admin, "类型筛选·带审批", "", []map[string]any{
		{"name": "人工审批", "type": "approve"},
		{"name": "执行变更", "type": "execute"},
	})
	r := app.submitRelease(admin, map[string]any{
		"title": "类型筛选", "pipelineId": pid, "connectionId": stg,
		"sql": "SELECT 1;", "reason": "类型筛选回归",
	})
	eq(t, r.Code, 0, "submit release")
	var rel releaseView
	_ = json.Unmarshal(r.Data, &rel)
	waiting := app.waitRelease(admin, rel.ID, "waiting", "failed", "success")
	gate := stageByType(waiting, "approve")
	if gate == nil || gate.ApprovalNo == "" {
		t.Fatalf("前置:审批阶段应开出一张工单,实际 %+v", gate)
	}

	// 命令单那一类:含刚才那张,不含升级单开的那张,且每一行的 type 都对。
	cmds := app.approvalsByType(admin, "cmd")
	sawCmd, sawRelInCmd := false, false
	for _, it := range cmds.Items {
		if it.Type != "cmd" {
			t.Errorf("按 cmd 筛,却筛出一行 type=%q(%s)", it.Type, it.ApNo)
		}
		if it.ApNo == cmd.ApNo {
			sawCmd = true
		}
		if it.ApNo == gate.ApprovalNo {
			sawRelInCmd = true
		}
	}
	if !sawCmd {
		t.Errorf("命令单 %s 应出现在 cmd 这一类里", cmd.ApNo)
	}
	if sawRelInCmd {
		t.Errorf("升级单开的工单 %s 不该被算成命令单", gate.ApprovalNo)
	}

	// 升级单那一类:反过来。
	rels := app.approvalsByType(admin, "release")
	sawRel := false
	for _, it := range rels.Items {
		if it.Type != "release" {
			t.Errorf("按 release 筛,却筛出一行 type=%q(%s)", it.Type, it.ApNo)
		}
		if it.ApNo == gate.ApprovalNo {
			sawRel = true
		}
		if it.ApNo == cmd.ApNo {
			t.Errorf("命令单 %s 不该被算成升级单", cmd.ApNo)
		}
	}
	if !sawRel {
		t.Errorf("升级单开的工单 %s 应出现在 release 这一类里", gate.ApprovalNo)
	}

	// 不筛的时候两张都在 —— 筛选不该把某一类从"全部"里弄丢。
	all := app.approvalsByType(admin, "")
	haveCmd, haveRel := false, false
	for _, it := range all.Items {
		haveCmd = haveCmd || it.ApNo == cmd.ApNo
		haveRel = haveRel || it.ApNo == gate.ApprovalNo
	}
	if !haveCmd || !haveRel {
		t.Errorf("不筛类型时两张单都该在:cmd=%v release=%v", haveCmd, haveRel)
	}
	if all.Total < cmds.Total+rels.Total {
		t.Errorf("全部(%d)不该少于分类之和(cmd %d + release %d)", all.Total, cmds.Total, rels.Total)
	}
}

// 拼错的类型要报错,不能静静返回空列表 —— 空列表和"确实没有"长得一样。
func TestApprovalTypeFilter_RejectsAnUnknownType(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")

	r := app.do(http.MethodGet, "/api/v1/approvals?scope=all&type=releases", admin, nil)
	if r.Code == 0 {
		t.Error("拼错的类型应被拒绝,而不是回一份空列表让人以为真的没有")
	}

	// 四个已知类型都要被接受(哪怕当下一条都没有)。
	for _, ok := range []string{"cmd", "release", "window", "export"} {
		if got := app.do(http.MethodGet, "/api/v1/approvals?scope=all&type="+ok, admin, nil); got.Code != 0 {
			t.Errorf("类型 %q 应被接受,实际 code=%d msg=%s", ok, got.Code, got.Msg)
		}
	}
}
