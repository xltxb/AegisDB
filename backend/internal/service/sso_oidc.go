package service

// OIDC 授权码流程(带 PKCE)。整体链路见 sso.go 开头。
//
// 两段状态都放在浏览器里,由网关自己加密(crypto.SealFor,按用途派生的子密钥 + AES-GCM,
// 用途同时作为附加数据 —— 别处加密的东西,哪怕明文恰好长成一张登录结果,也打不开):
//
//   - 流程 Cookie(start → callback):state、nonce、PKCE verifier。不放服务端内存,是因为网关可能
//     多副本部署,回调未必落在发起的那一台上。
//   - 登录结果(callback → POST /auth/sso):身份服务验过的邮箱与姓名,5 分钟有效。回调本身不签
//     会话,是因为管理员可能还要输 TOTP(M8),而回调是一次浏览器跳转,没有地方问他要码。
//
// 两者都带过期时间,并且只有网关能解开:改过的、过期的、别处来的,一律当作没有。

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"aegisdb/pkg/crypto"
)

const (
	// oidcFlowTTL:从点按钮到在身份服务登录完成,给 10 分钟。
	oidcFlowTTL = 10 * time.Minute
	// SSOTicketTTL:回调到登录页换会话之间只隔一次跳转;5 分钟足够输一次 TOTP。
	SSOTicketTTL = 5 * time.Minute
	// oidcTimeout:每次和身份服务的往返(发现、JWKS、换 token、userinfo)。
	oidcTimeout = 10 * time.Second
	// oidcProviderTTL:发现文档缓存多久。身份服务轮换端点很少见,但不该要重启才认。
	oidcProviderTTL = time.Hour
)

// 回调失败的原因,带回登录页(?sso_error=<原因>)由前端翻成人话。只有这几个固定值,
// 不把身份服务给的错误描述原样带回去:那是对方的文本,登录页不该替它说话。
const (
	OIDCErrDisabled = "disabled" // 没有启用 SSO
	OIDCErrState    = "state"    // 流程 Cookie 丢了、过期了、或 state 对不上
	OIDCErrDenied   = "denied"   // 身份服务回了 error(用户取消、未授权这个应用……)
	OIDCErrExchange = "exchange" // 换 token、验 ID Token 失败,或身份服务不可用
	OIDCErrEmail    = "email"    // 拿不到一个(已验证的)邮箱
)

// OIDCError 是回调失败;Reason 是上面那几个值之一。
type OIDCError struct {
	Reason string
	Err    error
}

func (e *OIDCError) Error() string {
	if e.Err == nil {
		return "oidc: " + e.Reason
	}
	return "oidc: " + e.Reason + ": " + e.Err.Error()
}
func (e *OIDCError) Unwrap() error { return e.Err }

func oidcFail(reason string, err error) error { return &OIDCError{Reason: reason, Err: err} }

// oidcFlow 是流程 Cookie 里的内容。
type oidcFlow struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	Issuer   string `json:"i"` // 发起时的 Issuer:中途改了配置,旧流程作废
	Exp      int64  `json:"e"`
}

// ssoTicket 是登录结果 Cookie 里的内容。
type ssoTicket struct {
	Email  string `json:"m"`
	Name   string `json:"u"`
	Issuer string `json:"i"`
	Sub    string `json:"s"`
	Exp    int64  `json:"e"`
}

// oidcProviderCache 缓存发现文档与 JWKS(go-oidc 的 Provider 自带 JWKS 缓存与轮换)。
type oidcProviderCache struct {
	mu     sync.Mutex
	issuer string
	p      *oidc.Provider
	at     time.Time
}

// 封装两段状态时用的用途串:流程与登录结果互不通用。
const (
	sealOIDCFlow  = "sso/oidc-flow/v1"
	sealSSOTicket = "sso/ticket/v1"
)

// oidcMaxBody:发现文档、JWKS、token、userinfo 的响应体上限。正常的都只有几 KB。
const oidcMaxBody = 1 << 20

