package repository

import (
	"velagateway/internal/model"
)

// ----------------------------------------------------------------- 项目 (Project)

func (r *Repo) ListProjects() ([]model.Project, error) {
	var ps []model.Project
	err := r.db.Order("name asc").Find(&ps).Error
	return ps, err
}

func (r *Repo) GetProject(id int64) (*model.Project, error) {
	var p model.Project
	if err := r.db.First(&p, id).Error; err != nil {
		return nil, err
	}
	return &p, nil
}

func (r *Repo) GetProjectByName(name string) (*model.Project, error) {
	var p model.Project
	if err := r.db.Where("name = ?", name).First(&p).Error; err != nil {
		return nil, err
	}
	return &p, nil
}

func (r *Repo) CreateProject(p *model.Project) error { return r.db.Create(p).Error }

func (r *Repo) UpdateProjectFields(id int64, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	return r.db.Model(&model.Project{}).Where("id = ?", id).Updates(fields).Error
}

func (r *Repo) DeleteProject(id int64) error {
	return r.db.Delete(&model.Project{}, id).Error
}

// CountConnectionsInProject is what makes "delete" answerable: a project still
// holding databases must say how many, not just refuse.
func (r *Repo) CountConnectionsInProject(id int64) int {
	var n int64
	if err := r.db.Model(&model.Connection{}).Where("project_id = ?", id).Count(&n).Error; err != nil {
		return 0
	}
	return int(n)
}

// CountReleasesInProject counts what has been raised under a project. Releases
// carry a snapshot, so this counts history — including releases whose database
// has since moved elsewhere, which is the point.
func (r *Repo) CountReleasesInProject(id int64) int {
	var n int64
	if err := r.db.Model(&model.Release{}).Where("project_id = ?", id).Count(&n).Error; err != nil {
		return 0
	}
	return int(n)
}

// SetConnectionProject files (or unfiles, with 0) one database.
func (r *Repo) SetConnectionProject(connID, projectID int64) error {
	return r.db.Model(&model.Connection{}).Where("id = ?", connID).Update("project_id", projectID).Error
}
