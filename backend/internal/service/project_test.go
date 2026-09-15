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
