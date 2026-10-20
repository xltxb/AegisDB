package slack

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"velagateway/internal/imchannel"
	"velagateway/internal/imchannel/slack/slacktest"
)

const formType = "application/x-www-form-urlencoded"

func header(m map[string]string) http.Header {
	h := http.Header{}
	for k, v := range m {
		h.Set(k, v)
	}
	return h
}

func webhookApp(t *testing.T, srv *slacktest.Server) *App {
	t.Helper()
	return newTestApp(t, srv, kv{"im.slack.mode": "webhook", "im.slack.signingSecret": srv.SigningSecret, "im.slack.appToken": ""})
}

func TestWebhook_IsClosedUnlessInWebhookModeWithASigningSecret(t *testing.T) {
	srv := slacktest.New(t)
	body := `{"type":"url_verification","challenge":"c-123"}`
	for name, a := range map[string]*App{
		"socket mode":       newTestApp(t, srv, kv{"im.slack.signingSecret": srv.SigningSecret}),
		"no signing secret": newTestApp(t, srv, kv{"im.slack.mode": "webhook"}),
	} {
		if r := a.HandleWebhook(bg, header(srv.SignedHeaders(body, "")), []byte(body), &recSink{}); r.Status != http.StatusForbidden {
			t.Errorf("%s: %d", name, r.Status)
		}
	}
	var _ imchannel.Webhooker = webhookApp(t, srv)
}

func TestWebhook_RefusesBadSignaturesAndStaleRequests(t *testing.T) {
	srv := slacktest.New(t)
	a := webhookApp(t, srv)
	sink := &recSink{}
	now := time.Now()
	a.now = func() time.Time { return now }
	body := slacktest.InteractionForm(slacktest.Interaction("U0OWNER", "D0OWNER", actionApprove, "ap:AP-2294:a"))
	signed := func(at time.Time, secret, payload string) http.Header {
		return header(map[string]string{
			"Content-Type":              formType,
			"X-Slack-Request-Timestamp": strconv.FormatInt(at.Unix(), 10),
			"X-Slack-Signature":         slacktest.Sign(secret, at.Unix(), payload),
		})
	}
	for name, c := range map[string]struct {
		h    http.Header
		body string
	}{
		"unsigned":             {header(map[string]string{"Content-Type": formType}), body},
		"no timestamp":         {header(map[string]string{"Content-Type": formType, "X-Slack-Signature": slacktest.Sign(srv.SigningSecret, now.Unix(), body)}), body},
		"wrong secret":         {signed(now, "0000000000000000000000000000000x", body), body},
		"tampered body":        {signed(now, srv.SigningSecret, body), body + "x"},
		"six minutes old":      {signed(now.Add(-6*time.Minute), srv.SigningSecret, body), body},
		"six minutes from now": {signed(now.Add(6*time.Minute), srv.SigningSecret, body), body},
	} {
		if r := a.HandleWebhook(bg, c.h, []byte(c.body), sink); r.Status != http.StatusForbidden {
			t.Errorf("%s: %d", name, r.Status)
		}
	}
	if n, _ := sink.counts(); n != 0 {
		t.Fatalf("%d refused requests reached the sink", n)
	}
	// 4 分钟前、签名正确:还在 ±5 分钟的窗口里(网关与 Slack 之间的时钟偏差)
	if r := a.HandleWebhook(bg, signed(now.Add(-4*time.Minute), srv.SigningSecret, body), []byte(body), sink); r.Status != http.StatusOK {
		t.Fatalf("a request inside the window → %d", r.Status)
	}
}

