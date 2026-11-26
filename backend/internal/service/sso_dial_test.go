package service

import "testing"

// 和身份服务说话时,拨号阶段挡住链路本地(云元数据)与未指定地址,内网与本机放行。
func TestCheckOIDCDial(t *testing.T) {
	for addr, blocked := range map[string]bool{
		"169.254.169.254:80":      true,
		"[fe80::1]:443":           true,
		"0.0.0.0:443":             true,
		"[64:ff9b::a9fe:a9fe]:80": true, // NAT64 包着 169.254.169.254
		"10.1.2.3:8443":           false,
		"127.0.0.1:8080":          false,
		"idp.example.com:443":     false, // 主机名在解析后的拨号里再查
	} {
		if err := checkOIDCDial(addr); (err != nil) != blocked {
			t.Errorf("checkOIDCDial(%s) err=%v, want blocked=%v", addr, err, blocked)
		}
	}
}
