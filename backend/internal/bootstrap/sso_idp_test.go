package bootstrap

// 一个够用的假 OIDC 身份服务,以及把一次浏览器登录完整走一遍的驱动。
//
// 假身份服务实现网关会碰到的全部端点:发现文档、JWKS、authorize、token、userinfo。它**强制**
// PKCE(S256)并核对 redirect_uri 与 client 凭据 —— 网关漏掉任何一样,登录就失败,测试就能发现。

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"

	"aegisdb/internal/service"
)

const (
	testClientID     = "aegisdb-test"
	testClientSecret = "s3cret-for-tests"
)

type idpGrant struct {
	nonce, challenge, redirect string
	claims                     map[string]any
}

// fakeIdP:next 是下一个来登录的人的声明(由测试设定)。
type fakeIdP struct {
	t   *testing.T
	srv *httptest.Server
	key *rsa.PrivateKey

	mu     sync.Mutex
	next   map[string]any
	grants map[string]idpGrant // code → 那一次授权
	tokens map[string]map[string]any

	// 故障开关
	deny          bool                        // authorize 回 error=access_denied
	failToken     bool                        // token 端点回 400
	idTokenTamper func(claims map[string]any) // 签发前改 ID Token 声明(错 nonce、错 aud……)
	userinfo      map[string]any              // 非空时 userinfo 回它(否则回登录的声明)
	stripIDToken  []string                    // 从 ID Token 里拿掉的声明(只放在 userinfo 里)
	signWith      *rsa.PrivateKey             // 非空时用它签 ID Token(JWKS 仍发布原来的公钥)
	tokenCalls    int
}

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeIdP{t: t, key: key, grants: map[string]idpGrant{}, tokens: map[string]map[string]any{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", f.discovery)
	mux.HandleFunc("/jwks", f.jwks)
	mux.HandleFunc("/authorize", f.authorize)
	mux.HandleFunc("/token", f.token)
	mux.HandleFunc("/userinfo", f.userInfo)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// as 设定下一个登录的人。email_verified 默认为真。
func (f *fakeIdP) as(claims map[string]any) *fakeIdP {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := map[string]any{"email_verified": true}
	for k, v := range claims {
		c[k] = v
	}
	if _, ok := c["sub"]; !ok {
		c["sub"] = "sub-" + strings.ToLower(strings.SplitN(c["email"].(string), "@", 2)[0])
	}
	f.next = c
	return f
}

func (f *fakeIdP) discovery(w http.ResponseWriter, _ *http.Request) {
	u := f.srv.URL
	_ = json.NewEncoder(w).Encode(map[string]any{
		"issuer": u, "authorization_endpoint": u + "/authorize", "token_endpoint": u + "/token",
		"jwks_uri": u + "/jwks", "userinfo_endpoint": u + "/userinfo",
		"response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"code_challenge_methods_supported":      []string{"S256"},
	})
}

func (f *fakeIdP) jwks(w http.ResponseWriter, _ *http.Request) {
	_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
		Key: &f.key.PublicKey, KeyID: "k1", Algorithm: string(jose.RS256), Use: "sig",
	}}})
}

func (f *fakeIdP) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	redirect, _ := url.Parse(q.Get("redirect_uri"))
	back := redirect.Query()
	back.Set("state", q.Get("state"))
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case f.deny:
		back.Set("error", "access_denied")
	case q.Get("client_id") != testClientID || q.Get("response_type") != "code":
		back.Set("error", "invalid_request")
	case q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "":
		// 网关没发 PKCE:规范允许身份服务拒绝,这里就拒绝 —— 测试要能看出来。
		back.Set("error", "invalid_request")
	case !strings.Contains(" "+q.Get("scope")+" ", " openid "):
		back.Set("error", "invalid_scope")
	default:
		code := randomString()
		f.grants[code] = idpGrant{nonce: q.Get("nonce"), challenge: q.Get("code_challenge"), redirect: q.Get("redirect_uri"), claims: f.next}
		back.Set("code", code)
	}
	redirect.RawQuery = back.Encode()
	http.Redirect(w, r, redirect.String(), http.StatusFound)
}

