package lark

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"

	"velagateway/internal/imchannel"
	"velagateway/internal/imchannel/lark/larktest"
)

func TestNew_PicksTheShapeFromWhatIsConfigured(t *testing.T) {
	if ch := New(deps(kv{})); ch != nil {
		t.Fatalf("nothing configured → nil, got %#v", ch)
	}
	if _, ok := New(deps(kv{"im.lark.notifyWebhook": "https://h"})).(*Channel); !ok {
		t.Fatal("only a group webhook → the notify-only *Channel")
	}
	if _, ok := New(deps(kv{"im.lark.appID": "cli_x"})).(*App); ok {
		t.Fatal("an App ID without its secret is not an app")
	}
	a, ok := New(deps(kv{"im.lark.appID": "cli_x", "im.lark.appSecret": "s"})).(*App)
	if !ok || a.base != "https://open.feishu.cn" || a.mode != modeWS {
		t.Fatalf("defaults: %#v", a)
	}
	a, _ = New(deps(kv{"im.lark.appID": "cli_x", "im.lark.appSecret": "s", "im.lark.domain": "lark", "im.lark.mode": "webhook"})).(*App)
	if a.base != "https://open.larksuite.com" || a.mode != modeWebhook {
		t.Fatalf("lark edition / webhook: base=%s mode=%s", a.base, a.mode)
	}
}

func TestApp_NotifyWithoutAGroupWebhookSaysSo(t *testing.T) {
	a := New(deps(kv{"im.lark.appID": "cli_x", "im.lark.appSecret": "s"})).(*App)
	err := a.NotifyApproval(context.Background(), imchannel.ApprovalCard{ApNo: "AP-1"})
	if err == nil || !strings.Contains(err.Error(), "未配置群通知") {
		t.Fatalf("err = %v", err)
	}
}

func TestSendCard_PostsAnInteractiveCardToTheOpenID(t *testing.T) {
	srv := larktest.New(t)
	a := newTestApp(t, srv, nil)
	ref, err := a.SendCard(context.Background(), "ou_1", imchannel.ApprovalCard{ApNo: "AP-1", Command: "DROP TABLE t;", Risk: "high"})
	if err != nil {
		t.Fatal(err)
	}
	if ref != "om_test101|AP-1" {
		t.Fatalf("ref = %q, want <message_id>|<ApNo>", ref)
	}
	c := srv.Calls("send")[0]
	if c.Str("receive_id_type") != "open_id" || c.Str("receive_id") != "ou_1" || c.Str("msg_type") != "interactive" {
		t.Fatalf("send params = %+v", c.Params)
	}
	for _, want := range []string{`"act":"a"`, `"act":"r"`, `"ap":"AP-1"`, `"update_multi":true`, "DROP TABLE t;"} {
		if !strings.Contains(c.Str("content"), want) {
			t.Errorf("card missing %s: %s", want, c.Str("content"))
		}
	}
}

func TestCloseCard_ReplacesTheCardWithItsFinalState(t *testing.T) {
	srv := larktest.New(t)
	a := newTestApp(t, srv, nil)
	final := imchannel.CardState{Status: "approved", Text: "已由 李四(飞书) 通过"}
	if err := a.CloseCard(context.Background(), "om_test7|AP-1", final); err != nil {
		t.Fatal(err)
	}
	c := srv.Calls("patch")[0]
	content := c.Str("content")
	if c.Str("message_id") != "om_test7" || !strings.Contains(content, "工单 AP-1") ||
		!strings.Contains(content, "已由 李四(飞书) 通过") || !strings.Contains(content, `"template":"green"`) ||
		!strings.Contains(content, `"update_multi":true`) {
		t.Fatalf("patch = %+v", c.Params)
	}
	if strings.Contains(content, `"act"`) {
		t.Fatalf("a closed card must have no buttons: %s", content)
	}
	if err := a.CloseCard(context.Background(), "garbage", final); err == nil {
		t.Fatal("an unparsable ref is an error")
	}
}

