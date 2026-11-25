package bootstrap

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"velagateway/internal/model"
)

// 风险字典:等级、分层要合法;PATCH 不存在的命令 404;POST 只传部分分层时其余分层取 off
// (工单 02 第 5 条)。
func TestRiskCommands_Validation(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")

	eq(t, app.do(http.MethodPost, "/api/v1/risk-commands", admin, map[string]any{
		"command": "NEWCMD", "tiers": map[string]string{"prod": "bogus"},
	}).Code, 40001, "非法等级")
	eq(t, app.do(http.MethodPost, "/api/v1/risk-commands", admin, map[string]any{
		"command": "NEWCMD", "tiers": map[string]string{"nope": "high"},
	}).Code, 40001, "不存在的分层")
	eq(t, app.do(http.MethodPatch, "/api/v1/risk-commands/DROP", admin, map[string]any{
		"tier": "dev", "level": "bogus",
	}).Code, 40001, "PATCH 非法等级")
	eq(t, app.do(http.MethodPatch, "/api/v1/risk-commands/DROP", admin, map[string]any{
		"tier": "nope", "level": "high",
	}).Code, 40001, "PATCH 不存在的分层")

	r := app.do(http.MethodPatch, "/api/v1/risk-commands/NOSUCHCMD", admin, map[string]any{"tier": "dev", "level": "high"})
	eq(t, r.Code, 40400, "PATCH 不存在的命令")
	var n int64
	app.repo.DB().Model(&model.RiskCommand{}).Where("command = ?", "NOSUCHCMD").Count(&n)
	eq(t, n, int64(0), "PATCH 不存在的命令不该新建")

	// 只给 staging 定级:没传的生产层(基准层 / 要 MFA)默认 high,其余 off —— 宁可多一次审批,
	// 也不让刚被标成危险的命令在生产上免审(defaultRiskLevel)。
	eq(t, app.do(http.MethodPost, "/api/v1/risk-commands", admin, map[string]any{
		"command": "PARTIALCMD", "tiers": map[string]string{"staging": "mid"},
	}).Code, 0, "只传 staging")
	eq2(t, app.riskLevel(admin, "PARTIALCMD", "staging"), "mid", "staging 照传入")
	eq2(t, app.riskLevel(admin, "PARTIALCMD", "prod"), "high", "prod 未传 → high")
	eq2(t, app.riskLevel(admin, "PARTIALCMD", "dev"), "off", "dev 未传 → off")
}

// SQL 规范自定义规则:保存时编译正则,category/dialect 非法值拒绝;审查时一条坏规则不拖垮全部
// (工单 02 第 6 条)。
func TestReviewRules_ValidateRegexCategoryDialect(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")

	base := func(over map[string]any) map[string]any {
		b := map[string]any{"code": "bad.rule", "name": "坏规则", "level": "error", "enabled": true, "params": `{"pattern":"FOO"}`}
		for k, v := range over {
			b[k] = v
		}
		return b
	}
	eq(t, app.do(http.MethodPost, "/api/v1/sql-review/rules", admin, base(map[string]any{"params": `{"pattern":"("}`})).Code, 40001, "坏正则")
	eq(t, app.do(http.MethodPost, "/api/v1/sql-review/rules", admin, base(map[string]any{"category": "bogus"})).Code, 40001, "非法 category")
	eq(t, app.do(http.MethodPost, "/api/v1/sql-review/rules", admin, base(map[string]any{"dialect": "bogus"})).Code, 40001, "非法 dialect")
	eq(t, app.do(http.MethodPost, "/api/v1/sql-review/rules", admin, base(map[string]any{"dialect": "mysql,bogus"})).Code, 40001, "dialect 里混了非法值")

	// 合法的建出来,再改成坏正则 —— 编辑同样要挡。
	r := app.do(http.MethodPost, "/api/v1/sql-review/rules", admin, base(map[string]any{"category": "perf", "dialect": "mysql"}))
	eq(t, r.Code, 0, "合法规则")
	var row struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(r.Data, &row)
	eq(t, app.do(http.MethodPut, "/api/v1/sql-review/rules/"+itoa(row.ID), admin, map[string]any{
		"level": "error", "enabled": true, "params": `{"pattern":"[a-"}`,
	}).Code, 40001, "编辑成坏正则")
	eq(t, app.do(http.MethodPut, "/api/v1/sql-review/rules/"+itoa(row.ID), admin, map[string]any{
		"level": "error", "enabled": true, "params": `{"pattern":"FOO"}`, "category": "bogus",
	}).Code, 40001, "编辑成非法 category")

	// 库里已经躺着一条坏规则(旧版本存进来的):不让所有审查都 failed,但也不静默跳过 ——
	// 报一条警告,让人知道这条规则没生效(2026-10-26)。
	app.repo.DB().Create(&model.SQLReviewRule{
		Code: "custom.legacy.bad", Name: "历史坏规则", Dialect: "all", Category: "dml",
		Level: "error", Kind: model.ReviewKindRegex, Enabled: true, Params: `{"pattern":"("}`,
	})
	chk := app.do(http.MethodPost, "/api/v1/sql-review/check", admin, map[string]any{"dialect": "mysql", "sql": "SELECT id FROM t WHERE id = 1;"})
	eq(t, chk.Code, 0, "审查")
	var res struct {
		Result struct {
			Passed   bool `json:"passed"`
			Findings []struct {
				Code  string `json:"code"`
				Level string `json:"level"`
			} `json:"findings"`
		} `json:"result"`
	}
	_ = json.Unmarshal(chk.Data, &res)
	bad := 0
	for _, f := range res.Result.Findings {
		if f.Code == "custom.legacy.bad" {
			bad++
			eq(t, f.Level, "warn", "坏规则只报警告,不按它的 error 级别")
		}
	}
	eq(t, bad, 1, "坏规则在结果里报一条")
	eq(t, res.Result.Passed, true, "一条坏规则不该让审查 failed")
}

