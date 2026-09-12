package bootstrap

// Webhook 请求带 HMAC-SHA256 签名。
//
// README 写着「Webhook(HMAC-SHA256 签名 + 指数退避重试)」,工单 09 的验收项写着
// 「请求头含 X-Vela-Signature」,而实际发出去的只有 `Authorization: Bearer <secret>`。
// 接收方于是没有任何办法按约定校验来源 —— 它能做的只是比对那枚 Bearer,而那等于
// **每一次推送都把密钥本身发一遍**:任何一跳(反向代理、APM、被错配成 http 的入口)
// 拿到一次请求就拿到了密钥,此后可以随意冒充这个网关往事件中心写数据。
//
// 签名不一样:它每次都不同,截获一条也推不出密钥,而且它同时证明了**这个 body 没被
// 改过**。Bearer 暂时保留(接收方迁移期间还在用它),但从此不再是唯一的凭据。

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestWebhook_RequestCarriesHMACSignature(t *testing.T) {
	var mu sync.Mutex
	var gotSig, gotAuth string
	var gotBody []byte
	received := make(chan struct{}, 1)

	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotSig, gotAuth, gotBody = r.Header.Get("X-Vela-Signature"), r.Header.Get("Authorization"), body
		mu.Unlock()
		select {
		case received <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer stub.Close()

	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	const secret = "sig-s3cr3t"
	app.configureWebhook(token, stub.URL, secret, "exec", 1)

	eq(t, app.do(http.MethodPost, "/api/v1/settings/webhook/test", token, nil).Code, 0, "触发一次测试推送")
	select {
	case <-received:
	case <-time.After(3 * time.Second):
		t.Fatal("stub 没收到推送")
	}

	mu.Lock()
	sig, auth, body := gotSig, gotAuth, append([]byte(nil), gotBody...)
	mu.Unlock()

	if sig == "" {
		t.Fatal("请求没有 X-Vela-Signature —— 接收方没法按 README 说的那样校验来源")
	}
	// 接收方该做的事:拿自己那份密钥对 body 重算一遍。
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(sig), []byte(want)) {
		t.Errorf("签名对不上:\n得到 %s\n期望 %s", sig, want)
	}
	// 过渡期:Bearer 仍然发,接收方迁到签名校验之前不能断。
	if !strings.Contains(auth, "Bearer ") {
		t.Errorf("Bearer 不该在接收方迁移完成之前就撤掉,实际 %q", auth)
	}
}
