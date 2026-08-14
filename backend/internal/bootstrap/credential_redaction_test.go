package bootstrap

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// A password typed at the terminal must not turn up in anything the platform
// stores or shows afterwards.
//
// The unit tests in pkg/sqlutil prove the masking rules are right. They cannot
// prove the rules are APPLIED — and every leak found so far was exactly that: a
// correct redactor that some path did not call. The external approval request
// sent the command raw, the notification bodies carried it, background jobs
// stored and displayed it. Each was invisible from the rule side because the
// rule was fine.
//
// So this suite works from the outside: run a real CREATE USER through the real
// HTTP API, then read back every surface a human or another system can reach and
// assert the password is in none of them. A new surface that forgets to redact
// fails here even if nobody remembers to write a test for it — provided it is
// one of the surfaces listed below, which is why they are enumerated rather than
// sampled.
//
// Oracle's unquoted form is used deliberately: it is the one a redactor built
// around quoted literals misses, so a regression to that shape shows up here too.

const (
	pwQuoted   = "X8wr^J+iu3n!L9cL"
	pwUnquoted = "Or4clePlainPass"
)

// mustNotContainSecrets fails with the surface name and the offending text.
//
// It checks a PREFIX as well as the whole password, because several surfaces
// shorten the command for display. A leak there arrives truncated: masking after
// clipping leaves `IDENTIFIED BY 'X8wr^J+iu` — no closing quote, so nothing
// matches and most of the password rides along in the excerpt. Asserting only on
// the complete string calls that a pass, which it plainly is not. This test made
// that exact mistake before the prefix check was added.
const secretPrefixLen = 8

func mustNotContainSecrets(t *testing.T, surface string, body []byte) {
	t.Helper()
	for _, s := range []string{pwQuoted, pwUnquoted} {
		text := string(body)
		if strings.Contains(text, s) {
			t.Errorf("%s leaked the password %q:\n%s", surface, s, truncateForMsg(text))
			continue
		}
		if p := s[:secretPrefixLen]; strings.Contains(text, p) {
			t.Errorf("%s leaked a truncated password (starts %q — still a leak):\n%s", surface, p, truncateForMsg(text))
		}
	}
}

func truncateForMsg(s string) string {
	if len(s) > 2000 {
		return s[:2000] + "…"
	}
	return s
}

func TestCredentials_NeverReachTheAuditLog(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	dev := app.connIDByEnv(token, "dev")

	for _, sql := range []string{
		`CREATE USER 'db_opt'@'%' IDENTIFIED WITH 'mysql_native_password' BY '` + pwQuoted + `' PASSWORD EXPIRE NEVER`,
		`ALTER USER app_user IDENTIFIED BY ` + pwUnquoted, // Oracle: unquoted
	} {
		r := app.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
			"connectionId": dev, "sql": sql, "reason": "运维加账号",
		})
		if r.Code != 0 && r.Code != 42200 { // executed, or intercepted for approval
			t.Fatalf("exec %q: code=%d msg=%s", sql, r.Code, r.Msg)
		}
	}

	// The audit row is immutable and hash-chained: whatever lands here is what the
	// platform keeps forever, so it is the one surface that cannot be cleaned up
	// after the fact.
	mustNotContainSecrets(t, "GET /audit", app.auditItemsRaw(token, ""))

	// The CSV export reads the same rows back out to a file that leaves the
	// building.
	mustNotContainSecrets(t, "GET /audit/export", app.raw(http.MethodGet, "/api/v1/audit/export?risk=all", token))
}

