package repository

import (
	"time"

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

// CountDatabasesInProject is what makes "delete" answerable: a project still
// holding databases must say how many, not just refuse.
func (r *Repo) CountDatabasesInProject(id int64) int {
	var n int64
	if err := r.db.Model(&model.DatabaseProject{}).Where("project_id = ?", id).Count(&n).Error; err != nil {
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

// ------------------------------------------------- 库的归属 (DatabaseProject)

// DatabaseProjectID returns the project one database is filed under, or 0.
func (r *Repo) DatabaseProjectID(connID int64, database string) int64 {
	var row model.DatabaseProject
	if err := r.db.Where("connection_id = ? AND db_name = ?", connID, database).First(&row).Error; err != nil {
		return 0
	}
	return row.ProjectID
}

// DatabaseProjectsForConnection maps this connection's databases to their
// projects, so the tree can label a whole instance in one query.
func (r *Repo) DatabaseProjectsForConnection(connID int64) map[string]int64 {
	var rows []model.DatabaseProject
	out := map[string]int64{}
	if err := r.db.Where("connection_id = ?", connID).Find(&rows).Error; err != nil {
		return out
	}
	for _, x := range rows {
		out[x.Database] = x.ProjectID
	}
	return out
}

// SetDatabaseProject files one database under a project. projectID 0 UNFILES it
// by deleting the row — "unassigned" is the absence of a row, not a row saying
// zero, so nothing has to remember to treat 0 specially in later queries.
func (r *Repo) SetDatabaseProject(connID int64, database string, projectID, actorID int64) error {
	if projectID == 0 {
		return r.db.Where("connection_id = ? AND db_name = ?", connID, database).
			Delete(&model.DatabaseProject{}).Error
	}
	var row model.DatabaseProject
	err := r.db.Where("connection_id = ? AND db_name = ?", connID, database).First(&row).Error
	if err == nil {
		// Refiling is an overwrite, not a second row — the unique key says so too.
		return r.db.Model(&model.DatabaseProject{}).Where("id = ?", row.ID).
			Updates(map[string]any{"project_id": projectID, "updated_at": time.Now()}).Error
	}
	now := time.Now()
	return r.db.Create(&model.DatabaseProject{
		ConnectionID: connID, Database: database, ProjectID: projectID,
		CreatedBy: actorID, CreatedAt: now, UpdatedAt: now,
	}).Error
}
