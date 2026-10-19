package lark

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"velagateway/internal/imchannel"
	"velagateway/internal/imchannel/lark/larktest"
)

type kv map[string]string

func (m kv) String(k string) string { return m[k] }
func (m kv) Bool(k string) bool     { return m[k] == "true" }
func (m kv) Secret(k string) string { return m[k] }

func deps(s kv) imchannel.Deps {
	return imchannel.Deps{
		Settings:    s,
		HTTP:        &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DialContext: (&net.Dialer{}).DialContext}},
		ValidateURL: func(string) error { return nil },
	}
}

// newTestApp 造一个指向假开放平台的 *App。
func newTestApp(t *testing.T, srv *larktest.Server, extra kv) *App {
	t.Helper()
	s := kv{"im.lark.appID": srv.AppID, "im.lark.appSecret": srv.AppSecret, "im.lark.apiBase": srv.URL}
	for k, v := range extra {
		s[k] = v
	}
	a, _ := New(deps(s)).(*App)
	if a == nil {
		t.Fatal("a configured app must build")
	}
	return a
}

func TestNew_UnconfiguredIsNil(t *testing.T) {
	if ch := New(deps(kv{})); ch != nil {
		t.Fatalf("no webhook → nil channel, got %#v", ch)
	}
}

func TestNotifyApproval_PostsASignedInteractiveCard(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		_, _ = w.Write([]byte(`{"code":0,"msg":"success"}`))
	}))
	defer srv.Close()

	ch := New(deps(kv{"im.lark.notifyWebhook": srv.URL, "im.lark.notifySecret": "s"})).(imchannel.Notifier)
	err := ch.NotifyApproval(context.Background(), imchannel.ApprovalCard{
		ApNo: "AP-1", Instance: "order", Env: "prod", Risk: "high", Initiator: "张三",
		Command: "SELECT * FROM `t`; ``` <x>", Reason: "r`x", ConsoleURL: "https://gw/#/approvals",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"msg_type":"interactive"`, `"sign"`, "AP-1", "https://gw/#/approvals"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %s: %s", want, body)
		}
	}
	if strings.Contains(body, "``` <x>") || strings.Contains(body, "r`x") {
		t.Errorf("backticks must be neutralised so the code fence cannot be closed: %s", body)
	}
}

func TestNotifyApproval_ReportsLarkErrorCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":19021,"msg":"sign match fail"}`))
	}))
	defer srv.Close()
	ch := New(deps(kv{"im.lark.notifyWebhook": srv.URL})).(imchannel.Notifier)
	err := ch.NotifyApproval(context.Background(), imchannel.ApprovalCard{ApNo: "AP-1"})
	if err == nil || !strings.Contains(err.Error(), "sign match fail") {
		t.Fatalf("err = %v, want the lark message", err)
	}
}

func TestNotifyApproval_HonoursTheOutboundGuard(t *testing.T) {
	d := deps(kv{"im.lark.notifyWebhook": "https://169.254.169.254/hook"})
	d.ValidateURL = func(string) error { return errors.New("blocked") }
	err := New(d).(imchannel.Notifier).NotifyApproval(context.Background(), imchannel.ApprovalCard{})
	if err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("err = %v, want the guard's refusal", err)
	}
}
