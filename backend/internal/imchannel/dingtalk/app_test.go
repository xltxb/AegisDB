package dingtalk

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"velagateway/internal/imchannel"
	"velagateway/internal/imchannel/dingtalk/dttest"
)

type kv map[string]string

func (m kv) String(k string) string { return m[k] }
func (m kv) Bool(k string) bool     { return m[k] == "true" }
func (m kv) Secret(k string) string { return m[k] }

func deps(s kv) imchannel.Deps {
	return imchannel.Deps{
		Settings: s,
		HTTP: &http.Client{
			Timeout:   5 * time.Second,
			Transport: &http.Transport{DialContext: (&net.Dialer{}).DialContext},
			// 与网关的出站客户端(service.newOutboundClient)一样拒绝跟随重定向
			CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("重定向已被禁止(防 SSRF)") },
		},
		ValidateURL: func(string) error { return nil },
	}
}

const testTemplate = "tmpl-test.schema"

// newTestApp 造一个指向假开放平台、配好卡片模板的 *App。
func newTestApp(t *testing.T, srv *dttest.Server, extra kv) *App {
	t.Helper()
	s := kv{
		"im.dingtalk.clientID": srv.ClientID, "im.dingtalk.clientSecret": srv.ClientSecret,
		"im.dingtalk.cardTemplateId": testTemplate, "im.dingtalk.apiBase": srv.URL,
	}
	for k, v := range extra {
		s[k] = v
	}
	a, _ := New(deps(s)).(*App)
	if a == nil {
		t.Fatal("a configured app must build")
	}
	return a
}

func TestNew_NeedsBothCredentials(t *testing.T) {
	if ch := New(deps(kv{})); ch != nil {
		t.Fatalf("nothing configured → nil, got %#v", ch)
	}
	if ch := New(deps(kv{"im.dingtalk.clientID": "dingx"})); ch != nil {
		t.Fatal("a Client ID without its secret is not an app")
	}
	a, ok := New(deps(kv{"im.dingtalk.clientID": "dingx", "im.dingtalk.clientSecret": "s"})).(*App)
	if !ok || a.base != "https://api.dingtalk.com" || a.robotCode != "dingx" || a.template != "" || a.Kind() != imchannel.DingTalk {
		t.Fatalf("defaults: %#v", a)
	}
	a, _ = New(deps(kv{
		"im.dingtalk.clientID": "dingx", "im.dingtalk.clientSecret": "s",
		"im.dingtalk.robotCode": "dingrobot", "im.dingtalk.apiBase": "https://dt.corp.io/",
	})).(*App)
	if a.robotCode != "dingrobot" || a.base != "https://dt.corp.io" {
		t.Fatalf("overrides: robotCode=%s base=%s", a.robotCode, a.base)
	}
}