func TestCall_RefreshesARejectedToken(t *testing.T) {
	srv := larktest.New(t)
	a := newTestApp(t, srv, nil)
	if _, err := a.SendCard(context.Background(), "ou_1", imchannel.ApprovalCard{ApNo: "AP-1"}); err != nil {
		t.Fatal(err)
	}
	srv.RotateToken() // 后台重置了 Secret、或 token 被提前作废
	if _, err := a.SendCard(context.Background(), "ou_1", imchannel.ApprovalCard{ApNo: "AP-2"}); err != nil {
		t.Fatalf("a rejected cached token must be replaced and the call retried once: %v", err)
	}
	if n := len(srv.Calls("token")); n != 2 {
		t.Fatalf("token fetched %d times, want 2", n)
	}
	// 第一张一次;第二张先带缓存的旧 token 被拒,换新 token 再发一次。不缓存 token 的实现
	// 在这里只会有 2 次。
	if n := len(srv.Calls("send")); n != 3 {
		t.Fatalf("send called %d times, want 3 (ok, rejected with the cached token, retried)", n)
	}
}

func TestCall_ErrorsCarryLarksMessageButNotTheSecret(t *testing.T) {
	srv := larktest.New(t)
	a := newTestApp(t, srv, nil)
	srv.SetFail("send", larktest.Failure{Code: 230013, Msg: "Bot has NO availability to this user."})
	_, err := a.SendCard(context.Background(), "ou_1", imchannel.ApprovalCard{ApNo: "AP-1"})
	if err == nil || !strings.Contains(err.Error(), "230013") || !strings.Contains(err.Error(), "NO availability") {
		t.Fatalf("err = %v", err)
	}
	// 假开放平台会把收到的 app_secret 回显进 msg:错误里只能剩 scrub 的占位符。
	bad := newTestApp(t, srv, kv{"im.lark.appSecret": "the-wrong-secret-value"})
	_, err = bad.botInfo(context.Background())
	if err == nil || !strings.Contains(err.Error(), "10014") || !strings.Contains(err.Error(), "<app-secret>") ||
		strings.Contains(err.Error(), "the-wrong-secret-value") {
		t.Fatalf("credential error = %v", err)
	}
}

func TestBotInfo_ReturnsTheAppName(t *testing.T) {
	srv := larktest.New(t)
	name, err := newTestApp(t, srv, nil).botInfo(context.Background())
	if err != nil || name != srv.BotName {
		t.Fatalf("botInfo = %q, %v", name, err)
	}
}

func TestPermanent(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{&apiError{HTTP: 400, Code: 10014}, true},            // 凭据被拒
		{&apiError{HTTP: 400, Code: 99991672}, true},         // 应用没开机器人能力之类
		{&apiError{HTTP: 404, Code: 0, Msg: "响应无法解析"}, true}, // 开放平台地址填错
		{&apiError{HTTP: 200, Code: 0, Msg: "响应无法解析"}, true}, // 地址指到了一个回 200 HTML 的控制台
		{&apiError{HTTP: 302, Code: 0, Msg: "响应无法解析"}, true}, // 地址被重定向(出站不跟随)
		{fmt.Errorf("%w: 出站地址必须使用 https", errBadBase), true},
		{&apiError{HTTP: 400, Code: codeRateLimited}, false},
		{&apiError{HTTP: 503, Code: 0}, false},
		{&apiError{HTTP: 429, Code: 0}, false},
		{errors.New("dial tcp: connection refused"), false},
		{&apiError{HTTP: 502, Code: 0, Msg: "响应无法解析"}, false},
	} {
		if got := permanent(tc.err); got != tc.want {
			t.Errorf("permanent(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

func TestRequest_AnOutboundGuardRefusalIsPermanentButADNSFailureIsNot(t *testing.T) {
	srv := larktest.New(t)
	a := newTestApp(t, srv, nil)
	a.validate = func(string) error { return errors.New("目标解析到内网/环回地址，已拒绝(防 SSRF)") }
	if _, err := a.botInfo(context.Background()); !permanent(err) {
		t.Fatalf("a refused open-platform address must be permanent, got %v", err)
	}
	a.validate = func(string) error {
		return fmt.Errorf("无法解析主机 %q: %w", "open.feishu.cn", &net.DNSError{Err: "no such host", Name: "open.feishu.cn"})
	}
	if _, err := a.botInfo(context.Background()); err == nil || permanent(err) {
		t.Fatalf("a DNS lookup failure is transient, got %v", err)
	}
}
