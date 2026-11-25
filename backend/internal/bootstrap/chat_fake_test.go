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

func (f *fakeChat) Send(_ context.Context, accountID string, c chat.Card) (chat.Ref, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sendErr != nil {
		return chat.Ref{}, f.sendErr
	}
	ref := chat.Ref{ChatID: "chat-" + accountID, MessageID: fmt.Sprintf("msg-%d", len(f.sent)+1)}
	f.sent = append(f.sent, fakeSent{AccountID: accountID, Ref: ref, Card: c})
	return ref, nil
}

func (f *fakeChat) Update(_ context.Context, r chat.Ref, c chat.Card) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.updateErr != nil {
		return f.updateErr
	}
	f.updated = append(f.updated, fakeUpdate{Ref: r, Card: c})
	return nil
}

var _ chat.Transport = (*fakeChat)(nil)
