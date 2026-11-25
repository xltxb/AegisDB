package bootstrap

// 已有凭据改 IP 白名单(feat/apiclient-ip-edit)。
//
// 控制台在每把凭据那一行加了"编辑白名单",走的是既有的 PUT /api-clients/:id
// 只带 {allowIps}。这里钉住它的端到端效果:改完立即按新名单放行/拒绝、非管理员改不了、
// 非法输入整单拒绝且不改原值、重复项去重、审计记下改前和改后。

import (
	"net/http"
	"strings"
	"testing"
)

func TestAPIClientAllowlistEditTakesEffectImmediately(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	_, id, tok := app.issueSA(admin, "ipedit-bot", "IP-Edit", []string{"owner"}, []string{"release:read"}, 0)

	// 新建时没填白名单 = 不限来源。
	eq(t, app.getWithIP("/api/v1/open/instances", tok, "198.51.100.7").Code, 0, "空白名单任意来源可调")

	r := app.do(http.MethodPut, "/api/v1/api-clients/"+itoa(id), admin, map[string]any{"allowIps": "10.30.0.0/16, 203.0.113.9"})
	eq(t, r.Code, 0, "改白名单")
	eq(t, app.apiClientByName(admin, "IP-Edit").AllowIPs, "10.30.0.0/16,203.0.113.9", "规范化后的白名单落库")

	// 立即生效:鉴权每次都查库,不需要等任何缓存过期。
	eq(t, app.getWithIP("/api/v1/open/instances", tok, "10.30.4.5").Code, 0, "网段内的来源放行")
	eq(t, app.getWithIP("/api/v1/open/instances", tok, "203.0.113.9").Code, 0, "单个 IP 放行")
	eq(t, app.getWithIP("/api/v1/open/instances", tok, "198.51.100.7").Code, 40301, "白名单外的来源被拒")

	// 再改一次:旧名单里的来源马上失效。
	r = app.do(http.MethodPut, "/api/v1/api-clients/"+itoa(id), admin, map[string]any{"allowIps": "198.51.100.7"})
	eq(t, r.Code, 0, "再改白名单")
	eq(t, app.getWithIP("/api/v1/open/instances", tok, "10.30.4.5").Code, 40301, "旧网段立刻被拒")
	eq(t, app.getWithIP("/api/v1/open/instances", tok, "198.51.100.7").Code, 0, "新来源立刻放行")

	// 清空 = 回到不限来源(控制台会先二次确认;接口语义保持不变)。
	eq(t, app.do(http.MethodPut, "/api/v1/api-clients/"+itoa(id), admin, map[string]any{"allowIps": ""}).Code, 0, "清空白名单")
	eq(t, app.getWithIP("/api/v1/open/instances", tok, "10.30.4.5").Code, 0, "清空后任意来源可调")

	got := app.apiClientByName(admin, "IP-Edit")
	eq(t, got.Enabled, true, "改白名单不动启停")
	eq(t, got.Scopes, "release:read", "改白名单不动范围")
}

func TestAPIClientAllowlistEditAuditsBeforeAndAfter(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	sa := app.createServiceAccount(admin, "ipaudit-bot", []int64{app.roleIDByCode(admin, "owner")}, nil)
	r := app.do(http.MethodPost, "/api/v1/api-clients", admin, map[string]any{
		"name": "IP-Audit", "userId": sa.ID, "allowIps": "10.1.0.0/16", "scopes": []string{"release:read"},
	})
	eq(t, r.Code, 0, "create")
	id := app.apiClientByName(admin, "IP-Audit").ID

	eq(t, app.do(http.MethodPut, "/api/v1/api-clients/"+itoa(id), admin, map[string]any{"allowIps": "10.2.0.0/16"}).Code, 0, "edit")
	rows := app.mustAudit("admin.apiclient.update", "改白名单写审计")
	last := rows[len(rows)-1]
	if !strings.Contains(last, "allow_ips=10.2.0.0/16") || !strings.Contains(last, "allow_ips_before=10.1.0.0/16") {
		t.Errorf("审计应同时记改前和改后,got %q", last)
	}

	// 清空也要能从审计看出原来挡着谁。
	eq(t, app.do(http.MethodPut, "/api/v1/api-clients/"+itoa(id), admin, map[string]any{"allowIps": ""}).Code, 0, "clear")
	rows = app.auditCommandsLike("admin.apiclient.update")
	last = rows[len(rows)-1]
	if !strings.Contains(last, "allow_ips=—") || !strings.Contains(last, "allow_ips_before=10.2.0.0/16") {
		t.Errorf("清空的审计应记 改前=10.2.0.0/16 改后=—,got %q", last)
	}

	// 只拨开关时不该冒出白名单字段(原有审计口径不变)。
	eq(t, app.do(http.MethodPut, "/api/v1/api-clients/"+itoa(id), admin, map[string]any{"enabled": false}).Code, 0, "disable")
	rows = app.auditCommandsLike("admin.apiclient.update")
	last = rows[len(rows)-1]
	if !strings.Contains(last, "enabled=false") || strings.Contains(last, "allow_ips") {
		t.Errorf("停用的审计只记 enabled,got %q", last)
	}
}

