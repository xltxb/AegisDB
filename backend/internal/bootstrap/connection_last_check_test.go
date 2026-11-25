package bootstrap

// 连通性巡检的结论要**留下来**。
//
// 这组用例是被一次线上现象换来的:
//
//   「数据源配置 → 状态 → 仅看异常」什么都没有,而实际上有两台新加的库根本连不上,
//    它们的状态显示是「在线」。
//
// 成因是三件事叠在一起:
//
//   1. status 只有 online / maint 两个值,是**人为标的维护态**,从不表示"连得上";
//   2.「仅看异常」筛的就是 status,所以它永远筛不出连不上的实例;
//   3. 连通性其实探过(新建、编辑、批量巡检都会打 /connections/:id/test,而那个接口
//      自 2026-09-10 起是真拨号的)—— 探完之后结论只活在那一次 HTTP 响应里,随手就扔了。
//
// 所以这里钉的是第三件:探过就要记下来,而且**不许碰 status** —— 把探测结果写进
// status,就是把"人标的维护态"和"此刻连不连得上"两件事混成一件,下次维护态就会被
// 一次成功的巡检悄悄抹掉。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

type connCheckView struct {
	ID            int64  `json:"id"`
	Status        string `json:"status"`
	LastCheckAt   string `json:"lastCheckAt"`
	LastCheckOK   *bool  `json:"lastCheckOk"`
	LastCheckErr  string `json:"lastCheckErr"`
	LastCheckKind string `json:"lastCheckKind"`
}

func (a *testApp) connView(token string, id int64) connCheckView {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/connections", token, nil)
	if r.Code != 0 {
		a.t.Fatalf("列连接: %s", r.Msg)
	}
	var rows []connCheckView
	if err := json.Unmarshal(r.Data, &rows); err != nil {
		a.t.Fatalf("解析: %v", err)
	}
	for _, c := range rows {
		if c.ID == id {
			return c
		}
	}
	a.t.Fatalf("没找到连接 %d", id)
	return connCheckView{}
}

// 一台连得上的实例:巡检之后留下"通了"和时间。
func TestConnCheck_SuccessIsRecorded(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	conn := app.realSQLiteConnIn(admin, "dev", "check-ok")

	if r := app.do(http.MethodPost, "/api/v1/connections/"+itoa(conn)+"/test", admin, nil); r.Code != 0 {
		t.Fatalf("这台是真连得上的,巡检不该失败: %s", r.Msg)
	}

	v := app.connView(admin, conn)
	if v.LastCheckOK == nil || !*v.LastCheckOK {
		t.Fatalf("巡检通过没被记下来:%+v", v)
	}
	if v.LastCheckAt == "" {
		t.Error("没记时间 —— 一个不说年龄的结论会被当成此刻的事实")
	}
	if v.LastCheckErr != "" {
		t.Errorf("通了就该清掉上次的错误,实际 %q", v.LastCheckErr)
	}
}

// 一台连不上的实例:这正是线上那两台的样子 —— 状态仍然是 online,而巡检说连不上。
func TestConnCheck_FailureIsRecordedAndStatusIsNotTouched(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	// 指向一个不存在的 MySQL 地址:建得成,但连不上 —— 与用户新加的那两台同形。
	r := app.do(http.MethodPost, "/api/v1/connections", admin, map[string]any{
		"name": "unreachable-probe", "engine": "MySQL", "host": "127.0.0.1", "port": 1,
		"env": "dev", "policy": "strict", "username": "u", "password": "p", "database": "appdb",
	})
	if r.Code != 0 {
		t.Fatalf("建连接: %s", r.Msg)
	}
	var created struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(r.Data, &created); err != nil {
		t.Fatalf("解析: %v", err)
	}

	if tr := app.do(http.MethodPost, "/api/v1/connections/"+itoa(created.ID)+"/test", admin, nil); tr.Code == 0 {
		t.Fatal("这台应当连不上,巡检却说通了")
	}

	v := app.connView(admin, created.ID)
	if v.LastCheckOK == nil {
		t.Fatal("巡检结论没被记下来 —— 这正是「仅看异常」永远为空的原因")
	}
	if *v.LastCheckOK {
		t.Errorf("连不上却记成了通:%+v", v)
	}
	if v.LastCheckErr == "" {
		t.Error("失败原因要留着:i/o timeout 和 Access denied 指向完全不同的处置动作")
	}
	// 端口 1 上没有服务,这是网络层的失败,不是凭据 —— 种类必须是 unreachable,
	// 否则它会被定时巡检当成认证失败永久跳过。
	if v.LastCheckKind != "unreachable" {
		t.Errorf("种类应当是 unreachable,实际 %q", v.LastCheckKind)
	}
	// 最要紧的一条:探测**不许**改 status。两者是不同的事实,混成一件之后,
	// 一次成功的巡检就会把人标的维护态悄悄抹掉。
	if v.Status != "online" {
		t.Errorf("巡检不该动 status,实际变成了 %q", v.Status)
	}
}

