package bootstrap

// 收单时的语法与对象检查。
//
// 跑在**真的 PostgreSQL 上**:这组用例要验的核心正是"把语法交给数据库自己判",拿一个
// 替身来测,测的是一段永远不会跑到的代码。PostgreSQL 的 EXPLAIN 支持 SELECT / INSERT /
// UPDATE / DELETE,与 MySQL 走的是同一段代码路径。
//
// 钉住的是四件会真正伤到人的事:
//
//   1. 语法错要带**数据库原话**。一句笼统的"语法错误"等于没说 —— 原话里有位置。
//   2. 对象不存在要报出来,并且说清**是怎么知道的**(缓存还是实时)。一份三天前的
//      缓存说"表不存在",很可能只是那张表是昨天建的。
//   3. `CREATE TABLE t; INSERT INTO t …` 不许报假警。不认这个形状,每一份
//      "建表 + 灌数据"的脚本都会挂一串红。
//   4. **没做的检查必须显示成没做**。一条没被检查的 DDL 如果不出现在结果里,
//      读的人会默认它通过了 —— 那是整份结果里最容易骗人的地方。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

type precheckIssue struct {
	Stmt    int    `json:"stmt"`
	Level   string `json:"level"`
	Kind    string `json:"kind"`
	Object  string `json:"object"`
	Message string `json:"message"`
	Source  string `json:"source"`
}

type precheckResult struct {
	Statements    int             `json:"statements"`
	Errors        int             `json:"errors"`
	Warnings      int             `json:"warnings"`
	SyntaxChecked int             `json:"syntaxChecked"`
	SyntaxSkipped int             `json:"syntaxSkipped"`
	ObjectSource  string          `json:"objectSource"`
	CacheSyncedAt string          `json:"cacheSyncedAt"`
	Note          string          `json:"note"`
	Issues        []precheckIssue `json:"issues"`
}

// submitForPrecheck 提一张单,返回单子里那份检查结果。
//
// 用只有 execute 阶段的流程:这组用例要看的是**收单**这一刻算出来的东西,
// 跟后面跑不跑得起来无关。
func submitForPrecheck(t *testing.T, app *testApp, token string, connID int64, sql string) precheckResult {
	t.Helper()
	pid := app.createPipeline(token, "收单检查流程-"+itoa(connID), "", []map[string]any{
		{"name": "执行变更", "type": "execute"},
	})
	r := app.submitRelease(token, map[string]any{
		"title": "收单检查", "pipelineId": pid, "connectionId": connID,
		"sql": sql, "reason": "收单检查回归",
	})
	if r.Code != 0 {
		t.Fatalf("提单被拒了(这组用例的前提是检查不拦单): code=%d msg=%s", r.Code, r.Msg)
	}
	var rel struct {
		ID       int64  `json:"id"`
		Precheck string `json:"precheck"`
	}
	if err := json.Unmarshal(r.Data, &rel); err != nil {
		t.Fatalf("解析发布单: %v", err)
	}
	if strings.TrimSpace(rel.Precheck) == "" {
		t.Fatal("发布单上没有检查结果 —— 空和「检查过、没问题」不是一回事")
	}
	var res precheckResult
	if err := json.Unmarshal([]byte(rel.Precheck), &res); err != nil {
		t.Fatalf("解析检查结果: %v", err)
	}
	return res
}

func issuesOfKind(res precheckResult, kind string) []precheckIssue {
	out := []precheckIssue{}
	for _, i := range res.Issues {
		if i.Kind == kind {
			out = append(out, i)
		}
	}
	return out
}

// 一、语法错由目标库判,并且带回它的原话。
func TestPrecheck_SyntaxErrorCarriesTheDatabaseOwnWords(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	conn := app.realPGConnIn(admin, "dev", "precheck-syntax")
	if r := app.execSQL(admin, conn, `CREATE TABLE t_order (id INTEGER, memo TEXT)`); r.Code != 0 {
		t.Fatalf("建表: %s", r.Msg)
	}

	// SELEC 是个打错的关键字 —— 平台不该自己认出它,该由数据库说。
	res := submitForPrecheck(t, app, admin, conn, `UPDATE t_order SET memo = WHERE id = 1`)

	syn := issuesOfKind(res, "syntax")
	if len(syn) == 0 {
		t.Fatalf("语法错没被报出来:%+v", res)
	}
	if syn[0].Level != "error" {
		t.Errorf("语法错应当是 error,实际 %q", syn[0].Level)
	}
	// 原话里有 PostgreSQL 自己的措辞;包装成一句"语法错误"会把位置信息丢掉。
	if !strings.Contains(strings.ToLower(syn[0].Message), "syntax") {
		t.Errorf("应当带数据库原话,实际 %q", syn[0].Message)
	}
	if res.SyntaxChecked == 0 {
		t.Error("syntaxChecked 应当大于 0 —— 这条是真检查过的")
	}
}

