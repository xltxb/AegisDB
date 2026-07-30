package bootstrap

import (
	"strings"

	"github.com/gin-gonic/gin"
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

// postLarkCallback POSTs the审批魔方 callback with an Authorization: Bearer secret
// and returns the decoded envelope + HTTP status.
func (a *testApp) postLarkCallback(secret string, body any) (apiResp, int) {
	return a.postLarkCallbackAt(a.srv.URL+"/api/v1/approvals/lark/callback", secret, body)
}

// postLarkCallbackAt POSTs to an explicit URL (used to test the ?secret= form),
// sending the secret as Authorization: Bearer only when provided.
func (a *testApp) postLarkCallbackAt(url, bearerSecret string, body any) (apiResp, int) {
	a.t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if bearerSecret != "" {
		req.Header.Set("Authorization", "Bearer "+bearerSecret)
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

// A vendor that can't send custom headers (e.g. 审批魔方) authenticates via the
// secret embedded in the callback URL (?secret=...); it must be accepted.
func TestExternalApproval_CallbackSecretViaQueryParam(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	app.setSettings(token, map[string]any{"approval.external.enabled": true, "approval.external.callbackSecret": "s3cr3t"})
	ap := app.submitProdHighRisk(token)

	url := app.srv.URL + "/api/v1/approvals/lark/callback?secret=s3cr3t"
	env, _ := app.postLarkCallbackAt(url, "", map[string]any{ // no header, secret in URL
		"external_task_id": ap.ApNo, "approved": true, "approver": []string{"herbert@tbu.net"},
	})
	eq(t, env.Code, 0, "query-param secret accepted")
	eq(t, app.approvalRow(token, ap.ApNo).Status, "approved", "approved via URL secret")

	// A wrong URL secret is still rejected.
	ap2 := app.submitProdHighRisk(token)
	badEnv, _ := app.postLarkCallbackAt(app.srv.URL+"/api/v1/approvals/lark/callback?secret=nope", "",
		map[string]any{"external_task_id": ap2.ApNo, "approved": true})
	eq(t, badEnv.Code, resp.CodeForbidden, "wrong URL secret rejected")
	eq(t, app.approvalRow(token, ap2.ApNo).Status, "pending", "wrong secret leaves ticket pending")
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
	app.setSettings(token, map[string]any{"approval.external.enabled": true, "approval.external.callbackSecret": "s3cr3t"})

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
	app.setSettings(token, map[string]any{"approval.external.enabled": true, "approval.external.callbackSecret": "s3cr3t"})
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
	app.setSettings(token, map[string]any{"approval.external.enabled": true, "approval.external.callbackSecret": "s3cr3t"})
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

// EA1: `approval.external.enabled` is the operator's switch for the whole
// integration, and the outbound side honours it (dispatch and cancel both bail
// when it is off). The inbound side never read it, so a stored callbackSecret
// left the endpoint fully live after the feature was switched off — anyone
// holding that secret could still drive a PROD command to execution. Turning the
// feature off has to close the door it opened.
func TestExternalApproval_CallbackRefusedWhenFeatureDisabled(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	app.setSettings(token, map[string]any{
		"approval.external.enabled":        false,
		"approval.external.callbackSecret": "s3cr3t",
	})
	ap := app.submitProdHighRisk(token)

	env, _ := app.postLarkCallback("s3cr3t", map[string]any{
		"external_task_id": ap.ApNo, "approved": true, "approver": []string{"herbert@tbu.net"},
	})
	eq(t, env.Code, resp.CodeForbidden, "callback refused while the feature is disabled")
	eq(t, app.approvalRow(token, ap.ApNo).Status, "pending", "ticket untouched")
}

// EA2: correlation runs on our own ApNo, which is a predictable running counter
// (AP-2295, AP-2296, …) that the submitter sees in their own exec response. The
// vendor's task_id was accepted but never checked against the one we recorded
// for that ticket, so a caller holding the callback secret could decide a ticket
// while quoting an unrelated task. When we know the vendor task for a ticket, a
// callback naming a different one is not a decision about this ticket.
func TestExternalApproval_CallbackRejectsMismatchedVendorTask(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	app.setSettings(token, map[string]any{"approval.external.enabled": true, "approval.external.callbackSecret": "s3cr3t"})
	ap := app.submitProdHighRisk(token)

	// Record the vendor task this ticket was dispatched as.
	if err := app.repo.SetApprovalExternalTask(app.approvalIDByNo(ap.ApNo), "vendor-task-1"); err != nil {
		t.Fatalf("set external task: %v", err)
	}

	env, _ := app.postLarkCallback("s3cr3t", map[string]any{
		"external_task_id": ap.ApNo, "task_id": "vendor-task-OTHER",
		"approved": true, "approver": []string{"herbert@tbu.net"},
	})
	eq(t, env.Code, resp.CodeForbidden, "callback quoting another vendor task refused")
	eq(t, app.approvalRow(token, ap.ApNo).Status, "pending", "ticket untouched")

	// The matching task id still decides it.
	okEnv, _ := app.postLarkCallback("s3cr3t", map[string]any{
		"external_task_id": ap.ApNo, "task_id": "vendor-task-1",
		"approved": true, "approver": []string{"herbert@tbu.net"},
	})
	eq(t, okEnv.Code, 0, "matching vendor task accepted")
	eq(t, app.approvalRow(token, ap.ApNo).Status, "approved", "decided by the right task")
}

// approvalIDByNo resolves an ApNo to its row id for direct-store assertions.
func (a *testApp) approvalIDByNo(apNo string) int64 {
	a.t.Helper()
	ap, err := a.repo.GetApprovalByApNo(apNo)
	if err != nil || ap == nil {
		a.t.Fatalf("approval %q not found: %v", apNo, err)
	}
	return ap.ID
}

// EA3: 审批魔方 cannot send custom headers, so the callback secret may travel in
// the URL (see TestExternalApproval_CallbackSecretViaQueryParam) — that form has
// to keep working. What must NOT happen is the access log recording it: gin's
// default formatter writes path+rawQuery, so every callback printed a working
// approve-anything credential into the log, readable by anyone with log access
// and never rotated. The second round fixed exactly this shape for ?token= on
// the WS route; the query secret reintroduced it on a higher-privilege endpoint.
func TestExternalApproval_CallbackSecretIsNotWrittenToAccessLog(t *testing.T) {
	var logBuf bytes.Buffer
	prev := gin.DefaultWriter
	gin.DefaultWriter = &logBuf
	t.Cleanup(func() { gin.DefaultWriter = prev })

	app := newTestApp(t) // router captures DefaultWriter at construction
	token := app.login("linwei@vela.io", "vela123")
	app.setSettings(token, map[string]any{"approval.external.enabled": true, "approval.external.callbackSecret": "s3cr3t"})
	ap := app.submitProdHighRisk(token)

	url := app.srv.URL + "/api/v1/approvals/lark/callback?secret=s3cr3t"
	env, _ := app.postLarkCallbackAt(url, "", map[string]any{
		"external_task_id": ap.ApNo, "approved": true, "approver": []string{"herbert@tbu.net"},
	})
	eq(t, env.Code, 0, "URL-secret callback still accepted")

	if strings.Contains(logBuf.String(), "s3cr3t") {
		t.Errorf("the callback secret was written to the access log:\n%s", logBuf.String())
	}
	// The request itself must still be logged (we only redact the credential).
	if !strings.Contains(logBuf.String(), "/api/v1/approvals/lark/callback") {
		t.Errorf("callback request missing from the access log:\n%s", logBuf.String())
	}
}

// EA4: finalizeApproval is shared by the in-app and the external decision paths,
// and it attributes the audit row to the ticket's INITIATOR — on the reasoning
// (recorded in an earlier round) that the approver is already captured in
// tbl_approval_step. External approval breaks that premise: the person who
// approved in 飞书 is not a gateway user, so they appear in neither the step rows
// nor the hash chain. The immutable record then says only "the initiator ran a
// DROP", with no way to identify who authorised it.
func TestExternalApproval_AuditIdentifiesTheExternalApprover(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	app.setSettings(token, map[string]any{"approval.external.enabled": true, "approval.external.callbackSecret": "s3cr3t"})
	ap := app.submitProdHighRisk(token)

	env, _ := app.postLarkCallback("s3cr3t", map[string]any{
		"external_task_id": ap.ApNo, "approved": true, "reason": "同意",
		"approver": []string{"herbert@tbu.net"},
	})
	eq(t, env.Code, 0, "callback accepted")

	var rows []struct {
		ApprovalNo string `json:"approvalNo"`
		Actor      string `json:"actor"`
		Operator   string `json:"operator"`
		Result     string `json:"result"`
	}
	if err := json.Unmarshal(app.auditItemsRaw(token, ""), &rows); err != nil {
		t.Fatalf("audit decode: %v", err)
	}
	found := false
	for _, r := range rows {
		if r.ApprovalNo == ap.ApNo && r.Result == "executed" {
			found = true
			if !strings.Contains(r.Operator, "herbert@tbu.net") {
				t.Errorf("audit does not name the approver who authorised the command: operator=%q actor=%q", r.Operator, r.Actor)
			}
		}
	}
	if !found {
		t.Fatalf("no executed audit row for %s", ap.ApNo)
	}
}
