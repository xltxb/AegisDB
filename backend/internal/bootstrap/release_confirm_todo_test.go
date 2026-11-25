package bootstrap

// 待办的第三类:发布单停在人工闸上等人点「继续」。
//
// 前两类都挂在审批工单上。这一类不是工单 —— 一条流水线跑到人工确认点就停住了,等一个
// 人来放行。它原先没有任何地方提示过:发布单列表要自己翻,而一条停在凌晨的流水线没人翻。
//
// 这里钉四条:
//   1. 闸停住时,**被路由到的那个角色的人**待办里出现一条 confirm,指向那张发布单,
//      并说清停在哪一道闸上;
//   2. 发起人自己不出现 —— 人工确认是两人控制,他点不动,亮给他看只会让他去找一个
//      灰按钮;
//   3. 点完就消失;
//   4. 被点名的那个人**打得开那张发布单** —— 而且点完之后照样打得开。待办把他叫来,
//      详情却回一句"无权查看",是这条链路上最说不通的一种失败。

import (
	"encoding/json"
	"net/http"
	"testing"
)

func (v todoView) confirmOn(relNo string) (stage string, found bool) {
	for _, it := range v.Items {
		if it.ApNo == relNo && it.Kind == "confirm" {
			return it.Stage, true
		}
	}
	return "", false
}

// 人工确认闸:路由到 l2,那么 l2 的人待办里有,发起人(admin)没有。
func TestConfirmTodo_ShowsTheGateToWhoeverMayPassIt(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123") // 发起人
	l2 := app.login("chenhao@aegisdb.io", "aegisdb123")   // 本流程指定的确认角色,既非发起人也非监管角色
	stg := app.connIDByEnv(admin, "staging")

	baseAdmin, baseL2 := app.todoOf(admin), app.todoOf(l2)

	pid := app.createPipeline(admin, "待办·人工闸", "", []map[string]any{
		{"name": "人工确认", "type": "manual", "config": `{"confirmRole":"l2"}`},
		{"name": "执行变更", "type": "execute"},
	})
	r := app.submitRelease(admin, map[string]any{
		"title": "待办第三类", "pipelineId": pid, "connectionId": stg,
		"sql": "SELECT 1;", "reason": "人工闸待办回归",
	})
	eq(t, r.Code, 0, "submit")
	var rel releaseView
	_ = json.Unmarshal(r.Data, &rel)
	waiting := app.waitRelease(admin, rel.ID, "waiting", "failed", "success")
	gate := stageByType(waiting, "manual")
	if gate == nil || gate.Status != "waiting" {
		t.Fatalf("前置:人工闸应停在 waiting,实际 %+v", gate)
	}

	// 1. 角色里的人看得到,而且说得出停在哪一道闸上。
	got := app.todoOf(l2)
	if got.Confirm != baseL2.Confirm+1 {
		t.Errorf("确认角色的待办应多一条,基线 %d 实际 %d", baseL2.Confirm, got.Confirm)
	}
	stage, found := got.confirmOn(waiting.RelNo)
	if !found {
		t.Fatalf("待办里应有这张发布单 %s,实际 %+v", waiting.RelNo, got.Items)
	}
	// 送的是阶段**类型**,不是那份派生出来的规范中文 —— 界面按读者的语言渲染它。
	if stage != "manual" {
		t.Errorf("待办要说清停在哪一道闸上(manual),实际 %q", stage)
	}

	// 2. 发起人不出现:人工确认是两人控制,他点不动。
	mine := app.todoOf(admin)
	if _, yes := mine.confirmOn(waiting.RelNo); yes {
		t.Error("人工确认是两人控制,发起人点不动 —— 不该出现在他的待办里")
	}
	if mine.Confirm != baseAdmin.Confirm {
		t.Errorf("发起人的待确认数不该变,基线 %d 实际 %d", baseAdmin.Confirm, mine.Confirm)
	}

	// 3. 点完就消失。
	eq(t, app.confirmStage(l2, rel.ID, gate.ID).Code, 0, "确认角色放行")
	after := app.todoOf(l2)
	if _, yes := after.confirmOn(waiting.RelNo); yes {
		t.Errorf("放行之后这条待办应消失,实际 %+v", after.Items)
	}
}

