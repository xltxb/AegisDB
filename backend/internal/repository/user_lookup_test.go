package repository

import (
	"testing"

	"velagateway/internal/testsupport"
)

// 邮箱查询不分大小写。
//
// MySQL 的 utf8mb4_unicode_ci 白送了这个行为,迁到 PG 之后它没了:入库时
// admin.go 会 ToLower,查询时不会,于是「用注册时那个大写写法登录」会失败,
// 而且失败得毫无线索 —— 账号明明在库里。
func TestGetUserByEmail_IsCaseInsensitive(t *testing.T) {
	db := testsupport.NewDB(t)
	r := New(db)

	if err := db.Exec(`INSERT INTO tbl_user (name, email, role_id, status)
	                   VALUES ('林薇', 'linwei@vela.io', 1, 'active')`).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}

	for _, in := range []string{"linwei@vela.io", "Linwei@Vela.io", "LINWEI@VELA.IO"} {
		u, err := r.GetUserByEmail(in)
		if err != nil {
			t.Errorf("GetUserByEmail(%q): %v", in, err)
			continue
		}
		if u.Email != "linwei@vela.io" {
			t.Errorf("GetUserByEmail(%q) = %q", in, u.Email)
		}
	}
}