// 从没巡检过 ≠ 正常。接口上必须能分辨这两种。
func TestConnCheck_NeverCheckedIsDistinctFromHealthy(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	stg := app.connIDByEnv(admin, "staging") // 种子实例,没人巡检过

	v := app.connView(admin, stg)
	if v.LastCheckOK != nil {
		t.Errorf("从没巡检过的实例不该带结论:%+v", v)
	}
}

// 好了之后要能恢复:上次的错误必须被清掉,否则一台已经修好的实例永远显示成坏的。
func TestConnCheck_RecoveryClearsThePreviousError(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	conn := app.realSQLiteConnIn(admin, "dev", "check-recover")

	// 先人为记一笔失败,再巡检一次真的(这台是连得上的)。
	if err := app.svc.Repo.SaveConnCheck(conn, "unreachable", false, "dial tcp: i/o timeout"); err != nil {
		t.Fatalf("预置失败记录: %v", err)
	}
	if v := app.connView(admin, conn); v.LastCheckErr == "" {
		t.Fatal("预置没生效")
	}

	if r := app.do(http.MethodPost, "/api/v1/connections/"+itoa(conn)+"/test", admin, nil); r.Code != 0 {
		t.Fatalf("巡检: %s", r.Msg)
	}

	v := app.connView(admin, conn)
	if v.LastCheckOK == nil || !*v.LastCheckOK || v.LastCheckErr != "" {
		t.Errorf("修好之后应当完全恢复,实际 %+v", v)
	}
}

// 没配凭据的实例,手动巡检**不记结论**。
//
// 记成"连不上"是把"我们没配凭据"说成"那台库坏了";而定时巡检刻意跳过没凭据的实例,
// 于是那条假的"连不上"永远不会被刷新 —— 审查时发现手动路径正好在这么做。
func TestConnCheck_NoCredentialsIsNotRecordedAsUnreachable(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	stg := app.connIDByEnv(admin, "staging") // 种子实例,没有凭据

	r := app.do(http.MethodPost, "/api/v1/connections/"+itoa(stg)+"/test", admin, nil)
	if r.Code == 0 {
		t.Fatal("没凭据时探测应当报不可连(那是给调用方的答案)")
	}

	v := app.connView(admin, stg)
	if v.LastCheckOK != nil {
		t.Errorf("没凭据不该留下巡检结论,实际 %+v", v)
	}
}

// 改了凭据/地址,上一次的结论说的就是另一台库 —— 必须清掉。
// 不清的话,改好密码的实例会带着旧的"认证失败"被定时巡检永久跳过。
func TestConnCheck_ChangingCredentialsClearsTheVerdict(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	conn := app.realSQLiteConnIn(admin, "dev", "clear-on-edit")
	if err := app.svc.Repo.SaveConnCheck(conn, "auth", false, "Access denied"); err != nil {
		t.Fatalf("预置: %v", err)
	}
	before := app.connView(admin, conn)
	if before.LastCheckKind != "auth" {
		t.Fatal("预置没生效")
	}

	// 改用户名 —— 拨号要素之一。
	full := app.connFull(admin, conn)
	full["username"] = "someone-else"
	if r := app.do(http.MethodPut, "/api/v1/connections/"+itoa(conn), admin, full); r.Code != 0 {
		t.Fatalf("编辑: %s", r.Msg)
	}

	// 编辑接口本身不探测(前端会随后调一次 /test);这里看的是"旧结论有没有被清"。
	v := app.connView(admin, conn)
	if v.LastCheckOK != nil || v.LastCheckKind != "" {
		t.Errorf("改了凭据应当清掉旧结论,实际 %+v", v)
	}
}

// 只改名字这类与拨号无关的字段,不该清 —— 否则每次改个备注都会丢掉巡检历史。
func TestConnCheck_RenamingKeepsTheVerdict(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	conn := app.realSQLiteConnIn(admin, "dev", "keep-on-rename")
	if err := app.svc.Repo.SaveConnCheck(conn, "unreachable", false, "i/o timeout"); err != nil {
		t.Fatalf("预置: %v", err)
	}

	full := app.connFull(admin, conn)
	full["name"] = "keep-on-rename-2"
	if r := app.do(http.MethodPut, "/api/v1/connections/"+itoa(conn), admin, full); r.Code != 0 {
		t.Fatalf("编辑: %s", r.Msg)
	}

	v := app.connView(admin, conn)
	if v.LastCheckOK == nil || v.LastCheckKind != "unreachable" {
		t.Errorf("只改名不该动巡检结论,实际 %+v", v)
	}
}

