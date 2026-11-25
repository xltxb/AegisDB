package bootstrap

// 发布页按单号 / 环境搜索。
//
// 单号的匹配口径(见 repository.releaseKeywordWhere):
//   - REL-11 / rel11:只认这一张,不带出 REL-110。
//   - 纯数字:REL 编号、外部引用里以冒号分隔的一段、标题里的 orderID= —— 都按整段比。
//     否则 11 会命中标题里的日期 20261105-…,1 会命中每张单末尾的 exec_order。
//   - 其它:标题 / REL 编号 / 外部引用的片段。
//
// 故意不搜 SQL 正文:正文可能带口令,列表和详情都是脱敏后才给人看的,按原文搜等于
// 能一个字符一个字符地猜出来。

import (
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"testing"

	"aegisdb/internal/model"
)

type searchPage struct {
	Items []releaseView `json:"items"`
	Total int64         `json:"total"`
	Envs  []string      `json:"envs"`
}

func (a *testApp) searchReleases(token string, params map[string]string) searchPage {
	a.t.Helper()
	v := url.Values{"pageSize": {"100"}}
	for k, s := range params {
		v.Set(k, s)
	}
	r := a.do(http.MethodGet, "/api/v1/releases?"+v.Encode(), token, nil)
	if r.Code != 0 {
		a.t.Fatalf("list releases %v: %d %s", params, r.Code, r.Msg)
	}
	var p searchPage
	if err := json.Unmarshal(r.Data, &p); err != nil {
		a.t.Fatalf("decode page: %v", err)
	}
	return p
}

func relNosOf(p searchPage) []string {
	out := []string{}
	for _, it := range p.Items {
		out = append(out, it.RelNo)
	}
	sort.Strings(out)
	return out
}

func (a *testApp) searchUserID(email string) int64 {
	a.t.Helper()
	var u model.User
	if err := a.repo.DB().Where("email = ?", email).First(&u).Error; err != nil {
		a.t.Fatalf("user %s: %v", email, err)
	}
	return u.ID
}

// seedSearchFixture 用截图里的真实形状造数据,直接落库:这里测的是列表的筛选,不是流水线。
func (a *testApp) seedSearchFixture() {
	a.t.Helper()
	adminID, roID := a.searchUserID("linwei@aegisdb.io"), a.searchUserID("zhaolei@aegisdb.io")
	for _, r := range []model.Release{
		{RelNo: "REL-911", Title: "k8s-c66-mkt-mmp-api-office-upgrade-20261105-001-orderID=16436-exec_order=1-FAT-DDL",
			ExternalRef: "autopub:16436:FAT:292820:1", CreatorID: adminID, Env: "fat", TierCode: "dev", ProjectID: 7},
		{RelNo: "REL-912", Title: "k8s-c66-bp-attribution-api-update-20261105-001-orderID=16428-exec_order=1-FAT-DDL",
			ExternalRef: "autopub:16428:FAT:292831:1", CreatorID: adminID, Env: "fat", TierCode: "dev", ProjectID: 7},
		{RelNo: "REL-913", Title: "k8s-c66-bp-attribution-api-update-20261105-001-orderID=16428-exec_order=2-FAT-DDL",
			ExternalRef: "autopub:16428:FAT:292837:1", CreatorID: adminID, Env: "fat", TierCode: "dev", ProjectID: 8},
		{RelNo: "REL-9110", Title: "uat 上的单 orderID=164361", ExternalRef: "autopub:164361:UAT:1:1",
			CreatorID: adminID, Env: "uat", TierCode: "uat", ProjectID: 7},
		{RelNo: "REL-914", Title: "手工提的单 100%_done", CreatorID: adminID, Env: "uat", TierCode: "uat", ProjectID: 8,
			SQL: "ALTER USER 'x' IDENTIFIED BY 's3cretpw';"},
		{RelNo: "REL-915", Title: "研发的单 orderID=16436", ExternalRef: "autopub:16436:PROD:1:2",
			CreatorID: roID, Env: "prod", TierCode: "prod", ProjectID: 7},
	} {
		r.PipelineID, r.ConnectionID, r.Status = 1, 1, "success"
		if err := a.repo.DB().Create(&r).Error; err != nil {
			a.t.Fatalf("seed %s: %v", r.RelNo, err)
		}
	}
}

