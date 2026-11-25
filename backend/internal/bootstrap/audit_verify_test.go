package bootstrap

// 审计哈希链的**校验**。
//
// 链此前只有写的那一半:每一行都算了哈希、也存了,但没有任何地方去验。那种状态下界面上
// 那句"不可篡改"是一句没人检查的话 —— 出事时才发现要现写脚本,还得先搞清楚这一行属于第
// 几代键集。
//
// 所以这一组测的不是"能不能算出哈希"(那在 service 包里逐字段测过),而是**它抓不抓得住
// 改动**:改内容、删中间一行、抹掉较新的字段按老键集重算 —— 三种手法各对应一种结论,而
// 且没动过的链必须报完好(一个动不动就喊狼来了的校验,和没有校验一样没人会去按)。

import (
	"net/http"
	"testing"

	"aegisdb/internal/model"
	"aegisdb/internal/service"
	"aegisdb/pkg/crypto"
)

// 干净的链验得过,而且把验过的行数如实说出来。
func TestVerifyAuditChain_CleanChainPasses(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	dev := app.connIDByEnv(admin, "dev")
	for _, sql := range []string{"SELECT 1", "SELECT 2", "SELECT 3"} {
		eq(t, app.do(http.MethodPost, "/api/v1/terminal/exec", admin, map[string]any{
			"connectionId": dev, "sql": sql, "reason": "校验用例",
		}).Code, 0, "执行 "+sql)
	}

	rep, err := app.svc.VerifyAuditChain(0)
	if err != nil {
		t.Fatalf("校验出错: %v", err)
	}
	if !rep.OK {
		t.Fatalf("没动过的链被报成断了:%s", rep.String())
	}
	if rep.Checked != rep.Total || rep.Total == 0 {
		t.Fatalf("验过 %d 行,表里有 %d 行 —— 全量校验要一行不落", rep.Checked, rep.Total)
	}
	// 种子数据里有 0039/0040/0041 之前的老行吗?有的话代会更低;这里的行都是新写的。
	if rep.Gen == 0 {
		t.Error("没报出用的是第几代键集")
	}
}

// 改掉一行的内容 —— 重算的哈希对不上,而且指名道姓是哪一行。
func TestVerifyAuditChain_CatchesAnEditedRow(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	dev := app.connIDByEnv(admin, "dev")
	const sql = "DELETE FROM orders WHERE id = 1"
	eq(t, app.do(http.MethodPost, "/api/v1/terminal/exec", admin, map[string]any{
		"connectionId": dev, "sql": sql, "reason": "待会儿改掉它",
	}).Code, 0, "执行")

	var row model.AuditLog
	if err := app.repo.DB().Where("command = ?", sql).Take(&row).Error; err != nil {
		t.Fatalf("找不到刚写的那一行: %v", err)
	}
	// 把一条 DELETE 改成 SELECT —— 事后最想抵赖的正是这一种。
	if err := app.repo.DB().Model(&model.AuditLog{}).Where("id = ?", row.ID).
		Update("command", "SELECT 1").Error; err != nil {
		t.Fatalf("改行失败: %v", err)
	}

	rep, err := app.svc.VerifyAuditChain(0)
	if err != nil {
		t.Fatalf("校验出错: %v", err)
	}
	if rep.OK {
		t.Fatal("一条 DELETE 被改成 SELECT,校验却说链是好的")
	}
	eq(t, rep.BadID, row.ID, "报出来的应当正是被改的那一行")
	eq(t, rep.BadReason, "content", "结论应当是「内容被改过」")
}

// 删掉中间一行 —— 后一行的前驱哈希接不上,报「链断了」而不是「内容被改」。
//
// 两者要人做的事不同:内容被改要去看那一行到底原来是什么,链断了要去找那一行去哪儿了。
func TestVerifyAuditChain_CatchesADeletedRow(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	dev := app.connIDByEnv(admin, "dev")
	for _, sql := range []string{"SELECT 'a'", "SELECT 'b'", "SELECT 'c'"} {
		eq(t, app.do(http.MethodPost, "/api/v1/terminal/exec", admin, map[string]any{
			"connectionId": dev, "sql": sql, "reason": "校验用例",
		}).Code, 0, "执行")
	}
	var mid model.AuditLog
	if err := app.repo.DB().Where("command = ?", "SELECT 'b'").Take(&mid).Error; err != nil {
		t.Fatalf("找不到中间那一行: %v", err)
	}
	if err := app.repo.DB().Delete(&model.AuditLog{}, mid.ID).Error; err != nil {
		t.Fatalf("删行失败: %v", err)
	}

	rep, err := app.svc.VerifyAuditChain(0)
	if err != nil {
		t.Fatalf("校验出错: %v", err)
	}
	if rep.OK {
		t.Fatal("中间一行被删掉了,校验却说链是好的")
	}
	eq(t, rep.BadReason, "link", "结论应当是「链断了」")
	if rep.BadID <= mid.ID {
		t.Fatalf("报出来的断点是 id=%d,应当是被删那一行(id=%d)之后的那一行", rep.BadID, mid.ID)
	}
}

