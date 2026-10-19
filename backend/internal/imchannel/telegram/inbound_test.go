package telegram_test

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"velagateway/internal/imchannel"
	"velagateway/internal/imchannel/telegram"
	"velagateway/internal/imchannel/telegram/tgtest"
)

type sink struct {
	mu      sync.Mutex
	actions []imchannel.Action
	dms     []imchannel.DM
	toast   string
}

func (s *sink) OnAction(_ context.Context, a imchannel.Action) imchannel.ActionResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.actions = append(s.actions, a)
	return imchannel.ActionResult{Toast: s.toast}
}

func (s *sink) OnDirectMessage(_ context.Context, dm imchannel.DM) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dms = append(s.dms, dm)
	return "reply:" + dm.Text
}

func (s *sink) snapshot() ([]imchannel.Action, []imchannel.DM) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]imchannel.Action(nil), s.actions...), append([]imchannel.DM(nil), s.dms...)
}

// runPolling 起一个 polling 循环,测试结束时停掉并等它退出。
func runPolling(t *testing.T, ch *telegram.Channel, sk *sink) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- ch.Run(ctx, sk) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Run did not return after cancel")
		}
	})
}

func TestPolling_ClicksReachTheSinkAndAreAnswered(t *testing.T) {
	srv := tgtest.New(t)
	ch := newChannel(t, srv, nil)
	sk := &sink{toast: strings.Repeat("长", 300)}
	runPolling(t, ch, sk)

	srv.WaitCall(t, "deleteWebhook", nil) // polling 先确保没有 webhook 占着
	id := srv.PushCallback(777, "ap:AP-9:a")
	c := srv.WaitCall(t, "answerCallbackQuery", func(c tgtest.Call) bool { return c.Str("callback_query_id") == id })
	if n := len([]rune(c.Str("text"))); n > 200 {
		t.Fatalf("toast is %d chars; Telegram caps it at 200", n)
	}
	acts, _ := sk.snapshot()
	if len(acts) != 1 || acts[0].PlatformUserID != "777" || acts[0].ApNo != "AP-9" || !acts[0].Approve {
		t.Fatalf("actions = %+v", acts)
	}
	if ch.BotName() != srv.BotUsername {
		t.Fatalf("BotName = %q after getMe", ch.BotName())
	}
}

func TestPolling_AnUpdateIsHandledOnce(t *testing.T) {
	srv := tgtest.New(t)
	ch := newChannel(t, srv, nil)
	sk := &sink{toast: "ok"}
	runPolling(t, ch, sk)

	id := srv.Push(telegram.Update{CallbackQuery: &telegram.CallbackQuery{ID: "q1", From: telegram.User{ID: 5}, Data: "ap:AP-1:r"}})
	srv.WaitCall(t, "answerCallbackQuery", nil)
	// 同一个 update_id 再来一次 —— 进程重启前没来得及确认的 update,Telegram 会重投。
	srv.Push(telegram.Update{UpdateID: id, CallbackQuery: &telegram.CallbackQuery{ID: "q1", From: telegram.User{ID: 5}, Data: "ap:AP-1:r"}})
	srv.PushCallback(5, "ap:AP-2:r") // 一条新的,用来确认循环还在往前走
	srv.WaitCall(t, "answerCallbackQuery", func(c tgtest.Call) bool { return c.Str("callback_query_id") != "q1" })
	acts, _ := sk.snapshot()
	if len(acts) != 2 {
		t.Fatalf("actions = %+v — the redelivered update must not be handled again", acts)
	}
}

func TestDispatch_DirectMessagesStartAndGroups(t *testing.T) {
	srv := tgtest.New(t)
	ch := newChannel(t, srv, nil)
	sk := &sink{}
	runPolling(t, ch, sk)

	srv.PushText(777, "private", "/start")
	srv.WaitCall(t, "sendMessage", func(c tgtest.Call) bool {
		return c.Str("chat_id") == "777" && strings.Contains(c.Str("text"), "IM 账号绑定")
	})
	srv.PushText(777, "private", "BIND-ABCDEFGH")
	srv.WaitCall(t, "sendMessage", func(c tgtest.Call) bool { return c.Str("text") == "reply:BIND-ABCDEFGH" })
	_, dms := sk.snapshot()
	if len(dms) != 1 || dms[0].PlatformUserID != "777" {
		t.Fatalf("dms = %+v — /start is answered locally, the code goes to the sink", dms)
	}
}

func TestDispatch_GroupTextIsIgnored(t *testing.T) {
	srv := tgtest.New(t)
	ch := newChannel(t, srv, nil)
	sk := &sink{}
	runPolling(t, ch, sk)

	srv.PushText(888, "group", "BIND-ABCDEFGH")
	srv.PushText(889, "private", "marker") // 一条私聊,等它被处理就说明前面那条也已经过去了
	srv.WaitCall(t, "sendMessage", func(c tgtest.Call) bool { return c.Str("text") == "reply:marker" })
	_, dms := sk.snapshot()
	if len(dms) != 1 {
		t.Fatalf("dms = %+v — group messages must never reach binding", dms)
	}
	for _, c := range srv.Calls("sendMessage") {
		if strings.HasPrefix(c.Str("chat_id"), "-") {
			t.Fatalf("replied into a group: %+v", c.Params)
		}
	}
}

