package service

// 执行阶段的测试夹具。
//
// fakeExecutor 换掉「把 SQL 发给数据库」这一步,fakeOSC 换掉「把一次迁移发起出去」
// 这一步 —— 两者都只替换最外层的一次调用,判定、审计、日志、游标全走真代码。

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"velagateway/internal/gateway"
	"velagateway/internal/model"
	"velagateway/internal/osc"
	"velagateway/internal/repository"
	"velagateway/internal/testsupport"
)

// errOSCDisabled 是 fakeOSC.Start 用来模拟"发起被拒"的哨兵错误 —— 具体原因
// 不重要(真实场景可能是前置检查不通过、也可能是配置开关关着),这几条用例
// 只关心"发起失败之后阶段怎么办"。
var errOSCDisabled = errors.New("osc: 发起被拒(模拟)")

// fakeOSC **只替换"把一次迁移发起出去/叫停"这一步**。startID 是 Start 成功时
// 回填的任务号,startErr 非空则 Start 失败 —— 两者互斥,由调用方按用例需要挑一个。
type fakeOSC struct {
	mu       sync.Mutex
	startID  int64
	startErr error
	started  []osc.StartRequest
	aborted  []int64
}

func (f *fakeOSC) Start(_ context.Context, req osc.StartRequest) (*osc.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startErr != nil {
		return nil, f.startErr
	}
	f.started = append(f.started, req)
	return &osc.Job{ID: f.startID}, nil
}

func (f *fakeOSC) Abort(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.aborted = append(f.aborted, id)
	return nil
}

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
	osc   *fakeOSC
	// rowsOfTable 是 svc.tableRowsFn 的假答案,换掉真的 information_schema 查询。
	// 用例在 newExecFixture 之后再赋值 —— 闭包按引用读它,读到的永远是当下的值。
	rowsOfTable int64
	// oscEnabled 是 svc.oscEnabled 的假答案 —— 换掉配置开关 + 急停开关那一对
	// 真实设置读取。默认 true(没打开急停时的常态),用例按需要拨成 false。
	oscEnabled bool
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

	fx := &execFixture{t: t, svc: svc, repo: repo, exec: &fakeExecutor{}, osc: &fakeOSC{}, oscEnabled: true}
	svc.Executor = fx.exec
	svc.osc = fx.osc
	// 闭包捕获 fx,不是此刻的值:用例在 newExecFixture 返回之后才设置
	// fx.rowsOfTable(阈值判定要等语句先过完 DDL 识别才会用到它)。
	svc.tableRowsFn = func(*model.Connection, string, string) int64 { return fx.rowsOfTable }
	svc.oscEnabled = func() bool { return fx.oscEnabled }

	fx.conn = seedFixtureConnection(t, repo)
	fx.user = seedFixtureUser(t, repo)
	fx.rel, fx.stage = seedFixtureRelease(t, repo, fx.conn, fx.user, sql)
	return fx
}

// saveStage 把夹具在内存里改过的阶段字段落库。row_count 也在其中(列名是
// row_count —— rows 是 MySQL 保留字,见 ADR 0016 §二):影响行数跨恢复要累计,
// 不带上它的话,任何一条"恢复时 Rows 不是从 0 起算"的用例都存不进前提条件。
func (f *execFixture) saveStage() {
	f.t.Helper()
	if err := f.repo.UpdateReleaseStage(f.stage.ID, map[string]any{
		"exec_cursor": f.stage.ExecCursor, "log": f.stage.Log, "row_count": f.stage.Rows,
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

// waitForCursor 轮询直到阶段的 exec_cursor 达到 want,或者超时。
//
// resumeReleaseAsync 把"续跑"扔到另一条 goroutine 上(见 pipeline_osc.go 的注释:
// 不能在 OnFinish 的同步调用链里等 driveRelease 跑完剩下的阶段,否则 IsRunning/
// Abort 会对一个已经结束的任务说谎)。测这一段只能等它,而不是假设一次固定的
// 睡眠一定够 —— 等法和 osc/runner_test.go 的 waitForStatus/waitForFinish 是同一
// 个套路:等不到就带着最后一次读到的值失败,方便看出它卡在哪,而不是用一个
// sleep 赌时间赌够了。
func (f *execFixture) waitForCursor(want int, timeout time.Duration) *model.ReleaseStage {
	f.t.Helper()
	deadline := time.Now().Add(timeout)
	var last *model.ReleaseStage
	for time.Now().Before(deadline) {
		last = f.reloadStage()
		if last.ExecCursor == want {
			return last
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.t.Fatalf("%v 内游标没有到 %d,卡在 %d", timeout, want, last.ExecCursor)
	return nil
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