func TestAPIClientAllowlistEditValidatesAndDedupes(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	sa := app.createServiceAccount(admin, "ipval-bot", []int64{app.roleIDByCode(admin, "owner")}, nil)
	r := app.do(http.MethodPost, "/api/v1/api-clients", admin, map[string]any{
		"name": "IP-Val", "userId": sa.ID, "allowIps": "10.1.0.0/16", "scopes": []string{"release:read"},
	})
	eq(t, r.Code, 0, "create")
	id := app.apiClientByName(admin, "IP-Val").ID

	for i, bad := range []string{
		"not-an-ip",
		"10.0.0.0/33",
		"10.0.0.1, 300.1.1.1",
		"10.0.0.1;10.0.0.2",
		longList(), // 去重后仍超 512 字符
	} {
		if r := app.do(http.MethodPut, "/api/v1/api-clients/"+itoa(id), admin, map[string]any{"allowIps": bad}); r.Code != resp400 {
			t.Errorf("case %d %q 应 40001,got %d %s", i, bad, r.Code, r.Msg)
		}
	}
	eq(t, app.apiClientByName(admin, "IP-Val").AllowIPs, "10.1.0.0/16", "被拒的修改不改原值")

	// 重复项(含写法不同但解析后相同的)去重,保留第一次出现的写法。
	r = app.do(http.MethodPut, "/api/v1/api-clients/"+itoa(id), admin, map[string]any{"allowIps": "10.0.0.1, 10.0.0.1\n10.5.0.0/16 10.5.9.9/16, ::1, 0:0::1"})
	eq(t, r.Code, 0, "带重复项的白名单")
	eq(t, app.apiClientByName(admin, "IP-Val").AllowIPs, "10.0.0.1,10.5.0.0/16,::1", "重复项被去掉")

	// 新建走同一套校验,去重同样生效。
	r = app.do(http.MethodPost, "/api/v1/api-clients", admin, map[string]any{
		"name": "IP-Val-2", "userId": sa.ID, "allowIps": "192.168.1.1,192.168.1.1", "scopes": []string{"release:read"},
	})
	eq(t, r.Code, 0, "create with dup")
	eq(t, app.apiClientByName(admin, "IP-Val-2").AllowIPs, "192.168.1.1", "新建同样去重")
}

func TestAPIClientAllowlistEditIsAdminOnly(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	sa := app.createServiceAccount(admin, "ipadm-bot", []int64{app.roleIDByCode(admin, "owner")}, nil)
	r := app.do(http.MethodPost, "/api/v1/api-clients", admin, map[string]any{
		"name": "IP-Adm", "userId": sa.ID, "allowIps": "10.1.0.0/16", "scopes": []string{"release:read"},
	})
	eq(t, r.Code, 0, "create")
	id := app.apiClientByName(admin, "IP-Adm").ID

	dba := app.login("chenhao@aegisdb.io", "aegisdb123")
	for _, body := range []map[string]any{{"allowIps": ""}, {"allowIps": "0.0.0.0/0"}} {
		if r := app.do(http.MethodPut, "/api/v1/api-clients/"+itoa(id), dba, body); r.Code == 0 {
			t.Fatalf("非管理员不该能改白名单: %v", body)
		}
	}
	eq(t, app.apiClientByName(admin, "IP-Adm").AllowIPs, "10.1.0.0/16", "非管理员的修改没有落库")
	if rows := app.auditCommandsLike("admin.apiclient.update"); len(rows) != 0 {
		t.Errorf("被拒的修改不该写更新审计,got %v", rows)
	}
}

// longList 是 40 个互不相同的 IP(去重后 600 来字符),超过 512 上限。
func longList() string {
	parts := make([]string, 0, 40)
	for i := 100; i < 140; i++ {
		parts = append(parts, "10.123.123."+itoa(int64(i)))
	}
	return strings.Join(parts, ",")
}
