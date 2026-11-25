package service

import "testing"

// 2026-11 白盒审计 V-09 / V-11。
func TestSameEmail(t *testing.T) {
	for _, c := range [][2]string{
		{"linwei@vela.io", "linwei@vela.io"},
		{"LinWei@Vela.IO", "linwei@vela.io"},
		{" linwei@vela.io ", "linwei@vela.io"},
		{"JOSÉ@corp.io", "josé@corp.io"}, // 非 ASCII 的大小写照常折叠
		{"张三@corp.io", "张三@corp.io"},
	} {
		if !sameEmail(c[0], c[1]) {
			t.Errorf("%q 与 %q 是同一个邮箱", c[0], c[1])
		}
	}
	for _, c := range [][2]string{
		{"josé@corp.io", "jose@corp.io"}, // 重音
		{"linwéi@vela.io", "linwei@vela.io"},
		{"K@corp.io", "k@corp.io"}, // 开尔文符号
		{"ſam@corp.io", "sam@corp.io"}, // 长 s
		{"a@corp.io", "b@corp.io"},
	} {
		if sameEmail(c[0], c[1]) {
			t.Errorf("%q 与 %q 不是同一个邮箱", c[0], c[1])
		}
	}
}

func TestParseSSOIdentity_KeepsLookalikeDistinct(t *testing.T) {
	id, ok := parseSSOIdentity([]byte("{\"username\":\"x\",\"email\":\"K@Corp.IO\"}"))
	if !ok || id.Email != "K@corp.io" {
		t.Fatalf("开尔文符号不能被折叠成 k,实际 %+v %v", id, ok)
	}
	id, ok = parseSSOIdentity([]byte(`{"username":"x","email":" LinWei@Vela.IO "}`))
	if !ok || id.Email != "linwei@vela.io" {
		t.Fatalf("普通邮箱照旧转小写,实际 %+v %v", id, ok)
	}
}