// 分层 PUT 只传名称时,未传的字段保持原值(工单 02 第 9 条)。
func TestEnvTierUpdate_IsPartial(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")

	var before model.EnvTier
	app.repo.DB().First(&before, "code = ?", "prod")
	if !before.RequireMFA || !before.DangerBanner || !before.ScanBaseline {
		t.Fatalf("前置条件:prod 的开关应当开着 %+v", before)
	}
	eq(t, app.do(http.MethodPut, "/api/v1/env-tiers/prod", admin, map[string]any{"displayName": "生产(改名)"}).Code, 0, "只改名")

	var after model.EnvTier
	app.repo.DB().First(&after, "code = ?", "prod")
	eq(t, after.DisplayName, "生产(改名)", "名称改了")
	eq(t, after.RequireMFA, before.RequireMFA, "requireMfa 保持")
	eq(t, after.DangerBanner, before.DangerBanner, "dangerBanner 保持")
	eq(t, after.CountsInPending, before.CountsInPending, "countsInPending 保持")
	eq(t, after.ScanBaseline, before.ScanBaseline, "scanBaseline 保持")
	eq(t, after.StrictNoWhere, before.StrictNoWhere, "strictNoWhere 保持")
	eq(t, after.ConnLayer, before.ConnLayer, "connLayer 保持")
	eq(t, after.DefaultRole, before.DefaultRole, "defaultRole 保持")
	eq(t, after.SortOrder, before.SortOrder, "sortOrder 保持")

	// 显式传空名称仍然拒绝。
	eq(t, app.do(http.MethodPut, "/api/v1/env-tiers/prod", admin, map[string]any{"displayName": ""}).Code, 40001, "空名称")
}

// 连接:新建校验与编辑一致 —— policy/engine 非法拒绝;PATCH status 只认 online/maint;
// POST 的 tags 支持、port 合并进 host(工单 02 第 10 条)。
func TestConnectionCreate_ValidationMatchesEdit(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")

	mk := func(over map[string]any) apiResp {
		b := map[string]any{"name": "v-conn", "engine": "MySQL 8.0", "host": "10.1.1.1", "env": "dev", "policy": "strict"}
		for k, v := range over {
			b[k] = v
		}
		return app.do(http.MethodPost, "/api/v1/connections", admin, b)
	}
	eq(t, mk(map[string]any{"policy": "bogus"}).Code, 40001, "非法 policy")
	eq(t, mk(map[string]any{"engine": "redis"}).Code, 40001, "非法 engine")
	eq(t, mk(map[string]any{"host": "10.1.1.1:3307", "port": 3306}).Code, 40001, "host 里的端口与 port 矛盾")
	eq(t, mk(map[string]any{"port": 70000}).Code, 40001, "port 越界")

	r := mk(map[string]any{"name": "v-conn-ok", "port": 3307, "tags": "orders, pay"})
	eq(t, r.Code, 0, "合法新建:"+r.Msg)
	var c model.Connection
	_ = json.Unmarshal(r.Data, &c)
	eq(t, c.Port, 3307, "port 合并进来")
	eq(t, c.Tags, "orders,pay", "tags 存下来")

	eq(t, app.do(http.MethodPatch, "/api/v1/connections/"+itoa(c.ID), admin, map[string]any{"status": "weird"}).Code, 40001, "PATCH 非法 status")
	eq(t, app.do(http.MethodPatch, "/api/v1/connections/"+itoa(c.ID), admin, map[string]any{"status": "maint"}).Code, 0, "PATCH maint")
	eq(t, app.do(http.MethodPatch, "/api/v1/connections/"+itoa(c.ID), admin, map[string]any{}).Code, 0, "PATCH 空 = 切换")
}

