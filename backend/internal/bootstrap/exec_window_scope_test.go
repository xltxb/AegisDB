package bootstrap

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"velagateway/internal/model"
)

// 管理后台拆分(工单 07):用户后台的执行窗口列表只列自己申请的;所有人的窗口在管理后台
// (scope=all,只给平台管理员,带申请人姓名);停用是收紧,不走审批,申请人或管理员可做。

func (a *testApp) windowList(token, query string) (int, []model.ExecWindow) {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/exec-windows"+query, token, nil)
	var ws []model.ExecWindow
	_ = json.Unmarshal(r.Data, &ws)
	return r.Code, ws
}

func hasWindow(ws []model.ExecWindow, id int64) *model.ExecWindow {
	for i := range ws {
		if ws[i].ID == id {
			return &ws[i]
		}
	}
	return nil
}

func TestExecWindow_ListIsOwnUnlessAdminAsksForAll(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	l2 := app.login("chenhao@vela.io", "vela123")
	prod := app.connIDByEnv(admin, "prod")
	mine := app.openWindow(l2, prod, "orders_db", time.Now().Add(-time.Minute), time.Now().Add(time.Hour))
	theirs := app.openWindow(admin, prod, "orders_db", time.Now().Add(-time.Minute), time.Now().Add(time.Hour))

	_, ws := app.windowList(l2, "")
	if hasWindow(ws, mine) == nil || hasWindow(ws, theirs) != nil {
		t.Fatalf("L2 只该看到自己申请的窗口,实际 %+v", ws)
	}
	if code, _ := app.windowList(l2, "?scope=all"); code != 40300 {
		t.Errorf("非管理员要全部窗口应 403,实际 %d", code)
	}
	code, all := app.windowList(admin, "?scope=all")
	eq(t, code, 0, "管理员 scope=all")
	w := hasWindow(all, mine)
	if w == nil || hasWindow(all, theirs) == nil {
		t.Fatalf("管理员 scope=all 应看到所有人的窗口,实际 %+v", all)
	}
	eq(t, w.CreatedByName, "Chen Hao", "全局列表带申请人姓名")
	_, adminOwn := app.windowList(admin, "")
	if hasWindow(adminOwn, mine) != nil {
		t.Error("不带 scope 时管理员也只看到自己的")
	}
}

func TestExecWindow_DisableStopsRelaxingWithoutApproval(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	l2 := app.login("chenhao@vela.io", "vela123")
	owner := app.login("zhangwei@vela.io", "vela123")
	prod := app.connIDByEnv(admin, "prod")
	const sql, db = "DROP TABLE orders_2024_q3", "orders_db"
	id := app.openWindow(l2, prod, db, time.Now().Add(-time.Minute), time.Now().Add(time.Hour))
	if app.riskCheckIn(l2, prod, sql, db).RequiresApproval {
		t.Fatal("前提:窗口内应放行")
	}

	if r := app.do(http.MethodPost, "/api/v1/exec-windows/"+itoa(id)+"/disable", owner, nil); r.Code != 40300 {
		t.Fatalf("别人(非申请人、非管理员)不能停用,实际 code=%d", r.Code)
	}
	eq(t, app.do(http.MethodPost, "/api/v1/exec-windows/"+itoa(id)+"/disable", admin, nil).Code, 0, "管理员停用别人的窗口")
	if !app.riskCheckIn(l2, prod, sql, db).RequiresApproval {
		t.Error("停用后应立即恢复审批")
	}
	_, ws := app.windowList(l2, "")
	if w := hasWindow(ws, id); w == nil || w.Enabled {
		t.Errorf("申请人的列表里应显示已停用: %+v", w)
	}
}

// 总览的「执行窗口生效中」面板列所有人此刻开着的窗口(2026-10-14 用户确认):门开在哪个库上,
// 在那台库上干活的人都该知道。只列开着的 —— 还没到时间的、别人的待审批申请,照旧各看各的。
func TestExecWindow_ActiveScopeShowsEveryonesOpenWindows(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	l2 := app.login("chenhao@vela.io", "vela123")
	ro := app.login("zhaolei@vela.io", "vela123")
	prod := app.connIDByEnv(admin, "prod")
	open := app.openWindow(admin, prod, "orders_db", time.Now().Add(-time.Minute), time.Now().Add(time.Hour))
	later := app.openWindow(admin, prod, "orders_db", time.Now().Add(time.Hour), time.Now().Add(2*time.Hour))

	for name, tok := range map[string]string{"DBA L2": l2, "研发只读": ro} {
		code, ws := app.windowList(tok, "?scope=active")
		eq(t, code, 0, name+" scope=active")
		w := hasWindow(ws, open)
		if w == nil {
			t.Fatalf("%s应看到别人此刻开着的窗口,实际 %+v", name, ws)
		}
		eq(t, w.CreatedByName, "Lin Wei", "生效面板带申请人姓名")
		if hasWindow(ws, later) != nil {
			t.Errorf("%s:还没到时间的窗口不该出现在生效列表里", name)
		}
	}
	if _, own := app.windowList(l2, ""); hasWindow(own, open) != nil {
		t.Error("不带 scope 的列表仍只列自己申请的")
	}
}

