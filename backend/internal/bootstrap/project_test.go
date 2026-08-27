package bootstrap

// 项目(Project):数据库与升级单的归属。
//
// 它是**组织维度,不是安全边界**。这一点必须一开始就说死:本系统已经有标签
// (Connection.Tags)做数据访问范围,再叠一层能限制访问的"项目",就有了两套互相
// 重叠的范围机制 —— 迟早有一层是错的,而错的那一层会以"本该看不见却看得见"的
// 形式出现。项目只回答"这个库、这张单归谁跟进",判定层完全不看它。
//
// 升级单的项目是**提交时快照**的,和 Env/TierCode/Engine 一样:库以后换了项目,
// 历史单据仍归属当初那个项目,否则一次归属调整会把过去所有单据的账改掉。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

type projectView struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Owner       string `json:"owner"`
	Description string `json:"description"`
	Connections int    `json:"connections"`
	Releases    int    `json:"releases"`
}

func (a *testApp) createProject(token, name, owner string) projectView {
	a.t.Helper()
	r := a.do(http.MethodPost, "/api/v1/projects", token, map[string]any{
		"name": name, "owner": owner, "description": name + " 的说明",
	})
	if r.Code != 0 {
		a.t.Fatalf("create project: code=%d msg=%s", r.Code, r.Msg)
	}
	var p projectView
	_ = json.Unmarshal(r.Data, &p)
	return p
}

func (a *testApp) projects(token string) []projectView {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/projects", token, nil)
	var ps []projectView
	_ = json.Unmarshal(r.Data, &ps)
	return ps
}

func TestProjectCRUD(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")

	p := app.createProject(admin, "订单业务线", "订单组")
	if p.ID == 0 {
		t.Fatal("created project should carry an id")
	}
	eq(t, p.Owner, "订单组", "owner stored")

	// 重名要拒:名字就是人用来指代项目的东西,两个同名项目谁也说不清哪个是哪个。
	dup := app.do(http.MethodPost, "/api/v1/projects", admin, map[string]any{"name": "订单业务线"})
	if dup.Code == 0 {
		t.Error("a duplicate project name must be refused")
	}
	// 空名字同样拒。
	blank := app.do(http.MethodPost, "/api/v1/projects", admin, map[string]any{"name": "   "})
	if blank.Code == 0 {
		t.Error("a blank project name must be refused")
	}

	// 编辑
	up := app.do(http.MethodPatch, "/api/v1/projects/"+itoa(p.ID), admin,
		map[string]any{"name": "订单与交易", "owner": "交易组"})
	eq(t, up.Code, 0, "edit project")
	got := app.projects(admin)
	found := false
	for _, x := range got {
		if x.ID == p.ID {
			found = true
			eq(t, x.Name, "订单与交易", "renamed")
			eq(t, x.Owner, "交易组", "owner updated")
		}
	}
	if !found {
		t.Fatal("edited project missing from the listing")
	}

	// 删除
	eq(t, app.do(http.MethodDelete, "/api/v1/projects/"+itoa(p.ID), admin, nil).Code, 0, "delete project")
	for _, x := range app.projects(admin) {
		if x.ID == p.ID {
			t.Error("deleted project still listed")
		}
	}
}

// 项目的增删改是管理动作 —— 与连接配置同一档:看得见,但改不了。
func TestProjectMutationsAreAdminOnly(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	dba := app.login("chenhao@vela.io", "vela123") // l2:能看配置,不能改

	p := app.createProject(admin, "只读验证项目", "x")
	if r := app.do(http.MethodPost, "/api/v1/projects", dba, map[string]any{"name": "越权建项目"}); r.Code == 0 {
		t.Error("a non-admin must not create projects")
	}
	if r := app.do(http.MethodPatch, "/api/v1/projects/"+itoa(p.ID), dba, map[string]any{"name": "越权改名"}); r.Code == 0 {
		t.Error("a non-admin must not edit projects")
	}
	if r := app.do(http.MethodDelete, "/api/v1/projects/"+itoa(p.ID), dba, nil); r.Code == 0 {
		t.Error("a non-admin must not delete projects")
	}
}

