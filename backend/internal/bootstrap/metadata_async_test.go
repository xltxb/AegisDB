package bootstrap

// 手动同步元数据改在后台跑(2026-10-28):POST ...?async=1 立刻返回,GET 同一路径轮询。

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

type metaSyncStatusResp struct {
	Running bool   `json:"running"`
	Failed  string `json:"failed"`
	Sync    *struct {
		Tables  int    `json:"tables"`
		Columns int    `json:"columns"`
		Err     string `json:"err"`
	} `json:"sync"`
}

func (a *testApp) metaSyncStatus(token string, connID int64) metaSyncStatusResp {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/connections/"+itoa(connID)+"/metadata/sync", token, nil)
	if r.Code != 0 {
		a.t.Fatalf("查同步状态失败: code=%d msg=%s", r.Code, r.Msg)
	}
	var out metaSyncStatusResp
	if err := json.Unmarshal(r.Data, &out); err != nil {
		a.t.Fatalf("decode: %v", err)
	}
	return out
}

func TestMetadata_AsyncSyncReturnsAtOnceThenFinishes(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	dev, _ := app.sameFileConns(admin, "meta-async")
	if r := app.execSQL(admin, dev, `CREATE TABLE t_meta_async (id INTEGER PRIMARY KEY, name TEXT)`); r.Code != 0 {
		t.Fatalf("建表: %s", r.Msg)
	}

	r := app.do(http.MethodPost, "/api/v1/connections/"+itoa(dev)+"/metadata/sync?async=1", admin, map[string]any{})
	if r.Code != 0 {
		t.Fatalf("发起后台同步失败: code=%d msg=%s", r.Code, r.Msg)
	}
	var started metaSyncStatusResp
	if err := json.Unmarshal(r.Data, &started); err != nil {
		t.Fatal(err)
	}
	if !started.Running {
		t.Fatal("后台同步发起后应当回 running=true")
	}

	deadline := time.Now().Add(30 * time.Second)
	var st metaSyncStatusResp
	for {
		st = app.metaSyncStatus(admin, dev)
		if !st.Running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("30 秒内后台同步没跑完")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if st.Failed != "" {
		t.Fatalf("同步不该失败: %s", st.Failed)
	}
	if st.Sync == nil || st.Sync.Columns < 2 {
		t.Fatalf("跑完后应当有同步结果: %+v", st.Sync)
	}
	if !hasTable(app.readMeta(admin, dev), "t_meta_async") {
		t.Fatal("后台同步跑完后缓存里应当有 t_meta_async")
	}
}

// 检查(存在 / 有凭据)当场报,不进后台 —— 这些不需要等。
func TestMetadata_AsyncSyncRejectsUnknownConnAtOnce(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	r := app.do(http.MethodPost, "/api/v1/connections/999999/metadata/sync?async=1", admin, map[string]any{})
	if r.Code == 0 {
		t.Fatal("不存在的实例应当当场报错")
	}
}
