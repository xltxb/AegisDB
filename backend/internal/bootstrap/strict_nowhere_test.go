package bootstrap

import (
	"net/http"
	"testing"

	"velagateway/internal/model"
	"velagateway/pkg/resp"
)

// tierStrict flips one tier's no-WHERE gate through the admin API.
func (a *testApp) tierStrict(token, code string, on bool) {
	a.t.Helper()
	var t model.EnvTier
	if err := a.repo.DB().First(&t, "code = ?", code).Error; err != nil {
		a.t.Fatalf("tier %s: %v", code, err)
	}
	r := a.do(http.MethodPut, "/api/v1/env-tiers/"+code, token, map[string]any{
		"displayName": t.DisplayName, "sortOrder": t.SortOrder,
		"requireMfa": t.RequireMFA, "dangerBanner": t.DangerBanner,
		"countsInPending": t.CountsInPending, "scanBaseline": t.ScanBaseline,
		"strictNoWhere": on, "connLayer": t.ConnLayer, "defaultRole": t.DefaultRole,
	})
	if r.Code != 0 {
		a.t.Fatalf("set strictNoWhere=%v on %s: code=%d msg=%s", on, code, r.Code, r.Msg)
	}
}

// The no-WHERE gate is the third judgement layer, and it used to be the only one
// with no tier dimension: one process-wide flag covering every environment. DEV
// deliberately seeds its whole dictionary `off` so a developer can empty a
// scratch table, yet strict mode still graded that DELETE high and demanded an
// approval — and the only way to stop it was to switch the layer off for PROD
// too. The flag now lives on the tier, like the two layers above it.
func TestStrictNoWhere_IsPerTier(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	dev := app.connIDByEnv(token, "dev")
	prod := app.connIDByEnv(token, "prod")

	// Seeded state: PROD gates a full-table write, DEV does not.
	if got := app.riskCheck(token, prod, "DELETE FROM orders"); !got.RequiresApproval {
		t.Errorf("PROD must gate a no-WHERE DELETE, got action=%s", got.Action)
	}
	if got := app.riskCheck(token, dev, "DELETE FROM orders"); got.RequiresApproval {
		t.Errorf("DEV seeds the gate off, so a no-WHERE DELETE must run: action=%s rule=%s", got.Action, got.Command)
	}
	// A scoped DELETE was never the point and stays allowed on DEV.
	if got := app.riskCheck(token, dev, "DELETE FROM orders WHERE id = 1"); got.RequiresApproval {
		t.Errorf("scoped DELETE on DEV must be allowed, got %s", got.Action)
	}

	// Turning it on for DEV gates it there, and PROD is untouched.
	app.tierStrict(token, "dev", true)
	if got := app.riskCheck(token, dev, "UPDATE orders SET status='x'"); !got.RequiresApproval {
		t.Errorf("DEV with the gate on must demand approval, got %s", got.Action)
	}

	// Turning it off for PROD is the move that was impossible before. The signal
	// is the GRADE, not the gating: a PROD UPDATE is sent to approval by the
	// capability matrix either way (write=approve), and it is the strict layer
	// that lifts that mid to high. With the layer off, the matrix verdict is what
	// should remain.
	if got := app.riskCheck(token, prod, "UPDATE orders SET status='x'"); got.Risk != model.RiskHigh {
		t.Errorf("PROD with the gate on grades a no-WHERE UPDATE high, got %s", got.Risk)
	}
	app.tierStrict(token, "prod", false)
	got := app.riskCheck(token, prod, "UPDATE orders SET status='x'")
	if got.Risk != model.RiskMid {
		t.Errorf("PROD with the gate off falls back to the matrix verdict (mid), got %s", got.Risk)
	}
	if !got.RequiresApproval {
		t.Error("the capability matrix still sends a PROD UPDATE to approval")
	}
	if got := app.riskCheck(token, dev, "UPDATE orders SET status='x'"); got.Risk != model.RiskHigh {
		t.Errorf("switching PROD off must not switch DEV off with it, DEV graded %s", got.Risk)
	}
	// The dictionary is a separate layer and still governs PROD.
	r := app.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
		"connectionId": prod, "sql": "DROP TABLE orders_2024_q3", "reason": "dictionary still applies",
	})
	eq(t, r.Code, resp.CodeIntercepted, "dictionary keeps gating PROD with strict off")
}

// A tier created with the gate switched OFF must stay off. GORM omits a
// zero-valued field that carries a `default` tag and then writes the applied
// default back into the struct, so both the stored row and the response used to
// come back with the gate ON: the switch looked saved and was not.
func TestStrictNoWhere_SurvivesTierCreation(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")

	r := app.do(http.MethodPost, "/api/v1/env-tiers", token, map[string]any{
		"code": "sandbox", "displayName": "沙盒", "templateCode": "dev",
		"sortOrder": 9, "strictNoWhere": false, "connLayer": "L4", "defaultRole": "developer",
	})
	eq(t, r.Code, 0, "create tier with the gate off")

	var got model.EnvTier
	if err := app.repo.DB().First(&got, "code = ?", "sandbox").Error; err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.StrictNoWhere {
		t.Error("a tier created with the no-WHERE gate off came back with it on")
	}
}
