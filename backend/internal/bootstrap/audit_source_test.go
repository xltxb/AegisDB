package bootstrap

// 审计行的**入口来源**(迁移 0063)。
//
// 起因:同样是一条 SELECT,有的来自数据查询页、有的来自 Web 命令行,审计表上完全看不出
// 区别。来源由服务端定 —— 请求进来时按路由盖(middleware.BindRequestUser),入口与路由
// 无关的(后台导出任务、发布阶段、审批决定、登录)由服务层显式写 —— 并进哈希链(第六代)。
//
// 这组按**入口**逐个钉:每个入口写出来的行来源对不对;旧行(来源为空)照样能验链、能单独
// 筛出来;筛选白名单挡掉拼错的取值。新加一个入口忘了标来源,它写出来的是空串,而空串在
// 这一列是合法值(旧行就是空)—— 不逐个钉的话没有任何地方会报错。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"aegisdb/internal/model"
	"aegisdb/internal/service"
	"aegisdb/pkg/crypto"
)

// latestAuditWhere 取最近一条满足条件的审计行。
func (a *testApp) latestAuditWhere(t *testing.T, what, query string, args ...any) model.AuditLog {
	t.Helper()
	var row model.AuditLog
	if err := a.repo.DB().Where(query, args...).Order("id desc").Take(&row).Error; err != nil {
		t.Fatalf("%s:没找到审计行(%s): %v", what, query, err)
	}
	return row
}

func (a *testApp) execOK(t *testing.T, token string, conn int64, sql string) {
	t.Helper()
	r := a.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
		"connectionId": conn, "sql": sql, "reason": "来源用例",
	})
	if r.Code != 0 {
		t.Fatalf("终端执行 %q: code=%d msg=%s", sql, r.Code, r.Msg)
	}
}

// 起因那一幕:同一条 SELECT,从数据查询页来和从终端来,来源不同。
func TestAuditSource_QueryVersusTerminal(t *testing.T) {
	b := newBizEnv(t, 0)
	const q = "SELECT id FROM orders WHERE id = 7"
	r, out := b.exec(q)
	if r.Code != 0 || out.Error != "" {
		t.Fatalf("数据查询: code=%d msg=%s err=%s", r.Code, r.Msg, out.Error)
	}
	// 数据查询写进审计的命令可能带标注,按内容找。
	row := b.app.latestAuditWhere(t, "数据查询", "actor_id = ? AND command LIKE ?", b.uid, "%WHERE id = 7%")
	eq(t, row.Source, model.AuditSourceQuery, "数据查询页的行")

	// 终端的两条路:REST 与 WS。
	dev := b.app.connIDByEnv(b.admin, "dev")
	const rest = "SELECT 'src-rest'"
	b.app.execOK(t, b.admin, dev, rest)
	eq(t, b.app.latestAuditWhere(t, "终端 REST", "command = ?", rest).Source, model.AuditSourceTerminal, "终端 REST")

	const ws = "SELECT 'src-ws'"
	conn := b.app.dialWS(b.admin)
	typ := wsExecType(t, conn, dev, ws)
	conn.Close()
	eq(t, typ, "output", "WS 执行")
	eq(t, b.app.latestAuditWhere(t, "终端 WS", "command = ?", ws).Source, model.AuditSourceTerminal, "终端 WS")
}

// 数据查询页的「导出」归数据导出(口径:按来源筛一次就该看到全部导出)。
func TestAuditSource_BizQueryExportIsExport(t *testing.T) {
	b := newBizEnv(t, 0)
	// 两道开关:全局导出开关 + 授权里批了导出(见 TestBizExport_TwoSwitchesAndMaskedCSV)。
	eq(t, b.app.do(http.MethodPut, "/api/v1/settings", b.admin, map[string]any{"bizQuery.exportEnabled": true}).Code, 0, "打开导出开关")
	g := b.body("biz@corp.io", "OA-SRC")
	g["allowExport"] = true
	grantOf(t, b.put(g))
	if _, ct, body := b.export("SELECT id FROM orders WHERE id < 3"); !strings.HasPrefix(ct, "text/csv") {
		t.Fatalf("查询页导出没拿到 CSV: %s %s", ct, body)
	}
	// 查询页导出的命令是原 SQL(不带 EXPORT 前缀),结果记 exported。
	row := b.app.latestAuditWhere(t, "查询页导出", "actor_id = ? AND result = ?", b.uid, model.ResultExported)
	eq(t, row.Source, model.AuditSourceExport, "查询页导出")
}

