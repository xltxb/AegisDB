package lark

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"velagateway/internal/imchannel/lark/larktest"
)

func webhookApp(t *testing.T, srv *larktest.Server, extra kv) *App {
	t.Helper()
	s := kv{"im.lark.mode": "webhook", "im.lark.encryptKey": srv.EncryptKey, "im.lark.verificationToken": srv.VerificationToken}
	for k, v := range extra {
		s[k] = v
	}
	return newTestApp(t, srv, s)
}

func encrypted(key string, plain []byte) []byte {
	b, _ := json.Marshal(map[string]string{"encrypt": larktest.Encrypt(key, plain)})
	return b
}

func TestHandleWebhook_RefusesUntilFullyConfigured(t *testing.T) {
	srv := larktest.New(t)
	h, body := srv.Signed(srv.CardAction("ou_1", map[string]any{"ap": "AP-1", "act": "a"}))
	ctx := context.Background()
	if r := newTestApp(t, srv, nil).HandleWebhook(ctx, h, body, &recSink{}); r.Status != http.StatusForbidden {
		t.Fatalf("long-connection mode must refuse webhooks, got %d", r.Status)
	}
	if r := webhookApp(t, srv, kv{"im.lark.encryptKey": ""}).HandleWebhook(ctx, h, body, &recSink{}); r.Status != http.StatusForbidden {
		t.Fatalf("no encrypt key → 403, got %d", r.Status)
	}
	if r := webhookApp(t, srv, kv{"im.lark.verificationToken": ""}).HandleWebhook(ctx, h, body, &recSink{}); r.Status != http.StatusForbidden {
		t.Fatalf("no verification token → 403, got %d", r.Status)
	}
}

func TestHandleWebhook_AnswersTheURLChallenge(t *testing.T) {
	srv := larktest.New(t)
	a := webhookApp(t, srv, nil)
	r := a.HandleWebhook(context.Background(), http.Header{}, encrypted(srv.EncryptKey, srv.Challenge(srv.VerificationToken)), &recSink{})
	if r.Status != http.StatusOK || string(r.Body) != `{"challenge":"c-123"}` {
		t.Fatalf("challenge reply = %d %s", r.Status, r.Body)
	}
	r = a.HandleWebhook(context.Background(), http.Header{}, encrypted(srv.EncryptKey, srv.Challenge("nope")), &recSink{})
	if r.Status != http.StatusForbidden {
		t.Fatalf("a challenge with the wrong token → 403, got %d", r.Status)
	}
}

// URL 校验不带签名,是唯一一条只凭 Token 就能拿到 200 的路。别让一个夹带了事件的请求、或一段
// 任意内容的 challenge 借着它过关。
func TestHandleWebhook_AnUnsignedChallengeCarriesNothingElse(t *testing.T) {
	srv := larktest.New(t)
	a := webhookApp(t, srv, nil)
	sink := &recSink{}
	challenge := func(extra map[string]any) []byte {
		m := map[string]any{"type": "url_verification", "challenge": "c-123", "token": srv.VerificationToken}
		for k, v := range extra {
			m[k] = v
		}
		b, _ := json.Marshal(m)
		return encrypted(srv.EncryptKey, b)
	}
	uuidLike := "3f1f7c2e-9a4b-4c1d-8e2f-0a1b2c3d4e5f"
	if r := a.HandleWebhook(context.Background(), http.Header{}, challenge(map[string]any{"challenge": uuidLike}), sink); r.Status != http.StatusOK {
		t.Fatalf("a real (UUID-like) challenge must pass, got %d", r.Status)
	}
	for name, extra := range map[string]map[string]any{
		"with a header":      {"header": map[string]any{"token": srv.VerificationToken, "event_type": "card.action.trigger"}},
		"with a schema":      {"schema": "2.0"},
		"with an event":      {"event": map[string]any{"operator": map[string]any{"open_id": "ou_1"}}},
		"an empty challenge": {"challenge": ""},
		"a long challenge":   {"challenge": strings.Repeat("a", 129)},
		"markup in it":       {"challenge": "<script>alert(1)</script>"},
	} {
		r := a.HandleWebhook(context.Background(), http.Header{}, challenge(extra), sink)
		if r.Status != http.StatusForbidden || len(r.Body) != 0 {
			t.Errorf("%s → %d %q, want 403 with an empty body", name, r.Status, r.Body)
		}
	}
	if n, m := sink.counts(); n+m != 0 {
		t.Fatal("nothing may reach the sink through the challenge path")
	}
}