// 切换维护态走的是"加载再整行 Save":它**不能**把并发那次巡检刚记下的结论写回去。
func TestConnCheck_StatusToggleDoesNotOverwriteTheVerdict(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	conn := app.realSQLiteConnIn(admin, "dev", "toggle-keeps")

	// 模拟"加载了旧快照 → 巡检写入 → 快照 Save"的交错:先记结论,再 Toggle。
	// Toggle 内部 GetConnection 时结论已在,所以这里能测的是 Save 是否 Omit 了那几列 ——
	// 若没 Omit,它写回的是同样的值,看不出差别;所以在 Toggle 之前再改一次结论,
	// 让"快照里的值"和"库里的值"不同。
	if err := app.svc.Repo.SaveConnCheck(conn, "unreachable", false, "old"); err != nil {
		t.Fatalf("预置: %v", err)
	}
	c, err := app.svc.Repo.GetConnection(conn) // 快照:unreachable/old
	if err != nil {
		t.Fatal(err)
	}
	if err := app.svc.Repo.SaveConnCheck(conn, "ok", true, ""); err != nil { // 并发巡检:通了
		t.Fatal(err)
	}
	c.Status = "maint"
	if err := app.svc.Repo.UpdateConnection(c); err != nil { // 用旧快照 Save
		t.Fatal(err)
	}

	v := app.connView(admin, conn)
	if v.Status != "maint" {
		t.Errorf("Status 应当已切换,实际 %q", v.Status)
	}
	if v.LastCheckOK == nil || !*v.LastCheckOK || v.LastCheckKind != "ok" {
		t.Errorf("整行 Save 把并发巡检的结论覆盖回去了:%+v", v)
	}
}

// 巡检失败的原文只给管理员 —— 它是驱动的原始错误,带网关出口 IP、pg_hba 拒绝详情。
func TestConnCheck_ErrorTextIsHiddenFromNonAdmins(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	conn := app.realSQLiteConnIn(admin, "dev", "err-hidden")
	if err := app.svc.Repo.SaveConnCheck(conn, "auth", false, "Access denied for user 'gw'@'10.1.2.3'"); err != nil {
		t.Fatalf("预置: %v", err)
	}
	// 让开发者能看到这台实例(按标签授权),但他不是管理员。
	app.grantConnToDeveloper(admin, conn)
	dev := app.login("zhaolei@aegisdb.io", "aegisdb123") // 研发只读,不是管理员

	if v := app.connView(admin, conn); v.LastCheckErr == "" {
		t.Error("管理员应当看到原文")
	}
	if v := app.connView(dev, conn); v.LastCheckErr != "" {
		t.Errorf("非管理员不该看到驱动原文,实际 %q", v.LastCheckErr)
	}
	// 但"连不上"这个事实本身要给:那正是这一栏存在的理由。
	if v := app.connView(dev, conn); v.LastCheckOK == nil || *v.LastCheckOK {
		t.Error("非管理员仍应看到连不上的结论")
	}
}

// connFull 把一条现有连接拼成更新请求的形状(密码留空 = 保持原样)。
func (a *testApp) connFull(token string, id int64) map[string]any {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/connections", token, nil)
	var rows []struct {
		ID       int64  `json:"id"`
		Name     string `json:"name"`
		Engine   string `json:"engine"`
		Host     string `json:"host"`
		Port     int    `json:"port"`
		Env      string `json:"env"`
		Policy   string `json:"policy"`
		Username string `json:"username"`
		Database string `json:"database"`
	}
	_ = json.Unmarshal(r.Data, &rows)
	for _, c := range rows {
		if c.ID != id {
			continue
		}
		host := c.Host
		if c.Port > 0 {
			host = fmt.Sprintf("%s:%d", c.Host, c.Port)
		}
		return map[string]any{
			"name": c.Name, "engine": c.Engine, "host": host, "env": c.Env,
			"policy": c.Policy, "username": c.Username, "database": c.Database,
		}
	}
	a.t.Fatalf("没找到连接 %d", id)
	return nil
}

// grantConnToDeveloper 让研发只读(zhaolei,标签 analytics/readonly)够得到这台实例。
func (a *testApp) grantConnToDeveloper(admin string, id int64) {
	a.t.Helper()
	if r := a.do(http.MethodPatch, "/api/v1/connections/"+itoa(id), admin, map[string]any{"tags": "analytics"}); r.Code != 0 {
		a.t.Fatalf("贴标签: %s", r.Msg)
	}
}
