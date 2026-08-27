package repository

import (
	"log/slog"
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
//
// Two things it DOES touch on existing rows, and why:
//
//  1. **随版本走的字段**会回填:出处(spec / spec_ref)与适用范围(dialect、
//     category)。这条线不是随手划的 —— SaveReviewRule 里内置规则的 dialect 本来
//     就不让运维改(改了就等于把规则接到别的方言上);出处则是关于规范的事实,不是
//     运营决定。运维拥有的是这条规则**值多少钱**:level、enabled、params、message。
//
//     不回填的后果是具体的:某条规则的方言在新版里收窄了,而库里的老行还是 all,
//     于是同一个字段会被两条规则各报一次 —— 人只会觉得这审查在重复啰嗦。
//
//  2. 已经没有实现的 builtin 规则会被删掉。规范改版后下线的规则码,留在库里会
//     列出来、看着启用、实际什么都不查(fire 找不到 checker 就直接返回) ——
//     一条永远不报的规则,和一条永远通过的规则,从外面看没有任何区别。自定义
//     规则(kind=regex)不在此列,那是运维自己的东西。
func (r *Repo) SeedSQLReviewRules() (int, error) {
	var existing []string
	if err := r.db.Model(&model.SQLReviewRule{}).Pluck("code", &existing).Error; err != nil {
		return 0, err
	}
	have := make(map[string]bool, len(existing))
	for _, c := range existing {
		have[c] = true
	}
	shipped := make(map[string]review.Builtin, len(review.Builtins))
	for _, b := range review.Builtins {
		shipped[b.Code] = b
	}
	if err := r.pruneOrphanBuiltins(shipped); err != nil {
		return 0, err
	}
	if err := r.backfillShippedFields(shipped); err != nil {
		return 0, err
	}
	n := 0
	for i, b := range review.Builtins {
		if have[b.Code] {
			continue
		}
		row := &model.SQLReviewRule{
			Code: b.Code, Name: b.Name, Dialect: b.Dialect, Category: b.Category,
			Level: b.Level, Kind: model.ReviewKindBuiltin, Enabled: true,
			Spec: b.Spec, SpecRef: b.SpecRef,
			Params: b.Params, SortOrder: (i + 1) * 10,
		}
		if err := r.db.Create(row).Error; err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}


// pruneOrphanBuiltins removes builtin rows whose code no longer ships — a rule
// the standard dropped. Custom rules are never touched: those belong to the
// operator, not to the shipped catalog.
func (r *Repo) pruneOrphanBuiltins(shipped map[string]review.Builtin) error {
	var rows []model.SQLReviewRule
	if err := r.db.Where("kind = ?", model.ReviewKindBuiltin).Find(&rows).Error; err != nil {
		return err
	}
	var orphans []int64
	for _, row := range rows {
		if _, ok := shipped[row.Code]; !ok {
			orphans = append(orphans, row.ID)
		}
	}
	if len(orphans) == 0 {
		return nil
	}
	slog.Info("dropping review rules that no longer ship", "count", len(orphans))
	return r.db.Where("id IN ?", orphans).Delete(&model.SQLReviewRule{}).Error
}

// backfillShippedFields keeps the columns THIS RELEASE owns — provenance and
// applicability — in step with the shipped catalog on rows that already exist.
// See SeedSQLReviewRules for where the line between ours and the operator's runs.
func (r *Repo) backfillShippedFields(shipped map[string]review.Builtin) error {
	var rows []model.SQLReviewRule
	if err := r.db.Where("kind = ?", model.ReviewKindBuiltin).Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		b, ok := shipped[row.Code]
		if !ok || (row.Spec == b.Spec && row.SpecRef == b.SpecRef &&
			row.Dialect == b.Dialect && row.Category == b.Category) {
			continue
		}
		if err := r.db.Model(&model.SQLReviewRule{}).Where("id = ?", row.ID).
			Updates(map[string]any{
				"spec": b.Spec, "spec_ref": b.SpecRef,
				"dialect": b.Dialect, "category": b.Category,
			}).Error; err != nil {
			return err
		}
	}
	return nil
}