// 数据库归属项目,且项目仍被引用时不能删 —— 悄悄把库变成"无归属",会让跟进
// 视图静静地说谎。
func TestProjectOwnsConnectionsAndRefusesDeleteWhileReferenced(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	p := app.createProject(admin, "订单业务线", "订单组")
	connID := app.connIDByEnv(admin, "staging")

	eq(t, app.do(http.MethodPatch, "/api/v1/connections/"+itoa(connID), admin,
		map[string]any{"projectId": p.ID}).Code, 0, "assign the connection to the project")

	// 列表里能看到归属与计数
	var listed projectView
	for _, x := range app.projects(admin) {
		if x.ID == p.ID {
			listed = x
		}
	}
	eq(t, listed.Connections, 1, "project reports how many databases it owns")

	del := app.do(http.MethodDelete, "/api/v1/projects/"+itoa(p.ID), admin, nil)
	if del.Code == 0 {
		t.Fatal("deleting a project that still owns databases must be refused")
	}
	if !strings.Contains(del.Msg, "1") {
		t.Errorf("the refusal should say how many still reference it, got %q", del.Msg)
	}
}

// 升级单按项目归属,且是提交时快照 —— 库以后换项目,历史单据不跟着改账。
func TestReleaseIsFiledUnderTheConnectionsProject(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	orders := app.createProject(admin, "订单业务线", "订单组")
	billing := app.createProject(admin, "计费业务线", "计费组")
	connID := app.connIDByEnv(admin, "staging")
	eq(t, app.do(http.MethodPatch, "/api/v1/connections/"+itoa(connID), admin,
		map[string]any{"projectId": orders.ID}).Code, 0, "assign to orders")

	pid := app.createPipeline(admin, "项目归属流程", "", []map[string]any{
		{"name": "人工审批", "type": "approve"},
	})
	r := app.submitRelease(admin, map[string]any{
		"title": "订单库变更", "pipelineId": pid, "connectionId": connID,
		"sql": "SELECT 1;", "reason": "项目归属回归",
	})
	eq(t, r.Code, 0, "submit")
	var rel struct {
		ID          int64  `json:"id"`
		ProjectID   int64  `json:"projectId"`
		ProjectName string `json:"projectName"`
	}
	_ = json.Unmarshal(r.Data, &rel)
	eq(t, rel.ProjectID, orders.ID, "release filed under the connection's project")
	eq(t, rel.ProjectName, "订单业务线", "project name snapshotted for history")

	// 把库改挂到另一个项目:已提交的单据不能跟着改账。
	eq(t, app.do(http.MethodPatch, "/api/v1/connections/"+itoa(connID), admin,
		map[string]any{"projectId": billing.ID}).Code, 0, "move the database to billing")
	after := app.getRelease(admin, rel.ID)
	eq(t, after.ProjectName, "订单业务线", "history keeps the project it was filed under")

	// 按项目过滤,这正是"方便跟进"的用法。
	lr := app.do(http.MethodGet, "/api/v1/releases?scope=all&projectId="+itoa(orders.ID), admin, nil)
	var page struct {
		Items []struct {
			ID int64 `json:"id"`
		} `json:"items"`
	}
	_ = json.Unmarshal(lr.Data, &page)
	hit := false
	for _, it := range page.Items {
		if it.ID == rel.ID {
			hit = true
		}
	}
	if !hit {
		t.Error("filtering releases by project should find the release filed under it")
	}
	// 另一个项目下不该出现它
	lr2 := app.do(http.MethodGet, "/api/v1/releases?scope=all&projectId="+itoa(billing.ID), admin, nil)
	_ = json.Unmarshal(lr2.Data, &page)
	for _, it := range page.Items {
		if it.ID == rel.ID {
			t.Error("a release must not appear under a project it was not filed under")
		}
	}
}

// 未归属的库照常可用 —— 项目是组织维度,不是准入条件。没有项目的库不该被挡。
func TestUnassignedConnectionStillWorks(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	connID := app.connIDByEnv(admin, "staging")

	pid := app.createPipeline(admin, "未归属流程", "", []map[string]any{
		{"name": "人工审批", "type": "approve"},
	})
	r := app.submitRelease(admin, map[string]any{
		"title": "未归属库的变更", "pipelineId": pid, "connectionId": connID,
		"sql": "SELECT 1;", "reason": "项目归属回归",
	})
	eq(t, r.Code, 0, "a database with no project must still be usable")
	var rel struct {
		ProjectID int64 `json:"projectId"`
	}
	_ = json.Unmarshal(r.Data, &rel)
	eq(t, rel.ProjectID, int64(0), "no project is a legitimate state, not an error")
}
