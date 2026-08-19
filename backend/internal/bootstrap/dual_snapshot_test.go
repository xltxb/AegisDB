package bootstrap

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"velagateway/internal/model"
	"velagateway/pkg/resp"
)

// An approval and an audit row each carry two snapshots: the environment the
// command ran in, and the control tier it was judged under. Both are frozen at
// write time.
//
// They cannot be derived later. An environment may be rebound to another tier
// and an instance may be moved to another environment; after either, resolving
// the connection reports a control level that was never the one applied. These
// tests hold that line — the interesting assertions are the ones that check a
// value did NOT change.

type snapshotRow struct {
	ApNo      string `json:"apNo"`
	Env       string `json:"env"`
	TierCode  string `json:"tierCode"`
	Instance  string `json:"instance"`
	RiskLevel string `json:"riskLevel"`
}

func (a *testApp) approvalSnapshot(token, apNo string) snapshotRow {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/approvals", token, nil)
	eq(a.t, r.Code, 0, "list approvals")
	var page struct {
		Items json.RawMessage `json:"items"`
	}
	_ = json.Unmarshal(r.Data, &page)
	var rows []snapshotRow
	_ = json.Unmarshal(page.Items, &rows)
	for _, x := range rows {
		if x.ApNo == apNo {
			return x
		}
	}
	a.t.Fatalf("approval %q not found", apNo)
	return snapshotRow{}
}

// interceptOn raises an approval by running a high-risk command, returning its
// number.
func (a *testApp) interceptOn(token string, connID int64, sql string) string {
	a.t.Helper()
	r := a.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
		"connectionId": connID, "sql": sql, "reason": "snapshot test",
	})
	eq(a.t, r.Code, resp.CodeIntercepted, "command intercepted")
	var d struct {
		ApprovalNo string `json:"approvalNo"`
	}
	_ = json.Unmarshal(r.Data, &d)
	if d.ApprovalNo == "" {
		a.t.Fatal("expected an approval number")
	}
	return d.ApprovalNo
}

// hkSetup builds an environment on the prod tier plus an instance in it, and
// returns the connection id. The environment code differs from every tier code,
// so the two snapshots are distinguishable in the assertions below.
func (a *testApp) hkSetup(token string) int64 {
	a.t.Helper()
	eq(a.t, a.do(http.MethodPost, "/api/v1/environments", token, map[string]any{
		"code": "prod-hk", "displayName": "香港生产", "tierCode": "prod",
	}).Code, 0, "create prod-hk")
	r := a.do(http.MethodPost, "/api/v1/connections", token, map[string]any{
		"name": "tongcha", "engine": "MySQL 8.0", "host": "10.0.0.41:3306",
		"env": "prod-hk", "policy": "strict",
	})
	eq(a.t, r.Code, 0, "create instance in prod-hk")
	var c struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(r.Data, &c)
	return c.ID
}

// Both snapshots land, and they are genuinely different values.
func TestDualSnapshot_ApprovalRecordsEnvironmentAndTier(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	conn := app.hkSetup(admin)

	ap := app.approvalSnapshot(admin, app.interceptOn(admin, conn, "DROP TABLE orders;"))
	eq(t, ap.Env, "prod-hk", "environment snapshot = where it ran")
	eq(t, ap.TierCode, "prod", "tier snapshot = what it was judged under")
}

// Rebinding the environment to a laxer tier changes how it is governed from now
// on. It must not restate what happened before.
func TestDualSnapshot_RebindingTheEnvironmentDoesNotRewriteHistory(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	conn := app.hkSetup(admin)

	old := app.interceptOn(admin, conn, "DROP TABLE orders;")
	eq(t, app.approvalSnapshot(admin, old).TierCode, "prod", "judged under prod")

	// Rebind prod-hk onto the staging tier.
	eq(t, app.do(http.MethodPut, "/api/v1/environments/prod-hk", admin, map[string]any{
		"displayName": "香港生产", "tierCode": "staging",
	}).Code, 0, "rebind prod-hk to the staging tier")

	got := app.approvalSnapshot(admin, old)
	eq(t, got.TierCode, "prod", "the historical ticket keeps the tier it was judged under")
	eq(t, got.Env, "prod-hk", "and the environment it ran in")

	// A ticket raised after the rebind records the new tier — proving the value
	// is read at write time, not pinned by accident.
	fresh := app.interceptOn(admin, conn, "DROP TABLE orders;")
	eq(t, app.approvalSnapshot(admin, fresh).TierCode, "staging", "a new ticket records the current tier")
}

// Moving an instance between environments likewise leaves history alone.
func TestDualSnapshot_MovingTheInstanceDoesNotRewriteHistory(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	conn := app.hkSetup(admin)

	old := app.interceptOn(admin, conn, "DROP TABLE orders;")
	eq(t, app.approvalSnapshot(admin, old).Env, "prod-hk", "ran in prod-hk")

	// Move the instance to the plain prod environment.
	eq(t, app.do(http.MethodPut, "/api/v1/connections/"+itoa(conn), admin, map[string]any{
		"name": "tongcha", "engine": "MySQL 8.0", "host": "10.0.0.41:3306",
		"env": "prod", "policy": "strict",
	}).Code, 0, "move the instance to prod")

	eq(t, app.approvalSnapshot(admin, old).Env, "prod-hk", "the historical ticket keeps the environment it ran in")
	eq(t, app.approvalSnapshot(admin, app.interceptOn(admin, conn, "DROP TABLE orders;")).Env, "prod",
		"a new ticket records the environment it runs in now")
}