func TestWebhook_AnswersTheURLVerificationChallenge(t *testing.T) {
	srv := slacktest.New(t)
	a := webhookApp(t, srv)
	const challenge = "3eZbrw1aBm2rZgRNFdxV2595E9CY3gmdALWMmHkvFXO7tYXAYM8P"
	body := `{"token":"legacy","challenge":"` + challenge + `","type":"url_verification"}`
	r := a.HandleWebhook(bg, header(srv.SignedHeaders(body, "")), []byte(body), &recSink{})
	if r.Status != http.StatusOK || string(r.Body) != `{"challenge":"`+challenge+`"}` {
		t.Fatalf("challenge → %d %s", r.Status, r.Body)
	}
	// 只回显 challenge 形状的串:这条路不该变成「让网关回显任意内容」
	odd := `{"type":"url_verification","challenge":"<script>alert(1)</script>"}`
	if r := a.HandleWebhook(bg, header(srv.SignedHeaders(odd, "")), []byte(odd), &recSink{}); r.Status != http.StatusOK || len(r.Body) != 0 {
		t.Fatalf("odd challenge → %d %s", r.Status, r.Body)
	}
}

func TestWebhook_AClickIsDecidedBeforeTheReplyAndAnsweredToTheClicker(t *testing.T) {
	srv := slacktest.New(t)
	a := webhookApp(t, srv)
	sink := &recSink{}
	body := slacktest.InteractionForm(slacktest.Interaction("U0OWNER", "D0OWNER", actionApprove, "ap:AP-2294:a"))
	r := a.HandleWebhook(bg, header(srv.SignedHeaders(body, formType)), []byte(body), sink)
	if r.Status != http.StatusOK || len(r.Body) != 0 {
		t.Fatalf("click → %d %s", r.Status, r.Body)
	}
	if n, _ := sink.counts(); n != 1 {
		t.Fatalf("the decision must be made before the 200: %d actions", n)
	}
	e := srv.WaitCall(t, "chat.postEphemeral", nil)
	if e.Get("text") != "ok U0OWNER AP-2294 true" || e.Get("user") != "U0OWNER" || e.Get("channel") != "D0OWNER" {
		t.Fatalf("ephemeral = %+v", e.Params)
	}
}

func TestWebhook_ADirectMessageIsAnsweredAndARetryIsHandledOnce(t *testing.T) {
	srv := slacktest.New(t)
	a := webhookApp(t, srv)
	sink := &recSink{}
	ev, _ := json.Marshal(slacktest.MessageEvent("U0OWNER", "BIND-7K3QX9MP", "EvWebhook1", nil))
	for i := 0; i < 2; i++ {
		h := header(srv.SignedHeaders(string(ev), ""))
		if i == 1 {
			h.Set("X-Slack-Retry-Num", "1") // Slack 没在 3 秒内拿到 200,重投
			h.Set("X-Slack-Retry-Reason", "http_timeout")
		}
		if r := a.HandleWebhook(bg, h, ev, sink); r.Status != http.StatusOK {
			t.Fatalf("delivery %d → %d", i+1, r.Status)
		}
	}
	if _, n := sink.counts(); n != 1 {
		t.Fatalf("the same event_id was handled %d times", n)
	}
	if posts := srv.Calls("chat.postMessage"); len(posts) != 1 || posts[0].Get("channel") != "D0OWNER" {
		t.Fatalf("replies = %+v", posts)
	}
}

func TestWebhook_ASignedButUnusableRequestIsStillAcknowledged(t *testing.T) {
	srv := slacktest.New(t)
	a := webhookApp(t, srv)
	sink := &recSink{}
	// 验签过了就一律 200:非 2xx 会让 Slack 一直重投,而重投不会让一次坏的处理变好。
	for _, c := range []struct{ body, ct string }{
		{"not json", ""},
		{"payload=%7Bbroken", formType},
		{"command=%2Faegis&user_id=U0OWNER", formType}, // 斜杠命令:网关不提供
		{`{"type":"event_callback","event":{"type":"app_mention"}}`, ""},
	} {
		if r := a.HandleWebhook(bg, header(srv.SignedHeaders(c.body, c.ct)), []byte(c.body), sink); r.Status != http.StatusOK {
			t.Errorf("%q → %d", c.body, r.Status)
		}
	}
	if n, m := sink.counts(); n+m != 0 {
		t.Fatalf("unusable requests reached the sink: %d actions, %d messages", n, m)
	}
}
