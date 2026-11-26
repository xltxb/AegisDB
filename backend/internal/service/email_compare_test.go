package service

import "testing"

// 2026-11 白盒审计 V-09 / V-11。
func TestSameEmail(t *testing.T) {
	for _, c := range [][2]string{
		{"linwei@aegisdb.io", "linwei@aegisdb.io"},
		{"LinWei@AegisDB.IO", "linwei@aegisdb.io"},
		{" linwei@aegisdb.io ", "linwei@aegisdb.io"},
		{"JOSÉ@corp.io", "josé@corp.io"}, // 非 ASCII 的大小写照常折叠
		{"张三@corp.io", "张三@corp.io"},
	} {
		if !sameEmail(c[0], c[1]) {
			t.Errorf("%q 与 %q 是同一个邮箱", c[0], c[1])
		}
	}
	for _, c := range [][2]string{
		{"josé@corp.io", "jose@corp.io"}, // 重音
		{"linwéi@aegisdb.io", "linwei@aegisdb.io"},
		{"K@corp.io", "k@corp.io"},     // 开尔文符号
		{"ſam@corp.io", "sam@corp.io"}, // 长 s
		{"a@corp.io", "b@corp.io"},
	} {
		if sameEmail(c[0], c[1]) {
			t.Errorf("%q 与 %q 不是同一个邮箱", c[0], c[1])
		}
	}
}

func TestIdentityFrom_KeepsLookalikeDistinct(t *testing.T) {
	email, _, ok := identityFrom(map[string]any{"email": "\u212aa@Corp.IO", "email_verified": true}, "email")
	if !ok || email != "\u212aa@corp.io" {
		t.Fatalf("开尔文符号不能被折叠成 k,实际 %q %v", email, ok)
	}
	email, _, ok = identityFrom(map[string]any{"email": " LinWei@AegisDB.IO ", "email_verified": true}, "email")
	if !ok || email != "linwei@aegisdb.io" {
		t.Fatalf("普通邮箱照旧转小写,实际 %q %v", email, ok)
	}
}
