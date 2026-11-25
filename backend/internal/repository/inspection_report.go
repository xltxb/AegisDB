package repository

import (
	"errors"

	"gorm.io/gorm"

	"aegisdb/internal/model"
)

// AI 巡检报告的元数据(.scratch/inspection-report)。完整 HTML 在文件里,见 service/inspection.go。

// UpsertInspectionReport 按 (实例, 巡检日期) 写入:已有就整行覆盖(保留 ID 与首次创建时间),
// 返回这次是不是覆盖。同一份报告 AI 重跑再推一次不会多出一条。
func (r *Repo) UpsertInspectionReport(rep *model.InspectionReport) (replaced bool, err error) {
	err = r.db.Transaction(func(tx *gorm.DB) error {
		var old model.InspectionReport
		e := tx.Where("connection_id = ? AND report_date = ?", rep.ConnectionID, rep.ReportDate).First(&old).Error
		switch {
		case e == nil:
			replaced = true
			rep.ID, rep.CreatedAt = old.ID, old.CreatedAt
			return tx.Save(rep).Error
		case errors.Is(e, gorm.ErrRecordNotFound):
			return tx.Create(rep).Error
		default:
			return e
		}
	})
	return replaced, err
}

// GetConnectionByName 按实例名取实例(实例名唯一,uk idx_connection_name)。巡检报告按实例名对应。
func (r *Repo) GetConnectionByName(name string) (*model.Connection, error) {
	var c model.Connection
	if err := r.db.Where("name = ?", name).First(&c).Error; err != nil {
		return nil, err
	}
	return &c, nil
}

// GetInspectionReport 按 ID 取一份。
func (r *Repo) GetInspectionReport(id int64) (*model.InspectionReport, error) {
	var rep model.InspectionReport
	if err := r.db.First(&rep, id).Error; err != nil {
		return nil, err
	}
	return &rep, nil
}

// ListInspectionReports 列某实例的报告历史,按巡检日期倒序。
func (r *Repo) ListInspectionReports(connID int64, offset, limit int) ([]model.InspectionReport, int64, error) {
	q := r.db.Model(&model.InspectionReport{}).Where("connection_id = ?", connID)
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var out []model.InspectionReport
	err := q.Order("report_date DESC").Offset(offset).Limit(limit).Find(&out).Error
	return out, total, err
}

// LatestInspectionReports 取每个实例最新的一份(巡检日期最大的那份)。
func (r *Repo) LatestInspectionReports() ([]model.InspectionReport, error) {
	var out []model.InspectionReport
	err := r.db.Raw(`SELECT r.* FROM tbl_inspection_report r
		JOIN (SELECT connection_id, MAX(report_date) AS d FROM tbl_inspection_report GROUP BY connection_id) m
		  ON r.connection_id = m.connection_id AND r.report_date = m.d`).Scan(&out).Error
	return out, err
}
