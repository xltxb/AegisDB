package service

import "strings"
import "testing"

// ADR 0003 要求出站与回调地址一律走 https。
//
// 明文 http 的代价在这一版尤其具体:每一次推送都带着 `Authorization: Bearer <密钥>`,
// 而 http 意味着链路上任何一跳都能读到它 —— 拿到一次请求就拿到了密钥,此后可以随意
// 冒充这个网关往事件中心写数据。签名(X-Vela-Signature)挡不住这一条:它保护的是
// **内容没被改过**,不是**密钥没被看见**。
//
// dev 仍然放行,理由和私网地址放行是同一个:本机联调的接收端就是 http://localhost。
// 两者共用 AllowPrivateWebhookTargets 这一个开关,因为它们描述的是同一件事 ——
// 「这是不是一台生产网关」。
func TestValidateOutboundURL_RequiresHTTPSInProd(t *testing.T) {
	orig := AllowPrivateWebhookTargets
	defer func() { AllowPrivateWebhookTargets = orig }()

	AllowPrivateWebhookTargets = false // prod posture
	err := validateOutboundURL("http://events.example.com/ingest")
	if err == nil {
		t.Fatal("生产上的明文 http 出站地址应当被拒 —— Bearer 密钥会随每次推送明文过网")
	}
	if !strings.Contains(err.Error(), "https") {
		t.Errorf("拒绝理由要说清是 https 那一条,实际:%v", err)
	}

	// https 不因为协议被拒。这里刻意**不**断言它整体通过 —— 那要真去解析域名,
	// 把一条单元用例变成对 DNS 的依赖;只断言它没有卡在 https 这一关上。
	if err := validateOutboundURL("https://events.example.com/ingest"); err != nil &&
		strings.Contains(err.Error(), "https") {
		t.Errorf("https 地址不该因为协议被拒:%v", err)
	}

	// dev 放行:本机联调的接收端就是 http://localhost。
	AllowPrivateWebhookTargets = true
	if err := validateOutboundURL("http://localhost:9000/ingest"); err != nil {
		t.Errorf("dev 下应当放行 http:%v", err)
	}
}

// 回调地址(别人回调我们)那把尺子松一处、紧一处。
func TestValidateCallbackBaseURL(t *testing.T) {
	orig := AllowPrivateWebhookTargets
	defer func() { AllowPrivateWebhookTargets = orig }()

	AllowPrivateWebhookTargets = false // prod posture

	// 紧的一处:http 不行 —— 回调 URL 里带着 callbackSecret(厂商不支持自定义头时
	// 走 ?secret=),明文等于把密钥挂在链路上。
	if err := ValidateCallbackBaseURL("http://gw.corp.example/api"); err == nil {
		t.Error("生产上的 http 回调地址应当被拒")
	}

	// 松的一处:不查私网。网关常常就装在内网,厂商走专线回调 —— 拿出站那把尺子
	// 去量它,会把一套合法部署判成非法。
	if err := ValidateCallbackBaseURL("https://10.20.3.11:8443"); err != nil {
		t.Errorf("内网的 https 回调地址应当放行(网关本来就可能装在内网):%v", err)
	}

	if err := ValidateCallbackBaseURL("ftp://gw.corp.example"); err == nil {
		t.Error("非 http(s) 协议应当被拒")
	}
	if err := ValidateCallbackBaseURL("https://"); err == nil {
		t.Error("缺主机名应当被拒")
	}
}
