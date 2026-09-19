package service

// 执行阶段的测试夹具。
//
// 只造 fakeExecutor 这一半 —— OSC 那一半（fakeOSC、svc.osc、svc.tableRowsFn）在
// Task 8 才引入，这里抄它们会编译不过。后面几个任务会往这个文件里加 OSC 那半。

import (
	"context"
	"sync"
	"testing"
	"time"

	"velagateway/internal/gateway"
	"velagateway/internal/model"
	"velagateway/internal/repository"
	"velagateway/internal/testsupport"
)

// 假执行器**只替换"把 SQL 发给数据库"这一步**。判定、审计、日志、游标都走真代码 ——
// 换掉更多的话,测的是夹具不是实现。
type fakeExecutor struct {
	mu   sync.Mutex
	sent []string
}

func (f *fakeExecutor) Run(_ context.Context, _ *model.Connection, sql string, _ time.Duration) gateway.ExecResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, sql)
	return gateway.ExecResult{Output: "执行成功 · 1 行受影响", Rows: 1, Ms: 3}
}

// Test 满足 sqlExecutor 接口 —— 这几条用例不测连通性探测,恒答"可连"就够了。
func (f *fakeExecutor) Test(*model.Connection) (bool, string) {
	return true, "已接入网关(假实现)"
}

func (f *fakeExecutor) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.sent) }
func (f *fakeExecutor) last() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) == 0 {
		return ""
	}
	return f.sent[len(f.sent)-1]
}

type execFixture struct {
	t     *testing.T
	svc   *Services
	repo  *repository.Repo
	rel   *model.Release
	stage *model.ReleaseStage
	conn  *model.Connection
	user  *model.User
	exec  *fakeExecutor
}

// newExecFixture 造一张停在执行阶段、人工闸已经点过的发布单。
//
// ConfirmedBy 预先填好是有意的:人工闸不是这几条用例要测的东西,而把它留在那里
// 会让每条用例都先走一遍"等待确认",测的是别人的逻辑。
func newExecFixture(t *testing.T, sql string) *execFixture {
	t.Helper()
	db := testsupport.NewDB(t)
	repo := repository.New(db)
	svc := New(repo, gateway.NewRiskEngine(repo), nil)

	fx := &execFixture{t: t, svc: svc, repo: repo, exec: &fakeExecutor{}}
	svc.Executor = fx.exec

	fx.conn = seedFixtureConnection(t, repo)
	fx.user = seedFixtureUser(t, repo)
	fx.rel, fx.stage = seedFixtureRelease(t, repo, fx.conn, fx.user, sql)
	return fx
}

func (f *execFixture) saveStage() {
	f.t.Helper()
	if err := f.repo.UpdateReleaseStage(f.stage.ID, map[string]any{
		"exec_cursor": f.stage.ExecCursor, "log": f.stage.Log,
	}); err != nil {
		f.t.Fatalf("写阶段失败: %v", err)
	}
}

func (f *execFixture) reloadStage() *model.ReleaseStage {
	f.t.Helper()
	st, err := f.repo.GetReleaseStage(f.stage.ID)
	if err != nil {
		f.t.Fatalf("读阶段失败: %v", err)
	}
	return st
}

func (f *execFixture) reloadRelease() *model.Release {
	f.t.Helper()
	rel, err := f.repo.GetRelease(f.rel.ID)
	if err != nil {
		f.t.Fatalf("读发布单失败: %v", err)
	}
	return rel
}

