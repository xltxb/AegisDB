package bootstrap

// 元数据缓存:同步一台实例、读回来、按表名/列名检索,以及范围收口。
//
// 这几条走的是真实的 HTTP + 真实的 SQLite 目标库(sameFileConns 建的那种),所以它验的
// 是整条链:路由 → 权限 → 探查 → 落库 → 读回。
//
// 最要紧的两条,都是"错了不报错"的那种:
//   - **先删后插**:同步是整台实例替换。少了这一步,改过名的表会在缓存里留着,而它
//     看起来和真表一模一样。
//   - **范围收口**:够不到那台实例的人不该在检索结果里看见它的表名和列名 —— 那本身
//     就是信息(哪个库里有 id_card 这一列)。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

type metaTablesResp struct {
	Tables []struct {
		Database string `json:"database"`
		Name     string `json:"name"`
		Kind     string `json:"kind"`
	} `json:"tables"`
	Sync *struct {
		Tables  int    `json:"tables"`
		Columns int    `json:"columns"`
		Err     string `json:"err"`
	} `json:"sync"`
}

func (a *testApp) syncMeta(token string, connID int64) apiResp {
	a.t.Helper()
	return a.do(http.MethodPost, "/api/v1/connections/"+itoa(connID)+"/metadata/sync", token, map[string]any{})
}

func (a *testApp) readMeta(token string, connID int64) metaTablesResp {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/connections/"+itoa(connID)+"/metadata", token, nil)
	if r.Code != 0 {
		a.t.Fatalf("读元数据失败: code=%d msg=%s", r.Code, r.Msg)
	}
	var out metaTablesResp
	if err := json.Unmarshal(r.Data, &out); err != nil {
		a.t.Fatalf("decode metadata: %v", err)
	}
	return out
}

// 同步之后,表和列都在;同步状态也在。
func TestMetadata_SyncThenRead(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	dev, _ := app.sameFileConns(admin, "meta-sync")
	if r := app.execSQL(admin, dev, `CREATE TABLE t_meta_a (id INTEGER PRIMARY KEY, name TEXT NOT NULL)`); r.Code != 0 {
		t.Fatalf("建表: %s", r.Msg)
	}

	if r := app.syncMeta(admin, dev); r.Code != 0 {
		t.Fatalf("同步失败: code=%d msg=%s", r.Code, r.Msg)
	}
	got := app.readMeta(admin, dev)
	if got.Sync == nil {
		t.Fatal("同步过的实例必须有同步状态 —— 没有它,'为什么没有数据'就没有答案")
	}
	if got.Sync.Err != "" {
		t.Errorf("同步不该报错: %s", got.Sync.Err)
	}
	found := false
	for _, tb := range got.Tables {
		if tb.Name == "t_meta_a" {
			found = true
			if tb.Kind != "table" {
				t.Errorf("t_meta_a 应当是 table,实际 %q", tb.Kind)
			}
		}
	}
	if !found {
		t.Fatalf("缓存里没有 t_meta_a:%+v", got.Tables)
	}
	if got.Sync.Columns < 2 {
		t.Errorf("列也该被收下(id + name),实际 %d", got.Sync.Columns)
	}
}

// **先删后插**:远端删掉的表,再同步一次之后必须从缓存里消失。
//
// 这一条是整个同步语义的落脚点。少了它,缓存会越积越多,而一张早已不存在的表在界面上
// 与真表长得一模一样 —— 有人会照着它去写 SQL。
func TestMetadata_SyncReplacesInsteadOfAccumulating(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	dev, _ := app.sameFileConns(admin, "meta-replace")
	if r := app.execSQL(admin, dev, `CREATE TABLE t_meta_gone (id INTEGER)`); r.Code != 0 {
		t.Fatalf("建表: %s", r.Msg)
	}
	if r := app.syncMeta(admin, dev); r.Code != 0 {
		t.Fatalf("首次同步: %s", r.Msg)
	}
	if !hasTable(app.readMeta(admin, dev), "t_meta_gone") {
		t.Fatal("首次同步之后应当能看到 t_meta_gone")
	}

	if r := app.execSQL(admin, dev, `DROP TABLE t_meta_gone`); r.Code != 0 {
		t.Fatalf("删表: %s", r.Msg)
	}
	if r := app.syncMeta(admin, dev); r.Code != 0 {
		t.Fatalf("再次同步: %s", r.Msg)
	}
	if hasTable(app.readMeta(admin, dev), "t_meta_gone") {
		t.Error("远端已删的表必须从缓存里消失 —— 同步是整台替换,不是往上堆")
	}
}