// oidcHTTP 是和身份服务说话的唯一客户端:
//
//   - 不跟随跳转:每个端点都是发现文档里写明的,一个 302 只可能是配错,或者有人想把网关引到别处。
//   - 拨号时拒绝链路本地、未指定与组播地址(169.254.169.254 云元数据就在其中),也看穿 NAT64 包装。
//     内网与本机地址放行:身份服务部署在内网(Keycloak 之类)是常态。拨号时检查,DNS 换绑也绕不过。
//   - 响应体限 1MB:go-oidc 读发现文档与 JWKS 时不设上限。
var oidcHTTP = &http.Client{
	Timeout: oidcTimeout,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("sso: redirects from the identity provider are not followed")
	},
	Transport: limitBody{&http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout: oidcTimeout,
			Control: func(_, address string, _ syscall.RawConn) error { return checkOIDCDial(address) },
		}).DialContext,
		TLSHandshakeTimeout: oidcTimeout,
	}},
}

func checkOIDCDial(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return nil
	}
	if ip16 := ip.To16(); ip16 != nil && nat64Prefix.Contains(ip16) {
		ip = net.IPv4(ip16[12], ip16[13], ip16[14], ip16[15])
	}
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
		return fmt.Errorf("sso: blocked connection to %s (link-local / metadata)", host)
	}
	return nil
}

type limitBody struct{ rt http.RoundTripper }

func (l limitBody) RoundTrip(r *http.Request) (*http.Response, error) {
	res, err := l.rt.RoundTrip(r)
	if err == nil && res.Body != nil {
		res.Body = struct {
			io.Reader
			io.Closer
		}{io.LimitReader(res.Body, oidcMaxBody), res.Body}
	}
	return res, err
}

func (s *Services) oidcContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), oidcTimeout)
	return oidc.ClientContext(ctx, oidcHTTP), cancel
}

// oidcProvider 返回 issuer 的发现结果。网络请求在锁外做:身份服务慢的时候,登录请求不该排着队
// 等一把锁。刷新失败时继续用上一份(同一个 issuer 的);第一次就失败才报不可用。
func (s *Services) oidcProvider(issuer string) (*oidc.Provider, error) {
	c := &s.oidc
	c.mu.Lock()
	cached, at, same := c.p, c.at, c.issuer == issuer
	c.mu.Unlock()
	if cached != nil && same && time.Since(at) < oidcProviderTTL {
		return cached, nil
	}
	// Provider 之后取 JWKS 用的是这里给的 context,所以它要长期有效:只带 HTTP 客户端,不带超时
	// (超时由 oidcHTTP 自己管)。
	p, err := oidc.NewProvider(oidc.ClientContext(context.Background(), oidcHTTP), issuer)
	if err == nil {
		err = checkOIDCEndpoints(p)
	}
	if err != nil {
		if cached != nil && same {
			slog.Warn("sso: 刷新身份服务的发现文档失败,继续使用上一份", "err", err)
			return cached, nil
		}
		return nil, fmt.Errorf("%w: discovery: %v", ErrSSOUnavailable, err)
	}
	c.mu.Lock()
	c.issuer, c.p, c.at = issuer, p, time.Now()
	c.mu.Unlock()
	return p, nil
}

// checkOIDCEndpoints:发现文档里给出的端点同样要是 https(本地开发除外)。Issuer 在保存时查过,
// 而授权码、Client Secret、token 实际是发往这些端点的 —— 只查 Issuer 等于只锁了门牌。
func checkOIDCEndpoints(p *oidc.Provider) error {
	var meta struct {
		JWKS     string `json:"jwks_uri"`
		UserInfo string `json:"userinfo_endpoint"`
	}
	_ = p.Claims(&meta)
	for name, raw := range map[string]string{
		"authorization_endpoint": p.Endpoint().AuthURL, "token_endpoint": p.Endpoint().TokenURL,
		"jwks_uri": meta.JWKS, "userinfo_endpoint": meta.UserInfo,
	} {
		if raw == "" && (name == "userinfo_endpoint") {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || (u.Scheme != "https" && !(u.Scheme == "http" && AllowInsecureSSO)) {
			return fmt.Errorf("%s must be an https URL: %q", name, raw)
		}
	}
	return nil
}

func oauthConfig(cfg SSOSettings, p *oidc.Provider) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		Endpoint:     p.Endpoint(),
		RedirectURL:  cfg.RedirectURL,
		Scopes:       cfg.Scopes,
	}
}

func randomToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// seal / unseal:JSON → 按用途加密 → base64url,放得进 Cookie。
func seal(purpose string, v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return crypto.SealFor(purpose, b)
}

func unseal(purpose, raw string, v any) error {
	b, err := crypto.OpenFor(purpose, strings.TrimSpace(raw))
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// OIDCStart 发起一次登录:返回要跳去的授权地址,以及要写进流程 Cookie 的值。
func (s *Services) OIDCStart() (authURL, flowCookie string, err error) {
	cfg := s.SSOConfig()
	if !cfg.Enabled {
		return "", "", ErrSSODisabled
	}
	p, err := s.oidcProvider(cfg.Issuer)
	if err != nil {
		return "", "", err
	}
	f := oidcFlow{
		State: randomToken(), Nonce: randomToken(), Verifier: oauth2.GenerateVerifier(),
		Issuer: cfg.Issuer, Exp: time.Now().Add(oidcFlowTTL).Unix(),
	}
	flowCookie, err = seal(sealOIDCFlow, f)
	if err != nil {
		return "", "", err
	}
	authURL = oauthConfig(cfg, p).AuthCodeURL(f.State, oidc.Nonce(f.Nonce), oauth2.S256ChallengeOption(f.Verifier))
	return authURL, flowCookie, nil
}

// OIDCCallback 处理身份服务的回跳:核对 state,换 token,验 ID Token,取邮箱。成功时返回登录结果
// (写进 Cookie,由 POST /auth/sso 消费)。失败返回 *OIDCError。
//
// 这里**不**查网关账号:账号存不存在、停没停用、要不要 TOTP,统一在 SSOLogin 里判,和以前
// 一样只有一处答案。
func (s *Services) OIDCCallback(flowCookie, state, code, idpError, clientIP string) (string, error) {
	cfg := s.SSOConfig()
	if !cfg.Enabled {
		return "", oidcFail(OIDCErrDisabled, nil)
	}
	var f oidcFlow
	if flowCookie == "" || unseal(sealOIDCFlow, flowCookie, &f) != nil || f.Exp < time.Now().Unix() ||
		f.Issuer != cfg.Issuer || state == "" || f.State != state {
		// 回调不是这个浏览器发起的那一次(或已过期)。可能是有人诱导别人的浏览器完成他发起的
		// 登录(登录 CSRF),也可能只是停在身份服务页面太久。留痕,不进门。
		s.auditLoginFail("sso", clientIP)
		return "", oidcFail(OIDCErrState, nil)
	}
	if idpError != "" {
		return "", oidcFail(OIDCErrDenied, errors.New(idpError))
	}
	if code == "" {
		s.auditLoginFail("sso", clientIP)
		return "", oidcFail(OIDCErrExchange, errors.New("no code"))
	}
	p, err := s.oidcProvider(cfg.Issuer)
	if err != nil {
		return "", oidcFail(OIDCErrExchange, err)
	}
	ctx, cancel := s.oidcContext()
	defer cancel()
	tok, err := oauthConfig(cfg, p).Exchange(ctx, code, oauth2.VerifierOption(f.Verifier))
	if err != nil {
		s.auditLoginFail("sso", clientIP)
		return "", oidcFail(OIDCErrExchange, err)
	}
	rawID, _ := tok.Extra("id_token").(string)
	if rawID == "" {
		s.auditLoginFail("sso", clientIP)
		return "", oidcFail(OIDCErrExchange, errors.New("no id_token in token response"))
	}
	idt, err := p.Verifier(&oidc.Config{ClientID: cfg.ClientID}).Verify(ctx, rawID)
	if err != nil {
		s.auditLoginFail("sso", clientIP)
		return "", oidcFail(OIDCErrExchange, err)
	}
	// 多个受众时,azp 必须是自己(OIDC Core 3.1.3.7):别的应用拿到的 token 不能拿来登这里。
	if len(idt.Audience) > 1 {
		var azp struct {
			AZP string `json:"azp"`
		}
		_ = idt.Claims(&azp)
		if azp.AZP != cfg.ClientID {
			s.auditLoginFail("sso", clientIP)
			return "", oidcFail(OIDCErrExchange, errors.New("azp is not this client"))
		}
	}
	if idt.Nonce != f.Nonce {
		s.auditLoginFail("sso", clientIP)
		return "", oidcFail(OIDCErrExchange, errors.New("nonce mismatch"))
	}
	var claims map[string]any
	if err := idt.Claims(&claims); err != nil {
		return "", oidcFail(OIDCErrExchange, err)
	}
	email, name, ok := identityFrom(claims, cfg.EmailClaim)
	if !ok {
		// 有的身份服务只在 userinfo 里给邮箱。sub 必须对得上:userinfo 回答的必须是同一个人。
		if ui, uerr := p.UserInfo(ctx, oauth2.StaticTokenSource(tok)); uerr == nil && ui.Subject == idt.Subject {
			var more map[string]any
			if ui.Claims(&more) == nil {
				for k, v := range more {
					if _, has := claims[k]; !has {
						claims[k] = v
					}
				}
				email, name, ok = identityFrom(claims, cfg.EmailClaim)
			}
		}
	}
	if !ok {
		slog.Warn("sso: 身份服务没有给出可用的邮箱", "claim", cfg.EmailClaim, "sub", idt.Subject)
		return "", oidcFail(OIDCErrEmail, nil)
	}
	t, err := seal(sealSSOTicket, ssoTicket{
		Email: email, Name: fitCol(name, 128), Issuer: idt.Issuer, Sub: fitCol(idt.Subject, 255),
		Exp: time.Now().Add(SSOTicketTTL).Unix(),
	})
	if err != nil {
		return "", oidcFail(OIDCErrExchange, err)
	}
	return t, nil
}

// identityFrom 从声明里取邮箱和姓名。
//
// 邮箱字段是标准的 email 时,要求 email_verified 为真:不少身份服务允许用户自己填邮箱,
// 未验证的邮箱可以是任何人的 —— 而邮箱是网关认人的唯一依据。管理员把字段换成别的(如 upn)
// 时,由他担保那个字段就是本人的邮箱。
func identityFrom(claims map[string]any, emailClaim string) (email, name string, ok bool) {
	raw, _ := claims[emailClaim].(string)
	email = normEmailForCompare(raw)
	// 254 是邮箱的长度上限(RFC 5321);更长的不是邮箱,也会把登录结果 Cookie 撑过浏览器的 4KB。
	if email == "" || !strings.Contains(email, "@") || len(email) > 254 {
		return "", "", false
	}
	if emailClaim == DefaultSSOEmailClaim && !claimTrue(claims["email_verified"]) {
		return "", "", false
	}
	for _, k := range []string{"name", "preferred_username"} {
		if v, _ := claims[k].(string); strings.TrimSpace(v) != "" {
			name = strings.TrimSpace(v)
			break
		}
	}
	return email, name, true
}

// claimTrue:email_verified 规范上是布尔值,但有的身份服务发成字符串 "true"。
func claimTrue(v any) bool {
	switch b := v.(type) {
	case bool:
		return b
	case string:
		return strings.EqualFold(b, "true")
	}
	return false
}

// openSSOTicket 解开回调留下的登录结果。解不开、过期、或者是另一个 Issuer 发的(中途改了配置),
// 都当作没有登录态。
func (s *Services) openSSOTicket(raw string, cfg SSOSettings) (*SSOIdentity, error) {
	var t ssoTicket
	if strings.TrimSpace(raw) == "" || unseal(sealSSOTicket, raw, &t) != nil || t.Exp < time.Now().Unix() ||
		t.Issuer != cfg.Issuer || t.Email == "" {
		return nil, ErrSSONoSession
	}
	return &SSOIdentity{UserName: t.Name, Email: t.Email}, nil
}
