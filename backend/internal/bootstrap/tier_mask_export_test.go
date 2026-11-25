package bootstrap

// 2026-10-29:敏感字段脱敏、数据查询导出限制改为按分层开关(迁移 0056)。默认 prod / staging / gli
// 开,dev / uat 关。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"aegisdb/internal/model"
)

func tierOfCode(t *testing.T, app *testApp, code string) model.EnvTier {
	t.Helper()
	var tier model.EnvTier
	if err := app.repo.DB().First(&tier, "code = ?", code).Error; err != nil {
		t.Fatal(err)
	}
	return tier
}

func TestTierSwitches_DefaultsAndBackfillRunsOnce(t *testing.T) {
	app := newTestApp(t)
	for code, want := range map[string]bool{"prod": true, "staging": true, "gli": true, "dev": false, "uat": false} {
		tier := tierOfCode(t, app, code)
		if tier.MaskSensitive != want || tier.BizExportRestrict != want {
			t.Errorf("%s:默认 脱敏=%v 导出限制=%v,应都是 %v", code, tier.MaskSensitive, tier.BizExportRestrict, want)
		}
	}
	// 运维把 uat 的脱敏打开之后,回填再跑一次(重启)不能把它改回去
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	app.setTierSwitch(admin, "uat", "maskSensitive", true)
	if err := backfillTierMaskExport(app.repo.DB()); err != nil {
		t.Fatal(err)
	}
	if !tierOfCode(t, app, "uat").MaskSensitive {
		t.Error("回填只能跑一次:重跑把运维打开的 uat 脱敏又关掉了")
	}
	// 局部更新:只改一个开关,另一个不动
	if tierOfCode(t, app, "uat").BizExportRestrict {
		t.Error("只改了脱敏,导出限制不该跟着变")
	}
}

func TestTierSwitches_NewTierInheritsTemplateUnlessGiven(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	create := func(code, tmpl string, extra map[string]any) {
		body := map[string]any{"code": code, "displayName": code, "templateCode": tmpl, "strictNoWhere": true}
		for k, v := range extra {
			body[k] = v
		}
		if r := app.do(http.MethodPost, "/api/v1/env-tiers", admin, body); r.Code != 0 {
			t.Fatalf("建分层 %s: %d %s", code, r.Code, r.Msg)
		}
	}
	create("prod-hk", "prod", nil)
	create("dev-2", "dev", nil)
	create("dev-3", "dev", map[string]any{"maskSensitive": true})
	for code, want := range map[string][2]bool{"prod-hk": {true, true}, "dev-2": {false, false}, "dev-3": {true, false}} {
		tier := tierOfCode(t, app, code)
		if tier.MaskSensitive != want[0] || tier.BizExportRestrict != want[1] {
			t.Errorf("%s:脱敏=%v 导出限制=%v,应为 %v", code, tier.MaskSensitive, tier.BizExportRestrict, want)
		}
	}
}

// 同一个库文件挂在 dev 与 prod 上:同一条查询,prod 打码、dev 原值;dev 打开开关后也打码。
func TestTierSwitches_TerminalMaskingFollowsTier(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	dev, prod := app.sameFileConns(admin, "tier-mask")
	const plaintext = "110101199003071234"
	for _, sql := range []string{
		`CREATE TABLE t_person (id INTEGER PRIMARY KEY, name TEXT, id_card TEXT)`,
		`INSERT INTO t_person (name, id_card) VALUES ('张三', '` + plaintext + `')`,
	} {
		if r := app.execSQL(admin, dev, sql); r.Code != 0 {
			t.Fatalf("准备数据(%s): %s", sql, r.Msg)
		}
	}
	app.addSensitiveColumn(admin, "t_person", "id_card", "partial")
	q := `SELECT name, id_card FROM t_person`

	if body := string(app.execSQL(admin, prod, q).Data); strings.Contains(body, plaintext) || !strings.Contains(body, "张三") {
		t.Fatalf("prod 上应打码:\n%s", body)
	}
	if body := string(app.execSQL(admin, dev, q).Data); !strings.Contains(body, plaintext) {
		t.Fatalf("dev 默认不脱敏,应看到原值:\n%s", body)
	}
	app.setTierSwitch(admin, "dev", "maskSensitive", true)
	if body := string(app.execSQL(admin, dev, q).Data); strings.Contains(body, plaintext) {
		t.Fatalf("dev 打开脱敏后应打码:\n%s", body)
	}
}