func TestReleaseSearch_ByNumber(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	app.seedSearchFixture()

	for _, c := range []struct {
		name, q string
		want    []string
	}{
		{"REL 编号精确,不带出 REL-9110", "REL-911", []string{"REL-911"}},
		{"REL 编号不分大小写、可省横杠", "rel911", []string{"REL-911"}},
		{"两端空白不算", "  REL-912 ", []string{"REL-912"}},
		{"裸数字也认 REL 编号", "9110", []string{"REL-9110"}},
		{"外部工单号:同一工单的单都出来,不带出 164361", "16436", []string{"REL-911", "REL-915"}},
		{"外部工单号拆成多张执行顺序", "16428", []string{"REL-912", "REL-913"}},
		{"外部引用中间那段发布号", "292820", []string{"REL-911"}},
		{"引用末尾的 exec_order 与标题里的 exec_order 都不算单号", "2", []string{}},
		{"标题里的日期片段不被数字误中", "1105", []string{}},
		{"外部引用片段", "autopub:16428", []string{"REL-912", "REL-913"}},
		{"标题片段", "orderID=16436-exec", []string{"REL-911"}},
		{"大小写不敏感", "ORDERID=16428", []string{"REL-912", "REL-913"}},
		{"% 是字面量,不是通配符", "100%", []string{"REL-914"}},
		{"只有 % 也只匹配字面量", "%%", []string{}},
		{"_ 是字面量,不是通配符", "api_office", []string{}},
		{"对不上就是空", "no-such-ticket", []string{}},
		{"注入串只是字面量", "' OR '1'='1", []string{}},
	} {
		got := relNosOf(app.searchReleases(admin, map[string]string{"scope": "all", "q": c.q}))
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:q=%q 得到 %v,应为 %v", c.name, c.q, got, c.want)
		}
	}
}

// 正文可能带口令,不能拿来当搜索的依据(见文件头)。
func TestReleaseSearch_DoesNotSearchSQLBody(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	app.seedSearchFixture()
	if got := relNosOf(app.searchReleases(admin, map[string]string{"scope": "all", "q": "s3cretpw"})); len(got) != 0 {
		t.Errorf("按 SQL 正文搜到了 %v —— 这是在猜口令", got)
	}
}

func TestReleaseSearch_FilterComposes(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	app.seedSearchFixture()

	for _, c := range []struct {
		name   string
		params map[string]string
		want   []string
	}{
		{"环境", map[string]string{"scope": "all", "env": "uat"}, []string{"REL-9110", "REL-914"}},
		{"环境 + 单号", map[string]string{"scope": "all", "env": "prod", "q": "16436"}, []string{"REL-915"}},
		{"环境 + 项目", map[string]string{"scope": "all", "env": "fat", "projectId": "8"}, []string{"REL-913"}},
		{"环境 + 单号 + 项目", map[string]string{"scope": "all", "env": "fat", "projectId": "7", "q": "16428"}, []string{"REL-912"}},
		{"环境 + 我的", map[string]string{"scope": "mine", "env": "prod"}, []string{}},
		{"单号 + 我的", map[string]string{"scope": "mine", "q": "16436"}, []string{"REL-911"}},
		{"分层码不是环境", map[string]string{"scope": "all", "env": "dev", "q": "16436"}, []string{}},
		{"单号 + 状态", map[string]string{"scope": "all", "status": "failed", "q": "16436"}, []string{}},
	} {
		got := relNosOf(app.searchReleases(admin, c.params))
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:%v 得到 %v,应为 %v", c.name, c.params, got, c.want)
		}
	}
}

