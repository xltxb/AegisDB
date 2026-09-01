package bootstrap

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"velagateway/internal/model"
)

// A script approval references the uploaded file instead of carrying its body.
// The body of a real migration runs to megabytes and tbl_approval.command is TEXT
// (65,535 bytes on MySQL), so submitting one simply failed.
//
// The reference is only safe because of what surrounds it: the gateway reads the
// file itself, scans every statement, records the digest of exactly those bytes,
// and refuses at execution time if the file no longer hashes to it. These tests
// hold that chain together — a reference without the digest would mean approving
// a path rather than a script.

// uploadScript stores a script for the caller and returns its upload id.
func (a *testApp) uploadScript(token, filename, content string) (int64, string) {
	a.t.Helper()
	a.setSettings(token, map[string]any{"script.savePath": a.t.TempDir()})
	r := a.do(http.MethodPost, "/api/v1/scripts/upload", token, map[string]any{
		"filename": filename, "content": content,
	})
	eq(a.t, r.Code, 0, "upload script")
	var up struct {
		ID   int64  `json:"id"`
		Path string `json:"path"`
	}
	if err := json.Unmarshal(r.Data, &up); err != nil {
		a.t.Fatalf("decode upload: %v", err)
	}
	return up.ID, up.Path
}

// riskyScript is big enough that its body would not fit in a MySQL TEXT column.
func riskyScript() string {
	var b strings.Builder
	b.WriteString("DROP TABLE orders_2024_q3;\n")
	for b.Len() < 200*1024 {
		b.WriteString("INSERT INTO t (a, b) VALUES (1, 'padding padding padding padding');\n")
	}
	return b.String()
}

func lastApproval(t *testing.T, app *testApp) model.Approval {
	t.Helper()
	var ap model.Approval
	if err := app.repo.DB().Order("id desc").First(&ap).Error; err != nil {
		t.Fatalf("read approval: %v", err)
	}
	return ap
}

// The ticket holds an excerpt and a digest — not 200KB of script.
func TestScriptRef_ApprovalReferencesTheFileInsteadOfCarryingIt(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	prod := app.connIDByEnv(token, "prod")
	content := riskyScript()
	id, _ := app.uploadScript(token, "migrate.sql", content)

	// No content in the request at all — the file is already on this machine.
	r := app.do(http.MethodPost, "/api/v1/scripts/execute", token, map[string]any{
		"connectionId": prod, "uploadId": id,
	})
	eq(t, r.Code, 42200, "risky script is intercepted for approval")

	ap := lastApproval(t, app)
	eq(t, ap.ScriptUploadID, id, "the ticket points at the uploaded file")
	if ap.ScriptSHA256 == "" {
		t.Fatal("the ticket must record what it hashed, or approval means approving a path")
	}
	if len(ap.Command) >= len(content) {
		t.Errorf("command carries the whole body (%d bytes) — the excerpt is the point", len(ap.Command))
	}
	// MySQL TEXT is 65,535 bytes; the excerpt has to fit with room to spare.
	if len(ap.Command) > 8*1024 {
		t.Errorf("excerpt is %d bytes, too close to the column limit it exists to avoid", len(ap.Command))
	}
	// The approver must be able to tell it is a fragment of something much larger.
	for _, want := range []string{"migrate.sql", "sha256:", "条语句", "省略"} {
		if !strings.Contains(ap.Command, want) {
			t.Errorf("excerpt should state %q so it cannot be mistaken for the whole script:\n%s", want, ap.Command)
		}
	}
}

// The requirement: the file is scanned, and what is scanned is what runs.
func TestScriptRef_FileIsScannedServerSideNotTakenFromTheRequest(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	prod := app.connIDByEnv(token, "prod")

	// The FILE holds a high-risk statement; the request body claims something
	// harmless. Before this, the body was what got scanned and approved while the
	// ticket referenced the file — two different things.
	id, _ := app.uploadScript(token, "sneaky.sql", "DROP TABLE orders;\n")
	r := app.do(http.MethodPost, "/api/v1/scripts/execute", token, map[string]any{
		"connectionId": prod, "uploadId": id, "content": "SELECT 1;",
	})
	eq(t, r.Code, 42200, "the file's DROP must be what is judged, not the request's SELECT")

	ap := lastApproval(t, app)
	if !strings.Contains(ap.Command, "DROP TABLE orders") {
		t.Errorf("the ticket must describe the FILE, got:\n%s", ap.Command)
	}
}

