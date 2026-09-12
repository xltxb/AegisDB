package bootstrap

// 改角色是一次权限变更 —— 要进审计链,而且要**当场生效**。
//
// 多角色那一支直接 `return s.Repo.SetUserRoles(...)` 就走了,跳过了后面的两件事:
//
//   · **不写审计**:同一个函数里的停用那一支写,单角色那一支也写,唯独这一支不写。
//     于是有人可以把一个账户换成高权角色、以它的名义做事、再换回来 —— 链上只看得到
//     那次动作,看不到允许它的那次授权(EU4 自己写在下面几行)。
//   · **不作废 MFA 宽限**:mfaGraceKey 的注释声称「角色变更会使宽限失效」,而这一支
//     什么也没做,那句话一直是空的。
//
// 权限本身不受影响 —— 它每个请求实时查库,降权立刻生效。所以这里**不**去 bump token
// 代次:那会把人踢下线,而「改完角色同一个会话立刻用上新权限」是这套东西明确要的行为
// (TestMultiRole_UnionGrantsAndRevokesAdmin 钉着它)。宽限的作废另有精确的做法,
// 见 service.voidMFAGraceFor。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func mustDecode(t *testing.T, raw []byte, out any) {
	t.Helper()
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("decode: %v", err)
	}
}

func TestPatchUser_MultiRoleChangeIsAudited(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	target := app.userIDByEmail(admin, "zhaolei@vela.io")

	roID := app.roleIDByCode(admin, "ro")
	dba := app.roleIDByCode(admin, "l2")
	eq(t, app.do(http.MethodPatch, "/api/v1/users/"+itoa(target), admin,
		map[string]any{"roleIds": []int64{dba, roID}}).Code, 0, "改成多角色")

	var rows []struct {
		Command string `json:"command"`
	}
	mustDecode(t, app.auditItemsRaw(admin, ""), &rows)
	found := false
	for _, row := range rows {
		if strings.Contains(row.Command, "admin.user") || strings.Contains(row.Command, "role") {
			found = true
		}
	}
	if !found {
		t.Error("换角色没进审计链 —— 链上看得到那次动作,看不到允许它的那次授权")
	}
}

// 邀请也要校验角色 —— 别的入口都校验,唯独这一条漏了。
func TestInvite_RejectsAnUnknownRole(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")

	r := app.do(http.MethodPost, "/api/v1/users/invite", admin,
		map[string]any{"email": "ghost@vela.io", "roleId": 999999})
	if r.Code == 0 {
		t.Error("邀请建出了一个挂着不存在角色的账户 —— 它在能力矩阵里一条规则都对不上")
	}

	eq(t, app.do(http.MethodPost, "/api/v1/users/invite", admin,
		map[string]any{"email": "real@vela.io", "roleId": app.roleIDByCode(admin, "ro")}).Code,
		0, "正经角色应当能邀请")
}
