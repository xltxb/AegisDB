package service

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"velagateway/internal/model"
)

// A high-risk command goes to 审批魔方 — a third-party service that stores it,
// renders it into a chat card and keeps its own history. It is the sink where a
// leaked credential is furthest outside our control and least revocable: we
// cannot delete it there, and we cannot know who read it.
//
// This request went out with the password in the clear. Not because redaction
// was wrong here, but because it was never applied on this path at all — the
// interactive card built elsewhere in the same file did redact, which is exactly
// what made the gap invisible. So the guard is placed on the WIRE rather than on
// the helper: it asserts about the bytes actually sent, and it would fail for any
// future field added to the payload that carries the command.

const testSecret = "X8wr^J+iu3n!L9cL"

func approvalWithSecret() *model.Approval {
	return &model.Approval{
		ApNo:      "AP-9001",
		Env:       "prod-hk",
		TierCode:  "prod",
		Instance:  "orders-primary",
		Database:  "orders",
		Initiator: "Lin Wei",
		Reason:    "新增只读运维账号",
		RiskLevel: model.RiskHigh,
		Command:   `create user 'db_opt'@'%' IDENTIFIED WITH 'mysql_native_password' by '` + testSecret + `' password expire never`,
	}
}

// captureExternalApproval posts one approval at a local stub and returns the raw
// request body it received.
func captureExternalApproval(t *testing.T, ap *model.Approval) []byte {
	t.Helper()

	var got []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"data":{"task_id":"T-1"}}`))
	}))
	defer srv.Close()

	// The stub is on loopback, which the outbound SSRF guard refuses by default.
	orig := AllowPrivateWebhookTargets
	AllowPrivateWebhookTargets = true
	defer func() { AllowPrivateWebhookTargets = orig }()

	d := NewDispatcher(nil)
	if _, err := d.SendExternalApproval(srv.URL, "token", "group", "https://cb.example/hook", "linwei", ap); err != nil {
		t.Fatalf("send external approval: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("the stub received no request body")
	}
	return got
}

func TestExternalApproval_DoesNotSendThePasswordAnywhereInTheRequest(t *testing.T) {
	body := captureExternalApproval(t, approvalWithSecret())

	// Whole-body check on purpose: the command appeared in TWO places (the summary
	// text and payload.command), and asserting field by field is how the second one
	// was missed. Anything added later that carries the command is covered too.
	if strings.Contains(string(body), testSecret) {
		t.Fatalf("the password was sent to the external approval service:\n%s", body)
	}
	if !strings.Contains(string(body), "'***'") {
		t.Errorf("expected the credential to be masked in the request, got:\n%s", body)
	}
}

// The approver still has to be able to judge the request. Masking the secret must
// not cost them the statement, the account it creates, or where it lands.
func TestExternalApproval_StillCarriesWhatTheApproverNeeds(t *testing.T) {
	body := captureExternalApproval(t, approvalWithSecret())

	var sent struct {
		Messages []struct{ Content string } `json:"messages"`
		Payload  struct {
			Command  string `json:"command"`
			Env      string `json:"env"`
			Tier     string `json:"tier"`
			Instance string `json:"instance"`
			Reason   string `json:"reason"`
			ApNo     string `json:"apNo"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatalf("decode sent body: %v", err)
	}

	for _, want := range []string{"create user", "'db_opt'@'%'", "mysql_native_password"} {
		if !strings.Contains(sent.Payload.Command, want) {
			t.Errorf("payload.command lost %q — the approver cannot see what they are approving: %q", want, sent.Payload.Command)
		}
	}
	if !strings.Contains(sent.Payload.Command, "'***'") {
		t.Errorf("payload.command is not masked: %q", sent.Payload.Command)
	}
	if len(sent.Messages) == 0 || !strings.Contains(sent.Messages[0].Content, "orders-primary") {
		t.Error("the summary should still name the instance")
	}
	if sent.Payload.Env != "prod-hk" || sent.Payload.Tier != "prod" || sent.Payload.ApNo != "AP-9001" {
		t.Errorf("routing fields changed: %+v", sent.Payload)
	}
}

// Excerpts are shortened for display. Shortening BEFORE masking cuts the closing
// quote off the literal, the pattern no longer matches, and most of the password
// rides along inside the excerpt. safeClip fixes the order; this pins it.
func TestSafeClip_MasksBeforeShortening(t *testing.T) {
	cmd := `create user 'db_opt'@'%' IDENTIFIED WITH 'mysql_native_password' by '` + testSecret + `' password expire never`

	// A length that lands inside the password literal — the dangerous case.
	for _, n := range []int{60, 70, 75, 80, 90, 120} {
		if out := safeClip(cmd, n); strings.Contains(out, testSecret) {
			t.Errorf("safeClip(%d) leaked the secret: %q", n, out)
		}
	}
	// Sanity: doing it the wrong way round really does leak, so the test above is
	// testing something.
	if !strings.Contains(clip(cmd, 75), testSecret[:4]) {
		t.Skip("clip boundary moved; the ordering example no longer demonstrates the trap")
	}
}