// 总览「执行窗口排期」里已批准、还没到点的班车也是所有人的(2026-10-19 用户确认,与生效中同口径):
// 今晚 / 这周哪个库会有一段免审批时间,在那台库上干活的人该提前知道。待审批的申请、停用的、
// 已经开完的、此刻正开着的(那归 scope=active)都不在这里。
func TestExecWindow_ScheduledScopeShowsEveryonesApprovedUpcoming(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	l2 := app.login("chenhao@vela.io", "vela123")
	ro := app.login("zhaolei@vela.io", "vela123")
	prod := app.connIDByEnv(admin, "prod")
	now := time.Now()
	later := app.openWindow(admin, prod, "orders_db", now.Add(time.Hour), now.Add(2*time.Hour))
	open := app.openWindow(admin, prod, "orders_db", now.Add(-time.Minute), now.Add(time.Hour))
	disabled := app.openWindow(admin, prod, "orders_db", now.Add(3*time.Hour), now.Add(4*time.Hour))
	eq(t, app.do(http.MethodPost, "/api/v1/exec-windows/"+itoa(disabled)+"/disable", admin, nil).Code, 0, "停用")
	done := app.openWindow(admin, prod, "orders_db", now.Add(time.Hour), now.Add(2*time.Hour))
	app.backdateWindow(done, now.Add(-3*time.Hour), now.Add(-2*time.Hour))
	// 还在等审批的申请:建了不批
	r := app.do(http.MethodPost, "/api/v1/exec-windows", admin, map[string]any{
		"name": "待批", "enabled": true, "connectionId": prod, "database": "orders_db",
		"kind": "once", "startsAt": now.Add(time.Hour).Format(time.RFC3339), "endsAt": now.Add(2 * time.Hour).Format(time.RFC3339),
		"reason": "待批",
	})
	eq(t, r.Code, 0, "建待审批窗口")
	var pending model.ExecWindow
	_ = json.Unmarshal(r.Data, &pending)

	for name, tok := range map[string]string{"DBA L2": l2, "研发只读": ro} {
		code, ws := app.windowList(tok, "?scope=scheduled")
		eq(t, code, 0, name+" scope=scheduled")
		w := hasWindow(ws, later)
		if w == nil {
			t.Fatalf("%s应看到别人已批准、还没到点的窗口,实际 %+v", name, ws)
		}
		eq(t, w.CreatedByName, "Lin Wei", "排期带申请人姓名")
		for what, id := range map[string]int64{"此刻开着的": open, "已停用的": disabled, "已经开完的": done, "待审批的": pending.ID} {
			if hasWindow(ws, id) != nil {
				t.Errorf("%s:%s窗口不该出现在排期里", name, what)
			}
		}
	}
}

// 停用只改 enabled 一列:申请人同时改了窗口(名称、时间),停用不能把它改回旧值(2026-10 审查)。
func TestExecWindow_DisableTouchesOnlyEnabled(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	l2 := app.login("chenhao@vela.io", "vela123")
	prod := app.connIDByEnv(admin, "prod")
	id := app.openWindow(l2, prod, "orders_db", time.Now().Add(-time.Minute), time.Now().Add(time.Hour))

	stale, err := app.repo.GetExecWindow(id) // 停用的人读到的旧值
	if err != nil {
		t.Fatal(err)
	}
	edited := *stale
	edited.Name = "申请人刚改的名字"
	if err := app.repo.UpdateExecWindow(&edited); err != nil {
		t.Fatal(err)
	}
	changed, err := app.repo.DisableExecWindow(id)
	if err != nil || !changed {
		t.Fatalf("应从启用变成停用: changed=%v err=%v", changed, err)
	}
	got, _ := app.repo.GetExecWindow(id)
	if got.Enabled || got.Name != "申请人刚改的名字" {
		t.Fatalf("停用后应只变 enabled,名称保持申请人改的值: enabled=%v name=%q", got.Enabled, got.Name)
	}
	if again, _ := app.repo.DisableExecWindow(id); again {
		t.Error("已经停用的再停一次不算变化(不重复记审计)")
	}
	eq(t, app.do(http.MethodPost, "/api/v1/exec-windows/"+itoa(id)+"/disable", admin, nil).Code, 0, "对已停用的窗口再按停用:照常成功")
}
