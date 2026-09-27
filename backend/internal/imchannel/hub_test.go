package imchannel_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"velagateway/internal/imchannel"
	"velagateway/internal/imchannel/imtest"
)

type memSettings struct {
	mu sync.Mutex
	kv map[string]any
}

func newSettings() *memSettings { return &memSettings{kv: map[string]any{}} }
func (s *memSettings) set(k string, v any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.kv[k] = v
}
func (s *memSettings) String(k string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, _ := s.kv[k].(string)
	return v
}
func (s *memSettings) Bool(k string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, _ := s.kv[k].(bool)
	return v
}
func (s *memSettings) Secret(k string) string { return s.String(k) }

type memStore struct {
	mu    sync.Mutex
	ids   map[string]map[int64]string
	cards []imchannel.Card
	seq   int64
}

func newStore() *memStore { return &memStore{ids: map[string]map[int64]string{}} }
func (m *memStore) bind(ch string, uid int64, pid string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ids[ch] == nil {
		m.ids[ch] = map[int64]string{}
	}
	m.ids[ch][uid] = pid
}
func (m *memStore) PlatformIDs(ch string, uids []int64) (map[int64]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[int64]string{}
	for _, u := range uids {
		if p, ok := m.ids[ch][u]; ok {
			out[u] = p
		}
	}
	return out, nil
}
func (m *memStore) RecordCard(c imchannel.Card) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	c.ID = m.seq
	m.cards = append(m.cards, c)
	return nil
}
func (m *memStore) OpenCards(apID int64) ([]imchannel.Card, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []imchannel.Card
	for _, c := range m.cards {
		if c.ApprovalID == apID && c.ID > 0 {
			out = append(out, c)
		}
	}
	return out, nil
}
func (m *memStore) MarkCardClosed(id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.cards {
		if m.cards[i].ID == id {
			m.cards[i].ID = -id // 负数 = 已关闭
		}
	}
	return nil
}
func (m *memStore) open() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, c := range m.cards {
		if c.ID > 0 {
			n++
		}
	}
	return n
}

var both = imchannel.Caps{Notify: true, Approve: true}

func newHub(t *testing.T, s *memSettings, st *memStore) *imchannel.Hub {
	t.Helper()
	h := imchannel.NewHub(imchannel.Deps{Settings: s}, st)
	t.Cleanup(h.Stop)
	return h
}

type nopSink struct{}

func (nopSink) OnAction(context.Context, imchannel.Action) imchannel.ActionResult {
	return imchannel.ActionResult{}
}
func (nopSink) OnDirectMessage(context.Context, imchannel.DM) string { return "" }

func TestHub_ApprovalCreatedSendsOnlyToBoundRecipientsOnEnabledChannels(t *testing.T) {
	s, st := newSettings(), newStore()
	a, b := imtest.New("a"), imtest.New("b")
	h := newHub(t, s, st)
	h.Register("a", both, a.Builder())
	h.Register("b", both, b.Builder())
	s.set("im.a.approval", true) // b 没开
	h.Reload()
	st.bind("a", 1, "pa1")
	st.bind("a", 2, "pa2")
	st.bind("b", 1, "pb1")

	h.ApprovalCreated(10, imchannel.ApprovalCard{ApNo: "AP-10"}, []int64{1, 2, 3})
	h.Flush()

	if got := a.Sent(); len(got) != 2 || got[0].PlatformUserID != "pa1" || got[1].PlatformUserID != "pa2" {
		t.Fatalf("channel a sent = %+v, want pa1 then pa2", got)
	}
	if got := b.Sent(); len(got) != 0 {
		t.Fatalf("disabled channel b must send nothing, sent %+v", got)
	}
	if st.open() != 2 {
		t.Fatalf("recorded cards = %d, want 2", st.open())
	}
}

func TestHub_ApprovalSettledClosesEveryOpenCardEvenAfterChannelIsSwitchedOff(t *testing.T) {
	s, st := newSettings(), newStore()
	a := imtest.New("a")
	h := newHub(t, s, st)
	h.Register("a", both, a.Builder())
	s.set("im.a.approval", true)
	h.Reload()
	st.bind("a", 1, "pa1")
	h.ApprovalCreated(10, imchannel.ApprovalCard{ApNo: "AP-10"}, []int64{1})
	h.Flush()

	s.set("im.a.approval", false)
	h.Reload()
	h.ApprovalSettled(10, imchannel.CardState{Status: "approved", Text: "已由 李四 通过"})
	h.Flush()

	if c := a.Closed(); len(c) != 1 || c[0].State.Status != "approved" {
		t.Fatalf("closed = %+v, want one approved close", c)
	}
	if st.open() != 0 {
		t.Fatal("card should be marked closed")
	}
	h.ApprovalSettled(10, imchannel.CardState{Status: "approved"})
	h.Flush()
	if len(a.Closed()) != 1 {
		t.Fatal("a closed card must not be closed twice")
	}
}