// 语法没问题时不许无中生有。上一条证明它会报,这一条证明它不乱报。
func TestPrecheck_ValidStatementRaisesNoSyntaxIssue(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	conn := app.realPGConnIn(admin, "dev", "precheck-clean")
	if r := app.execSQL(admin, conn, `CREATE TABLE t_order (id INTEGER, memo TEXT)`); r.Code != 0 {
		t.Fatalf("建表: %s", r.Msg)
	}

	res := submitForPrecheck(t, app, admin, conn, `UPDATE t_order SET memo = 'x' WHERE id = 1`)

	if n := len(issuesOfKind(res, "syntax")); n != 0 {
		t.Errorf("一条正确的语句不该有语法结论:%+v", res.Issues)
	}
	if res.Errors != 0 {
		t.Errorf("不该有 error:%+v", res.Issues)
	}
	if res.SyntaxChecked != 1 {
		t.Errorf("应当真检查了 1 条,实际 %d", res.SyntaxChecked)
	}
}

// 二、对象不存在要报出来,并且说清是怎么知道的。
func TestPrecheck_MissingObjectIsReportedWithItsSource(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	conn := app.realPGConnIn(admin, "dev", "precheck-object")
	if r := app.execSQL(admin, conn, `CREATE TABLE t_order (id INTEGER)`); r.Code != 0 {
		t.Fatalf("建表: %s", r.Msg)
	}

	res := submitForPrecheck(t, app, admin, conn, `UPDATE t_nowhere SET id = 1 WHERE id = 2`)

	objs := issuesOfKind(res, "object")
	if len(objs) == 0 {
		t.Fatalf("对象不存在没被报出来:%+v", res)
	}
	if objs[0].Object != "t_nowhere" {
		t.Errorf("报的对象名不对:%q", objs[0].Object)
	}
	// 这一条是本次改动的要点:结果必须说明它是缓存说的还是目标库说的。
	if objs[0].Source == "" {
		t.Error("对象结论没带来源 —— 读的人没法判断该不该信它")
	}
	if res.ObjectSource == "" || res.ObjectSource == "none" {
		t.Errorf("整份结果要说明对象检查的取数来源,实际 %q", res.ObjectSource)
	}
	// 对象都缺了就别再报一遍语法:EXPLAIN 会因为"表不存在"失败,
	// 把同一件事说成两件。
	if len(issuesOfKind(res, "syntax")) != 0 {
		t.Errorf("对象缺失时不该再报语法错:%+v", res.Issues)
	}
}

// 三、同一批里前面刚建的对象,不许被报成不存在 —— 它在提交这一刻确实还不存在。
//
// 例子是「建表 + 给它建索引」而不是「建表 + 灌数据」:这个系统**禁止 DML 与 DDL
// 同单**(审批人按类型评估风险,混装单让两种评估都失效),所以后者根本提不了单。
// 两者要验的是同一件事 —— 第二条语句引用的对象由第一条创建。
func TestPrecheck_ObjectCreatedEarlierInTheSameBatchIsNotMissing(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	conn := app.realPGConnIn(admin, "dev", "precheck-batch")

	res := submitForPrecheck(t, app, admin, conn,
		"CREATE TABLE t_fresh (id INTEGER);\nCREATE INDEX idx_fresh ON t_fresh (id);")

	for _, i := range issuesOfKind(res, "object") {
		t.Errorf("同一批里前面刚建的表被报成不存在了:%+v", i)
	}
}

// DROP 掉之后再用,才是真的错 —— 上一条不能是靠"对象检查整个没跑"过的。
func TestPrecheck_ObjectDroppedEarlierInTheSameBatchIsFlagged(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	conn := app.realPGConnIn(admin, "dev", "precheck-dropped")
	if r := app.execSQL(admin, conn, `CREATE TABLE t_gone (id INTEGER)`); r.Code != 0 {
		t.Fatalf("建表: %s", r.Msg)
	}

	// 同样两条都是 DDL,理由见上一条。
	res := submitForPrecheck(t, app, admin, conn,
		"DROP TABLE t_gone;\nCREATE INDEX idx_gone ON t_gone (id);")

	objs := issuesOfKind(res, "object")
	if len(objs) == 0 {
		t.Fatalf("先删后用没被报出来:%+v", res)
	}
	if !strings.Contains(objs[0].Message, "已被删除") {
		t.Errorf("应当说清是本次发布删的,实际 %q", objs[0].Message)
	}
}

