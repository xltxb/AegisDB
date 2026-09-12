package bootstrap

// 校验阶段的只读闸,和备份阶段一样必须**逐条**判。
//
// 校验语句写在流程模板里,和发布内容不同 —— 它不过判定引擎,只有 IsRead 这一道闸。
// 而 IsRead 只看首动词:`SELECT 1; DROP TABLE t` 判成只读,整串原样下发。
// PostgreSQL/DWS/GaussDB 的 simple query 协议一次报文把整串跑完,于是第二条真的
// 执行了 —— 一次没有人审过的变更,挂在"校验"这个名字底下。
//
// 同一个文件里的备份阶段早就是"先逐条判完再跑"(审查修复 #2),两处口径必须一致。

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestVerifyStageJudgesEveryStatement(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	stg := app.connIDByEnv(admin, "staging")

	pid := app.createPipeline(admin, "夹带校验", "", []map[string]any{
		{"name": "校验", "type": "verify",
			"config": `{"sql":"SELECT 1; DROP TABLE tbl_order"}`},
	})
	r := app.submitRelease(admin, map[string]any{
		"title": "夹带校验", "pipelineId": pid, "connectionId": stg,
		"sql": "SELECT 1;", "reason": "回归 #3",
	})
	eq(t, r.Code, 0, "submit release")
	var rel releaseView
	_ = json.Unmarshal(r.Data, &rel)

	done := app.waitRelease(admin, rel.ID, "failed", "success")
	eq(t, done.Status, "failed", "藏在只读首句后面的 DROP 必须让这一阶段失败")
	vf := stageByType(done, "verify")
	eq(t, vf.Status, "failed", "校验阶段应当拒绝")
	if !strings.Contains(vf.Log, "只读") {
		t.Errorf("拒绝的理由要说清是「只读」那一条,实际 %q", vf.Log)
	}
}

// 正经的只读校验不受影响 —— 包括写成多条的那种。
func TestVerifyStageAllowsReadOnlyStatements(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	stg := app.connIDByEnv(admin, "staging")

	pid := app.createPipeline(admin, "正经校验", "", []map[string]any{
		{"name": "校验", "type": "verify",
			"config": `{"sql":"SELECT COUNT(*) FROM tbl_order; SELECT 1"}`},
	})
	r := app.submitRelease(admin, map[string]any{
		"title": "正经校验", "pipelineId": pid, "connectionId": stg,
		"sql": "SELECT 1;", "reason": "回归 #3",
	})
	eq(t, r.Code, 0, "submit release")
	var rel releaseView
	_ = json.Unmarshal(r.Data, &rel)

	done := app.waitRelease(admin, rel.ID, "failed", "success")
	eq(t, stageByType(done, "verify").Status, "success", "两条都是只读查询,该跑完")
}
