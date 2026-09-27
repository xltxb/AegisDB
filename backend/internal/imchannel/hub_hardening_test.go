package imchannel_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"velagateway/internal/imchannel"
	"velagateway/internal/imchannel/imtest"
)

// flakyNotifier 按 fail 决定这一次群通知成功与否。
type flakyNotifier struct {
	mu   sync.Mutex
	fail bool
}

func (n *flakyNotifier) Kind() string { return "n" }
func (n *flakyNotifier) set(fail bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.fail = fail
}
func (n *flakyNotifier) NotifyApproval(context.Context, imchannel.ApprovalCard) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.fail {
		return errors.New("webhook answered 500")
	}
	return nil
}

// 群通知失败只打一条 WARN 的话,管理员在设置页上看到的永远是「一切正常」。
func TestHub_NotifyFailureShowsInStatusUntilTheNextSuccess(t *testing.T) {
	s, st := newSettings(), newStore()
	n := &flakyNotifier{fail: true}
	h := newHub(t, s, st)
	h.Register("n", imchannel.Caps{Notify: true}, func(imchannel.Deps) imchannel.Channel { return n })
	s.set("im.n.notify", true)
	h.Reload()

	h.NotifyApproval(imchannel.ApprovalCard{ApNo: "AP-1"})
	h.Flush()
	if got := h.Status()[0].LastNotifyError; !strings.Contains(got, "webhook answered 500") {
		t.Fatalf("lastNotifyError = %q, want the notify failure", got)
	}

	n.set(false)
	h.NotifyApproval(imchannel.ApprovalCard{ApNo: "AP-2"})
	h.Flush()
	if got := h.Status()[0].LastNotifyError; got != "" {
		t.Fatalf("lastNotifyError = %q, want it cleared by a successful notify", got)
	}
}

// wrongChannel 上报点击与私聊时填了别的渠道名。
type wrongChannel struct{ *imtest.Fake }

func (w wrongChannel) Run(ctx context.Context, sink imchannel.ActionSink) error {
	sink.OnAction(ctx, imchannel.Action{Channel: "other", PlatformUserID: "p", ApNo: "AP-1"})
	sink.OnDirectMessage(ctx, imchannel.DM{Channel: "other", PlatformUserID: "p", Text: "hi"})
	<-ctx.Done()
	return nil
}

type recSink struct {
	mu       sync.Mutex
	actions  []string
	messages []string
}

func (r *recSink) OnAction(_ context.Context, a imchannel.Action) imchannel.ActionResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.actions = append(r.actions, a.Channel)
	return imchannel.ActionResult{}
}
func (r *recSink) OnDirectMessage(_ context.Context, dm imchannel.DM) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.messages = append(r.messages, dm.Channel)
	return ""
}
func (r *recSink) seen() ([]string, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.actions...), append([]string(nil), r.messages...)
}

// 渠道名决定了用哪张绑定表认人:它必须是投递这条事件的那个适配器,不能由适配器自己说。
func TestHub_InboundEventsArePinnedToTheDeliveringChannel(t *testing.T) {
	s, st := newSettings(), newStore()
	w := wrongChannel{imtest.New("a")}
	h := newHub(t, s, st)
	h.Register("a", both, func(imchannel.Deps) imchannel.Channel { return w })
	sink := &recSink{}
	h.SetSink(sink)
	s.set("im.a.approval", true)
	h.Start(context.Background())

	deadline := time.Now().Add(3 * time.Second)
	for {
		acts, dms := sink.seen()
		if len(acts) == 1 && len(dms) == 1 {
			if acts[0] != "a" || dms[0] != "a" {
				t.Fatalf("action channel %q, dm channel %q; want both pinned to %q", acts[0], dms[0], "a")
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("sink saw actions=%v dms=%v", acts, dms)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// panicky 在被要求时于 SendCard / Run / NotifyApproval 里 panic。
type panicky struct {
	*imtest.Fake
	mu        sync.Mutex
	sendPanic bool
	runPanic  bool
}

func (p *panicky) setSendPanic(v bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sendPanic = v
}

func (p *panicky) SendCard(ctx context.Context, pid string, c imchannel.ApprovalCard) (string, error) {
	p.mu.Lock()
	boom := p.sendPanic
	p.mu.Unlock()
	if boom {
		panic("send exploded")
	}
	return p.Fake.SendCard(ctx, pid, c)
}

func (p *panicky) Run(ctx context.Context, sink imchannel.ActionSink) error {
	if p.runPanic {
		panic("run exploded")
	}
	return p.Fake.Run(ctx, sink)
}

func (p *panicky) NotifyApproval(context.Context, imchannel.ApprovalCard) error {
	panic("notify exploded")
}

// 一个适配器的 bug 不能拖垮网关,也不能让同一张单后面的发卡 / 收卡永远排不上。
func TestHub_APanickingSendDoesNotStallTheQueue(t *testing.T) {
	s, st := newSettings(), newStore()
	p := &panicky{Fake: imtest.New("a"), sendPanic: true}
	h := newHub(t, s, st)
	h.Register("a", both, func(imchannel.Deps) imchannel.Channel { return p })
	s.set("im.a.approval", true)
	h.Reload()
	st.bind("a", 1, "pa1")

	h.ApprovalCreated(10, imchannel.ApprovalCard{ApNo: "AP-10"}, []int64{1})
	h.Flush() // 能返回,说明 panic 的那一单也记了 work.Done

	p.setSendPanic(false)
	h.ApprovalCreated(10, imchannel.ApprovalCard{ApNo: "AP-10"}, []int64{1})
	h.ApprovalSettled(10, imchannel.CardState{Status: "approved"})
	h.Flush()
	if len(p.Sent()) != 1 || len(p.Closed()) != 1 {
		t.Fatalf("after a panic: sent=%d closed=%d, want later jobs for the same ticket to still run",
			len(p.Sent()), len(p.Closed()))
	}
}

func TestHub_APanickingNotifyIsContainedAndReported(t *testing.T) {
	s, st := newSettings(), newStore()
	p := &panicky{Fake: imtest.New("a")}
	h := newHub(t, s, st)
	h.Register("a", both, func(imchannel.Deps) imchannel.Channel { return p })
	s.set("im.a.notify", true)
	h.Reload()

	h.NotifyApproval(imchannel.ApprovalCard{ApNo: "AP-1"})
	h.Flush()
	if got := h.Status()[0].LastNotifyError; !strings.Contains(got, "notify exploded") {
		t.Fatalf("lastNotifyError = %q, want the panic recorded", got)
	}
}

func TestHub_APanickingRunLoopIsMarkedStopped(t *testing.T) {
	s, st := newSettings(), newStore()
	p := &panicky{Fake: imtest.New("a"), runPanic: true}
	h := newHub(t, s, st)
	h.Register("a", both, func(imchannel.Deps) imchannel.Channel { return p })
	h.SetSink(nopSink{})
	s.set("im.a.approval", true)
	h.Start(context.Background())

	deadline := time.Now().Add(3 * time.Second)
	for {
		got := h.Status()[0]
		if !got.Running {
			if !strings.Contains(got.LastError, "run exploded") {
				t.Fatalf("lastError = %q, want the panic recorded", got.LastError)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("status = %+v, want the panicked run loop marked stopped", got)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