// 四、没做的检查必须显示成没做。
func TestPrecheck_UncheckedStatementsSaySoRatherThanLookingClean(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	conn := app.realPGConnIn(admin, "dev", "precheck-ddl")

	res := submitForPrecheck(t, app, admin, conn, `CREATE TABLE t_ddl (id INTEGER)`)

	skipped := issuesOfKind(res, "skipped")
	if len(skipped) == 0 {
		t.Fatalf("DDL 没做语法检查,却没说:%+v", res)
	}
	if !strings.Contains(skipped[0].Message, "未做语法检查") {
		t.Errorf("说明里要有「未做语法检查」,实际 %q", skipped[0].Message)
	}
	if res.SyntaxSkipped == 0 {
		t.Error("syntaxSkipped 应当大于 0")
	}
	if res.SyntaxChecked != 0 {
		t.Errorf("这条 DDL 不该被算作检查过,实际 syntaxChecked=%d", res.SyntaxChecked)
	}
}

// 没有真实凭据的实例上,整份检查都做不了 —— 那也要说出来,不能交一份空结果。
// 空结果读起来就是"检查过、没问题"。
func TestPrecheck_SimulatedConnectionSaysNothingWasChecked(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	stg := app.connIDByEnv(admin, "staging") // 种子实例,没有连接凭据

	res := submitForPrecheck(t, app, admin, stg, `UPDATE tbl_order SET memo = 'x' WHERE id = 1`)

	if res.ObjectSource != "none" {
		t.Errorf("没凭据时对象来源应当是 none,实际 %q", res.ObjectSource)
	}
	if len(issuesOfKind(res, "skipped")) == 0 {
		t.Fatalf("没检查就要说没检查:%+v", res)
	}
	if res.Errors != 0 {
		t.Errorf("没检查不等于有错:%+v", res.Issues)
	}
}

// 检查不拦单 —— 这是本次的明确决定(见 service/precheck.go 顶部)。
// 一条引用了不存在的表的变更,单子照建,结果随响应带回。
func TestPrecheck_DoesNotBlockTheRelease(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	conn := app.realPGConnIn(admin, "dev", "precheck-nonblock")
	pid := app.createPipeline(admin, "不拦单流程", "", []map[string]any{
		{"name": "执行变更", "type": "execute"},
	})

	r := app.submitRelease(admin, map[string]any{
		"title": "引用了不存在的表", "pipelineId": pid, "connectionId": conn,
		"sql": `UPDATE t_nowhere SET id = 1 WHERE id = 2`, "reason": "不拦单回归",
	})
	if r.Code != 0 {
		t.Fatalf("检查不该拦住收单,却拒了: code=%d msg=%s", r.Code, r.Msg)
	}
	if r.Code == 0 && len(r.Data) == 0 {
		t.Fatal("收下了却没给单子")
	}
}

// 开放接口走的是同一个 submitRelease,所以 CI 提单也拿得到这份结果。
// 单独钉一条:这正是"返回检查结果"这个需求的主场景。
func TestPrecheck_OpenAPISubmissionCarriesTheResult(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	app.realPGConnIn(admin, "dev", "precheck-open")
	// 流程绑在凭据上 —— 外部请求不指定流程(见 ADR 0006)。
	pid := app.createPipeline(admin, "开放接口收单流程", "", []map[string]any{
		{"name": "执行变更", "type": "execute"},
	})
	token := app.issueClientPiped(admin, "CI 平台", "zhangwei@aegisdb.io", nil, pid)

	r := app.openDo(http.MethodPost, "/api/v1/open/releases", token, map[string]any{
		"title": "CI 提单", "instance": "precheck-open",
		"sql": `UPDATE t_nowhere SET id = 1 WHERE id = 2`, "reason": "开放接口回归",
	})
	if r.Code != 0 {
		t.Fatalf("开放接口提单失败: code=%d msg=%s", r.Code, r.Msg)
	}
	// 对外是一份**结构化**的结果(dto.OpenPrecheck),不是内部存的那串 JSON 文本:
	// 对接方的契约不跟着内部存储走。
	var rel struct {
		Precheck *precheckResult `json:"precheck"`
	}
	if err := json.Unmarshal(r.Data, &rel); err != nil {
		t.Fatalf("解析: %v", err)
	}
	if rel.Precheck == nil {
		t.Fatal("开放接口的响应里没有检查结果 —— 这正是这个需求的主场景")
	}
	found := false
	for _, i := range rel.Precheck.Issues {
		if i.Object == "t_nowhere" {
			found = true
		}
	}
	if !found {
		t.Errorf("结果里应当点名那个不存在的对象,实际 %+v", rel.Precheck.Issues)
	}
	// 检查不拦单:有 error 的单子照样收下。
	if rel.Precheck.Errors == 0 {
		t.Error("应当报出 error")
	}
}
