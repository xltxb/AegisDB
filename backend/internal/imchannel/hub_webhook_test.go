package imchannel_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"velagateway/internal/imchannel"
	"velagateway/internal/imchannel/imtest"
)

type recordingSink struct{ got []imchannel.Action }

func (r *recordingSink) OnAction(_ context.Context, a imchannel.Action) imchannel.ActionResult {
	r.got = append(r.got, a)
	return imchannel.ActionResult{Toast: "ok"}
}
func (r *recordingSink) OnDirectMessage(context.Context, imchannel.DM) string { return "" }

func TestHub_ServeWebhook(t *testing.T) {
	s, st := newSettings(), newStore()
	a := imtest.New("a")
	a.WebhookSecret = "s3"
	h := newHub(t, s, st)
	h.Register("a", both, a.Builder())
	h.Register("n", imchannel.Caps{Notify: true}, a.Builder()) // 能收 webhook 但不能审批
	sink := &recordingSink{}
	h.SetSink(sink)
	h.Reload()
	hdr := http.Header{"X-Fake-Secret": []string{"s3"}}
	body := []byte(`{"pid":"p1","apNo":"AP-1","approve":true}`)
	ctx := context.Background()

	check := func(got, want int, what string) {
		t.Helper()
		if got != want {
			t.Fatalf("%s: status %d, want %d", what, got, want)
		}
	}
	check(h.ServeWebhook(ctx, "nope", hdr, body).Status, http.StatusNotFound, "unknown channel")
	check(h.ServeWebhook(ctx, "a", hdr, body).Status, http.StatusForbidden, "approval switch off")
	s.set("im.a.approval", true)
	s.set("im.n.approval", true)
	check(h.ServeWebhook(ctx, "n", hdr, body).Status, http.StatusForbidden, "notify-only channel")
	check(h.ServeWebhook(ctx, "a", http.Header{"X-Fake-Secret": []string{"bad"}}, body).Status, http.StatusForbidden, "bad secret")
	check(h.ServeWebhook(ctx, "a", hdr, body).Status, http.StatusOK, "good request")

	if len(sink.got) != 1 || sink.got[0].Channel != "a" || sink.got[0].PlatformUserID != "p1" {
		t.Fatalf("sink got %+v — Channel must be pinned to the delivering kind", sink.got)
	}
}

type panicHook struct{ *imtest.Fake }

func (panicHook) HandleWebhook(context.Context, http.Header, []byte, imchannel.ActionSink) imchannel.WebhookReply {
	panic("parser bug")
}

func TestHub_APanickingWebhookIsContained(t *testing.T) {
	s, st := newSettings(), newStore()
	p := panicHook{imtest.New("p")}
	h := newHub(t, s, st)
	h.Register("p", both, func(imchannel.Deps) imchannel.Channel { return p })
	h.SetSink(&recordingSink{})
	s.set("im.p.approval", true)
	h.Reload()
	if got := h.ServeWebhook(context.Background(), "p", http.Header{}, nil); got.Status != http.StatusOK {
		t.Fatalf("status %d — a panic must be contained and acknowledged, not retried forever", got)
	}
}

func TestHub_ServeWebhookPassesTheReplyBodyThrough(t *testing.T) {
	s, st := newSettings(), newStore()
	s.set(imchannel.Key("a", "approval"), true)
	f := imtest.New("a")
	f.WebhookSecret = "s3"
	h := newHub(t, s, st)
	h.Register("a", both, f.Builder())
	h.SetSink(nopSink{})
	h.Reload()

	r := h.ServeWebhook(context.Background(), "a", http.Header{"X-Fake-Secret": []string{"s3"}}, []byte(`{"pid":"p","apNo":"AP-1","approve":true}`))
	if r.Status != http.StatusOK || !strings.Contains(string(r.Body), `"toast"`) {
		t.Fatalf("reply = %d %s, want 200 with the channel's body", r.Status, r.Body)
	}
}