func TestHub_SettledWhileSendingStillClosesLateCards(t *testing.T) {
	s, st := newSettings(), newStore()
	a := imtest.New("a")
	a.SendDelay = 100 * time.Millisecond
	h := newHub(t, s, st)
	h.Register("a", both, a.Builder())
	s.set("im.a.approval", true)
	h.Reload()
	st.bind("a", 1, "pa1")

	h.ApprovalCreated(10, imchannel.ApprovalCard{ApNo: "AP-10"}, []int64{1})
	h.ApprovalSettled(10, imchannel.CardState{Status: "rejected"}) // 决定比发送快
	h.Flush()

	if len(a.Sent()) != 1 || len(a.Closed()) != 1 {
		t.Fatalf("sent=%d closed=%d, want the late card closed too", len(a.Sent()), len(a.Closed()))
	}
}

func TestHub_FailedSendRecordsNoCard(t *testing.T) {
	s, st := newSettings(), newStore()
	a := imtest.New("a")
	a.FailSend = true
	h := newHub(t, s, st)
	h.Register("a", both, a.Builder())
	s.set("im.a.approval", true)
	h.Reload()
	st.bind("a", 1, "pa1")

	h.ApprovalCreated(10, imchannel.ApprovalCard{ApNo: "AP-10"}, []int64{1})
	h.Flush()
	if st.open() != 0 {
		t.Fatal("a failed send must not leave a card to close")
	}
}

func TestHub_ReloadStartsAndStopsRunLoops(t *testing.T) {
	s, st := newSettings(), newStore()
	a := imtest.New("a")
	h := newHub(t, s, st)
	h.Register("a", both, a.Builder())
	h.SetSink(nopSink{})
	s.set("im.a.approval", true)
	h.Start(context.Background())
	a.WaitRunning(t)

	s.set("im.a.approval", false)
	h.Reload()
	if a.Running() {
		t.Fatal("switching approval off must stop the run loop")
	}
	s.set("im.a.approval", true)
	h.Reload()
	a.WaitRunning(t)
	if a.Runs() != 2 {
		t.Fatalf("runs = %d, want 2", a.Runs())
	}
	if st := h.Status(); len(st) != 1 || !st[0].Running || !st[0].Approval || st[0].BotName != "fake-bot" {
		t.Fatalf("status = %+v", st)
	}
}

func TestHub_NotifyOnlyReachesNotifyEnabledChannels(t *testing.T) {
	s, st := newSettings(), newStore()
	a, b := imtest.New("a"), imtest.New("b")
	h := newHub(t, s, st)
	h.Register("a", both, a.Builder())
	h.Register("b", both, b.Builder())
	s.set("im.a.notify", true)
	h.Reload()

	h.NotifyApproval(imchannel.ApprovalCard{ApNo: "AP-1"})
	h.Flush()
	if len(a.Notified()) != 1 || len(b.Notified()) != 0 {
		t.Fatalf("a=%d b=%d, want 1/0", len(a.Notified()), len(b.Notified()))
	}
}

func TestHub_UnconfiguredChannelIsSkipped(t *testing.T) {
	s, st := newSettings(), newStore()
	h := newHub(t, s, st)
	h.Register("x", both, func(imchannel.Deps) imchannel.Channel { return nil })
	s.set("im.x.approval", true)
	s.set("im.x.notify", true)
	h.Reload()
	st.bind("x", 1, "p")

	h.ApprovalCreated(1, imchannel.ApprovalCard{}, []int64{1})
	h.NotifyApproval(imchannel.ApprovalCard{})
	h.Flush()
	if h.ApprovalEnabled("x") {
		t.Fatal("an unconfigured channel is not approval-enabled")
	}
	if err := h.TestNotify(context.Background(), "x", imchannel.ApprovalCard{}); err != imchannel.ErrNotConfigured {
		t.Fatalf("TestNotify err = %v, want ErrNotConfigured", err)
	}
	if got := h.Status(); got[0].Configured || !got[0].CanApprove {
		t.Fatalf("status = %+v", got[0])
	}
}
