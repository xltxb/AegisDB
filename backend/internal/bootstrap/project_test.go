package bootstrap

// 项目(Project):**数据库**与升级单的归属。
//
// 归属的单位是库,不是数据源。一个实例底下的几个库分属不同团队是常态;把归属挂在
// 实例上,等于逼着人按项目去拆实例 —— 那是让组织结构去改数据库拓扑,方向反了。
// 下面第三个用例就是钉这一条的:同一个实例,两个库,两个项目。
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
	Databases   int    `json:"databases"`
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

// fileDatabase 把一个库归到项目下(projectID = 0 取消归属)。
func (a *testApp) fileDatabase(token string, connID int64, database string, projectID int64) apiResp {
	a.t.Helper()
	return a.do(http.MethodPut, "/api/v1/connections/"+itoa(connID)+"/database-project", token,
		map[string]any{"database": database, "projectId": projectID})
}

func (a *testApp) projectByID(token string, id int64) projectView {
	a.t.Helper()
	for _, x := range a.projects(token) {
		if x.ID == id {
			return x
		}
	}
	a.t.Fatalf("project %d missing from the listing", id)
	return projectView{}
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
	got := app.projectByID(admin, p.ID)
	eq(t, got.Name, "订单与交易", "renamed")
	eq(t, got.Owner, "交易组", "owner updated")

	// 删除
	eq(t, app.do(http.MethodDelete, "/api/v1/projects/"+itoa(p.ID), admin, nil).Code, 0, "delete project")
	for _, x := range app.projects(admin) {
		if x.ID == p.ID {
			t.Error("deleted project still listed")
		}
	}
}

// 项目的增删改、以及给库定归属,都是管理动作 —— 与连接配置同一档:看得见,改不了。
func TestProjectMutationsAreAdminOnly(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	dba := app.login("chenhao@vela.io", "vela123") // l2:能看配置,不能改

	p := app.createProject(admin, "只读验证项目", "x")
	connID := app.connIDByEnv(admin, "staging")

	if r := app.do(http.MethodPost, "/api/v1/projects", dba, map[string]any{"name": "越权建项目"}); r.Code == 0 {
		t.Error("a non-admin must not create projects")
	}
	if r := app.do(http.MethodPatch, "/api/v1/projects/"+itoa(p.ID), dba, map[string]any{"name": "越权改名"}); r.Code == 0 {
		t.Error("a non-admin must not edit projects")
	}
	if r := app.fileDatabase(dba, connID, "db_orders", p.ID); r.Code == 0 {
		t.Error("a non-admin must not change a database's project")
	}
	if r := app.do(http.MethodDelete, "/api/v1/projects/"+itoa(p.ID), dba, nil); r.Code == 0 {
		t.Error("a non-admin must not delete projects")
	}
}

// 归属的单位是**库**,不是数据源:同一个实例底下的两个库,可以分属两个项目。
// 这条是整个特性的立足点 —— 如果归属只能挂在实例上,一个实例住着两个团队的库时,
// 跟进视图就只能二选一地说谎。
func TestTwoDatabasesOnOneInstanceBelongToDifferentProjects(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	orders := app.createProject(admin, "订单业务线", "订单组")
	billing := app.createProject(admin, "计费业务线", "计费组")
	connID := app.connIDByEnv(admin, "staging") // 同一个实例

	eq(t, app.fileDatabase(admin, connID, "db_orders", orders.ID).Code, 0, "file db_orders")
	eq(t, app.fileDatabase(admin, connID, "db_billing", billing.ID).Code, 0, "file db_billing")

	eq(t, app.projectByID(admin, orders.ID).Databases, 1, "orders owns exactly its own database")
	eq(t, app.projectByID(admin, billing.ID).Databases, 1, "billing owns exactly its own database")

	// 改挂:同一个库再定一次归属是覆盖,不是又添一条。
	eq(t, app.fileDatabase(admin, connID, "db_orders", billing.ID).Code, 0, "refile db_orders")
	eq(t, app.projectByID(admin, orders.ID).Databases, 0, "the database left orders")
	eq(t, app.projectByID(admin, billing.ID).Databases, 2, "and landed in billing, not duplicated")

	// 取消归属
	eq(t, app.fileDatabase(admin, connID, "db_orders", 0).Code, 0, "unfile db_orders")
	eq(t, app.projectByID(admin, billing.ID).Databases, 1, "unfiling removes it")

	// 不存在的项目要拒:悬空的归属会让库在实例侧显示"已归属",却没有任何项目列出它。
	if r := app.fileDatabase(admin, connID, "db_orders", 999999); r.Code == 0 {
		t.Error("filing a database under a nonexistent project must be refused")
	}
}