// seedFixtureConnection 建一台 engine=mysql 的连接,并把它挂到一个分层与环境上。
//
// tierCodeOf 是 stageExecute 无条件要过的一道闸:实例的环境解析不出分层,执行前
// 复判会直接判成 Unavailable(见 gateway.Unavailable),阶段还没送到执行器就先失败了
// —— 不是这几条用例要测的东西,但绕不过去。StrictNoWhere 显式关掉:夹具语句都不带
// WHERE,严格模式会把它们判成高危、需要审批,而这里测的是游标与日志,不是无 WHERE
// 拦截。
func seedFixtureConnection(t *testing.T, repo *repository.Repo) *model.Connection {
	t.Helper()
	tier := &model.EnvTier{Code: "fx-tier", DisplayName: "夹具分层"}
	if err := repo.DB().Create(tier).Error; err != nil {
		t.Fatalf("建分层失败: %v", err)
	}
	// StrictNoWhere 的列带着 `default:true`,而 false 又是 bool 的零值 —— GORM 在
	// Create 时会把"零值 + 有 default 标签"的字段直接替换成 default(哪怕 Select("*")
	// 强制把列纳入 INSERT,写进去的字面量仍然是 true),所以只能建完之后再用 map 形式
	// 的 Update 覆盖一次:那条路径不做零值替换,写的是字面意思。
	if err := repo.DB().Model(&model.EnvTier{}).Where("code = ?", tier.Code).
		Update("strict_nowhere", false).Error; err != nil {
		t.Fatalf("关闭严格模式失败: %v", err)
	}
	env := &model.Environment{Code: "fx-env", DisplayName: "夹具环境", TierCode: tier.Code}
	if err := repo.CreateEnvironment(env); err != nil {
		t.Fatalf("建环境失败: %v", err)
	}
	conn := &model.Connection{
		Name: "fixture-conn", Engine: "mysql", Host: "127.0.0.1", Port: 3306,
		Env: env.Code, Policy: "audit-only", Database: "fixturedb", Status: model.ConnOnline,
	}
	if err := repo.CreateConnection(conn); err != nil {
		t.Fatalf("建连接失败: %v", err)
	}
	return conn
}

// seedFixtureUser 建一个平台管理员。
//
// 角色不是摆设:Repo.EffectiveRoleIDs 会把不指向真实角色行的 role_id 直接过滤掉
// (existingRoles),角色列表一旦变空,三层判定里的能力矩阵检查对"零个角色"的
// 答案是 deny(capabilityLevelUnion),于是发起人会在夹具语句送到执行器之前就被拒。
func seedFixtureUser(t *testing.T, repo *repository.Repo) *model.User {
	t.Helper()
	role := &model.Role{Code: model.RoleAdmin, Name: "平台管理员"}
	if err := repo.DB().Create(role).Error; err != nil {
		t.Fatalf("建角色失败: %v", err)
	}
	user := &model.User{
		Name: "Lin Wei", Email: "linwei@fixture.test", RoleID: role.ID,
		Status: "active", Kind: model.UserKindHuman,
	}
	if err := repo.CreateUser(user); err != nil {
		t.Fatalf("建用户失败: %v", err)
	}
	return user
}

// seedFixtureRelease 建一张停在执行阶段的发布单:一个 execute 阶段,ConfirmedBy
// 已经填好(人工闸已经点过),SQL 是调用方给的那几条语句。
func seedFixtureRelease(t *testing.T, repo *repository.Repo, conn *model.Connection, user *model.User, sql string) (*model.Release, *model.ReleaseStage) {
	t.Helper()
	rel := &model.Release{
		RelNo: "REL-FIXTURE-1", Title: "夹具发布单", ConnectionID: conn.ID,
		Instance: conn.Name, Database: conn.Database, Env: conn.Env, Engine: conn.Engine,
		SQL: sql, CreatorID: user.ID, Creator: user.Name, Source: model.ReleaseSourceConsole,
		Status: model.RunRunning,
	}
	stages := []model.ReleaseStage{{
		Name: "执行", Type: model.StageExecute, Status: model.RunRunning, ConfirmedBy: "Lin Wei",
	}}
	if err := repo.CreateRelease(rel, stages); err != nil {
		t.Fatalf("建发布单失败: %v", err)
	}
	return rel, &stages[0]
}
