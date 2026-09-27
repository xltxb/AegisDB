// Package imtest 提供一个假渠道,用来端到端验证 Hub 与 service 的接线,不碰任何真实平台。
package imtest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"velagateway/internal/imchannel"
)

type Sent struct {
	PlatformUserID string
	Card           imchannel.ApprovalCard
	Ref            string
}

type Closed struct {
	Ref   string
	State imchannel.CardState
}

// Fake 同时实现 Notifier 与 Approver。
type Fake struct {
	FailSend  bool
	SendDelay time.Duration

	kind     string
	mu       sync.Mutex
	seq      int
	sent     []Sent
	closed   []Closed
	notified []imchannel.ApprovalCard
	sink     imchannel.ActionSink
	runs     int
}

func New(kind string) *Fake { return &Fake{kind: kind} }

func (f *Fake) Kind() string    { return f.kind }
func (f *Fake) BotName() string { return "fake-bot" }

// Builder 每次 Reload 都交回同一个实例,测试因此能跨 Reload 观察它。
func (f *Fake) Builder() imchannel.Builder {
	return func(imchannel.Deps) imchannel.Channel { return f }
}

func (f *Fake) NotifyApproval(_ context.Context, c imchannel.ApprovalCard) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notified = append(f.notified, c)
	return nil
}

func (f *Fake) SendCard(ctx context.Context, pid string, c imchannel.ApprovalCard) (string, error) {
	if f.SendDelay > 0 {
		select {
		case <-time.After(f.SendDelay):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.FailSend {
		return "", errors.New("fake: send failed")
	}
	f.seq++
	ref := fmt.Sprintf("%s-%d", f.kind, f.seq)
	f.sent = append(f.sent, Sent{PlatformUserID: pid, Card: c, Ref: ref})
	return ref, nil
}

func (f *Fake) CloseCard(_ context.Context, ref string, s imchannel.CardState) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = append(f.closed, Closed{Ref: ref, State: s})
	return nil
}

func (f *Fake) Run(ctx context.Context, sink imchannel.ActionSink) error {
	f.mu.Lock()
	f.sink = sink
	f.runs++
	f.mu.Unlock()
	<-ctx.Done()
	f.mu.Lock()
	f.sink = nil
	f.mu.Unlock()
	return nil
}

// Click 模拟一次按钮点击。渠道没在运行时返回零值 —— 那正是真实平台上「点了没人收」。
func (f *Fake) Click(pid, apNo string, approve bool) imchannel.ActionResult {
	f.mu.Lock()
	sink := f.sink
	f.mu.Unlock()
	if sink == nil {
		return imchannel.ActionResult{}
	}
	return sink.OnAction(context.Background(), imchannel.Action{
		Channel: f.kind, PlatformUserID: pid, ApNo: apNo, Approve: approve,
	})
}

// DirectMessage 模拟一条私聊文本,返回机器人的回复。
func (f *Fake) DirectMessage(pid, text string) string {
	f.mu.Lock()
	sink := f.sink
	f.mu.Unlock()
	if sink == nil {
		return ""
	}
	return sink.OnDirectMessage(context.Background(), imchannel.DM{Channel: f.kind, PlatformUserID: pid, Text: text})
}

func (f *Fake) Running() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sink != nil
}

func (f *Fake) WaitRunning(t testing.TB) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if f.Running() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("fake channel %q never started running", f.kind)
}

func (f *Fake) Runs() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.runs
}

func (f *Fake) Sent() []Sent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Sent(nil), f.sent...)
}

func (f *Fake) Closed() []Closed {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Closed(nil), f.closed...)
}

func (f *Fake) Notified() []imchannel.ApprovalCard {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]imchannel.ApprovalCard(nil), f.notified...)
}