// 抹掉较新的字段、按**老键集**重算 —— 这是"逐代重试"开出来的那个口子,必须被代不倒退
// 这一条堵上。
func TestVerifyAuditChain_CatchesADowngradedRow(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	dev := app.connIDByEnv(admin, "dev")
	for _, sql := range []string{"SELECT 'x'", "SELECT 'y'"} {
		eq(t, app.do(http.MethodPost, "/api/v1/terminal/exec", admin, map[string]any{
			"connectionId": dev, "sql": sql, "reason": "校验用例",
		}).Code, 0, "执行")
	}
	var row model.AuditLog
	if err := app.repo.DB().Where("command = ?", "SELECT 'x'").Take(&row).Error; err != nil {
		t.Fatalf("找不到那一行: %v", err)
	}
	// 伪造者干的事:把来源 IP 抹掉(第 5 代的字段),再按第 4 代的键集把哈希重算一遍。
	// 单看这一行,它"验得过"。
	row.ClientIP = ""
	forged := crypto.ChainHash(row.PrevHash, service.AuditPayloadGen(&row, 4))
	if err := app.repo.DB().Model(&model.AuditLog{}).Where("id = ?", row.ID).
		Updates(map[string]any{"client_ip": "", "hash": forged}).Error; err != nil {
		t.Fatalf("伪造失败: %v", err)
	}

	rep, err := app.svc.VerifyAuditChain(0)
	if err != nil {
		t.Fatalf("校验出错: %v", err)
	}
	if rep.OK {
		t.Fatal("一行被降代重算了,校验却说链是好的 —— 逐代重试的口子没堵上")
	}
	// 被动手的那一行自己能"验过"(第 4 代),但它**后面**那一行接的是旧哈希 —— 先被
	// 抓住的是链断了。降代这一条挡的是另一种情形:伪造者连后面的行一起重写。
	if rep.BadReason != "link" && rep.BadReason != "gen" {
		t.Fatalf("结论是 %q,应当是「链断了」或「降代」", rep.BadReason)
	}
}

// 只验最近 N 行:一样抓得住窗口内的改动,而且如实说出"只验了这么多"。
func TestVerifyAuditChain_LimitChecksTheRecentWindow(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	dev := app.connIDByEnv(admin, "dev")
	const last = "SELECT 'last'"
	for _, sql := range []string{"SELECT 'p'", "SELECT 'q'", last} {
		eq(t, app.do(http.MethodPost, "/api/v1/terminal/exec", admin, map[string]any{
			"connectionId": dev, "sql": sql, "reason": "校验用例",
		}).Code, 0, "执行")
	}

	rep, err := app.svc.VerifyAuditChain(2)
	if err != nil {
		t.Fatalf("校验出错: %v", err)
	}
	if !rep.OK {
		t.Fatalf("干净的窗口被报成断了:%s", rep.String())
	}
	eq(t, rep.Checked, int64(2), "只该验最近两行")
	if rep.Total <= rep.Checked {
		t.Fatalf("总数 %d 不大于验过的 %d —— 这一版种子数据本该有更多行", rep.Total, rep.Checked)
	}

	// 窗口里的那一行被改掉,照样抓得住。
	var row model.AuditLog
	if err := app.repo.DB().Where("command = ?", last).Take(&row).Error; err != nil {
		t.Fatalf("找不到最后那一行: %v", err)
	}
	if err := app.repo.DB().Model(&model.AuditLog{}).Where("id = ?", row.ID).
		Update("actor_name", "Someone Else").Error; err != nil {
		t.Fatalf("改行失败: %v", err)
	}
	rep2, err := app.svc.VerifyAuditChain(2)
	if err != nil {
		t.Fatalf("校验出错: %v", err)
	}
	if rep2.OK {
		t.Fatal("窗口内的行被改了,只验最近两行却说没事")
	}
	eq(t, rep2.BadID, row.ID, "报出来的应当正是被改的那一行")
}

// 接口这一头:审计菜单 + 监督角色才验得动,普通只读角色拿 403。
//
// 校验要扫的是**全表**(链是一条),所以它天然越过了"没有监督角色只看得见自己"那条线 ——
// 门槛因此得单独立,不能靠可见性收窄来代劳。
func TestVerifyAuditChain_EndpointIsForOverseersOnly(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	r := app.do(http.MethodGet, "/api/v1/audit/verify", admin, nil)
	eq(t, r.Code, 0, "管理员应当验得动")

	auditor := app.login("hujun@aegisdb.io", "aegisdb123") // 审计角色
	eq(t, app.do(http.MethodGet, "/api/v1/audit/verify", auditor, nil).Code, 0, "审计员应当验得动")

	ro := app.login("zhaolei@aegisdb.io", "aegisdb123") // 只读:有 audit 菜单,但不是监督角色
	eq(t, app.do(http.MethodGet, "/api/v1/audit/verify", ro, nil).Code, 40300, "只读角色应当被挡住")
}
