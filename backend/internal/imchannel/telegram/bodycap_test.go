package telegram_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"velagateway/internal/imchannel/telegram"
)

// bigServer 回一个带 n 字节填充的 ok 响应,不管调的是哪个方法。
func bigServer(t *testing.T, n int) *httptest.Server {
	t.Helper()
	pad := strings.Repeat("a", n)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":[{"update_id":1,"message":{"message_id":1,"chat":{"id":1,"type":"private"},"text":"` + pad + `"}}]}`))
	}))
	t.Cleanup(s.Close)
	return s
}

func TestCall_GetUpdatesAllowsALargerBodyThanOtherMethods(t *testing.T) {
	srv := bigServer(t, 2<<20)
	ch, _ := telegram.New(deps(kv{"im.telegram.botToken": "1:tok", "im.telegram.apiBase": srv.URL})).(*telegram.Channel)
	var ups []telegram.Update
	if err := ch.CallForTest(context.Background(), "getUpdates", map[string]any{}, &ups); err != nil || len(ups) != 1 {
		t.Fatalf("a 2 MiB getUpdates body must parse: err=%v ups=%d", err, len(ups))
	}
	err := ch.CallForTest(context.Background(), "sendMessage", map[string]any{}, nil)
	if err == nil || !strings.Contains(err.Error(), "response exceeds") || !strings.Contains(err.Error(), "sendMessage") {
		t.Fatalf("a 2 MiB sendMessage body must hit the cap with a clear error, got %v", err)
	}
}