func TestCredentials_NeverReachTheApprovalRecord(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	prod := app.connIDByEnv(token, "prod")

	// On prod this is gated, so it becomes an approval ticket rather than running.
	r := app.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
		"connectionId": prod,
		"sql":          `CREATE USER 'db_opt'@'%' IDENTIFIED WITH 'mysql_native_password' BY '` + pwQuoted + `'`,
		"reason":       "生产加只读账号",
	})
	if r.Code != 42200 {
		t.Fatalf("expected the prod statement to be intercepted for approval, got code=%d msg=%s", r.Code, r.Msg)
	}

	// The approval list is what an approver reads to decide. The stored command
	// stays verbatim so the gateway can execute it after approval — masking has to
	// happen on the way out, which is exactly the kind of arrangement that gets
	// forgotten when a second read path is added.
	approver := app.login("zhangwei@vela.io", "vela123")
	ar := app.do(http.MethodGet, "/api/v1/approvals", approver, nil)
	eq(t, ar.Code, 0, "list approvals")
	mustNotContainSecrets(t, "GET /approvals", ar.Data)

	// It must still be reviewable: an approver who cannot see the account or the
	// plugin cannot judge the request, and a mask that ate them would pass the
	// leak check above while making the feature useless.
	if !strings.Contains(string(ar.Data), "db_opt") {
		t.Errorf("the approver can no longer see which account is being created:\n%s", truncateForMsg(string(ar.Data)))
	}

	// The initiator's inbox: approve it and check the notification body, which is
	// persisted and rendered in the UI.
	var aps struct {
		Items []struct {
			ID int64 `json:"id"`
		} `json:"items"`
	}
	_ = json.Unmarshal(ar.Data, &aps)
	if len(aps.Items) == 0 {
		t.Fatal("no approval ticket was created")
	}
	dr := app.do(http.MethodPost, fmt.Sprintf("/api/v1/approvals/%d/reject", aps.Items[0].ID), approver, map[string]any{"reason": "先走工单"})
	eq(t, dr.Code, 0, "reject the ticket")

	nr := app.do(http.MethodGet, "/api/v1/notifications", token, nil)
	eq(t, nr.Code, 0, "list notifications")
	mustNotContainSecrets(t, "GET /notifications", nr.Data)

	// And the audit rows the approval flow wrote along the way.
	mustNotContainSecrets(t, "GET /audit (approval flow)", app.auditItemsRaw(token, ""))
}

// Background jobs store the statement so a worker can run it later — the same
// arrangement as an approval, and the same trap. They are also visible to
// oversight roles, so the credential would be in front of someone who never ran
// the command.
func TestCredentials_NeverReachABackgroundJob(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	dev := app.connIDByEnv(token, "dev")

	r := app.do(http.MethodPost, "/api/v1/terminal/exec-async", token, map[string]any{
		"connectionId": dev,
		"sql":          `CREATE USER 'db_opt'@'%' IDENTIFIED WITH 'mysql_native_password' BY '` + pwQuoted + `'`,
		"reason":       "后台建账号",
	})
	eq(t, r.Code, 0, "submit async")
	var sub struct {
		JobID int64 `json:"jobId"`
	}
	_ = json.Unmarshal(r.Data, &sub)
	if sub.JobID == 0 {
		t.Fatalf("expected a jobId, got %s", r.Data)
	}

	// Poll to completion so the streamed log and any driver error are populated —
	// the log is the field easiest to forget, and a failed CREATE USER is exactly
	// what lands in it.
	deadline := time.Now().Add(5 * time.Second)
	var detail []byte
	for time.Now().Before(deadline) {
		jr := app.do(http.MethodGet, fmt.Sprintf("/api/v1/async-jobs/%d", sub.JobID), token, nil)
		eq(t, jr.Code, 0, "get async job")
		detail = jr.Data
		var st struct {
			Status string `json:"status"`
		}
		_ = json.Unmarshal(jr.Data, &st)
		if st.Status == "done" || st.Status == "failed" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	mustNotContainSecrets(t, "GET /async-jobs/:id", detail)

	lr := app.do(http.MethodGet, "/api/v1/async-jobs", token, nil)
	eq(t, lr.Code, 0, "list async jobs")
	mustNotContainSecrets(t, "GET /async-jobs", lr.Data)

	mustNotContainSecrets(t, "GET /audit (async flow)", app.auditItemsRaw(token, ""))
}