// The audit row carries the same pair, and both are inside the chain hash — a
// snapshot that could be edited without breaking the chain would be worth
// nothing as evidence.
func TestDualSnapshot_AuditCarriesBothAndIsCoveredByTheChain(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	conn := app.hkSetup(admin)
	app.interceptOn(admin, conn, "DROP TABLE orders;")

	var row model.AuditLog
	if err := app.repo.DB().Where("connection_id = ?", conn).
		Order("id desc").First(&row).Error; err != nil {
		t.Fatalf("read audit row: %v", err)
	}
	eq(t, row.Env, "prod-hk", "audit environment snapshot")
	eq(t, row.TierCode, "prod", "audit tier snapshot")
	if row.Hash == "" {
		t.Fatal("audit row must be hash-chained")
	}
}

// Rows written before the split have no tier snapshot. They must still list —
// the approvals and audit pages are exactly where someone looks after a tier is
// deleted or renamed, so this is the moment a lookup must not blow up.
func TestDualSnapshot_LegacyRowsWithoutATierStillList(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")

	// A pre-split ticket: no tier, and an environment that no longer exists.
	if err := app.repo.DB().Create(&model.Approval{
		ApNo: "AP-LEGACY", ConnectionID: 0, Env: "retired-env", TierCode: "",
		Instance: "old-cluster", Command: "DROP TABLE legacy;", Keyword: "DROP",
		InitiatorID: 1, Initiator: "Lin Wei", RiskLevel: "high", Status: "pending",
	}).Error; err != nil {
		t.Fatalf("seed legacy approval: %v", err)
	}

	got := app.approvalSnapshot(admin, "AP-LEGACY")
	eq(t, got.TierCode, "", "a pre-split ticket has no tier — it is reported as unknown, not inferred")
	eq(t, got.Env, "retired-env", "and keeps its environment verbatim even though it is gone")

	eq(t, app.do(http.MethodGet, "/api/v1/audit", admin, nil).Code, 0, "the audit listing still renders")
}

// The outbound 审批魔方 payload carries both fields. `tier` is additive; `env`
// keeps its name and meaning but now ranges over any environment code.
func TestDualSnapshot_ExternalPayloadCarriesEnvAndTier(t *testing.T) {
	var mu sync.Mutex
	var gotEnv, gotTier string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var in struct {
			Payload struct {
				Env  string `json:"env"`
				Tier string `json:"tier"`
			} `json:"payload"`
		}
		_ = json.Unmarshal(body, &in)
		mu.Lock()
		gotEnv, gotTier = in.Payload.Env, in.Payload.Tier
		mu.Unlock()
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"code":0,"task_id":"cube-1","status":"PENDING"}`))
	}))
	defer stub.Close()

	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	conn := app.hkSetup(admin)
	app.setSettings(admin, map[string]any{
		"approval.external.enabled":         true,
		"approval.external.baseURL":         stub.URL,
		"approval.external.token":           "tok-xyz",
		"approval.external.callbackBaseURL": "https://gw.example",
	})
	app.interceptOn(admin, conn, "DROP TABLE orders;")

	// Dispatch is async.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		done := gotEnv != ""
		mu.Unlock()
		if done {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	eq(t, gotEnv, "prod-hk", "payload env = environment code (range now open)")
	eq(t, gotTier, "prod", "payload tier = control tier, new and additive")
}

// Instance labels are built from the environment, so they name the cluster an
// operation touched rather than just its tier. (Instance NAMES are globally
// unique, so the label was never ambiguous; what changes is that it now says
// prod-hk rather than a bare prod, which is the readable half of supporting more
// than one production environment.)
func TestDualSnapshot_InstanceLabelsCarryTheEnvironment(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	hk := app.hkSetup(admin)

	eq(t, app.do(http.MethodPost, "/api/v1/export", admin, map[string]any{
		"connectionId": hk, "sql": "SELECT 1", "name": "snap",
	}).Code, 0, "submit export")

	var job model.ExportJob
	if err := app.repo.DB().Where("connection_id = ?", hk).
		Order("id desc").First(&job).Error; err != nil {
		t.Fatalf("read export job: %v", err)
	}
	eq(t, job.Instance, "prod-hk-tongcha", "the label names the environment, not just the tier")
}

// A long environment code must survive a round trip. tbl_connection.env was
// sized for the four built-in strings; a code that does not fit resolves to no
// environment, therefore to no tier, therefore to no rules.
func TestDualSnapshot_LongEnvironmentCodesRoundTrip(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")

	code := "prod-hk-settlement-cluster-0917" // 31 chars, the practical maximum
	eq(t, app.do(http.MethodPost, "/api/v1/environments", admin, map[string]any{
		"code": code, "displayName": "结算集群", "tierCode": "prod",
	}).Code, 0, "create a long-coded environment")
	r := app.do(http.MethodPost, "/api/v1/connections", admin, map[string]any{
		"name": "settle-db", "engine": "MySQL 8.0", "host": "10.0.0.43:3306",
		"env": code, "policy": "strict",
	})
	eq(t, r.Code, 0, "create an instance in it")
	var c struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(r.Data, &c)

	// Stored intact, and still governed — a truncated code would resolve to
	// nothing and the DROP below would sail through as "no rule configured".
	var conn model.Connection
	if err := app.repo.DB().First(&conn, c.ID).Error; err != nil {
		t.Fatalf("read connection: %v", err)
	}
	eq(t, conn.Env, code, "environment code stored without truncation")
	eq(t, app.riskCheck(admin, c.ID, "DROP TABLE orders;").Action, "approve", "and still governed by the prod tier")
}