func TestSendCard_DeliversTheTemplateToTheUsersRobotChat(t *testing.T) {
	srv := dttest.New(t)
	a := newTestApp(t, srv, nil)
	ref, err := a.SendCard(context.Background(), "staff1", imchannel.ApprovalCard{
		ApNo: "AP-2294", Command: "DELETE FROM t WHERE a < b & c;", Risk: "high", Env: "prod", Instance: "pg-main", Initiator: "张伟",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ap, ok := trackApNo(ref); !ok || ap != "AP-2294" {
		t.Fatalf("ref = %q, want a gateway outTrackId for AP-2294", ref)
	}
	c := srv.Calls("deliver")[0]
	for k, want := range map[string]string{
		"cardTemplateId": testTemplate, "outTrackId": ref, "callbackType": "STREAM",
		"openSpaceId": "dtv1.card//IM_ROBOT.staff1", "userIdType": "1",
	} {
		if got := c.Str(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if c.At("imRobotOpenDeliverModel", "robotCode") != srv.ClientID || c.At("imRobotOpenDeliverModel", "spaceType") != "IM_ROBOT" ||
		c.At("imRobotOpenSpaceModel", "supportForward") != "false" {
		t.Fatalf("deliver models = %+v", c.Params)
	}
	if got := c.At("cardData", "cardParamMap", "command"); got != "DELETE FROM t WHERE a < b & c;" {
		t.Errorf("the command must arrive verbatim, got %q", got)
	}
	if c.At("cardData", "cardParamMap", "status") != statusPending {
		t.Errorf("a fresh card is pending: %+v", c.Params)
	}
	other, err := a.SendCard(context.Background(), "staff2", imchannel.ApprovalCard{ApNo: "AP-2294"})
	if err != nil || other == ref {
		t.Fatalf("each recipient's card needs its own outTrackId: %q vs %q (%v)", other, ref, err)
	}
}

func TestSendCard_RefusesWhatItCannotAddress(t *testing.T) {
	srv := dttest.New(t)
	a := newTestApp(t, srv, nil)
	if _, err := a.SendCard(context.Background(), "staff.1/x", imchannel.ApprovalCard{ApNo: "AP-1"}); err == nil {
		t.Error("a userid outside the whitelist must not reach openSpaceId")
	}
	if _, err := a.SendCard(context.Background(), "staff1", imchannel.ApprovalCard{ApNo: "AP 1"}); err == nil {
		t.Error("an approval number outside the whitelist cannot name a card")
	}
	noTmpl := newTestApp(t, srv, kv{"im.dingtalk.cardTemplateId": ""})
	if _, err := noTmpl.SendCard(context.Background(), "staff1", imchannel.ApprovalCard{ApNo: "AP-1"}); !errors.Is(err, errNoTemplate) {
		t.Errorf("no template → errNoTemplate, got %v", err)
	}
	if n := len(srv.Calls("deliver")); n != 0 {
		t.Fatalf("%d deliveries were attempted", n)
	}
}

func TestSendCard_AFailedDeliveryIsAnError(t *testing.T) {
	srv := dttest.New(t)
	a := newTestApp(t, srv, nil)
	srv.RejectDelivery("用户不在应用可见范围内")
	if _, err := a.SendCard(context.Background(), "staff1", imchannel.ApprovalCard{ApNo: "AP-1"}); err == nil || !strings.Contains(err.Error(), "可见范围") {
		t.Fatalf("an HTTP-200 delivery that failed must be an error, got %v", err)
	}
}

func TestCloseCard_UpdatesOnlyTheStatusVariables(t *testing.T) {
	srv := dttest.New(t)
	a := newTestApp(t, srv, nil)
	final := imchannel.CardState{Status: "approved", Text: "已由 李四(钉钉) 通过"}
	const ref = "aegis-AP-1-0123456789abcdef"
	if err := a.CloseCard(context.Background(), ref, final); err != nil {
		t.Fatal(err)
	}
	c := srv.Calls("update")[0]
	if c.Str("outTrackId") != ref || c.At("cardData", "cardParamMap", "status") != statusDone ||
		c.At("cardData", "cardParamMap", "result") != final.Text || c.At("cardUpdateOptions", "updateCardDataByKey") != "true" {
		t.Fatalf("update = %+v", c.Params)
	}
	if c.At("cardData", "cardParamMap", "command") != "" {
		t.Fatal("closing must not rewrite the rest of the card")
	}
	if err := a.CloseCard(context.Background(), "garbage", final); err == nil {
		t.Fatal("an unparsable ref is an error")
	}
}

func TestSendText_SendsARobotDirectMessage(t *testing.T) {
	srv := dttest.New(t)
	a := newTestApp(t, srv, nil)
	if err := a.sendText(context.Background(), "staff1", "已绑定到 张伟"); err != nil {
		t.Fatal(err)
	}
	c := srv.Calls("oto")[0]
	if c.Str("robotCode") != srv.ClientID || c.Str("userIds") != `["staff1"]` || c.Str("msgKey") != "sampleText" ||
		c.Str("msgParam") != `{"content":"已绑定到 张伟"}` {
		t.Fatalf("oto = %+v", c.Params)
	}
}

func TestCall_CachesTheTokenAndRefreshesARejectedOne(t *testing.T) {
	srv := dttest.New(t)
	a := newTestApp(t, srv, nil)
	for i := 0; i < 2; i++ {
		if _, err := a.SendCard(context.Background(), "staff1", imchannel.ApprovalCard{ApNo: "AP-1"}); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(srv.Calls("token")); n != 1 {
		t.Fatalf("token fetched %d times for two cards, want it cached", n)
	}
	srv.RotateToken() // 后台重置了 Secret、或 token 被提前作废
	if _, err := a.SendCard(context.Background(), "staff1", imchannel.ApprovalCard{ApNo: "AP-2"}); err != nil {
		t.Fatalf("a rejected cached token must be replaced and the call retried once: %v", err)
	}
	if n := len(srv.Calls("token")); n != 2 {
		t.Fatalf("token fetched %d times, want 2", n)
	}
	// 两张正常的,第三张先带缓存的旧 token 被拒(401),换新 token 再投一次。
	if n := len(srv.Calls("deliver")); n != 4 {
		t.Fatalf("deliver called %d times, want 4", n)
	}
}

func TestCall_ErrorsCarryDingTalksMessageButNotTheSecret(t *testing.T) {
	srv := dttest.New(t)
	a := newTestApp(t, srv, nil)
	srv.SetFail("deliver", dttest.Failure{Code: "param.templateNotExist", Msg: "卡片模板不存在"})
	_, err := a.SendCard(context.Background(), "staff1", imchannel.ApprovalCard{ApNo: "AP-1"})
	if err == nil || !strings.Contains(err.Error(), "param.templateNotExist") || !strings.Contains(err.Error(), "卡片模板不存在") {
		t.Fatalf("err = %v", err)
	}
	// 假开放平台会把收到的 secret 回显进 message:错误里只能剩 scrub 的占位符。
	bad := newTestApp(t, srv, kv{"im.dingtalk.clientSecret": "the-wrong-secret-value"})
	_, err = bad.token(context.Background())
	if err == nil || !strings.Contains(err.Error(), "invalidClientIdOrSecret") || !strings.Contains(err.Error(), "<client-secret>") ||
		strings.Contains(err.Error(), "the-wrong-secret-value") {
		t.Fatalf("credential error = %v", err)
	}
}

func TestPermanent(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{&apiError{HTTP: 400, Code: "invalidClientIdOrSecret"}, true},                            // 凭据被拒
		{&apiError{HTTP: 403, Code: "Forbidden.AccessDenied.AccessTokenPermissionDenied"}, true}, // 没开权限
		{&apiError{HTTP: 200, Msg: "响应无法解析,检查开放平台地址"}, true},                                     // 地址指到了一个回 200 HTML 的页面
		{&apiError{HTTP: 302, Msg: "响应无法解析,检查开放平台地址"}, true},                                     // 地址被重定向(出站不跟随)
		{fmt.Errorf("%w: 出站地址必须使用 https", errBadBase), true},
		{errNoTemplate, true},
		{&apiError{HTTP: 403, Code: "Forbidden.AccessDenied.QpsLimitForApi"}, false}, // 限流
		{&apiError{HTTP: 400, Code: "Throttling.Api"}, false},
		{&apiError{HTTP: 429}, false},
		{&apiError{HTTP: 503}, false},
		{&apiError{HTTP: 502, Msg: "响应无法解析"}, false},
		{errors.New("dial tcp: connection refused"), false},
	} {
		if got := permanent(tc.err); got != tc.want {
			t.Errorf("permanent(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

func TestRequest_AWrongOpenPlatformAddressIsPermanent(t *testing.T) {
	srv := dttest.New(t)
	a := newTestApp(t, srv, nil)
	srv.SetFail("token", dttest.Failure{Raw: "<html>console</html>"})
	if _, err := a.token(context.Background()); !permanent(err) {
		t.Fatalf("a 200 HTML page must be permanent, got %v", err)
	}
	srv.SetFail("token", dttest.Failure{HTTP: http.StatusFound, Location: "https://elsewhere.example/"})
	if _, err := a.token(context.Background()); !permanent(err) {
		t.Fatalf("a redirect must be permanent, got %v", err)
	}
	a.validate = func(string) error { return errors.New("目标解析到内网/环回地址，已拒绝(防 SSRF)") }
	if _, err := a.token(context.Background()); !permanent(err) {
		t.Fatalf("a refused open-platform address must be permanent, got %v", err)
	}
	a.validate = func(string) error {
		return fmt.Errorf("无法解析主机 %q: %w", "api.dingtalk.com", &net.DNSError{Err: "no such host", Name: "api.dingtalk.com"})
	}
	if _, err := a.token(context.Background()); err == nil || permanent(err) {
		t.Fatalf("a DNS lookup failure is transient, got %v", err)
	}
}
