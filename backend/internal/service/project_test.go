package service

import (
	"strings"
	"testing"

	"velagateway/internal/dto"
	"velagateway/internal/model"
	"velagateway/internal/repository"
	"velagateway/internal/testsupport"
)

// 项目查重(GetProjectByName)是 CreateProject 用来给出友好提示的预检查,它守护的
// uk_project_name 唯一索引现在建在 lower(name) 上(同一次任务的上一轮修的)。查重
// 如果不跟着折叠,就会出现:已有项目 Foo,创建 foo 时预检查裸列比较查不到、放行、
// 撞上索引 —— 那个未经转换的 Postgres 错误(duplicate key value violates unique
// constraint "uk_project_name" ...)会原样从 CreateProject 一路吐到前端,取代本该
// 出现的「项目「foo」已存在」。
func TestCreateProject_DuplicateNameDifferentCase_GetsFriendlyError(t *testing.T) {
	db := testsupport.NewDB(t)
	s := &Services{Repo: repository.New(db)}
	actor := &model.User{ID: 1} // CreateProject 需要一个非空 actor 才能落 CreatedBy

	if _, err := s.CreateProject(actor, dto.ProjectReq{Name: "Foo"}); err != nil {
		t.Fatalf("create first project: %v", err)
	}

	_, err := s.CreateProject(actor, dto.ProjectReq{Name: "foo"})
	if err == nil {
		t.Fatal("创建同名(仅大小写不同)的第二个项目本应被拒绝,却成功了")
	}
	if strings.Contains(err.Error(), "SQLSTATE") || strings.Contains(err.Error(), "duplicate key value") {
		t.Fatalf("拿到了裸 Postgres 错误,预检查没有拦住: %v", err)
	}
	want := `项目「foo」已存在`
	if err.Error() != want {
		t.Errorf("错误信息 = %q, 想要 %q", err.Error(), want)
	}
}

// 改名走的是同一个 GetProjectByName 预检查,所以它和 CreateProject 一起受益于
// 折叠 —— 但此前没有任何用例钉住这半边。缺的不是正确性而是锁定:谁要是日后为了
// 给创建和改名两条路径分别定制提示文案,把其中一条的查重内联掉,这一条就会静默
// 退回「预检查放行 → 撞 uk_project_name → 裸 SQLSTATE 直达前端」。
func TestUpdateProject_RenameOntoAnotherNameDifferentCase_GetsFriendlyError(t *testing.T) {
	db := testsupport.NewDB(t)
	s := &Services{Repo: repository.New(db)}
	actor := &model.User{ID: 1}

	if _, err := s.CreateProject(actor, dto.ProjectReq{Name: "Foo"}); err != nil {
		t.Fatalf("create Foo: %v", err)
	}
	bar, err := s.CreateProject(actor, dto.ProjectReq{Name: "Bar"})
	if err != nil {
		t.Fatalf("create Bar: %v", err)
	}

	_, err = s.UpdateProject(actor, bar.ID, dto.ProjectReq{Name: "foo"})
	if err == nil {
		t.Fatal("把 Bar 改名成 foo(已被 Foo 占着,仅大小写不同)本应被拒绝,却成功了")
	}
	if strings.Contains(err.Error(), "SQLSTATE") || strings.Contains(err.Error(), "duplicate key value") {
		t.Fatalf("拿到了裸 Postgres 错误,预检查没有拦住: %v", err)
	}
	want := `项目「foo」已存在`
	if err.Error() != want {
		t.Errorf("错误信息 = %q, 想要 %q", err.Error(), want)
	}
}
