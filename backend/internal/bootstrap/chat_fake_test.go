package bootstrap

import (
	"context"
	"fmt"
	"sync"

	"aegisdb/internal/chat"
)

// fakeChat 是聊天渠道的替身:不连任何平台,记下发出去的和更新过的卡片。
// 绑定、发卡、点击、状态这几组用例共用它。
type fakeChat struct {
	mu        sync.Mutex
	botID     string
	botName   string
	sent      []fakeSent
	updated   []fakeUpdate
	sendErr   error // 非 nil 时 Send 一律失败
	updateErr error
	// onSend / onUpdate 非 nil 时先于记录被调,不持锁:用来挂起、panic、在发送途中改数据。
	// 返回错误 = 这一次失败。
	onSend   func(ctx context.Context, accountID string, c chat.Card) error
	onUpdate func(ctx context.Context, r chat.Ref, c chat.Card) error
}

type fakeSent struct {
	AccountID string
	Ref       chat.Ref
	Card      chat.Card
}

type fakeUpdate struct {
	Ref  chat.Ref
	Card chat.Card
}

func newFakeChat(botID, botName string) *fakeChat { return &fakeChat{botID: botID, botName: botName} }

func (f *fakeChat) BotID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.botID
}

func (f *fakeChat) BotName() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.botName
}

// setBot 模拟换了机器人(或还没连上:空标识)。
func (f *fakeChat) setBot(id, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.botID, f.botName = id, name
}

func (f *fakeChat) Run(ctx context.Context, _ chat.Handler, onState func(chat.State, string)) error {
	onState(chat.StateConnected, "")
	<-ctx.Done()
	return ctx.Err()
}

// setHooks 换掉两个钩子;扫描在别的 goroutine 里跑,不能直接改字段。
func (f *fakeChat) setHooks(onSend func(context.Context, string, chat.Card) error, onUpdate func(context.Context, chat.Ref, chat.Card) error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onSend, f.onUpdate = onSend, onUpdate
}

// setErrs 让 Send / Update 一律失败(nil = 恢复)。
func (f *fakeChat) setErrs(send, update error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sendErr, f.updateErr = send, update
}

// sends / updates 是到目前为止发出、更新过的卡片的副本。
func (f *fakeChat) sends() []fakeSent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeSent(nil), f.sent...)
}

func (f *fakeChat) updates() []fakeUpdate {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeUpdate(nil), f.updated...)
}

func (f *fakeChat) Send(ctx context.Context, accountID string, c chat.Card) (chat.Ref, error) {
	f.mu.Lock()
	hook := f.onSend
	f.mu.Unlock()
	if hook != nil {
		if err := hook(ctx, accountID, c); err != nil {
			return chat.Ref{}, err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sendErr != nil {
		return chat.Ref{}, f.sendErr
	}
	ref := chat.Ref{ChatID: "chat-" + accountID, MessageID: fmt.Sprintf("msg-%d", len(f.sent)+1)}
	f.sent = append(f.sent, fakeSent{AccountID: accountID, Ref: ref, Card: c})
	return ref, nil
}

func (f *fakeChat) Update(ctx context.Context, r chat.Ref, c chat.Card) error {
	f.mu.Lock()
	hook := f.onUpdate
	f.mu.Unlock()
	if hook != nil {
		if err := hook(ctx, r, c); err != nil {
			return err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.updateErr != nil {
		return f.updateErr
	}
	f.updated = append(f.updated, fakeUpdate{Ref: r, Card: c})
	return nil
}

var _ chat.Transport = (*fakeChat)(nil)
