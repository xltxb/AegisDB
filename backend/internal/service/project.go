package service

// 项目(Project) —— 数据库与升级单的归属。
//
// 一句话定位:**组织维度,不是安全边界**。系统已经用 Connection.Tags 做数据访问
// 范围,判定层(能力矩阵 / 标签范围 / 高危字典)一概不看 project_id。再叠一层能
// 限制访问的"项目",就是两套互相重叠的范围机制 —— 迟早有一层是错的,而错的那层
// 会以"本该看不见却看得见"的形式出现。项目只回答一个问题:这个库、这张单归谁跟进。
//
// 于是也就有了两条推论,下面的代码按它们写:
//   - 未归属(project_id = 0)是**合法状态**。项目是后加的维度,存量库不该因为没人
//     填归属就变得不可用。
//   - 升级单的项目在提交时**快照**。现算会让一次归属调整把过去所有单据的账改掉。

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"velagateway/internal/dto"
	"velagateway/internal/model"
)

// ListProjects returns every project with what it currently owns.
func (s *Services) ListProjects() []dto.ProjectView {
	ps, err := s.Repo.ListProjects()
	if err != nil {
		return []dto.ProjectView{}
	}
	out := make([]dto.ProjectView, 0, len(ps))
	for _, p := range ps {
		out = append(out, dto.ProjectView{
			ID: p.ID, Name: p.Name, Owner: p.Owner, Description: p.Description,
			Connections: s.Repo.CountConnectionsInProject(p.ID),
			Releases:    s.Repo.CountReleasesInProject(p.ID),
			CreatedAt:   p.CreatedAt,
		})
	}
	return out
}

// CreateProject adds one. The name IS the identity — projects are a thing people
// refer to by name, and two projects called the same thing help nobody.
func (s *Services) CreateProject(actor *model.User, req dto.ProjectReq) (*model.Project, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, fmt.Errorf("项目名称不能为空")
	}
	if _, err := s.Repo.GetProjectByName(name); err == nil {
		return nil, fmt.Errorf("项目「%s」已存在", name)
	}
	p := &model.Project{
		Name: clip(name, 64), Owner: clip(strDeref(req.Owner), 64),
		Description: clip(strDeref(req.Description), 500),
		CreatedBy:   actor.ID, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := s.Repo.CreateProject(p); err != nil {
		return nil, err
	}
	s.auditAdminAction(actor, "admin.project.create name="+p.Name)
	return p, nil
}

// UpdateProject edits name / owner / description. Partial: an absent field keeps
// its value, so an edit form that only touches the owner cannot blank the rest.
func (s *Services) UpdateProject(actor *model.User, id int64, req dto.ProjectReq) (*model.Project, error) {
	p, err := s.Repo.GetProject(id)
	if err != nil {
		return nil, ErrNotFound
	}
	fields := map[string]any{"updated_at": time.Now()}
	if n := strings.TrimSpace(req.Name); n != "" && n != p.Name {
		if _, e := s.Repo.GetProjectByName(n); e == nil {
			return nil, fmt.Errorf("项目「%s」已存在", n)
		}
		fields["name"] = clip(n, 64)
	}
	if req.Owner != nil {
		fields["owner"] = clip(strings.TrimSpace(*req.Owner), 64)
	}
	if req.Description != nil {
		fields["description"] = clip(strings.TrimSpace(*req.Description), 500)
	}
	if err := s.Repo.UpdateProjectFields(id, fields); err != nil {
		return nil, err
	}
	s.auditAdminAction(actor, "admin.project.update id="+itoa64(id))
	return s.Repo.GetProject(id)
}

// DeleteProject removes a project that owns nothing.
//
// A project still holding databases is NOT deleted and silently detached: the
// databases would become unassigned without anyone deciding that, and the
// tracking view would quietly start lying about what is covered. Say what is in
// the way and let a person move them.
//
// Historical releases do NOT block deletion — they carry a snapshot of the
// project name, so they stay readable after it is gone. That is what the
// snapshot is for.
func (s *Services) DeleteProject(actor *model.User, id int64) error {
	p, err := s.Repo.GetProject(id)
	if err != nil {
		return ErrNotFound
	}
	if n := s.Repo.CountConnectionsInProject(id); n > 0 {
		return fmt.Errorf("项目「%s」下还有 %d 个数据库,请先把它们移到其它项目或清空归属再删除", p.Name, n)
	}
	if err := s.Repo.DeleteProject(id); err != nil {
		return err
	}
	s.auditAdminAction(actor, "admin.project.delete name="+p.Name)
	return nil
}

// SetConnectionProject files a database under a project (0 = unassign).
//
// The project must exist: a dangling id would show the database as "filed" in
// the connection row while no project lists it, and the tracking view would be
// quietly incomplete — the exact failure this feature exists to prevent.
func (s *Services) SetConnectionProject(connID, projectID int64) error {
	if _, err := s.Repo.GetConnection(connID); err != nil {
		return ErrNotFound
	}
	if projectID != 0 {
		if _, err := s.Repo.GetProject(projectID); err != nil {
			return fmt.Errorf("项目不存在: %d", projectID)
		}
	}
	return s.Repo.SetConnectionProject(connID, projectID)
}

// strDeref reads an optional string field; nil = 未提供 = 空。
func strDeref(p *string) string {
	if p == nil {
		return ""
	}
	return strings.TrimSpace(*p)
}

// itoa64 renders an id for audit lines.
func itoa64(v int64) string { return strconv.FormatInt(v, 10) }

// projectOf resolves a connection's project for the release snapshot. A missing
// or deleted project reads as unassigned rather than failing the submit —
// filing is not a precondition for changing a database.
func (s *Services) projectOf(conn *model.Connection) (int64, string) {
	if conn == nil || conn.ProjectID == 0 {
		return 0, ""
	}
	p, err := s.Repo.GetProject(conn.ProjectID)
	if err != nil {
		return conn.ProjectID, ""
	}
	return p.ID, p.Name
}
