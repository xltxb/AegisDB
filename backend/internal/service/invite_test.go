package service

import (
	"errors"
	"testing"

	"velagateway/internal/dto"
	"velagateway/internal/model"
	"velagateway/internal/repository"
	"velagateway/internal/testsupport"
)

// seedInviteRole 落一个角色,让 Invite 的 validateRoleIDs 能过 —— 这条用例要压的
// 是邮箱校验,不该被角色不存在挡在更前面。
func seedInviteRole(t *testing.T, s *Services) {
	t.Helper()
	r := &model.Role{Code: "invitee", Name: "受邀者", Layer: "platform"}
	if err := s.Repo.DB().Create(r).Error; err != nil {
		t.Fatalf("seed role: %v", err)
	}
}

// Invite 是全仓唯一一条不校验邮箱形状的建号路径。binding:"required" 只挡得住
// 空字符串,而 " " 经 TrimSpace 之后写进库的是一行**空 email** —— 那一行随后要
// 参与 lower(email) 唯一索引,于是第二次同样的误操作会撞上唯一约束,而报错指向
// 的是索引不是那次输入。CreateUser(admin.go:768) 早就在做这个校验,照它对齐。
func TestInvite_RejectsMalformedEmail(t *testing.T) {
	db := testsupport.NewDB(t)
	s := &Services{Repo: repository.New(db)}
	seedInviteRole(t, s)

	for _, email := range []string{"", "   ", "nobody-at-all"} {
		_, err := s.Invite(dto.InviteReq{Email: email, RoleID: 1})
		if err == nil {
			t.Errorf("Invite(%q) 本应被拒绝,却建出了账号", email)
			continue
		}
		if !errors.Is(err, ErrBadRequest) {
			t.Errorf("Invite(%q) 的错误 = %v, 想要 ErrBadRequest", email, err)
		}
	}
}

// 邀请一个已经在库里的邮箱,应当在 service 层被拒,而不是放它去撞 idx_user_email。
//
// 对外的 code 两种走法都是 40001 —— handler 把 Invite 的任何 error 都翻成
// 「邀请失败:邮箱可能已存在」。差别在服务端:撞索引会在日志里留下一条唯一约束
// 冲突,读日志的人看到的是数据库在报错,而不是一次被正常拒绝的输入。
//
// 大小写变体一并压住:库里存的已经是折叠后的小写,而 idx_user_email 建在
// lower(email) 上,所以 `Ops@Vela.io` 和 `ops@vela.io` 指的是同一个账号。
func TestInvite_RejectsDuplicateEmail(t *testing.T) {
	db := testsupport.NewDB(t)
	s := &Services{Repo: repository.New(db)}
	seedInviteRole(t, s)

	if _, err := s.Invite(dto.InviteReq{Email: "ops@vela.io", RoleID: 1}); err != nil {
		t.Fatalf("第一次邀请: %v", err)
	}

	for _, again := range []string{"ops@vela.io", "Ops@Vela.io", "OPS@VELA.IO"} {
		_, err := s.Invite(dto.InviteReq{Email: again, RoleID: 1})
		if err == nil {
			t.Errorf("Invite(%q) 重复邀请本应被拒绝,却又建了一个账号", again)
			continue
		}
		if !errors.Is(err, ErrBadRequest) {
			t.Errorf("Invite(%q) 的错误 = %v, 想要 ErrBadRequest", again, err)
		}
	}
}
