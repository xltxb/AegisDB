package telegram_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"velagateway/internal/imchannel/telegram"
	"velagateway/internal/imchannel/telegram/tgtest"
)

func runReturns(t *testing.T, ch *telegram.Channel) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := ch.Run(ctx, &sink{})
	if ctx.Err() != nil {
		t.Fatalf("Run kept retrying a permanent failure until the deadline (err=%v)", err)
	}
	return err
}

func TestRun_PermanentClientErrorsStopTheLoop(t *testing.T) {
	t.Run("getMe 404 (malformed token)", func(t *testing.T) {
		srv := tgtest.New(t)
		srv.SetFail("getMe", tgtest.Failure{Code: 404, Desc: "Not Found"})
		err := runReturns(t, newChannel(t, srv, nil))
		if err == nil || !strings.Contains(err.Error(), "404") || strings.Contains(err.Error(), srv.Token) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("setWebhook 400", func(t *testing.T) {
		srv := tgtest.New(t)
		srv.SetFail("setWebhook", tgtest.Failure{Code: 400, Desc: "Bad Request: bad webhook: port is not allowed"})
		ch := newChannel(t, srv, map[string]string{
			"im.telegram.mode": "webhook", "im.telegram.webhookSecret": "s3", "im.callbackBaseURL": "https://gw.example",
		})
		err := runReturns(t, ch)
		if err == nil || !strings.Contains(err.Error(), "port is not allowed") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("getUpdates 404", func(t *testing.T) {
		srv := tgtest.New(t)
		srv.SetFail("getUpdates", tgtest.Failure{Code: 404, Desc: "Not Found"})
		err := runReturns(t, newChannel(t, srv, nil))
		if err == nil || !strings.Contains(err.Error(), "404") {
			t.Fatalf("err = %v", err)
		}
	})
}
