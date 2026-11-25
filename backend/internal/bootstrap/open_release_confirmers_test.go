package bootstrap

// 人工确认节点对外给出确认人(2026-11 用户反馈:「人工确认节点没有对外发送该节点审批人,
// 对方没有办法知道审批卡片应该发送给谁」)。
//
// 口径(与用户确认):
//   - 出口只有查单接口 GET /open/releases/:relNo,字段与审批阶段的 approvers 一致。
//   - 只给**正在等**的 manual 阶段;执行变更闸不给(开放接口放行不了它)。
//   - 配了 confirmRole = 该角色的在职真人成员;没配 = 全体有审批能力的在职真人。
//     两种都去掉发起人(未开自审批时)、服务账号、停用账号 —— 名单上的人调 confirm 必能过授权。

import (
	"encoding/json"
	"net/http"
	"sort"
	"testing"
	"time"

	"velagateway/internal/model"
)

type openStageWithApprovers struct {
	Order     int    `json:"order"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	Status    string `json:"status"`
	Approvers []struct {
		Name    string `json:"name"`
		Account string `json:"account"`
		Status  string `json:"status"`
	} `json:"approvers"`
}

func (a *testApp) openStages(token, relNo string) []openStageWithApprovers {
	a.t.Helper()
	r := a.openDo(http.MethodGet, "/api/v1/open/releases/"+relNo, token, nil)
	if r.Code != 0 {
		a.t.Fatalf("open get release: code=%d msg=%s", r.Code, r.Msg)
	}
	var v struct {
		Stages []openStageWithApprovers `json:"stages"`
	}
	if err := json.Unmarshal(r.Data, &v); err != nil {
		a.t.Fatalf("decode open release: %v", err)
	}
	return v.Stages
}

func openStageOf(stages []openStageWithApprovers, typ string) *openStageWithApprovers {
	for i := range stages {
		if stages[i].Type == typ {
			return &stages[i]
		}
	}
	return nil
}

// openReleaseAtManual 经开放接口提一张单,等它停在人工确认上。
func (a *testApp) openReleaseAtManual(admin, name, manualCfg string) (token, relNo string) {
	a.t.Helper()
	stage := map[string]any{"name": "人工确认", "type": "manual"}
	if manualCfg != "" {
		stage["config"] = manualCfg
	}
	pid := a.createPipeline(admin, name+"-流程", "", []map[string]any{
		stage,
		{"name": "执行变更", "type": "execute"},
	})
	token = a.approverCredential(admin, name, nil,
		[]string{"release:create", "release:read", "release:approve"}, pid)
	r := a.openDo(http.MethodPost, "/api/v1/open/releases", token, map[string]any{
		"title": name, "instance": "order-cluster-stg", "sql": "SELECT 1;", "externalRef": name + "-1",
	})
	if r.Code != 0 {
		a.t.Fatalf("open create: code=%d msg=%s", r.Code, r.Msg)
	}
	var created openRelease
	_ = json.Unmarshal(r.Data, &created)
	for i := 0; i < 300; i++ {
		if g := openStageOf(a.openStages(token, created.RelNo), "manual"); g != nil && g.Status == "waiting" {
			return token, created.RelNo
		}
		time.Sleep(20 * time.Millisecond)
	}
	a.t.Fatalf("release %s never parked on the manual gate", created.RelNo)
	return "", ""
}

func listedAccounts(st *openStageWithApprovers) []string {
	out := []string{}
	for _, ap := range st.Approvers {
		out = append(out, ap.Account)
	}
	sort.Strings(out)
	return out
}

func hasAccount(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// 基准:按库里的事实独立算一遍「有审批能力的在职真人,去掉发起人」。
func (a *testApp) expectedApproveCapable(creatorEmail string) []string {
	a.t.Helper()
	users, err := a.repo.ListUsers()
	if err != nil {
		a.t.Fatalf("list users: %v", err)
	}
	out := []string{}
	for i := range users {
		u := &users[i]
		if u.Kind == model.UserKindService || (u.Status != "" && u.Status != "active") || u.Email == creatorEmail {
			continue
		}
		capable := false
		for _, id := range a.repo.EffectiveRoleIDs(u) {
			if r, err := a.repo.GetRole(id); err == nil && (r.CanApprove || r.Code == "admin") {
				capable = true
			}
		}
		if capable {
			out = append(out, u.Email)
		}
	}
	sort.Strings(out)
	return out
}

// 配了确认角色:名单 = 该角色的在职真人成员;放行后名单消失,放行行为不变。
func TestOpenReleaseListsManualConfirmersByRole(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	token, relNo := app.openReleaseAtManual(admin, "card-role", `{"confirmRole":"l2"}`)

	stages := app.openStages(token, relNo)
	g := openStageOf(stages, "manual")
	got := listedAccounts(g)
	if !hasAccount(got, "chenhao@vela.io") {
		t.Fatalf("l2 member chenhao should be listed, got %v", got)
	}
	if hasAccount(got, "zhangwei@vela.io") || hasAccount(got, "linwei@vela.io") {
		t.Fatalf("people outside the confirm role must not be listed, got %v", got)
	}
	role, err := app.repo.GetRoleByCode("l2")
	if err != nil {
		t.Fatal(err)
	}
	members, _ := app.repo.MembersOfRole(role.ID)
	want := []string{}
	for _, m := range members {
		if m.Kind != model.UserKindService && (m.Status == "" || m.Status == "active") {
			want = append(want, m.Email)
		}
	}
	sort.Strings(want)
	eq(t, len(got), len(want), "every active human l2 member, nobody else")
	for _, ap := range g.Approvers {
		if ap.Name == "" || ap.Status != "active" {
			t.Errorf("each confirmer carries a name and status=active, got %+v", ap)
		}
	}
	if ex := openStageOf(stages, "execute"); ex != nil && len(ex.Approvers) > 0 {
		t.Errorf("the execute gate is not ours to list, got %+v", ex.Approvers)
	}

	// 名单上的人确能放行(原有放行行为不变)。
	r, c := app.openConfirm(token, relNo, g.Approvers[0].Account)
	eq(t, r.Code, 0, "a listed confirmer passes the gate")
	eq(t, c.AlreadyConfirmed, false, "this call passed it")
	for i := 0; i < 300; i++ {
		if m := openStageOf(app.openStages(token, relNo), "manual"); m != nil && m.Status == "success" {
			if len(m.Approvers) > 0 {
				t.Errorf("a passed gate lists nobody, got %+v", m.Approvers)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the manual stage should be success once confirmed")
}

// 没配确认角色:名单 = 全体有审批能力的在职真人,不含发起服务账号、停用账号。
func TestOpenReleaseListsManualConfirmersWithoutRole(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	token, relNo := app.openReleaseAtManual(admin, "card-norole", "")

	got := listedAccounts(openStageOf(app.openStages(token, relNo), "manual"))
	want := app.expectedApproveCapable("")
	if !hasAccount(got, "linwei@vela.io") || !hasAccount(got, "zhangwei@vela.io") {
		t.Fatalf("approve-capable people should be listed, got %v", got)
	}
	eq(t, len(got), len(want), "exactly the approve-capable active humans")
	for i := range want {
		if i < len(got) && got[i] != want[i] {
			t.Fatalf("listed %v, want %v", got, want)
		}
	}
	for _, acc := range got {
		if u, err := app.repo.GetUserByEmail(acc); err != nil || u.Kind == model.UserKindService {
			t.Errorf("service accounts (incl. the submitting one) are never listed: %s", acc)
		}
	}

	// 停用一位:他立刻从名单上消失,也放行不了。
	zw, err := app.repo.GetUserByEmail("zhangwei@vela.io")
	if err != nil {
		t.Fatal(err)
	}
	if err := app.repo.UpdateUserFields(zw.ID, map[string]any{"status": "disabled"}); err != nil {
		t.Fatal(err)
	}
	got = listedAccounts(openStageOf(app.openStages(token, relNo), "manual"))
	if hasAccount(got, "zhangwei@vela.io") {
		t.Fatalf("a disabled account must drop off the list, got %v", got)
	}
	r, _ := app.openConfirm(token, relNo, "zhangwei@vela.io")
	if r.Code == 0 {
		t.Fatal("a disabled account must not pass the gate either")
	}

	r, _ = app.openConfirm(token, relNo, "linwei@vela.io")
	eq(t, r.Code, 0, "a listed approve-capable person passes the gate")
}

// 发起人(真人)不在名单上 —— 他点不了自己的单,名单不能把卡片发给他。
func TestOpenReleaseConfirmersExcludeCreator(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	_, relNo := app.openReleaseAtManual(admin, "card-creator", "")
	rel, err := app.repo.GetReleaseByNo(relNo)
	if err != nil {
		t.Fatal(err)
	}
	// 把发起人改成一位真人(linwei),模拟控制台建单的形态。
	lw, _ := app.repo.GetUserByEmail("linwei@vela.io")
	if err := app.repo.DB().Model(&model.Release{}).Where("id = ?", rel.ID).
		Updates(map[string]any{"creator_id": lw.ID, "creator": lw.Name}).Error; err != nil {
		t.Fatal(err)
	}
	rel, _ = app.repo.GetRelease(rel.ID)
	resp := app.svc.OpenReleaseResp(rel, true)
	for _, st := range resp.Stages {
		if st.Type != model.StageManual {
			continue
		}
		for _, ap := range st.Approvers {
			if ap.Account == "linwei@vela.io" {
				t.Fatalf("the creator must not be listed as a confirmer: %+v", st.Approvers)
			}
		}
		if len(st.Approvers) == 0 {
			t.Fatal("others with approve ability are still listed")
		}
	}
}