// 终端发起的数据导出:入队那一行(请求里)与后台执行完那一行(没有请求)都是 export。
func TestAuditSource_ExportJobBothRows(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	conn := app.exportSetup(admin)
	const sql = "SELECT 'src-export'"
	eq(t, app.do(http.MethodPost, "/api/v1/export", admin, map[string]any{
		"connectionId": conn, "sql": sql, "name": "src",
	}).Code, 0, "提交导出")
	deadline := time.Now().Add(15 * time.Second)
	for {
		var rows []model.AuditLog
		_ = app.repo.DB().Where("command = ?", "EXPORT "+sql).Find(&rows).Error
		done := false
		for _, r := range rows {
			if r.Result == model.ResultExecuted {
				done = true
			}
		}
		if done || time.Now().After(deadline) {
			if !done {
				t.Fatalf("导出任务 15 秒内没跑完(已有 %d 行审计)", len(rows))
			}
			if len(rows) < 2 {
				t.Fatalf("导出应当留下入队与执行两行,实际 %d 行", len(rows))
			}
			for _, r := range rows {
				eq(t, r.Source, model.AuditSourceExport, "导出行("+r.Result+")")
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// 上传脚本 / 整脚本执行:script。
func TestAuditSource_ScriptExecute(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	dev := app.connIDByEnv(admin, "dev")
	eq(t, app.do(http.MethodPut, "/api/v1/settings", admin, map[string]any{"script.savePath": t.TempDir()}).Code, 0, "脚本目录")
	r := app.do(http.MethodPost, "/api/v1/scripts/execute", admin, map[string]any{
		"content": "SELECT 'src-script';\n", "filename": "src.sql", "connectionId": dev,
	})
	if r.Code != 0 {
		t.Fatalf("安全脚本执行: code=%d msg=%s", r.Code, r.Msg)
	}
	row := app.latestAuditWhere(t, "脚本执行", "command LIKE ?", "%src-script%")
	eq(t, row.Source, model.AuditSourceScript, "脚本执行")
}

// 审批单:拦截那一行是发起的入口(终端),审批决定与执行审批单是 approval。
func TestAuditSource_ApprovalFlow(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	dev, conn := app.sameDBConns(admin, "src-ap")
	if r := app.execSQL(admin, dev, `CREATE TABLE t_src_ap (id INTEGER PRIMARY KEY)`); r.Code != 0 {
		t.Fatalf("建表: %s", r.Msg)
	}
	const sql = `DROP TABLE t_src_ap`
	id := app.approvedOn(admin, conn, sql)
	if r := app.do(http.MethodPost, "/api/v1/approvals/"+itoa(id)+"/execute", admin, nil); r.Code != 0 {
		t.Fatalf("执行审批单: code=%d msg=%s", r.Code, r.Msg)
	}
	var rows []model.AuditLog
	_ = app.repo.DB().Where("command = ?", sql).Order("id").Find(&rows).Error
	want := map[string]string{
		model.AuditEventIntercept: model.AuditSourceTerminal,
		model.AuditEventApprove:   model.AuditSourceApproval,
		model.AuditEventExec:      model.AuditSourceApproval,
	}
	seen := map[string]bool{}
	for _, r := range rows {
		w, ok := want[r.EventType]
		if !ok {
			continue
		}
		seen[r.EventType] = true
		eq(t, r.Source, w, "审批流程的 "+r.EventType+" 行")
	}
	for ev := range want {
		if !seen[ev] {
			t.Errorf("审批流程里没有 %s 行", ev)
		}
	}
}

// 外部系统经开放接口提交的发布单记 release(口径:发布单的每一行都是变更发布,
// 「API:系统名」由 operator 列说明);发布流水线以外的开放接口调用记 api。
func TestAuditSource_OpenAPIReleaseIsRelease_ReviewIsAPI(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	pid := app.createPipeline(admin, "来源用例流程", "", []map[string]any{
		{"name": "规范审查", "type": "review", "config": `{"failOn":"error"}`},
		{"name": "执行变更", "type": "execute"},
	})
	token := app.issueClientPiped(admin, "来源用例", "zhangwei@aegisdb.io", nil, pid)
	r := app.openDo(http.MethodPost, "/api/v1/open/releases", token, map[string]any{
		"title": "来源用例", "externalRef": "SRC-1", "instance": "sandbox-dev",
		"sql": "CREATE TABLE tbl_src_probe (\n id BIGINT NOT NULL AUTO_INCREMENT COMMENT 'id',\n" +
			" PRIMARY KEY (id)\n) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='probe';",
		"reason": "来源用例",
	})
	if r.Code != 0 {
		t.Fatalf("开放接口建单: code=%d msg=%s", r.Code, r.Msg)
	}
	var created struct {
		RelNo string `json:"relNo"`
	}
	_ = json.Unmarshal(r.Data, &created)
	var rows []model.AuditLog
	_ = app.repo.DB().Where("approval_no = ?", created.RelNo).Find(&rows).Error
	if len(rows) == 0 {
		t.Fatalf("发布单 %s 没留下审计行", created.RelNo)
	}
	for _, row := range rows {
		eq(t, row.Source, model.AuditSourceRelease, "开放接口发布单的行 "+clipStr(row.Command, 40))
	}

	review := app.issueClient(admin, "来源审查", "zhangwei@aegisdb.io", []string{model.ScopeReviewCheck})
	eq(t, app.openDo(http.MethodPost, "/api/v1/open/sql-review", review, map[string]any{
		"dialect": "mysql", "sql": "CREATE TABLE t (id INT);",
	}).Code, 0, "开放接口 SQL 审查")
	rv := app.latestAuditWhere(t, "SQL 审查", "command LIKE ?", "open.sqlreview%")
	eq(t, rv.Source, model.AuditSourceAPI, "开放接口 SQL 审查")
}

// 登录(成功与失败)记 auth;管理后台的动作记 admin。
func TestAuditSource_LoginAndAdmin(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	_ = app.do(http.MethodPost, "/api/v1/auth/login", "", map[string]any{"email": "linwei@aegisdb.io", "password": "wrong-pass"})

	var rows []model.AuditLog
	_ = app.repo.DB().Where("event_type = ? AND entry_source <> ''", model.AuditEventLogin).Find(&rows).Error
	var ok, fail bool
	for _, r := range rows {
		eq(t, r.Source, model.AuditSourceAuth, "登录行 "+r.Command)
		if r.Result == model.ResultRejected {
			fail = true
		} else {
			ok = true
		}
	}
	if !ok || !fail {
		t.Fatalf("登录成功 / 失败的行没有都带上 auth(成功=%v 失败=%v)", ok, fail)
	}

	eq(t, app.do(http.MethodPost, "/api/v1/projects", admin, map[string]any{"name": "来源用例项目"}).Code, 0, "建项目")
	row := app.latestAuditWhere(t, "建项目", "command LIKE ?", "admin.project.create%")
	eq(t, row.Source, model.AuditSourceAdmin, "管理操作")
}

// 接口:返回 source,按来源筛(含 unknown 旧行),拼错的取值报错。
func TestAuditSource_ListFilter(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	dev := app.connIDByEnv(admin, "dev")
	const sql = "SELECT 'src-filter'"
	app.execOK(t, admin, dev, sql)
	// 造一行旧记录:来源列为空(0063 之前写下的行)。直接改库,只为了筛选,不验链。
	const legacy = "SELECT 'src-legacy'"
	app.execOK(t, admin, dev, legacy)
	if err := app.repo.DB().Model(&model.AuditLog{}).Where("command = ?", legacy).Update("entry_source", "").Error; err != nil {
		t.Fatal(err)
	}

	row := rowWithCommand(app.auditQ(admin, nil).Items, sql)
	if row == nil || row.Source != model.AuditSourceTerminal {
		t.Fatalf("列表里这一行没带来源 terminal: %+v", row)
	}
	if lg := rowWithCommand(app.auditQ(admin, nil).Items, legacy); lg == nil || lg.Source != "" {
		t.Fatalf("旧行在列表里应当是空来源: %+v", lg)
	}

	term := app.auditQ(admin, map[string]string{"source": "terminal"}).Items
	if rowWithCommand(term, sql) == nil {
		t.Error("source=terminal 筛不到终端那一行")
	}
	if rowWithCommand(term, legacy) != nil || rowWithCommand(term, "login") != nil {
		t.Error("source=terminal 混进了别的来源")
	}
	if rowWithCommand(app.auditQ(admin, map[string]string{"source": "query"}).Items, sql) != nil {
		t.Error("source=query 筛出了终端的行")
	}
	unk := app.auditQ(admin, map[string]string{"source": "unknown"}).Items
	if rowWithCommand(unk, legacy) == nil || rowWithCommand(unk, sql) != nil {
		t.Error("source=unknown 应当只筛出来源为空的旧行")
	}
	multi := app.auditQ(admin, map[string]string{"source": "terminal,auth"}).Items
	if rowWithCommand(multi, sql) == nil || rowWithCommand(multi, "login") == nil {
		t.Error("多选 terminal,auth 应当两类都有")
	}
	if r := app.do(http.MethodGet, "/api/v1/audit?source=bizquery", admin, nil); r.Code == 0 {
		t.Error("拼错的来源应当报错,而不是返回空列表")
	}
}

// 哈希链:模拟一次升级 —— 表里先躺着 0063 之前按老键集签的旧行(第一代、第五代,来源为
// 空),升级后新写的行接在它们后面按第六代签。整条链要验得过;新行改了来源要破链。
func TestAuditSource_ChainCoversSourceAndOldRowsStillVerify(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	dev := app.connIDByEnv(admin, "dev")
	_, cur := service.AuditGenRange()
	eq(t, cur, 6, "当前载荷代")

	// 清空后按老键集造两行旧记录(升级前的表)。
	if err := app.repo.DB().Where("1 = 1").Delete(&model.AuditLog{}).Error; err != nil {
		t.Fatal(err)
	}
	prev := ""
	for i, gen := range []int{1, 5} {
		old := model.AuditLog{
			OccurredAt: time.Now().Add(time.Duration(i-5) * time.Minute).Truncate(time.Second),
			ActorID:    1, ActorName: "legacy", Command: "SELECT 'pre-0063'",
			Risk: model.RiskLow, Result: model.ResultExecuted, PrevHash: prev,
		}
		if gen >= 5 {
			old.EventType, old.DataAction, old.ClientIP = model.AuditEventExec, "read", "10.0.0.1"
		}
		old.Hash = crypto.ChainHash(old.PrevHash, service.AuditPayloadGen(&old, gen))
		if err := app.repo.DB().Create(&old).Error; err != nil {
			t.Fatal(err)
		}
		prev = old.Hash
	}

	// 升级后:新行接在旧行后面。
	app.execOK(t, admin, dev, "SELECT 'c1'")
	app.execOK(t, admin, dev, "SELECT 'c2'")

	row := app.latestAuditWhere(t, "c1", "command = ?", "SELECT 'c1'")
	eq(t, row.Source, model.AuditSourceTerminal, "新行来源")
	if crypto.ChainHash(row.PrevHash, service.AuditPayloadGen(&row, 6)) != row.Hash {
		t.Fatal("新行不是按第六代签的")
	}
	if crypto.ChainHash(row.PrevHash, service.AuditPayloadGen(&row, 5)) == row.Hash {
		t.Fatal("新行按第五代也算得对 —— source 没进载荷")
	}

	rep, err := app.svc.VerifyAuditChain(0)
	if err != nil || !rep.OK {
		t.Fatalf("旧行 + 新行的链验不过: err=%v rep=%s", err, rep.String())
	}
	if rep.Checked < 4 {
		t.Fatalf("只验了 %d 行,旧行没被验到", rep.Checked)
	}
	eq(t, rep.Gen, 6, "链尾的代")

	// 改掉新行的来源 → 破链,指名是这一行。
	if err := app.repo.DB().Model(&model.AuditLog{}).Where("id = ?", row.ID).Update("entry_source", "query").Error; err != nil {
		t.Fatal(err)
	}
	rep, err = app.svc.VerifyAuditChain(0)
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK || rep.BadID != row.ID {
		t.Fatalf("改了来源链却没断在这一行: %s", rep.String())
	}
}