// 用户:邮箱格式、角色存在、长度上限,各自明确的错误信息;重置口令提示与规则一致(工单 02 第 11 条)。
func TestUserInputValidation(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	roID := app.roleIDByCode(admin, "ro")

	check := func(r apiResp, wantSub, what string) {
		t.Helper()
		if r.Code != 40001 {
			t.Errorf("%s:应当 40001,实际 code=%d msg=%s", what, r.Code, r.Msg)
			return
		}
		if !strings.Contains(r.Msg, wantSub) {
			t.Errorf("%s:错误信息应当说到「%s」,实际 %q", what, wantSub, r.Msg)
		}
	}

	check(app.do(http.MethodPost, "/api/v1/users/invite", admin, map[string]any{"email": "not-an-email", "roleId": roID}), "邮箱", "invite 非法邮箱")
	check(app.do(http.MethodPost, "/api/v1/users/invite", admin, map[string]any{"email": "a b@vela.io", "roleId": roID}), "邮箱", "invite 邮箱带空格")
	check(app.do(http.MethodPost, "/api/v1/users/invite", admin, map[string]any{"email": "ghost@vela.io", "roleId": 999999}), "角色", "invite 不存在的角色")
	check(app.do(http.MethodPost, "/api/v1/users/invite", admin, map[string]any{"email": strings.Repeat("a", 250) + "@vela.io", "roleId": roID}), "邮箱过长", "invite 邮箱过长")
	check(app.do(http.MethodPost, "/api/v1/users/invite", admin, map[string]any{"email": "chenhao@vela.io", "roleId": roID}), "已存在", "invite 重复邮箱")

	mk := func(over map[string]any) apiResp {
		b := map[string]any{"name": "新人", "email": "newbie@vela.io", "password": "Str0ng-Passw0rd!", "roleIds": []int64{roID}}
		for k, v := range over {
			b[k] = v
		}
		return app.do(http.MethodPost, "/api/v1/users", admin, b)
	}
	check(mk(map[string]any{"email": "bad@"}), "邮箱", "create 非法邮箱")
	check(mk(map[string]any{"name": strings.Repeat("名", 65)}), "64", "create 姓名过长")
	check(mk(map[string]any{"password": "short"}), "8 位", "create 口令过短")
	// M7(2026-10 安全审计):只要求 8 位时 12345678 / password 都能过
	check(mk(map[string]any{"password": "12345678"}), "过于常见", "create 常见弱口令")
	check(mk(map[string]any{"password": "Password"}), "过于常见", "create 常见弱口令(大小写不敏感)")
	check(mk(map[string]any{"password": "aaaaaaaaaa"}), "过于常见", "create 单一字符重复")
	check(mk(map[string]any{"roleIds": []int64{999999}}), "角色", "create 不存在的角色")
	check(mk(map[string]any{"email": "chenhao@vela.io"}), "已存在", "create 重复邮箱")

	u, _ := app.repo.GetUserByEmail("chenhao@vela.io")
	check(app.do(http.MethodPost, "/api/v1/users/"+itoa(u.ID)+"/password", admin, map[string]any{"password": "1234567"}), "8 位", "重置口令过短")
	check(app.do(http.MethodPost, "/api/v1/users/"+itoa(u.ID)+"/password", admin, map[string]any{"password": "qwerty123"}), "过于常见", "重置为常见弱口令")
}