// 项目名下还有库时不能删 —— 悄悄把库变成"未归属",会让跟进视图静静地说谎。
func TestProjectRefusesDeleteWhileDatabasesAreFiledUnderIt(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	p := app.createProject(admin, "订单业务线", "订单组")
	connID := app.connIDByEnv(admin, "staging")
	eq(t, app.fileDatabase(admin, connID, "db_orders", p.ID).Code, 0, "file one database")

	del := app.do(http.MethodDelete, "/api/v1/projects/"+itoa(p.ID), admin, nil)
	if del.Code == 0 {
		t.Fatal("deleting a project that still owns databases must be refused")
	}
	if !strings.Contains(del.Msg, "1") {
		t.Errorf("the refusal should say how many still reference it, got %q", del.Msg)
	}

	// 清空归属后就能删了。
	eq(t, app.fileDatabase(admin, connID, "db_orders", 0).Code, 0, "unfile")
	eq(t, app.do(http.MethodDelete, "/api/v1/projects/"+itoa(p.ID), admin, nil).Code, 0, "now deletable")
}

// 升级单归到**目标库**所属的项目,且是提交时快照 —— 库以后换项目,历史单据不改账。
func TestReleaseIsFiledUnderTheTargetDatabasesProject(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	orders := app.createProject(admin, "订单业务线", "订单组")
	billing := app.createProject(admin, "计费业务线", "计费组")
	connID := app.connIDByEnv(admin, "staging")
	eq(t, app.fileDatabase(admin, connID, "db_orders", orders.ID).Code, 0, "file db_orders under orders")
	eq(t, app.fileDatabase(admin, connID, "db_billing", billing.ID).Code, 0, "file db_billing under billing")

	pid := app.createPipeline(admin, "项目归属流程", "", []map[string]any{
		{"name": "人工审批", "type": "approve"},
	})
	r := app.submitRelease(admin, map[string]any{
		"title": "订单库变更", "pipelineId": pid, "connectionId": connID,
		"database": "db_orders", "sql": "SELECT 1;", "reason": "项目归属回归",
	})
	eq(t, r.Code, 0, "submit")
	var rel struct {
		ID          int64  `json:"id"`
		ProjectID   int64  `json:"projectId"`
		ProjectName string `json:"projectName"`
	}
	_ = json.Unmarshal(r.Data, &rel)
	eq(t, rel.ProjectID, orders.ID, "release filed under the TARGET DATABASE's project")
	eq(t, rel.ProjectName, "订单业务线", "project name snapshotted for history")

	// 同一个实例、另一个库 —— 归到另一个项目,证明归属确实是按库分的。
	r2 := app.submitRelease(admin, map[string]any{
		"title": "计费库变更", "pipelineId": pid, "connectionId": connID,
		"database": "db_billing", "sql": "SELECT 1;", "reason": "项目归属回归",
	})
	eq(t, r2.Code, 0, "submit the sibling database")
	var rel2 struct {
		ID          int64  `json:"id"`
		ProjectName string `json:"projectName"`
	}
	_ = json.Unmarshal(r2.Data, &rel2)
	eq(t, rel2.ProjectName, "计费业务线", "the sibling database files under its own project")

	// 把库改挂到另一个项目:已提交的单据不能跟着改账。
	eq(t, app.fileDatabase(admin, connID, "db_orders", billing.ID).Code, 0, "move db_orders to billing")
	after := app.getRelease(admin, rel.ID)
	eq(t, after.ProjectName, "订单业务线", "history keeps the project it was filed under")

	// 按项目过滤,这正是"方便跟进"的用法。
	if !app.releaseListHas(admin, orders.ID, rel.ID) {
		t.Error("filtering releases by project should find the release filed under it")
	}
	if app.releaseListHas(admin, orders.ID, rel2.ID) {
		t.Error("a release must not appear under a project it was not filed under")
	}
}

// releaseListHas 按项目筛出升级单列表,看某张单在不在里面。
func (a *testApp) releaseListHas(token string, projectID, releaseID int64) bool {
	a.t.Helper()
	lr := a.do(http.MethodGet, "/api/v1/releases?scope=all&projectId="+itoa(projectID), token, nil)
	var page struct {
		Items []struct {
			ID int64 `json:"id"`
		} `json:"items"`
	}
	_ = json.Unmarshal(lr.Data, &page)
	for _, it := range page.Items {
		if it.ID == releaseID {
			return true
		}
	}
	return false
}

// 未归属的库照常可用 —— 项目是组织维度,不是准入条件。没有项目的库不该被挡。
func TestUnassignedDatabaseStillWorks(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	connID := app.connIDByEnv(admin, "staging")

	pid := app.createPipeline(admin, "未归属流程", "", []map[string]any{
		{"name": "人工审批", "type": "approve"},
	})
	r := app.submitRelease(admin, map[string]any{
		"title": "未归属库的变更", "pipelineId": pid, "connectionId": connID,
		"database": "db_nowhere", "sql": "SELECT 1;", "reason": "项目归属回归",
	})
	eq(t, r.Code, 0, "a database with no project must still be usable")
	var rel struct {
		ProjectID int64 `json:"projectId"`
	}
	_ = json.Unmarshal(r.Data, &rel)
	eq(t, rel.ProjectID, int64(0), "no project is a legitimate state, not an error")
}
