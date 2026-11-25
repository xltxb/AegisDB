package bootstrap

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"velagateway/pkg/resp"
)

// A script PASTED into the terminal's script panel (no prior upload) used to
// reach the approval ticket as its full body: SaveUploadedScript created an
// upload record but returned only the path, so uploadID stayed 0 and
// SubmitScriptForApproval fell back to embedding everything in `command` — a
// 100KB paste died at submission with the raw driver error "Data too long for
// column 'command'". The paste is an upload like any other: the ticket must
// carry a bounded excerpt + file reference, whatever the script's size.
func TestScriptExecute_PastedScriptTicketIsReferenceNotBody(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	prodConn := app.connIDByEnv(token, "prod")
	eq(t, app.do(http.MethodPut, "/api/v1/settings", token,
		map[string]any{"script.savePath": t.TempDir()}).Code, 0, "set script path")

	// >64KB (past the old TEXT cap) with one high-risk statement to force the
	// whole-script approval path.
	content := "DROP TABLE orders;\nSELECT id FROM users WHERE id IN (1" +
		strings.Repeat(",1234567", 10_000) + ");\n"
	if len(content) < 65_536 {
		t.Fatalf("fixture too short: %d", len(content))
	}
	r := app.do(http.MethodPost, "/api/v1/scripts/execute", token, map[string]any{
		"connectionId": prodConn, "filename": "paste.sql", "content": content,
	})
	eq(t, r.Code, resp.CodeIntercepted, "risky pasted script goes to approval")

	var data struct {
		Exec struct {
			ApprovalNo string `json:"approvalNo"`
		} `json:"exec"`
	}
	_ = json.Unmarshal(r.Data, &data)
	if data.Exec.ApprovalNo == "" {
		t.Fatal("expected an approval number")
	}

	// The stored ticket references the file: bounded excerpt, never the body.
	lr := app.do(http.MethodGet, "/api/v1/approvals", token, nil)
	var page struct {
		Items []map[string]any `json:"items"`
	}
	_ = json.Unmarshal(lr.Data, &page)
	for _, it := range page.Items {
		if it["apNo"] != data.Exec.ApprovalNo {
			continue
		}
		cmd, _ := it["command"].(string)
		if len(cmd) == 0 || len(cmd) > 8_192 {
			t.Fatalf("ticket command must be a bounded excerpt, got %d bytes", len(cmd))
		}
		if up, ok := it["scriptUploadId"].(float64); !ok || up <= 0 {
			t.Errorf("ticket must reference the saved upload, got %v", it["scriptUploadId"])
		}
		return
	}
	t.Fatalf("approval %s not found in list", data.Exec.ApprovalNo)
}
