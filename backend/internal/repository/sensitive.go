package repository

import (
	"strings"

	"velagateway/internal/model"
)

// ------------------------------------------------- 敏感字段 (SensitiveColumn)

// ListSensitiveColumns returns every rule, table then column, so the console
// lists a table's fields together.
func (r *Repo) ListSensitiveColumns() ([]model.SensitiveColumn, error) {
	var rows []model.SensitiveColumn
	err := r.db.Order("table_name asc, column_name asc").Find(&rows).Error
	return rows, err
}

func (r *Repo) GetSensitiveColumn(id int64) (*model.SensitiveColumn, error) {
	var row model.SensitiveColumn
	if err := r.db.First(&row, id).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// GetSensitiveColumnBy looks a rule up by its identity (table + column).
//
// 比较用小写:库里写 T_USER.ID_CARD、页面上填 t_user.id_card,是同一条规则,
// 不该被当成两条各自生效(那会让人以为改了一条,其实另一条还在)。
func (r *Repo) GetSensitiveColumnBy(table, column string) (*model.SensitiveColumn, error) {
	var row model.SensitiveColumn
	err := r.db.Where("LOWER(table_name) = ? AND LOWER(column_name) = ?",
		strings.ToLower(strings.TrimSpace(table)), strings.ToLower(strings.TrimSpace(column))).
		First(&row).Error
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *Repo) CreateSensitiveColumn(row *model.SensitiveColumn) error {
	return r.db.Create(row).Error
}

func (r *Repo) UpdateSensitiveColumnFields(id int64, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	return r.db.Model(&model.SensitiveColumn{}).Where("id = ?", id).Updates(fields).Error
}

func (r *Repo) DeleteSensitiveColumn(id int64) error {
	return r.db.Delete(&model.SensitiveColumn{}, id).Error
}
