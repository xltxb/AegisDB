package bootstrap

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"velagateway/internal/model"
)

// 真实实例的库列表要现查。从前只要缓存里有这台实例就直接回缓存:新库要等同步,没有表的库永远不出现,
// 外部审批系统选不到、也授不了(2026-11-05,fat 环境)。
func TestQuerySourceDatabases_LiveBeatsCache(t *testing.T) {
	e := newGrantEnv(t)
	// 接口不让建 SQLite 实例,直接写库:它是测试里唯一不用外部服务就能真连的引擎。
	conn := &model.Connection{
		Name: "live-source", Engine: "SQLite", Env: "prod", Policy: "audit-only", BizQuery: true,
		Database: filepath.Join(t.TempDir(), "live.db"),
	}
	if err := e.app.repo.DB().Create(conn).Error; err != nil {
		t.Fatal(err)
	}
	id := conn.ID
	e.seedMeta(t, id, "stale_db")
	if got := e.sourceDBs(t, id); !reflect.DeepEqual(got, []string{"main"}) {
		t.Fatalf("应回现查结果,不是缓存,实际 %v", got)
	}

	// 下发授权用同一份名单:现查有的库能授,只在缓存里的库不能授。
	body := e.body("live@example.com", "OA-LIVE-1")
	body["sourceId"], body["database"] = id, "main"
	eq(t, e.put(body).Code, 0, "现查到的库能授权")
	body = e.body("live@example.com", "OA-LIVE-2")
	body["sourceId"], body["database"] = id, "stale_db"
	if r := e.put(body); r.Code == 0 {
		t.Error("实例上已经没有的库不该能授权")
	}
}

// 实例暂时连不上时退回缓存,不把下拉清空;缓存也没有才报错。
func TestQuerySourceDatabases_FallsBackWhenInstanceDown(t *testing.T) {
	e := newGrantEnv(t)
	down := map[string]any{
		"engine": "MySQL 8.0", "host": "127.0.0.1", "port": 1, "username": "u", "password": "p",
	}
	withCache := e.newRealSource(t, "down-cached", down)
	e.seedMeta(t, withCache, "cached_db")
	if got := e.sourceDBs(t, withCache); !reflect.DeepEqual(got, []string{"cached_db"}) {
		t.Fatalf("应退回缓存,实际 %v", got)
	}

	noCache := e.newRealSource(t, "down-empty", down)
	r := e.app.do(http.MethodGet, "/api/v1/open/query-sources/"+itoa(noCache)+"/databases", e.token, nil)
	if r.Code == 0 {
		t.Fatalf("连不上又没有缓存时应报错,不能回空列表: %s", r.Data)
	}
}

func (e *grantEnv) newRealSource(t *testing.T, name string, fields map[string]any) int64 {
	t.Helper()
	body := map[string]any{"name": name, "env": "prod", "policy": "audit-only"}
	for k, v := range fields {
		body[k] = v
	}
	r := e.app.do(http.MethodPost, "/api/v1/connections", e.admin, body)
	eq(t, r.Code, 0, "建实例")
	var c struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(r.Data, &c)
	eq(t, e.app.do(http.MethodPatch, "/api/v1/connections/"+itoa(c.ID), e.admin, map[string]any{"bizQuery": true}).Code, 0, "标记业务查询源")
	return c.ID
}

func (e *grantEnv) seedMeta(t *testing.T, conn int64, db string) {
	t.Helper()
	row := &model.MetaTable{ConnectionID: conn, DBName: db, Name: "t1", Kind: "table", SyncedAt: time.Now()}
	if err := e.app.repo.DB().Create(row).Error; err != nil {
		t.Fatal(err)
	}
}

func (e *grantEnv) sourceDBs(t *testing.T, id int64) []string {
	t.Helper()
	r := e.app.do(http.MethodGet, "/api/v1/open/query-sources/"+itoa(id)+"/databases", e.token, nil)
	eq(t, r.Code, 0, "列库名")
	var dbs []string
	_ = json.Unmarshal(r.Data, &dbs)
	return dbs
}
