package bootstrap

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"aegisdb/internal/model"
)

// M9(2026-10 安全审计):审计 CSV 导出把整表读进内存拼成一个字符串;单独的 \r 不加引号,表格软件
// 会把它当换行,后面的内容另起一行、行首是公式。改为分批读、encoding/csv 流式写出,防公式注入照旧。

func TestAuditExport_StreamsAllRowsAndQuotesCarriageReturn(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	base := time.Now().Add(-time.Hour)
	const n = 2500 // 跨过一批(1000)的边界
	for i := range n {
		cmd := "SELECT 1 /* zz-export-row */"
		if i == 7 {
			cmd = "SELECT 1 /* zz-export-row */\r=1+1" // 不含逗号、双引号:旧实现不会顺手加引号
		}
		row := &model.AuditLog{
			OccurredAt: base.Add(time.Duration(i) * time.Millisecond), ActorName: "zz-export-actor",
			Command: cmd, Risk: "low", Result: "executed",
			PrevHash: fmt.Sprintf("zz-prev-%d", i), Hash: fmt.Sprintf("zz-hash-%d", i),
		}
		if err := app.repo.InsertAudit(row); err != nil {
			t.Fatal(err)
		}
	}
	raw := app.raw(http.MethodGet, "/api/v1/audit/export?q=zz-export-actor", admin)
	raw = bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF})
	// 表格软件把单独的 \r 当换行:含 \r 的单元格必须整个包在引号里
	if !bytes.Contains(raw, []byte("\"SELECT 1 /* zz-export-row */\r")) {
		t.Error("含 \\r 的单元格应加引号")
	}
	recs, err := csv.NewReader(bytes.NewReader(raw)).ReadAll()
	if err != nil {
		t.Fatalf("导出应是合法 CSV: %v", err)
	}
	if len(recs) != n+1 {
		t.Fatalf("应有表头 + %d 行,实际 %d 行", n, len(recs))
	}
	cols := len(recs[0])
	for _, r := range recs[1:] {
		if len(r) != cols {
			t.Fatalf("每行列数应与表头一致(%d),实际 %d: %q", cols, len(r), r)
		}
		for _, cell := range r {
			if strings.HasPrefix(cell, "=") {
				t.Errorf("单元格不应以 = 开头(公式注入): %q", cell)
			}
		}
	}
}