// 流水线:恰好一个 execute 阶段、tierCode 必须存在、名称不可重复(工单 02 第 12 条)。
func TestPipelineValidation(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")

	save := func(name, tier string, stages []map[string]any) apiResp {
		return app.do(http.MethodPost, "/api/v1/pipelines", admin, map[string]any{
			"name": name, "tierCode": tier, "enabled": true, "stages": stages,
		})
	}
	exec := map[string]any{"type": "execute"}
	review := map[string]any{"type": "review", "config": `{"failOn":"none"}`}

	eq(t, save("没有执行", "", []map[string]any{review}).Code, 40001, "没有 execute 阶段")
	eq(t, save("两个执行", "", []map[string]any{exec, exec}).Code, 40001, "两个 execute 阶段")
	eq(t, save("错分层", "nope", []map[string]any{exec}).Code, 40001, "不存在的分层")
	eq(t, save("标准发布流程", "", []map[string]any{exec}).Code, 40001, "与种子流程重名")

	r := save("唯一流程", "dev", []map[string]any{review, exec})
	eq(t, r.Code, 0, "合法流程:"+r.Msg)
	var p struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(r.Data, &p)
	eq(t, save("唯一流程", "", []map[string]any{exec}).Code, 40001, "重名")
	// 改自己不算重名。
	eq(t, app.do(http.MethodPut, "/api/v1/pipelines/"+itoa(p.ID), admin, map[string]any{
		"name": "唯一流程", "tierCode": "dev", "enabled": true, "stages": []map[string]any{exec},
	}).Code, 0, "保存自己")
}

// 不存在的角色返回 40400,也不写审计(工单 02 第 13 条)。
func TestNonexistentRole_Is404(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")

	eq(t, app.do(http.MethodPut, "/api/v1/roles/999999/capabilities", admin, map[string]any{
		"matrix": map[string]any{"ddl": map[string]string{"prod": "allow"}},
	}).Code, 40400, "改不存在角色的能力")
	eq(t, app.do(http.MethodPut, "/api/v1/roles/999999/tags", admin, map[string]any{"tags": []string{"orders"}}).Code, 40400, "改不存在角色的标签")
	eq(t, app.do(http.MethodDelete, "/api/v1/roles/999999/members/999999", admin, nil).Code, 40400, "移除不存在角色的成员")
	roID := app.roleIDByCode(admin, "ro")
	eq(t, app.do(http.MethodDelete, "/api/v1/roles/"+itoa(roID)+"/members/999999", admin, nil).Code, 40400, "移除不存在的用户")

	for _, r := range app.auditCommandsLike("role=999999") {
		t.Errorf("不存在的角色不该写审计:%s", r)
	}
}

// 项目名:大小写不敏感去重,长度上限 64(工单 02 第 14 条)。
func TestProjectName_CaseInsensitiveUniqueAndBounded(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")

	r := app.do(http.MethodPost, "/api/v1/projects", admin, map[string]any{"name": "Alpha"})
	eq(t, r.Code, 0, "建 Alpha")
	eq(t, app.do(http.MethodPost, "/api/v1/projects", admin, map[string]any{"name": "alpha"}).Code, 40001, "alpha 与 Alpha 重名")
	eq(t, app.do(http.MethodPost, "/api/v1/projects", admin, map[string]any{"name": strings.Repeat("项", 65)}).Code, 40001, "65 字符")
	eq(t, app.do(http.MethodPost, "/api/v1/projects", admin, map[string]any{"name": strings.Repeat("项", 64)}).Code, 0, "64 字符可以")

	var p struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(r.Data, &p)
	r2 := app.do(http.MethodPost, "/api/v1/projects", admin, map[string]any{"name": "Beta"})
	var p2 struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(r2.Data, &p2)
	eq(t, app.do(http.MethodPatch, "/api/v1/projects/"+itoa(p2.ID), admin, map[string]any{"name": "ALPHA"}).Code, 40001, "改名撞大小写")
	eq(t, app.do(http.MethodPatch, "/api/v1/projects/"+itoa(p.ID), admin, map[string]any{"name": "ALPHA"}).Code, 0, "改自己的大小写可以")
	eq(t, app.do(http.MethodPatch, "/api/v1/projects/"+itoa(p.ID), admin, map[string]any{"name": strings.Repeat("项", 65)}).Code, 40001, "改名过长")
}