// 导出页:脱敏关掉的分层上,「原值导出」与「结构可能绕过脱敏」都不用审批,直接进队列。
func TestTierSwitches_ExportNeedsNoSensitiveReviewWhereMaskingIsOff(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	eq(t, app.do(http.MethodPut, "/api/v1/settings", admin, map[string]any{"export.savePath": t.TempDir()}).Code, 0, "配置导出路径")
	conn := app.connIDByEnv(admin, "dev")
	app.addSensitiveColumn(admin, "*", "id_card", "partial")

	eq(t, app.do(http.MethodPost, "/api/v1/export", admin, map[string]any{
		"connectionId": conn, "sql": "SELECT 1", "name": "raw", "includeSensitive": true,
	}).Code, 0, "dev 上勾原值导出")
	job := app.lastExportJob(conn)
	if job.Status == model.ExportAwaiting || job.ApprovalID != 0 || job.IncludeSensitive {
		t.Errorf("dev 不脱敏,原值导出不该等审批,也不该记原值标记:%+v", job)
	}
	eq(t, app.do(http.MethodPost, "/api/v1/export", admin, map[string]any{
		"connectionId": conn, "sql": nestedSQL, "name": "nested",
	}).Code, 0, "dev 上提交改名嵌套的导出")
	if job := app.lastExportJob(conn); job.Status == model.ExportAwaiting || job.ApprovalID != 0 {
		t.Errorf("dev 不脱敏,结构绕过脱敏的导出也不该等审批:%+v", job)
	}

	// prod 照旧要审批
	prod := app.connIDByEnv(admin, "prod")
	eq(t, app.do(http.MethodPost, "/api/v1/export", admin, map[string]any{
		"connectionId": prod, "sql": "SELECT 1", "name": "raw", "includeSensitive": true,
	}).Code, 0, "prod 上勾原值导出")
	if job := app.lastExportJob(prod); job.Status != model.ExportAwaiting || job.ApprovalID == 0 {
		t.Errorf("prod 上原值导出仍要审批,实际 %+v", job)
	}
}

// 数据查询:dev 上的查询源不受导出限制(全局开关关着、授权没勾允许导出也能导),改名也不拦、
// 结果不打码;prod 上照旧。
func TestTierSwitches_BizQueryFollowsTier(t *testing.T) {
	sensitive := func(b *bizEnv) {
		b.app.addSensitiveColumn(b.admin, "notes", "t", "full")
	}
	grants := func(b *bizEnv) bool {
		var mine struct {
			Grants []struct {
				CanExport bool `json:"canExport"`
			} `json:"grants"`
		}
		_ = json.Unmarshal(b.app.do(http.MethodGet, "/api/v1/bizquery/grants", b.user, nil).Data, &mine)
		if len(mine.Grants) != 1 {
			t.Fatalf("应有一条授权,实际 %+v", mine)
		}
		return mine.Grants[0].CanExport
	}
	evasion := "SELECT x FROM (SELECT t AS x FROM notes) s ORDER BY x"

	dev := newBizEnvIn(t, 0, "dev")
	sensitive(dev)
	if !grants(dev) {
		t.Error("dev 查询源不受导出限制,canExport 应为真")
	}
	if status, ctype, body := dev.export("SELECT id, t FROM notes"); status != 200 || !strings.Contains(ctype, "csv") || !strings.Contains(string(body), "plain") {
		t.Errorf("dev 上应能直接导出原值,实际 %d %s %s", status, ctype, body)
	}
	if r, out := dev.exec(evasion); r.Code != 0 || len(out.Rows) != 2 || out.Rows[1][0] != "plain" {
		t.Errorf("dev 不脱敏:改名不拦、原值返回,实际 code=%d msg=%q %+v", r.Code, r.Msg, out)
	}

	prod := newBizEnvIn(t, 0, "prod")
	sensitive(prod)
	if grants(prod) {
		t.Error("prod 上全局导出开关关着,canExport 应为假")
	}
	if _, _, body := prod.export("SELECT id, t FROM notes"); func() bool { code, _ := envelopeMsg(body); return code != 40300 }() {
		t.Errorf("prod 上全局导出开关关着,导出应以 40300 拒绝,实际 %s", body)
	}
	if r, _ := prod.exec(evasion); r.Code != 40300 {
		t.Errorf("prod 上改名绕过脱敏应被拒,实际 %d %s", r.Code, r.Msg)
	}
	if _, out := prod.exec("SELECT t FROM notes WHERE id = 2"); len(out.Rows) != 1 || out.Rows[0][0] == "plain" {
		t.Errorf("prod 上敏感字段应打码,实际 %+v", out)
	}
}
