package bootstrap

// 一批语句在同一条连接上逐条执行。
//
// 从前逐条从连接池取连接,相邻两条可能落在两条不同的物理连接上:SET、BEGIN … COMMIT、临时表
// 这类依赖会话的写法中间就断了。SQLite 把这件事暴露得最干脆 —— 临时表只活在建它的那条连接上,
// 而 SQLite 每次执行都是新开的句柄,所以旧行为下第二条就找不到表。

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestExecBatch_RunsInOneSession(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	dev, _ := app.sameFileConns(admin, "one-session")

	r := app.execSQL(admin, dev, "CREATE TEMP TABLE tt (x INTEGER); INSERT INTO tt VALUES (1); INSERT INTO tt VALUES (2)")
	var out struct {
		Output string `json:"output"`
		Rows   int    `json:"rows"`
	}
	_ = json.Unmarshal(r.Data, &out)
	if r.Code != 0 || strings.Contains(out.Output, "失败") || out.Rows != 2 {
		t.Fatalf("临时表在同一个会话里建、写,应当整批成功: code=%d rows=%d output=%q", r.Code, out.Rows, out.Output)
	}
}