// 被待办叫来的人,必须打得开那张发布单 —— 点之前点之后都要打得开。
func TestConfirmTodo_TheStaffedConfirmerCanOpenTheRelease(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	// 确认人得是一个本来看不到别人发布单的角色,这条例外才测得到:l2 自 2026-10-30 起
	// 能看全部发布单(canSeeAllReleases),于是换成研发只读,并给它发布菜单。
	app.enableMenuForCode("ro", "pipeline")
	ro := app.login("zhaolei@aegisdb.io", "aegisdb123") // 非发起人、看不到全部发布单
	stg := app.connIDByEnv(admin, "staging")

	pid := app.createPipeline(admin, "待办·可见性", "", []map[string]any{
		{"name": "人工确认", "type": "manual", "config": `{"confirmRole":"ro"}`},
		{"name": "执行变更", "type": "execute"},
	})
	r := app.submitRelease(admin, map[string]any{
		"title": "确认人看得见", "pipelineId": pid, "connectionId": stg,
		"sql": "SELECT 1;", "reason": "可见性回归",
	})
	eq(t, r.Code, 0, "submit")
	var rel releaseView
	_ = json.Unmarshal(r.Data, &rel)
	waiting := app.waitRelease(admin, rel.ID, "waiting", "failed", "success")
	gate := stageByType(waiting, "manual")
	if gate == nil {
		t.Fatal("前置:应有人工闸")
	}

	if got := app.do(http.MethodGet, "/api/v1/releases/"+itoa(rel.ID), ro, nil); got.Code != 0 {
		t.Fatalf("待办把他叫来了,详情却回 %q —— 被点名确认的人必须看得见那张单", got.Msg)
	}
	eq(t, app.confirmStage(ro, rel.ID, gate.ID).Code, 0, "放行")
	if got := app.do(http.MethodGet, "/api/v1/releases/"+itoa(rel.ID), ro, nil); got.Code != 0 {
		t.Errorf("点完「继续」就失去可见性,刷新回来是 %q —— 他刚被要求对这张单负责", got.Msg)
	}

	// 但这不是一把万能钥匙:另一张发布单把闸路由给了 owner,zhaolei(ro)不在那道闸上,
	// 照样看不见 —— 放宽的判据是"这道闸交给了你",不是"你有发布菜单"。
	otherPid := app.createPipeline(admin, "待办·别人的闸", "", []map[string]any{
		{"name": "人工确认", "type": "manual", "config": `{"confirmRole":"owner"}`},
		{"name": "执行变更", "type": "execute"}, // 流程必须恰好一个执行阶段(工单 02 第 12 条)
	})
	o := app.submitRelease(admin, map[string]any{
		"title": "不该被看到", "pipelineId": otherPid, "connectionId": stg,
		"sql": "SELECT 1;", "reason": "可见性边界",
	})
	eq(t, o.Code, 0, "submit other")
	var other releaseView
	_ = json.Unmarshal(o.Data, &other)
	app.waitRelease(admin, other.ID, "waiting", "failed", "success")
	if got := app.do(http.MethodGet, "/api/v1/releases/"+itoa(other.ID), ro, nil); got.Code == 0 {
		t.Error("闸路由给了 owner,ro 不该因为这次放宽而看到这张发布单")
	}
}

// 执行确认闸默认允许发起人 —— 这一路的待办要出现在**发起人**那里。
// 两道闸的规则不同,而待办读的是同一处判断,所以两路都要钉。
func TestConfirmTodo_ExecuteGateWaitsOnTheCreatorByDefault(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	stg := app.connIDByEnv(admin, "staging")

	pid := app.createPipeline(admin, "待办·执行闸", "", []map[string]any{
		{"name": "执行变更", "type": "execute"},
	})
	r := app.submitRelease(admin, map[string]any{
		"title": "执行闸待办", "pipelineId": pid, "connectionId": stg,
		"sql": "SELECT 1;", "reason": "执行闸待办回归",
	})
	eq(t, r.Code, 0, "submit")
	var rel releaseView
	_ = json.Unmarshal(r.Data, &rel)
	waiting := app.waitRelease(admin, rel.ID, "waiting", "failed", "success")
	ex := stageByType(waiting, "execute")
	if ex == nil || ex.Status != "waiting" {
		t.Fatalf("前置:执行闸应停在 waiting,实际 %+v", ex)
	}

	got := app.todoOf(admin)
	stage, found := got.confirmOn(waiting.RelNo)
	if !found {
		t.Fatalf("执行确认默认归发起人,应出现在他的待办里,实际 %+v", got.Items)
	}
	if stage != "execute" {
		t.Errorf("待办应指出停在执行闸(execute)上,实际 %q", stage)
	}
}

// 待办不谎报:够不到发布菜单的人,一条「等你确认」都不该拿到。
//
// 放行端点挂在 menu("pipeline") 上 —— 审计角色够不到它,点下去只会拿到「无菜单权限」。
// 这里同时记录了一个**更深的配置问题**:把闸路由到一个没有发布菜单的角色,那条流水线会
// 永远停在那儿而没有人被通知。待办做到不谎报为止,真正的修法属于 validateConfirmRole。
func TestConfirmTodo_SaysNothingToSomeoneWhoCouldNotClickItAnyway(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	auditor := app.login("hujun@aegisdb.io", "aegisdb123") // audit:有人在这个角色里,但没有发布菜单
	stg := app.connIDByEnv(admin, "staging")

	pid := app.createPipeline(admin, "待办·路由给审计", "", []map[string]any{
		{"name": "人工确认", "type": "manual", "config": `{"confirmRole":"audit"}`},
		{"name": "执行变更", "type": "execute"}, // 流程必须恰好一个执行阶段(工单 02 第 12 条)
	})
	r := app.submitRelease(admin, map[string]any{
		"title": "点不动的闸", "pipelineId": pid, "connectionId": stg,
		"sql": "SELECT 1;", "reason": "菜单闸回归",
	})
	eq(t, r.Code, 0, "submit")
	var rel releaseView
	_ = json.Unmarshal(r.Data, &rel)
	waiting := app.waitRelease(admin, rel.ID, "waiting", "failed", "success")

	got := app.todoOf(auditor)
	if _, yes := got.confirmOn(waiting.RelNo); yes {
		t.Error("他点下去只会拿到「无菜单权限」—— 待办里列一条动不了的,人会学着无视整枚徽章")
	}
	if got.Confirm != 0 {
		t.Errorf("待确认应为 0,实际 %d", got.Confirm)
	}
}