func TestDispatch_DoneAndGarbageCallbacksAreAnsweredWithoutTheSink(t *testing.T) {
	srv := tgtest.New(t)
	ch := newChannel(t, srv, nil)
	sk := &sink{}
	runPolling(t, ch, sk)

	done := srv.PushCallback(1, telegram.DoneData)
	junk := srv.PushCallback(1, "ap:../../x:a")
	srv.WaitCall(t, "answerCallbackQuery", func(c tgtest.Call) bool { return c.Str("callback_query_id") == done })
	srv.WaitCall(t, "answerCallbackQuery", func(c tgtest.Call) bool { return c.Str("callback_query_id") == junk })
	if acts, _ := sk.snapshot(); len(acts) != 0 {
		t.Fatalf("actions = %+v", acts)
	}
}

func TestRun_BadTokenGivesUpWithoutLeakingIt(t *testing.T) {
	srv := tgtest.New(t)
	srv.SetFail("getMe", tgtest.Failure{Code: 401, Desc: "Unauthorized"})
	ch := newChannel(t, srv, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := ch.Run(ctx, &sink{})
	if err == nil || !strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), srv.Token) {
		t.Fatalf("Run err = %v — a rejected token is final, and must not appear in the error", err)
	}
}

func TestRun_WebhookModeRegistersAndNeedsItsConfig(t *testing.T) {
	srv := tgtest.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	bare := newChannel(t, srv, map[string]string{"im.telegram.mode": "webhook"})
	if err := bare.Run(ctx, &sink{}); err == nil || !strings.Contains(err.Error(), "webhook") {
		t.Fatalf("webhook mode without base URL / secret must fail loudly, got %v", err)
	}

	ch := newChannel(t, srv, map[string]string{
		"im.telegram.mode": "webhook", "im.telegram.webhookSecret": "s3",
		"im.callbackBaseURL": "https://gw.example/",
	})
	runCtx, stop := context.WithCancel(context.Background())
	go func() { _ = ch.Run(runCtx, &sink{}) }()
	defer stop()
	c := srv.WaitCall(t, "setWebhook", nil)
	if c.Str("url") != "https://gw.example"+telegram.WebhookPath || c.Str("secret_token") != "s3" {
		t.Fatalf("setWebhook params = %+v", c.Params)
	}
	if len(srv.Calls("getUpdates")) != 0 {
		t.Fatal("webhook mode must not poll")
	}
}

func TestHandleWebhook_AuthenticatesThenDispatches(t *testing.T) {
	srv := tgtest.New(t)
	sk := &sink{toast: "ok"}
	body := []byte(`{"update_id":1,"callback_query":{"id":"q","from":{"id":777},"data":"ap:AP-3:a"}}`)
	hdr := func(v string) http.Header { return http.Header{"X-Telegram-Bot-Api-Secret-Token": []string{v}} }
	ctx := context.Background()

	polling := newChannel(t, srv, map[string]string{"im.telegram.webhookSecret": "s3"})
	if got := polling.HandleWebhook(ctx, hdr("s3"), body, sk); got.Status != http.StatusForbidden {
		t.Fatalf("polling mode must refuse webhooks, got %d", got)
	}
	noSecret := newChannel(t, srv, map[string]string{"im.telegram.mode": "webhook"})
	if got := noSecret.HandleWebhook(ctx, hdr(""), body, sk); got.Status != http.StatusForbidden {
		t.Fatalf("no configured secret must refuse everything, got %d", got)
	}
	ch := newChannel(t, srv, map[string]string{"im.telegram.mode": "webhook", "im.telegram.webhookSecret": "s3"})
	if got := ch.HandleWebhook(ctx, hdr("nope"), body, sk); got.Status != http.StatusForbidden {
		t.Fatalf("wrong secret, got %d", got)
	}
	if got := ch.HandleWebhook(ctx, hdr("s3"), []byte("{not json"), sk); got.Status != http.StatusOK {
		t.Fatalf("an authenticated but unparsable body is acknowledged, got %d", got)
	}
	if got := ch.HandleWebhook(ctx, hdr("s3"), body, sk); got.Status != http.StatusOK {
		t.Fatalf("good request, got %d", got)
	}
	if acts, _ := sk.snapshot(); len(acts) != 1 || acts[0].ApNo != "AP-3" {
		t.Fatalf("actions = %+v", acts)
	}
	srv.WaitCall(t, "answerCallbackQuery", nil)
}

func TestPolling_AHugeBacklogIsFetchedInBoundedBatches(t *testing.T) {
	srv := tgtest.New(t)
	ch := newChannel(t, srv, nil)
	sk := &sink{}
	// 先排好再起循环:第一次 getUpdates 看到的就是全部积压。
	for i := 0; i < 30; i++ {
		srv.PushText(int64(100+i), "private", strings.Repeat("长", 15000))
	}
	srv.PushText(999, "private", "marker")
	runPolling(t, ch, sk)

	srv.WaitCall(t, "sendMessage", func(c tgtest.Call) bool { return c.Str("text") == "reply:marker" })
	if _, dms := sk.snapshot(); len(dms) != 31 {
		t.Fatalf("dms = %d, want 31 — an oversized batch must not wedge polling", len(dms))
	}
	for _, c := range srv.Calls("getUpdates") {
		if n, _ := c.Params["limit"].(float64); n < 1 || n > 10 {
			t.Fatalf("getUpdates limit = %v, want 1..10 so a batch fits the response cap", c.Params["limit"])
		}
	}
}
