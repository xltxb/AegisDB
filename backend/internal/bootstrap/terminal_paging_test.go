package bootstrap

import (
	"encoding/json"
	"net/http"
	"testing"
)

// Web 命令行结果分批(2026-10:不再强限制 200 行):每批行数来自系统设置(默认 1000),
// 超出的部分暂存,GET /terminal/results/:id 取下一批;只取得到本人的。
func TestTerminal_ResultPagingOverHTTP(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	dev := app.realSQLiteConnIn(admin, "dev", "paging")
	const q = "WITH RECURSIVE s(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM s WHERE n < 1500) SELECT n FROM s"

	type execData struct {
		Data      [][]string `json:"data"`
		ResultID  string     `json:"resultId"`
		TotalRows int        `json:"totalRows"`
		Truncated bool       `json:"truncated"`
	}
	exec := func() execData {
		r := app.do(http.MethodPost, "/api/v1/terminal/exec", admin, map[string]any{"connectionId": dev, "sql": q})
		eq(t, r.Code, 0, "exec: "+r.Msg)
		var d execData
		_ = json.Unmarshal(r.Data, &d)
		return d
	}

	d := exec()
	if len(d.Data) != 1000 || d.TotalRows != 1500 || d.ResultID == "" || !d.Truncated {
		t.Fatalf("默认每批 1000 行、共 1500、有暂存: rows=%d total=%d id=%q", len(d.Data), d.TotalRows, d.ResultID)
	}
	r := app.do(http.MethodGet, "/api/v1/terminal/results/"+d.ResultID+"?offset=1000", admin, nil)
	eq(t, r.Code, 0, "加载更多: "+r.Msg)
	var p struct {
		Data   [][]string `json:"data"`
		More   bool       `json:"more"`
		Offset int        `json:"offset"`
	}
	_ = json.Unmarshal(r.Data, &p)
	if len(p.Data) != 500 || p.More || p.Offset != 1000 || p.Data[0][0] != "1001" {
		t.Fatalf("第二批应是 1001-1500: rows=%d more=%v offset=%d", len(p.Data), p.More, p.Offset)
	}
	other := app.login("zhangwei@aegisdb.io", "aegisdb123")
	if r := app.do(http.MethodGet, "/api/v1/terminal/results/"+d.ResultID+"?offset=1000", other, nil); r.Code == 0 {
		t.Error("别人不能取我暂存的结果")
	}

	// 改每批行数
	app.setSettings(admin, map[string]any{"terminal.resultPageSize": 300})
	if d := exec(); len(d.Data) != 300 || d.TotalRows != 1500 {
		t.Errorf("每批改成 300 后第一批应是 300 行: rows=%d total=%d", len(d.Data), d.TotalRows)
	}
}
