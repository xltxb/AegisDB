package repository

// "谁算平台管理员"这件事,只该有一个答案。
//
// 从前它被抄了四遍 —— 中间件的管理员守卫、执行窗口的改删授权、API 凭据不许绑管理员
// 的校验、审批单的撤回授权 —— 每一处都是同一个循环配同一个裸字符串 "admin"。
//
// 抄四遍的代价不在于哪一遍写错了(四遍都是对的),在于这个答案里有一条**不显然**的
// 规则:管理员身份走并集,一个人的**次要**角色是 admin 也算数。四份拷贝今天都记得
// 这条;而下一个在别处再写一遍 `u.Role.Code == "admin"` 的人不会记得,那一处就会把
// 靠次要角色当管理员的人挡在外面 —— 而且只在他身上出错。

import (
	"testing"

	"velagateway/internal/model"
	"velagateway/internal/testsupport"
)

func newRoleDB(t *testing.T) *Repo {
	t.Helper()
	db := testsupport.NewDB(t)
	return New(db)
}

func seedRoles(t *testing.T, r *Repo) (adminID, devID int64) {
	t.Helper()
	admin := model.Role{Code: model.RoleAdmin, Name: "平台管理员"}
	dev := model.Role{Code: "dev", Name: "开发"}
	for _, role := range []*model.Role{&admin, &dev} {
		if err := r.db.Create(role).Error; err != nil {
			t.Fatalf("create role: %v", err)
		}
	}
	return admin.ID, dev.ID
}

func TestIsPlatformAdmin(t *testing.T) {
	r := newRoleDB(t)
	adminID, devID := seedRoles(t, r)

	primary := model.User{Name: "主岗管理员", Email: "primary@vela.io", RoleID: adminID}
	plain := model.User{Name: "开发", Email: "plain@vela.io", RoleID: devID}
	secondary := model.User{Name: "兼管理员", Email: "secondary@vela.io", RoleID: devID}
	for _, u := range []*model.User{&primary, &plain, &secondary} {
		if err := r.db.Create(u).Error; err != nil {
			t.Fatalf("create user: %v", err)
		}
	}
	// secondary 的主岗是开发,管理员是他的**次要**角色 —— 这正是并集语义那一条。
	if err := r.db.Create(&model.RoleMember{RoleID: adminID, UserID: secondary.ID}).Error; err != nil {
		t.Fatalf("create membership: %v", err)
	}

	for _, tc := range []struct {
		name string
		u    *model.User
		want bool
	}{
		{"主岗就是管理员", &primary, true},
		{"次要角色是管理员", &secondary, true},
		{"不是管理员", &plain, false},
		{"没有用户", nil, false},
	} {
		if got := r.IsPlatformAdmin(tc.u); got != tc.want {
			t.Errorf("%s:IsPlatformAdmin = %v,want %v", tc.name, got, tc.want)
		}
	}
}
