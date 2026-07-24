package bootstrap

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"velagateway/pkg/resp"
)

// apStatusRow mirrors the GET /approvals view fields this suite asserts on.
type apStatusRow struct {
	ApNo           string `json:"apNo"`
	Status         string `json:"status"`
	ExternalTaskID string `json:"externalTaskId"`
}

func (a *testApp) approvalRow(token, apNo string) apStatusRow {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/approvals", token, nil)
	eq(a.t, r.Code, 0, "list approvals code")
	var aps []apStatusRow
	_ = json.Unmarshal(r.Data, &aps)
	for _, ap := range aps {
		if ap.ApNo == apNo {
			return ap
		}
	}
	a.t.Fatalf("approval %q not found", apNo)
	return apStatusRow{}
}

// postLarkCallback POSTs the审批魔方 callback with an X-Callback-Secret header and
// returns the decoded envelope + HTTP status.
func (a *testApp) postLarkCallback(secret string, body any) (apiResp, int) {
	a.t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, a.srv.URL+"/api/v1/approvals/lark/callback", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if secret != "" {
		req.Header.Set("X-Callback-Secret", secret)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatalf("callback: %v", err)
	}
	defer res.Body.Close()
	var out apiResp
	_ = json.NewDecoder(res.Body).Decode(&out)
	return out, res.StatusCode
}

func (a *testApp) setSettings(token string, kv map[string]any) {
	a.t.Helper()
	eq(a.t, a.do(http.MethodPut, "/api/v1/settings", token, kv).Code, 0, "save settings")
}

// A callback with a valid secret drives the ticket to a terminal state: approve
// executes the command (status approved), reject marks it rejected — reusing the
// same decision core as the in-app path, correlated by ApNo (external_task_id).
func TestExternalApproval_CallbackApproveAndReject(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	app.setSettings(token, map[string]any{"approval.external.callbackSecret": "s3cr3t"})

	// approve path
	ap := app.submitProdHighRisk(token)
	env, code := app.postLarkCallback("s3cr3t", map[string]any{
		"external_task_id": ap.ApNo, "approved": true, "reason": "同意",
		"approver": []string{"herbert@tbu.net"}, "message_id": "om_abc",
	})
	eq(t, code, 200, "callback http status")
	eq(t, env.Code, 0, "callback envelope code")
	eq(t, app.approvalRow(token, ap.ApNo).Status, "approved", "approve → status approved")

	// reject path (a fresh ticket)
	ap2 := app.submitProdHighRisk(token)
	app.postLarkCallback("s3cr3t", map[string]any{
		"external_task_id": ap2.ApNo, "approved": false, "reason": "不同意",
		"approver": []string{"herbert@tbu.net"},
	})
	eq(t, app.approvalRow(token, ap2.ApNo).Status, "rejected", "reject → status rejected")
}

// The callback fails closed on bad auth, 400s an unknown ticket, and is idempotent
// (a repeat on a decided ticket returns its status without re-executing).
func TestExternalApproval_CallbackAuthAndIdempotency(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	app.setSettings(token, map[string]any{"approval.external.callbackSecret": "s3cr3t"})
	ap := app.submitProdHighRisk(token)

	// wrong / missing secret → forbidden, ticket untouched
	envBad, _ := app.postLarkCallback("wrong", map[string]any{"external_task_id": ap.ApNo, "approved": true})
	eq(t, envBad.Code, resp.CodeForbidden, "wrong secret forbidden")
	envNone, _ := app.postLarkCallback("", map[string]any{"external_task_id": ap.ApNo, "approved": true})
	eq(t, envNone.Code, resp.CodeForbidden, "missing secret forbidden")
	eq(t, app.approvalRow(token, ap.ApNo).Status, "pending", "ticket still pending after bad auth")

	// unknown ticket → business error (400)
	env, _ := app.postLarkCallback("s3cr3t", map[string]any{"external_task_id": "AP-doesnotexist", "approved": true})
	eq(t, env.Code, resp.CodeBadRequest, "unknown ticket rejected")

	// approve once, then a repeat callback is idempotent (still approved)
	app.postLarkCallback("s3cr3t", map[string]any{"external_task_id": ap.ApNo, "approved": true, "approver": []string{"x@vela.io"}})
	eq(t, app.approvalRow(token, ap.ApNo).Status, "approved", "first approve")
	env2, code2 := app.postLarkCallback("s3cr3t", map[string]any{"external_task_id": ap.ApNo, "approved": false, "approver": []string{"x@vela.io"}})
	eq(t, code2, 200, "repeat callback ok")
	eq(t, env2.Code, 0, "repeat callback envelope ok")
	eq(t, app.approvalRow(token, ap.ApNo).Status, "approved", "repeat does not flip a decided ticket")
}

