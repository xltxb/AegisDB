package bootstrap

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"velagateway/internal/model"
)

// 2026-10-26 回归排查 08:拆分后用户后台的窗口列表只剩自己的,回看按钮挂在列表行上,DBA 负责人、L2
// 点不到别人班车的回看(后端 canReviewWindow 一直开着)。scope=reviewable 给有回看权限的人列
// 点得开回看的窗口(含已过期,带申请人姓名);没有回看权限的 40300。原有的默认 / all / active /
// scheduled 口径不变。
func TestExecWindow_ReviewableScope(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	owner := app.login("zhangwei@vela.io", "vela123")
	l2 := app.login("chenhao@vela.io", "vela123")
	ro := app.login("zhaolei@vela.io", "vela123")
	prod := app.connIDByEnv(admin, "prod")
	now := time.Now()

	// 管理员的一扇已经开完的窗口(已批准、已过期)
	expired := app.openWindow(admin, prod, "orders_db", now.Add(time.Hour), now.Add(2*time.Hour))
	app.backdateWindow(expired, now.Add(-3*time.Hour), now.Add(-2*time.Hour))
	// L2 自己的窗口
	l2own := app.openWindow(l2, prod, "orders_db", now.Add(-time.Minute), now.Add(time.Hour))
	// 管理员一张还在等审批的申请
	r := app.do(http.MethodPost, "/api/v1/exec-windows", admin, map[string]any{
		"name": "待批", "enabled": true, "connectionId": prod, "database": "orders_db",
		"kind": "once", "startsAt": now.Add(time.Hour).Format(time.RFC3339), "endsAt": now.Add(2 * time.Hour).Format(time.RFC3339),
		"reason": "待批",
	})
	eq(t, r.Code, 0, "建待审批窗口")
	var pending model.ExecWindow
	_ = json.Unmarshal(r.Data, &pending)

	// DBA 负责人(监督角色):全部,含已过期与待审批,带申请人姓名
	code, ws := app.windowList(owner, "?scope=reviewable")
	eq(t, code, 0, "owner scope=reviewable")
	w := hasWindow(ws, expired)
	if w == nil || hasWindow(ws, l2own) == nil || hasWindow(ws, pending.ID) == nil {
		t.Fatalf("DBA 负责人应看到所有人的窗口(含已过期、待审批),实际 %+v", ws)
	}
	eq(t, w.CreatedByName, "Lin Wei", "带申请人姓名")
	if !w.Expired {
		t.Error("已开完的窗口应标 expired")
	}
	// 回看真的点得开(列出来的就是回看那边认的)
	eq(t, app.do(http.MethodGet, "/api/v1/exec-windows/"+itoa(expired)+"/statements", owner, nil).Code, 0, "owner 回看别人过期的窗口")

	// L2(有班车菜单,非监督):自己的 + 够得到的实例上已批准的别人的;别人待审批的不列(回看不了)
	code, ws = app.windowList(l2, "?scope=reviewable")
	eq(t, code, 0, "l2 scope=reviewable")
	if hasWindow(ws, expired) == nil || hasWindow(ws, l2own) == nil {
		t.Fatalf("L2 应看到自己的与别人已批准的窗口,实际 %+v", ws)
	}
	if hasWindow(ws, pending.ID) != nil {
		t.Error("L2 不该看到别人待审批的窗口(回看不了)")
	}
	eq(t, app.do(http.MethodGet, "/api/v1/exec-windows/"+itoa(expired)+"/statements", l2, nil).Code, 0, "L2 回看别人已批准的窗口")

	// 没有回看权限(研发只读:无班车菜单、非监督):40300
	if code, _ := app.windowList(ro, "?scope=reviewable"); code != 40300 {
		t.Errorf("无回看权限应 40300,实际 %d", code)
	}

	// 回归:默认列表仍只列自己申请的
	if _, own := app.windowList(owner, ""); hasWindow(own, expired) != nil {
		t.Error("不带 scope 的列表仍只列自己申请的")
	}
	if _, own := app.windowList(l2, ""); hasWindow(own, expired) != nil || hasWindow(own, l2own) == nil {
		t.Error("L2 不带 scope 的列表仍只列自己申请的")
	}

	// L2 够不到那台实例:别人的窗口不再列出(回看那边回"不存在"),自己的照旧
	l2u := app.userByEmail(admin, "chenhao@vela.io")
	eq(t, app.setUserTags(admin, l2u.ID, []string{"analytics"}).Code, 0, "把 L2 收窄到 analytics")
	_, ws = app.windowList(l2, "?scope=reviewable")
	if hasWindow(ws, expired) != nil {
		t.Error("够不到的实例上别人的窗口不该列出")
	}
	if hasWindow(ws, l2own) == nil {
		t.Error("自己申请的窗口照旧列出")
	}
}
