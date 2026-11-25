package bootstrap

import (
	"encoding/json"
	"net/http"
	"testing"
)

// auditPage decodes the paginated audit envelope.
type auditPage struct {
	Items    []map[string]any `json:"items"`
	Total    int64            `json:"total"`
	Page     int              `json:"page"`
	PageSize int              `json:"pageSize"`
}

func (a *testApp) auditPage(token, query string) auditPage {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/audit"+query, token, nil)
	eq(a.t, r.Code, 0, "audit page code")
	var p auditPage
	if err := json.Unmarshal(r.Data, &p); err != nil {
		a.t.Fatalf("decode audit page: %v", err)
	}
	return p
}

// The audit endpoint paginates: it returns a {items,total,page,pageSize} envelope,
// honours the pageSize cap, and exposes the same total across pages so the UI can
// render page counts. It also filters by an absolute time window (from/to).
func TestAudit_PaginationAndAbsoluteWindow(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	prod := app.connIDByEnv(token, "prod")

	// Generate a few audit rows deterministically.
	for i := 0; i < 3; i++ {
		app.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
			"connectionId": prod, "sql": "SELECT 1;", "reason": "ping",
		})
	}

	// Page 1 with pageSize=1 must return exactly one item and the real total.
	first := app.auditPage(token, "?pageSize=1&page=1")
	eq(t, first.PageSize, 1, "pageSize echoed")
	eq(t, first.Page, 1, "page echoed")
	if first.Total < 3 {
		t.Fatalf("expected total >= 3 audit rows, got %d", first.Total)
	}
	if len(first.Items) != 1 {
		t.Errorf("pageSize=1 should return 1 item, got %d", len(first.Items))
	}

	// A page past the end returns no items but the same total.
	beyond := app.auditPage(token, "?pageSize=1&page=99999")
	eq(t, beyond.Total, first.Total, "total stable across pages")
	if len(beyond.Items) != 0 {
		t.Errorf("page past the end should be empty, got %d items", len(beyond.Items))
	}

	// Default pageSize is 100 (the product requirement) when unspecified.
	def := app.auditPage(token, "")
	eq(t, def.PageSize, 100, "default page size is 100")

	// Absolute window: a future lower bound excludes everything.
	future := app.auditPage(token, "?from=2999-01-01")
	eq(t, future.Total, int64(0), "future 'from' matches nothing")

	// A wide absolute window covers all rows.
	wide := app.auditPage(token, "?from=2000-01-01&to=2999-01-01")
	eq(t, wide.Total, first.Total, "wide window covers all rows")
}