func (f *fakeIdP) token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokenCalls++
	fail := func(code string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
	}
	id, secret, ok := r.BasicAuth()
	if !ok {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	g, found := f.grants[r.PostForm.Get("code")]
	delete(f.grants, r.PostForm.Get("code")) // 授权码一次性
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	switch {
	case f.failToken:
		fail("server_error")
		return
	case id != testClientID || secret != testClientSecret:
		fail("invalid_client")
		return
	case !found || r.PostForm.Get("grant_type") != "authorization_code":
		fail("invalid_grant")
		return
	case base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge:
		fail("invalid_grant") // PKCE 对不上
		return
	case r.PostForm.Get("redirect_uri") != g.redirect:
		fail("invalid_grant")
		return
	}
	claims := map[string]any{
		"iss": f.srv.URL, "aud": testClientID, "iat": time.Now().Unix(),
		"exp": time.Now().Add(5 * time.Minute).Unix(), "nonce": g.nonce,
	}
	for k, v := range g.claims {
		claims[k] = v
	}
	for _, k := range f.stripIDToken {
		delete(claims, k)
	}
	if f.idTokenTamper != nil {
		f.idTokenTamper(claims)
	}
	at := "at-" + randomString()
	f.tokens[at] = g.claims
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": at, "token_type": "Bearer", "expires_in": 300, "id_token": f.sign(claims),
	})
}

func (f *fakeIdP) userInfo(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	claims, ok := f.tokens[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if f.userinfo != nil {
		claims = f.userinfo
	}
	_ = json.NewEncoder(w).Encode(claims)
}

func (f *fakeIdP) sign(claims map[string]any) string {
	key := f.key
	if f.signWith != nil {
		key = f.signWith
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "k1"))
	if err != nil {
		f.t.Fatal(err)
	}
	b, _ := json.Marshal(claims)
	obj, err := signer.Sign(b)
	if err != nil {
		f.t.Fatal(err)
	}
	s, _ := obj.CompactSerialize()
	return s
}

func randomString() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// newSSOApp:一个启用了 SSO、指向假身份服务的网关。
func newSSOApp(t *testing.T, idp *fakeIdP) *testApp {
	t.Helper()
	app := newTestApp(t)
	for k, v := range map[string]string{
		service.SettingSSOEnabled:      "true",
		service.SettingSSOIssuer:       `"` + idp.srv.URL + `"`,
		service.SettingSSOClientID:     `"` + testClientID + `"`,
		service.SettingSSOClientSecret: `"` + testClientSecret + `"`,
		service.SettingSSORedirectURL:  `"` + app.srv.URL + service.OIDCCallbackPath + `"`,
	} {
		if err := app.repo.SetSetting(k, v); err != nil {
			t.Fatal(err)
		}
	}
	return app
}

// browser 是一个带 Cookie、不自动跟随跳转的浏览器 —— 每一跳都由测试看着走。
func newBrowser() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func location(t *testing.T, c *http.Client, rawURL string) string {
	t.Helper()
	res, err := c.Get(rawURL)
	if err != nil {
		t.Fatalf("GET %s: %v", rawURL, err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusFound {
		t.Fatalf("GET %s: 期望 302,实际 %d", rawURL, res.StatusCode)
	}
	return res.Header.Get("Location")
}

// ssoBrowse 走完浏览器那一段:点按钮 → 身份服务 → 回调。返回浏览器和回调最后跳去的地址
// (成功是 …/#/login?sso=1,失败是 …/#/login?sso_error=<原因>)。
func (a *testApp) ssoBrowse(b *http.Client) string {
	a.t.Helper()
	toIdP := location(a.t, b, a.srv.URL+service.OIDCStartPath)
	if !strings.Contains(toIdP, "/authorize?") {
		return toIdP // 没去身份服务:start 自己就失败了
	}
	toCallback := location(a.t, b, toIdP)
	return location(a.t, b, toCallback)
}

// ssoPost:登录页回来后换会话(浏览器带着回调留下的 Cookie)。
func (a *testApp) ssoPost(b *http.Client, body any) apiResp {
	a.t.Helper()
	raw, _ := json.Marshal(body)
	if body == nil {
		raw = []byte("{}")
	}
	res, err := b.Post(a.srv.URL+"/api/v1/auth/sso", "application/json", strings.NewReader(string(raw)))
	if err != nil {
		a.t.Fatalf("POST /auth/sso: %v", err)
	}
	defer res.Body.Close()
	var r apiResp
	_ = json.NewDecoder(res.Body).Decode(&r)
	return r
}

// ssoLogin 以 claims 这个人完整登录一次。
func (a *testApp) ssoLogin(idp *fakeIdP, claims map[string]any) apiResp {
	a.t.Helper()
	idp.as(claims)
	b := newBrowser()
	if back := a.ssoBrowse(b); !strings.HasSuffix(back, "/#/login?sso=1") {
		a.t.Fatalf("回调没有成功跳回登录页:%s", back)
	}
	return a.ssoPost(b, nil)
}

func ssoToken(t *testing.T, r apiResp) string {
	t.Helper()
	var d struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(r.Data, &d)
	if d.Token == "" {
		t.Fatalf("SSO 登录应成功,实际 code=%d msg=%q", r.Code, r.Msg)
	}
	return d.Token
}
