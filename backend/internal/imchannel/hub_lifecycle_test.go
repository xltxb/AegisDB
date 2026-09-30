package imchannel_test

import (
	"context"
	"testing"
	"time"

	"velagateway/internal/imchannel"
	"velagateway/internal/imchannel/imtest"
)

// quitter 的 Run 一进来就返回 nil —— 一个「悄悄放弃」的渠道。
type quitter struct{ *imtest.Fake }

func (q quitter) Run(context.Context, imchannel.ActionSink) error { return nil }

func TestHub_WorkAfterStopIsDropped(t *testing.T) {
	s, st := newSettings(), newStore()
	a := imtest.New("a")
	h := imchannel.NewHub(imchannel.Deps{Settings: s}, st)
	h.Register("a", both, a.Builder())
	h.SetSink(nopSink{})
	s.set("im.a.approval", true)
	s.set("im.a.notify", true)
	h.Start(context.Background())
	a.WaitRunning(t)
	st.bind("a", 1, "pa1")

	h.Stop()
	if a.Running() {
		t.Fatal("Stop must end the run loop")
	}
	h.ApprovalCreated(10, imchannel.ApprovalCard{ApNo: "AP-10"}, []int64{1})
	h.NotifyApproval(imchannel.ApprovalCard{ApNo: "AP-10"})
	h.Reload()
	h.Flush()
	if len(a.Sent()) != 0 || len(a.Notified()) != 0 || a.Running() {
		t.Fatalf("after Stop: sent=%d notified=%d running=%v — want nothing", len(a.Sent()), len(a.Notified()), a.Running())
	}
}

func TestHub_RunThatGivesUpSilentlyIsReported(t *testing.T) {
	s, st := newSettings(), newStore()
	q := quitter{imtest.New("q")}
	h := newHub(t, s, st)
	h.Register("q", both, func(imchannel.Deps) imchannel.Channel { return q })
	h.SetSink(nopSink{})
	s.set("im.q.approval", true)
	h.Start(context.Background())

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if st := h.Status(); !st[0].Running && st[0].LastError != "" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("a run loop that returned nil while live must show up in status: %+v", h.Status())
}

func TestHub_ReRegisteringARunningChannelDoesNotHang(t *testing.T) {
	s, st := newSettings(), newStore()
	old, fresh := imtest.New("a"), imtest.New("a")
	h := newHub(t, s, st)
	h.Register("a", both, old.Builder())
	h.SetSink(nopSink{})
	s.set("im.a.approval", true)
	h.Start(context.Background())
	old.WaitRunning(t)

	done := make(chan struct{})
	go func() {
		h.Register("a", both, fresh.Builder())
		h.Reload()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Reload after re-Register hung")
	}
	fresh.WaitRunning(t)
	if old.Running() {
		t.Fatal("the replaced channel's loop must be stopped")
	}
	if st := h.Status(); !st[0].Running {
		t.Fatalf("the new entry must report running: %+v", st[0])
	}
}
