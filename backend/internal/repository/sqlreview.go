package repository

import (
	"time"

	"velagateway/internal/model"
	"velagateway/internal/review"
)

// ----------------------------------------------------- SQL 规范审查规则库

// ListSQLReviewRules returns the whole library in display order.
func (r *Repo) ListSQLReviewRules() ([]model.SQLReviewRule, error) {
	var rs []model.SQLReviewRule
	err := r.db.Order("sort_order asc, id asc").Find(&rs).Error
	return rs, err
}

// ReviewRules returns the library as the engine consumes it. Keeping the
// conversion here means callers never hand the engine a half-built rule.
func (r *Repo) ReviewRules() []review.Rule {
	rows, err := r.ListSQLReviewRules()
	if err != nil {
		return nil
	}
	out := make([]review.Rule, 0, len(rows))
	for _, row := range rows {
		out = append(out, review.Rule{
			Code: row.Code, Name: row.Name, Dialect: row.Dialect, Category: row.Category,
			Level: row.Level, Kind: row.Kind, Message: row.Message, Params: row.Params,
			Enabled: row.Enabled,
		})
	}
	return out
}

func (r *Repo) GetSQLReviewRule(id int64) (*model.SQLReviewRule, error) {
	var row model.SQLReviewRule
	if err := r.db.First(&row, id).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *Repo) GetSQLReviewRuleByCode(code string) (*model.SQLReviewRule, error) {
	var row model.SQLReviewRule
	if err := r.db.Where("code = ?", code).First(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *Repo) CreateSQLReviewRule(row *model.SQLReviewRule) error { return r.db.Create(row).Error }

// UpdateSQLReviewRuleFields writes only the named columns, so toggling a rule's
// level never rewrites the params someone else just tuned.
func (r *Repo) UpdateSQLReviewRuleFields(id int64, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	fields["updated_at"] = time.Now()
	return r.db.Model(&model.SQLReviewRule{}).Where("id = ?", id).Updates(fields).Error
}

func (r *Repo) DeleteSQLReviewRule(id int64) error {
	return r.db.Delete(&model.SQLReviewRule{}, id).Error
}

// SeedSQLReviewRules inserts the shipped catalog for every code that is not in
// the table yet, and returns how many rows it wrote.
//
// Only ABSENT codes are written. An operator who lowered a rule to warn,
// disabled it, or tightened its pattern has made a policy decision, and an
// upgrade that reset it would silently re-block releases they had chosen to let
// through. New releases therefore only ADD rules — which also means a rule the
// operator deleted stays deleted only if it is a custom one; a builtin code
// comes back on the next boot, because the alternative is a library that
// silently loses coverage nobody can see it lost.
func (r *Repo) SeedSQLReviewRules() (int, error) {
	var existing []string
	if err := r.db.Model(&model.SQLReviewRule{}).Pluck("code", &existing).Error; err != nil {
		return 0, err
	}
	have := make(map[string]bool, len(existing))
	for _, c := range existing {
		have[c] = true
	}
	n := 0
	for i, b := range review.Builtins {
		if have[b.Code] {
			continue
		}
		row := &model.SQLReviewRule{
			Code: b.Code, Name: b.Name, Dialect: b.Dialect, Category: b.Category,
			Level: b.Level, Kind: model.ReviewKindBuiltin, Enabled: true,
			Params: b.Params, SortOrder: (i + 1) * 10,
		}
		if err := r.db.Create(row).Error; err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
