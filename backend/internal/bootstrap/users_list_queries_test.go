package bootstrap

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"

	"gorm.io/gorm"

	"velagateway/internal/model"
)

// 黑盒 2026-10 工单 05 第 4 条:GET /users 在 65 个用户时要约 2.5 秒。
//
// 根因是 N²:每一行都要判"能不能停用",而那一判(guardDisable)每次都重新算一遍"现有的活跃
// 管理员" —— 把全部用户读出来、再给每个用户各查两次角色。65 个用户就是 65 × (1 + 65×2)
// 次查询。这里钉住的是形状而不是毫秒:用户再多 40 个,这个接口的查询次数不该跟着涨。
func TestUsersList_QueryCountDoesNotGrowWithUsers(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")

	var n int64
	db := app.repo.DB()
	if err := db.Callback().Query().After("gorm:query").Register("test:count_users_list", func(*gorm.DB) {
		atomic.AddInt64(&n, 1)
	}); err != nil {
		t.Fatal(err)
	}
	measure := func() int64 {
		atomic.StoreInt64(&n, 0)
		r := app.do(http.MethodGet, "/api/v1/users", admin, nil)
		if r.Code != 0 {
			t.Fatalf("list users: %d %s", r.Code, r.Msg)
		}
		return atomic.LoadInt64(&n)
	}

	before := measure()
	role, err := app.repo.GetRoleByCode("admin")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 40; i++ {
		u := &model.User{Name: fmt.Sprintf("bulk%02d", i), Email: fmt.Sprintf("bulk%02d@vela.io", i), RoleID: role.ID, Status: "active", Kind: model.UserKindHuman}
		if err := app.repo.CreateUser(u); err != nil {
			t.Fatal(err)
		}
		if err := app.repo.SetUserRoles(u.ID, []int64{role.ID}); err != nil {
			t.Fatal(err)
		}
	}
	after := measure()
	if after-before > 3 {
		t.Errorf("GET /users ran %d queries with the seed users and %d after adding 40 more: the count must not scale with the user list", before, after)
	}
}

// 优化不能改变每一行的内容:角色名、角色 id、可否停用(自己 / 最后一位管理员)照旧。
func TestUsersList_RowsUnchangedByBatching(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	type row struct {
		ID           int64    `json:"id"`
		Email        string   `json:"email"`
		Roles        []string `json:"roles"`
		RoleIDs      []int64  `json:"roleIds"`
		PrimaryRole  int64    `json:"primaryRoleId"`
		CanDisable   bool     `json:"canDisable"`
		DisableBlock string   `json:"disableBlock"`
	}
	var rows []row
	r := app.do(http.MethodGet, "/api/v1/users", admin, nil)
	if err := json.Unmarshal(r.Data, &rows); err != nil {
		t.Fatal(err)
	}
	for _, x := range rows {
		u, err := app.repo.GetUserByID(x.ID)
		if err != nil {
			t.Fatal(err)
		}
		wantNames, _ := app.repo.RolesOfUser(x.ID)
		if fmt.Sprint(wantNames) != fmt.Sprint(x.Roles) {
			t.Errorf("%s roles = %v, want %v", x.Email, x.Roles, wantNames)
		}
		if want := app.repo.EffectiveRoleIDs(u); fmt.Sprint(want) != fmt.Sprint(x.RoleIDs) {
			t.Errorf("%s roleIds = %v, want %v", x.Email, x.RoleIDs, want)
		}
		if x.Email == "linwei@vela.io" && x.CanDisable {
			t.Errorf("the caller must not be able to disable themself")
		}
	}
}