// SoD net: when every approver is the initiator themselves and self-approve is
// off, the gateway blocks it even though审批魔方 doesn't enforce non-initiator.
func TestExternalApproval_SelfApproveBlocked(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	app.setSettings(token, map[string]any{"approval.external.callbackSecret": "s3cr3t"})
	ap := app.submitProdHighRisk(token) // initiator = linwei@vela.io

	app.postLarkCallback("s3cr3t", map[string]any{
		"external_task_id": ap.ApNo, "approved": true,
		"approver": []string{"linwei@vela.io"}, // self-approval
	})
	eq(t, app.approvalRow(token, ap.ApNo).Status, "rejected", "self-approval blocked → rejected")
}

// Phase 2 功能 B: when an externally-dispatched ticket is auto-rejected by the
// internal timeout sweep, the gateway PATCHes审批魔方 to cancel (approval_status
// 2) the still-open card — keyed by the vendor task_id, Bearer-authenticated.
func TestExternalApproval_TimeoutCancelsExternalCard(t *testing.T) {
	var mu sync.Mutex
	var patchPath, patchAuth string
	var patchStatus int
	patchStatus = -1
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost { // create-approval
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"code":0,"task_id":"cube-77","status":"PENDING"}`))
			return
		}
		// PATCH /api/v1/approvals/{id}/status
		body, _ := io.ReadAll(r.Body)
		var in struct {
			ApprovalStatus int `json:"approval_status"`
		}
		_ = json.Unmarshal(body, &in)
		mu.Lock()
		patchPath, patchAuth, patchStatus = r.URL.Path, r.Header.Get("Authorization"), in.ApprovalStatus
		mu.Unlock()
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"code":0,"msg":"success","status":"CANCELLED"}`))
	}))
	defer stub.Close()

	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	app.setSettings(token, map[string]any{
		"approval.external.enabled":         true,
		"approval.external.baseURL":         stub.URL,
		"approval.external.token":           "tok-xyz",
		"approval.external.callbackBaseURL": "https://gw.example",
		"approval.onTimeout":                "auto-reject",
		"approval.timeoutMinutes":           0,
	})
	ap := app.submitProdHighRisk(token)

	// wait until the async outbound stored the vendor task_id, then time it out.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && app.approvalRow(token, ap.ApNo).ExternalTaskID == "" {
		time.Sleep(50 * time.Millisecond)
	}
	app.svc.SweepApprovalTimeouts()

	// poll for the PATCH cancel to land (async best-effort).
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		done := patchStatus != -1
		mu.Unlock()
		if done {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	eq(t, patchPath, "/api/v1/approvals/cube-77/status", "PATCH keyed by vendor task_id")
	eq(t, patchStatus, 2, "approval_status 2 = cancel")
	eq(t, patchAuth, "Bearer tok-xyz", "PATCH uses Bearer token")
}

// When enabled, building an approval dispatches it to审批魔方 (Bearer auth,
// external_task_id = ApNo) and stores the returned vendor task_id.
func TestExternalApproval_OutboundDispatchStoresTaskID(t *testing.T) {
	var mu sync.Mutex
	var gotAuth, gotExtID string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var in struct {
			ExternalTaskID string `json:"external_task_id"`
		}
		_ = json.Unmarshal(body, &in)
		mu.Lock()
		gotAuth = r.Header.Get("Authorization")
		gotExtID = in.ExternalTaskID
		mu.Unlock()
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"code":0,"task_id":"cube-123","status":"PENDING"}`))
	}))
	defer stub.Close()

	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	app.setSettings(token, map[string]any{
		"approval.external.enabled":         true,
		"approval.external.baseURL":         stub.URL,
		"approval.external.token":           "tok-xyz",
		"approval.external.callbackBaseURL": "https://gw.example",
	})
	ap := app.submitProdHighRisk(token)

	// dispatch is async — poll until the vendor task_id is stored.
	deadline := time.Now().Add(5 * time.Second)
	var stored string
	for time.Now().Before(deadline) {
		if stored = app.approvalRow(token, ap.ApNo).ExternalTaskID; stored != "" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	eq(t, stored, "cube-123", "vendor task_id stored on the approval")
	mu.Lock()
	defer mu.Unlock()
	eq(t, gotAuth, "Bearer tok-xyz", "outbound uses Bearer token")
	eq(t, gotExtID, ap.ApNo, "outbound external_task_id = ApNo")
}