func TestHandleWebhook_AClickNeedsEncryptionTokenAndSignature(t *testing.T) {
	srv := larktest.New(t)
	a := webhookApp(t, srv, nil)
	sink := &recSink{}
	ctx := context.Background()
	plain := srv.CardAction("ou_1", map[string]any{"ap": "AP-1", "act": "a"})

	if r := a.HandleWebhook(ctx, http.Header{}, plain, sink); r.Status != http.StatusForbidden {
		t.Fatalf("plaintext → 403, got %d", r.Status)
	}
	h, body := srv.Signed(plain)
	if r := a.HandleWebhook(ctx, http.Header{}, body, sink); r.Status != http.StatusForbidden {
		t.Fatalf("no signature → 403, got %d", r.Status)
	}
	bad := h.Clone()
	bad.Set("X-Lark-Signature", strings.Repeat("0", 64))
	if r := a.HandleWebhook(ctx, bad, body, sink); r.Status != http.StatusForbidden {
		t.Fatalf("wrong signature → 403, got %d", r.Status)
	}
	if r := a.HandleWebhook(ctx, h, []byte(`{"encrypt":"bm90IGEgcmVhbCBjaXBoZXJ0ZXh0IQ=="}`), sink); r.Status != http.StatusForbidden {
		t.Fatalf("tampered ciphertext → 403, got %d", r.Status)
	}
	srv.VerificationToken = "someone-elses-token"
	wrongTok, wrongBody := srv.Signed(srv.CardAction("ou_1", map[string]any{"ap": "AP-1", "act": "a"}))
	srv.VerificationToken = "test-verification-token"
	if r := a.HandleWebhook(ctx, wrongTok, wrongBody, sink); r.Status != http.StatusForbidden {
		t.Fatalf("wrong verification token → 403, got %d", r.Status)
	}
	if n, _ := sink.counts(); n != 0 {
		t.Fatal("nothing refused may reach the sink")
	}

	r := a.HandleWebhook(ctx, h, body, sink)
	if r.Status != http.StatusOK || !strings.Contains(string(r.Body), "ok ou_1 AP-1 true") {
		t.Fatalf("good click → %d %s", r.Status, r.Body)
	}
}

func TestHandleWebhook_AnEventItDoesNotHandleIsStillAcknowledged(t *testing.T) {
	srv := larktest.New(t)
	a := webhookApp(t, srv, nil)
	plain, _ := json.Marshal(map[string]any{"schema": "2.0", "header": map[string]any{
		"event_type": "im.chat.updated_v1", "token": srv.VerificationToken, "event_id": "ev-x"}, "event": map[string]any{}})
	h, body := srv.Signed(plain)
	if r := a.HandleWebhook(context.Background(), h, body, &recSink{}); r.Status != http.StatusOK {
		t.Fatalf("authenticated but unhandled → 200 (else Lark redelivers forever), got %d", r.Status)
	}
}

func TestRun_WebhookModeNeverDialsTheLongConnection(t *testing.T) {
	srv := larktest.New(t)
	stop := startRun(t, webhookApp(t, srv, nil), &recSink{})
	srv.WaitCall(t, "botInfo", nil)
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if len(srv.Calls("wsEndpoint")) != 0 || srv.ConnectCount() != 0 {
		t.Fatal("webhook mode must not open the long connection")
	}

	a := webhookApp(t, srv, kv{"im.lark.encryptKey": ""})
	before := len(srv.Calls("token")) + len(srv.Calls("botInfo"))
	if err := a.Run(context.Background(), &recSink{}); err == nil || !strings.Contains(err.Error(), "Encrypt Key") {
		t.Fatalf("webhook mode without keys must stop with a reason, got %v", err)
	}
	if len(srv.Calls("token"))+len(srv.Calls("botInfo")) != before {
		t.Fatal("a missing key is a config error: refuse before any request to Lark (it must show even when Lark is unreachable)")
	}
}