// total 必须是筛选后的总数,分页才对;翻页也在筛选结果里翻。
func TestReleaseSearch_PagingCountsMatchesOnly(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	app.seedSearchFixture()

	first := app.searchReleases(admin, map[string]string{"scope": "all", "q": "16428", "pageSize": "1", "page": "1"})
	second := app.searchReleases(admin, map[string]string{"scope": "all", "q": "16428", "pageSize": "1", "page": "2"})
	if first.Total != 2 || second.Total != 2 {
		t.Fatalf("total 应是命中数 2,实际 %d / %d", first.Total, second.Total)
	}
	got := append(relNosOf(first), relNosOf(second)...)
	sort.Strings(got)
	if !reflect.DeepEqual(got, []string{"REL-912", "REL-913"}) {
		t.Errorf("两页合起来应是全部命中,实际 %v", got)
	}
}

// 搜索不能越过可见范围:看不到全部的人,带 scope=all 搜也只有自己的。
func TestReleaseSearch_RespectsVisibility(t *testing.T) {
	app := newTestApp(t)
	app.seedSearchFixture()
	app.enableMenuForCode("ro", "pipeline")
	ro := app.login("zhaolei@aegisdb.io", "aegisdb123")

	if got := relNosOf(app.searchReleases(ro, map[string]string{"scope": "all", "q": "16436"})); !reflect.DeepEqual(got, []string{"REL-915"}) {
		t.Errorf("ro 搜 16436 只该见到自己的 REL-915,实际 %v", got)
	}
	for _, q := range []string{"REL-911", "16428", "orderID"} {
		for _, it := range app.searchReleases(ro, map[string]string{"scope": "all", "q": q}).Items {
			if it.RelNo != "REL-915" {
				t.Errorf("ro 搜 %q 见到了别人的 %s", q, it.RelNo)
			}
		}
	}
	if got := relNosOf(app.searchReleases(ro, map[string]string{"scope": "all", "env": "fat"})); len(got) != 0 {
		t.Errorf("ro 按 fat 筛不该见到别人的单,实际 %v", got)
	}
	// 环境选项也不能透露别人的单落在哪些环境。
	p := app.searchReleases(ro, map[string]string{"scope": "all"})
	if !reflect.DeepEqual(p.Envs, []string{"prod"}) {
		t.Errorf("ro 的环境选项应只来自自己的单,实际 %v", p.Envs)
	}
}

// 环境选项来自单子本身,不写死;选中某个环境后选项不收窄,否则选了就换不回别的。
func TestReleaseSearch_EnvOptions(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	app.seedSearchFixture()

	all := []string{"fat", "prod", "uat"}
	for _, c := range []struct {
		name   string
		params map[string]string
		want   []string
	}{
		{"全部范围", map[string]string{"scope": "all"}, all},
		{"选了环境不收窄", map[string]string{"scope": "all", "env": "uat"}, all},
		{"搜了单号不收窄", map[string]string{"scope": "all", "q": "16428"}, all},
		{"我的", map[string]string{"scope": "mine"}, []string{"fat", "uat"}},
		{"按项目", map[string]string{"scope": "all", "projectId": "8"}, []string{"fat", "uat"}},
	} {
		if got := app.searchReleases(admin, c.params).Envs; !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:%v 的环境选项 %v,应为 %v", c.name, c.params, got, c.want)
		}
	}
}

// 不带新参数时行为不变。
func TestReleaseSearch_NoParamsListsEverything(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	app.seedSearchFixture()
	if got := app.searchReleases(admin, map[string]string{"scope": "all"}); got.Total != 6 {
		t.Errorf("不带筛选应列出全部 6 张,实际 %d", got.Total)
	}
	if got := app.searchReleases(admin, map[string]string{"scope": "all", "q": "   ", "env": ""}); got.Total != 6 {
		t.Errorf("空白关键字等于不筛,实际 %d", got.Total)
	}
}
