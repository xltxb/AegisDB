package bootstrap

// 控制台的搜索框在**服务端**筛。
//
// 列表是分页的:只搜当前页的搜索框,会对一张躺在第三页的工单回答"没有",而人会据此
// 认为它不存在。一个会说谎的搜索框比没有搜索框糟。

import (
	"net/http"
	"net/url"
	"testing"
)

func (a *testApp) searchApprovals(token, q string) apListView {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/approvals?scope=all&page=1&pageSize=200&q="+url.QueryEscape(q), token, nil)
	if r.Code != 0 {
		a.t.Fatalf("search %q: code=%d msg=%s", q, r.Code, r.Msg)
	}
	return decodeApList(a.t, r.Data)
}

// 按单号搜得到那一张,而且**不受分页影响** —— 每页只取一条时也要能搜到。
func TestApprovalSearch_FindsTicketRegardlessOfPage(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")

	all := app.listApprovals(token, "")
	if all.Total == 0 {
		t.Skip("这套种子里没有工单")
	}
	target := all.Items[len(all.Items)-1] // 最旧的一张:它一定不在第一页

	hit := app.searchApprovals(token, target.ApNo)
	if hit.Total != 1 || len(hit.Items) != 1 || hit.Items[0].ApNo != target.ApNo {
		t.Fatalf("按单号搜 %s 应当只命中它自己,得到 total=%d items=%d", target.ApNo, hit.Total, len(hit.Items))
	}

	// 每页只放一条,搜索仍要找得到 —— 这正是"客户端筛"会失败的那个场景。
	r := app.do(http.MethodGet, "/api/v1/approvals?scope=all&page=1&pageSize=1&q="+url.QueryEscape(target.ApNo), token, nil)
	page := decodeApList(t, r.Data)
	if page.Total != 1 || len(page.Items) != 1 {
		t.Errorf("每页 1 条时也该搜得到,得到 total=%d items=%d", page.Total, len(page.Items))
	}
}

// 下划线和百分号是**字面量**,不是通配符。
//
// 表名里到处都是下划线(t_order、db_billing)。不转义的话搜 t_order 会连 tXorder
// 一起命中 —— 搜出来的东西比搜的人以为的多,而多出来的那些看着和真的一样。
//
// 判据不是"命中了几条"(种子里可能只有一条工单,那样什么都证明不了),而是一个
// **只有通配符生效时才可能命中**的探针:拿一条真实工单的实例名,把中间一个字符
// 换成 _ 或 %。字面量匹配必然一条都搜不到;能搜到就说明它被当成了通配符。
func TestApprovalSearch_WildcardsAreLiteral(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")

	all := app.listApprovals(token, "")
	if all.Total == 0 || all.Items[0].Instance == "" {
		t.Skip("这套种子里没有带实例名的工单")
	}
	inst := all.Items[0].Instance
	if len(inst) < 4 {
		t.Skip("实例名太短,构造不出探针")
	}

	// 对照组:原样的子串一定搜得到 —— 先证明探针本身是有效的,否则下面的
	// "搜不到"可能只是因为搜索坏了。
	base := inst[:len(inst)-1]
	if got := app.searchApprovals(token, base); got.Total == 0 {
		t.Fatalf("对照组失败:%q 本该搜得到,搜索本身可能就是坏的", base)
	}

	for _, probe := range []string{
		inst[:len(inst)-1] + "_",            // 末位换成 _
		inst[:2] + "%" + inst[len(inst)-2:], // 中间换成 %
	} {
		if got := app.searchApprovals(token, probe); got.Total != 0 {
			t.Errorf("探针 %q 命中了 %d 条 —— 通配符没有被转义", probe, got.Total)
		}
	}
}

// 搜索不能成为越权的旁路:能搜到的,仍然只是本来就能看到的那些。
func TestApprovalSearch_StaysInsideVisibility(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")

	all := app.listApprovals(token, "")
	// 空搜索词等于不筛;任意搜索词的结果都不该超过可见集合。
	for _, q := range []string{"a", "e", "SELECT", "prod"} {
		if got := app.searchApprovals(token, q); got.Total > all.Total {
			t.Errorf("搜 %q 返回 %d 条,超过了可见的 %d 条", q, got.Total, all.Total)
		}
	}
}
