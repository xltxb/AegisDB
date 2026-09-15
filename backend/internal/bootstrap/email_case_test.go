package bootstrap

// 邮箱在这套系统里有且只有一种规范形式:小写。baseline 把 `idx_user_email` 建在
// `lower(email)` 上,所以**大小写不同的两个邮箱是同一个账号** —— 而这件事必须由
// 入库侧和查询侧一起兑现,漏掉任何一侧都会把「同一个账号」变成「查不到 + 建不进去」。
//
// 迁移到 PostgreSQL 之前,MySQL 的 `utf8mb4_unicode_ci` 让裸列比较自带折叠,所以
// 两侧都不折也能跑对。迁移之后这层默认没有了,而漏网的那两处正好凑成一条闭环:
//
//  1. `Invite` 把 `Ops@Vela.io` 原样写进库(全仓唯一一条既不折叠也不查重的建号路径);
//  2. 数月后运维按 DEPLOY.md §5 重置平台管理员,`UpsertAdmin` 先把入参折成小写,
//     再用裸列 `email = ?` 去找 —— 在 PG 上找不到那一行;
//  3. 于是走进 `db.Create` 分支,撞上 `lower(email)` 上的唯一索引,报
//     `duplicate key value violates unique constraint "idx_user_email"`;
//  4. `main.go` 把它当致命错误 `os.Exit(1)`。**管理员口令重置这条应急通道当场失效**,
//     而报错完全不指向真因。
//
// 下面两条用例各按住闭环的一端。

import (
	"net/http"
	"testing"

	"velagateway/internal/model"
	"velagateway/internal/repository"
	"velagateway/internal/testsupport"
	"velagateway/pkg/crypto"
)

// UpsertAdmin 必须认出库里那一行大小写不同的邮箱,走「重置口令」而不是「再建一个」。
func TestUpsertAdmin_MatchesAnExistingRowCaseInsensitively(t *testing.T) {
	cfg := &Config{}
	cfg.Gateway.DefaultPolicy = "strict"

	db := testsupport.NewDB(t)
	repo := repository.New(db)
	if err := InitDatabase(repo, cfg, "ops@corp.io", "S3cret!Passw0rd", "Ops Admin"); err != nil {
		t.Fatalf("init: %v", err)
	}

	// 一行历史数据,字面量带大小写 —— 正是 Invite 从前会留下的那种。
	role, err := repo.GetRoleByCode(model.RoleAdmin)
	if err != nil || role == nil {
		t.Fatalf("admin role: %v", err)
	}
	legacy := &model.User{
		Name: "Ops", Email: "Ops@Vela.io", RoleID: role.ID, Status: "active",
		Initials: "OP", Dept: "—", LastActive: "—", PasswordHash: "not-a-real-hash",
	}
	if err := repo.CreateUser(legacy); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}

	before := repo.Count(&model.User{})

	// 运维照 DEPLOY.md 敲的是全小写。
	u, err := UpsertAdmin(repo, "ops@vela.io", "R3set!Passw0rd", "")
	if err != nil {
		t.Fatalf("UpsertAdmin 没能重置那一行的口令,而这是管理员口令重置的唯一通道:%v", err)
	}
	if u.ID != legacy.ID {
		t.Errorf("UpsertAdmin 动的不是那一行历史数据(id=%d,期望 %d)", u.ID, legacy.ID)
	}
	if after := repo.Count(&model.User{}); after != before {
		t.Errorf("用户数从 %d 变成 %d —— 大小写不同的同一个邮箱被当成两个账号了", before, after)
	}

	got, err := repo.GetUserByEmail("ops@vela.io")
	if err != nil {
		t.Fatalf("重置之后查不到那个账号:%v", err)
	}
	if !crypto.CheckPassword(got.PasswordHash, "R3set!Passw0rd") {
		t.Error("口令没有被重置 —— 应急通道等于没通")
	}
}

// Invite 是全仓唯一一条既不 ToLower 也不查重的建号路径,也就是上面那行历史数据的源头。
func TestInvite_StoresTheEmailLowercased(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")

	eq(t, app.do(http.MethodPost, "/api/v1/users/invite", admin,
		map[string]any{"email": "  Mixed@Case.io  ", "roleId": app.roleIDByCode(admin, "ro")}).Code,
		0, "邀请应当成功")

	u, err := app.repo.GetUserByEmail("mixed@case.io")
	if err != nil {
		t.Fatalf("邀请出来的账号查不到:%v", err)
	}
	if u.Email != "mixed@case.io" {
		t.Errorf("入库的邮箱是 %q,期望 %q —— 大小写/空白没被规范化,"+
			"这一行日后会让 UpsertAdmin 撞上 lower(email) 的唯一索引", u.Email, "mixed@case.io")
	}
}