func hasTable(r metaTablesResp, name string) bool {
	for _, tb := range r.Tables {
		if tb.Name == name {
			return true
		}
	}
	return false
}

// 检索能按**列名**找 —— 这是缓存换来的能力,实时探查做不到。
func TestMetadata_SearchByColumnName(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	dev, _ := app.sameFileConns(admin, "meta-search")
	if r := app.execSQL(admin, dev, `CREATE TABLE t_meta_search (id INTEGER, id_card_no TEXT)`); r.Code != 0 {
		t.Fatalf("建表: %s", r.Msg)
	}
	if r := app.syncMeta(admin, dev); r.Code != 0 {
		t.Fatalf("同步: %s", r.Msg)
	}

	r := app.do(http.MethodGet, "/api/v1/metadata/search?q=id_card_no", admin, nil)
	if r.Code != 0 {
		t.Fatalf("检索失败: code=%d msg=%s", r.Code, r.Msg)
	}
	var out struct {
		Columns []struct {
			Table string `json:"table"`
			Name  string `json:"name"`
		} `json:"columns"`
	}
	if err := json.Unmarshal(r.Data, &out); err != nil {
		t.Fatalf("decode search: %v", err)
	}
	hit := false
	for _, c := range out.Columns {
		if c.Table == "t_meta_search" && c.Name == "id_card_no" {
			hit = true
		}
	}
	if !hit {
		t.Errorf("按列名没找到:%+v", out.Columns)
	}
}

// 范围收口:够不到那台实例的人,既不能同步它,也不该在检索结果里看见它的表。
//
// 表名和列名本身就是信息 —— "哪个库里有 id_card 这一列"是一个不该白送的答案。
func TestMetadata_RespectsInstanceScope(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	dev, conn := app.sameFileConns(admin, "meta-scope")
	if r := app.execSQL(admin, dev, `CREATE TABLE t_meta_secret (id INTEGER, secret_col TEXT)`); r.Code != 0 {
		t.Fatalf("建表: %s", r.Msg)
	}
	// 贴一个只有 l2 有的标签 —— ro 的标签是 analytics/readonly,够不到。
	eq(t, app.do(http.MethodPatch, "/api/v1/connections/"+itoa(dev), admin,
		map[string]any{"tags": "orders"}).Code, 0, "贴上 ro 够不到的标签")
	_ = conn
	if r := app.syncMeta(admin, dev); r.Code != 0 {
		t.Fatalf("同步: %s", r.Msg)
	}

	ro := app.login("zhaolei@vela.io", "vela123")
	if r := app.syncMeta(ro, dev); r.Code == 0 {
		t.Error("够不到这台实例的人不该能让网关去连它")
	}
	r := app.do(http.MethodGet, "/api/v1/metadata/search?q=secret_col", ro, nil)
	if r.Code == 0 {
		var out struct {
			Columns []struct {
				Name string `json:"name"`
			} `json:"columns"`
		}
		_ = json.Unmarshal(r.Data, &out)
		for _, c := range out.Columns {
			if strings.Contains(c.Name, "secret_col") {
				t.Error("够不到这台实例的人在检索结果里看到了它的列名")
			}
		}
	}
}

// 还没同步过的实例:sync 为 null。它和"同步过、确实一张表都没有"必须分得开 ——
// 两者在界面上长得一模一样,而只有前者是正常的等待。
func TestMetadata_NeverSyncedHasNoState(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	dev, _ := app.sameFileConns(admin, "meta-nosync")

	got := app.readMeta(admin, dev)
	if got.Sync != nil {
		t.Errorf("没同步过的实例不该有同步状态:%+v", got.Sync)
	}
	if len(got.Tables) != 0 {
		t.Errorf("没同步过的实例不该有表:%+v", got.Tables)
	}
}
