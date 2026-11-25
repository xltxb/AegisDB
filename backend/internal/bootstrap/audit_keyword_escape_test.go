package bootstrap

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
)

// 审计关键字搜索的转义:_ 与 % 是字面量,不是通配符;转义字符 ! 本身也能搜。
//
// 这一组跑在 SQLite 上,验的是转义**语义**没有因为换转义字符而变。MySQL 上那个
// "ESCAPE '\' 是语法错误"的问题在这里测不出来 —— 由 repository 包的源码级守卫挡。
func (a *testApp) auditTotal(token, q string) int64 {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/audit?range=30d&pageSize=1&q="+url.QueryEscape(q), token, nil)
	if r.Code != 0 {
		a.t.Fatalf("审计查询 q=%q: code=%d msg=%s", q, r.Code, r.Msg)
	}
	var page struct {
		Total int64 `json:"total"`
	}
	_ = json.Unmarshal(r.Data, &page)
	return page.Total
}

func TestAuditKeyword_UnderscoreAndPercentAreLiteral(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")

	// 登录本身就写了一行审计,操作人是 Lin Wei —— 按名字能搜到
	if n := app.auditTotal(admin, "Lin"); n == 0 {
		t.Fatal("按操作人名字应能搜到登录记录")
	}
	// 下划线是字面量:Lin_Wei 里的 _ 不该当成"任意一个字符"去匹配 "Lin Wei" 中间的空格
	if n := app.auditTotal(admin, "Lin_Wei"); n != 0 {
		t.Fatalf("下划线应是字面量,Lin_Wei 不该匹配 Lin Wei,实际命中 %d", n)
	}
	// 百分号是字面量:单独一个 % 不该等于"全部"
	if n := app.auditTotal(admin, "%"); n != 0 {
		t.Fatalf("百分号应是字面量,不该匹配所有行,实际命中 %d", n)
	}
	// 转义字符自己也能搜(不会因为加倍处理而炸掉)
	if n := app.auditTotal(admin, "!"); n != 0 {
		t.Fatalf("搜 ! 不该命中没有 ! 的行,实际 %d", n)
	}
}

// 关键字也匹配**邮箱**。审计行上存的是姓名(Lin Wei),而 DBA 想到一个人首先想到的是
// 登录名(linwei)—— 生产上一次"按 garett 搜不到"就是这么来的:姓名字段不是 garett,
// 邮箱才是。姓名里没有 "linwei" 这个串,能搜到就只能是邮箱匹配上了。
func TestAuditKeyword_MatchesActorEmail(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")

	if n := app.auditTotal(admin, "linwei"); n == 0 {
		t.Fatal("按邮箱前缀 linwei 应能搜到 Lin Wei 的登录记录")
	}
	if n := app.auditTotal(admin, "linwei@aegisdb.io"); n == 0 {
		t.Fatal("按完整邮箱应能搜到")
	}
	if n := app.auditTotal(admin, "nobody@nowhere.invalid"); n != 0 {
		t.Fatalf("不存在的邮箱不该命中,实际 %d", n)
	}
}
