package bootstrap

// 改角色本身也要进审计链。
//
// 同一个角色页上的三个动作:改能力矩阵、改菜单、改角色本身。前两个早就写审计了,第三个
// 没有 —— 而它能改的是 canApprove:**这个角色能不能审批高危命令**。
//
// 换句话说,"谁有权放行 PROD 上的 DROP"这件事可以被改掉,而链上没有任何一行提到它。
// 审计链是这个产品本身(ADR 0006 的哈希链存在的全部理由就是事后能查),而这一格是空的。
//
// 缺口的形状很典型:审计是在**调用点**手写的一行,handler 直接调仓储、改完自己记得补
// 一句。旁边两个 handler 记得了,这一个没有。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestAdmin_RoleEditIsAudited(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	rid := itoa(app.roleIDByCode(admin, "ro"))

	no := false
	yes := true
	// 先关掉再打开:两次都要留痕 —— 把审批权**收回**同样是要查的事。
	eq(t, app.do(http.MethodPatch, "/api/v1/roles/"+rid, admin, map[string]any{
		"canApprove": &no}).Code, 0, "revoke approval right")
	eq(t, app.do(http.MethodPatch, "/api/v1/roles/"+rid, admin, map[string]any{
		"canApprove": &yes, "description": "临时提权"}).Code, 0, "grant approval right")

	var rows []struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(app.auditItemsRaw(admin, ""), &rows); err != nil {
		t.Fatalf("audit decode: %v", err)
	}
	n := 0
	for _, r := range rows {
		if strings.Contains(r.Command, "admin.role.") && strings.Contains(r.Command, "ro") {
			n++
		}
	}
	if n < 2 {
		t.Errorf("改了两次角色,审计链上只找到 %d 行 —— canApprove 被动过而链上没说", n)
	}
}