// Swapping the file between review and execution must not go unnoticed.
//
// 注意这个窗口比以前**长了**:审批通过不再顺带执行(ADR 0010),文件可以在
// 审查之后、发起人执行之前的任何时刻被换掉。所以哈希校验放在执行那一步,才是
// 校验"即将执行的这些字节" —— 也正因为窗口变长了,这条测试比以前更要紧。
func TestScriptRef_AFileChangedAfterReviewIsRefused(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	prod := app.connIDByEnv(token, "prod")
	id, path := app.uploadScript(token, "migrate.sql", "DROP TABLE orders;\nSELECT 1;\n")

	eq(t, app.do(http.MethodPost, "/api/v1/scripts/execute", token, map[string]any{
		"connectionId": prod, "uploadId": id,
	}).Code, 42200, "submitted for approval")
	ap := lastApproval(t, app)
	// The approver must not be the initiator (self-approval is off by default).
	approver := app.login("zhangwei@vela.io", "vela123")

	// Someone replaces the file while the ticket sits in the queue.
	if err := os.WriteFile(path, []byte("DROP TABLE payments;\n"), 0o600); err != nil {
		t.Fatalf("rewrite script: %v", err)
	}

	r := app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/approve", approver, nil)
	eq(t, r.Code, 0, "the decision itself is recorded")

	// 通过不执行任何命令,发起人自己来执行 —— 校验就发生在这一下。
	app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/execute", token, nil)

	var after model.Approval
	app.repo.DB().First(&after, ap.ID)
	if !strings.Contains(after.Result, "修改") {
		t.Errorf("a script that changed after review must be refused, got result: %q", after.Result)
	}
	if strings.Contains(after.Result, "执行完成") {
		t.Error("the swapped script was executed — approval was given to different bytes")
	}
}

// A missing file is refused rather than reported as a successful run.
func TestScriptRef_AMissingFileIsRefused(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	prod := app.connIDByEnv(token, "prod")
	id, path := app.uploadScript(token, "gone.sql", "DROP TABLE orders;\n")

	eq(t, app.do(http.MethodPost, "/api/v1/scripts/execute", token, map[string]any{
		"connectionId": prod, "uploadId": id,
	}).Code, 42200, "submitted for approval")
	ap := lastApproval(t, app)
	// The approver must not be the initiator (self-approval is off by default).
	approver := app.login("zhangwei@vela.io", "vela123")

	if err := os.Remove(path); err != nil {
		t.Fatalf("remove script: %v", err)
	}
	eq(t, app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/approve", approver, nil).Code, 0, "decision recorded")

	var after model.Approval
	app.repo.DB().First(&after, ap.ID)
	if strings.Contains(after.Result, "执行完成") {
		t.Errorf("a vanished script must not report success, got: %q", after.Result)
	}
}

// An upload belonging to someone else is not readable through this path.
func TestScriptRef_AnotherUsersUploadIsRefused(t *testing.T) {
	app := newTestApp(t)
	owner := app.login("linwei@vela.io", "vela123")
	prod := app.connIDByEnv(owner, "prod")
	id, _ := app.uploadScript(owner, "mine.sql", "DROP TABLE orders;\n")

	other := app.login("chenhao@vela.io", "vela123")
	r := app.do(http.MethodPost, "/api/v1/scripts/execute", other, map[string]any{
		"connectionId": prod, "uploadId": id,
	})
	if r.Code == 0 || r.Code == 42200 {
		t.Errorf("another user's upload must not be executable, got code=%d", r.Code)
	}
}
