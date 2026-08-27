package service

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"velagateway/internal/dto"
	"velagateway/internal/model"
	"velagateway/internal/review"
)

// 数据库规范审查 — the rule library and the checker that runs it.
//
// This channel never executes anything and never decides whether a command may
// run: it reports how a change measures up against the standards. The gateway's
// three layers (capability matrix, high-risk dictionary, strict mode) are
// untouched by it. What consumes a review verdict is the release pipeline, which
// stops a run whose SQL trips an error-level rule — a policy the operator sets
// per rule, not something inferred here.

// ListReviewRules returns the whole library (the console groups it by dialect
// and category client-side, so one round trip serves every tab).
func (s *Services) ListReviewRules() []model.SQLReviewRule {
	rs, err := s.Repo.ListSQLReviewRules()
	if err != nil {
		return []model.SQLReviewRule{}
	}
	return rs
}

// ReviewCatalog reports the vocabulary the console renders its pickers from, so
// a dialect or category added in Go never has to be re-typed in TypeScript.
func (s *Services) ReviewCatalog() dto.ReviewCatalogResp {
	return dto.ReviewCatalogResp{
		Dialects:   append([]string{review.DialectAll}, review.Dialects...),
		Categories: review.Categories,
		Levels:     []string{model.ReviewError, model.ReviewWarn, model.ReviewInfo},
		Specs:      review.SpecLevels,
	}
}

// SaveReviewRule creates a custom rule or edits an existing one.
//
// A builtin rule's CODE, KIND and dialect scope are fixed: the code is what
// binds the row to its implementation, so a renamed builtin would become a rule
// that is listed, looks enabled, and checks nothing. Everything an operator
// legitimately owns — level, enabled, params, message, display name — stays
// editable.
func (s *Services) SaveReviewRule(id int64, req dto.ReviewRuleReq) (*model.SQLReviewRule, error) {
	if err := validateReviewLevel(req.Level); err != nil {
		return nil, err
	}
	if err := validateReviewSpec(req.Spec); err != nil {
		return nil, err
	}
	if req.Params != "" && !json.Valid([]byte(req.Params)) {
		return nil, fmt.Errorf("规则参数不是合法 JSON")
	}
	if id > 0 {
		row, err := s.Repo.GetSQLReviewRule(id)
		if err != nil {
			return nil, ErrNotFound
		}
		fields := map[string]any{
			"level": req.Level, "enabled": req.Enabled, "params": req.Params,
			"message": clip(req.Message, 200), "sort_order": req.SortOrder,
		}
		if req.Spec != nil {
			fields["spec"] = strings.TrimSpace(*req.Spec)
		}
		if req.SpecRef != nil {
			fields["spec_ref"] = clip(strings.TrimSpace(*req.SpecRef), 128)
		}
		if n := strings.TrimSpace(req.Name); n != "" {
			fields["name"] = clip(n, 60)
		}
		if row.Kind == model.ReviewKindRegex {
			// A custom rule is entirely the operator's, scope included.
			if d := normalizeDialect(req.Dialect); d != "" {
				fields["dialect"] = d
			}
			if c := strings.TrimSpace(req.Category); c != "" {
				fields["category"] = c
			}
		}
		if err := s.Repo.UpdateSQLReviewRuleFields(id, fields); err != nil {
			return nil, err
		}
		return s.Repo.GetSQLReviewRule(id)
	}

	code := strings.TrimSpace(req.Code)
	if code == "" {
		return nil, fmt.Errorf("规则编码不能为空")
	}
	if !strings.HasPrefix(code, "custom.") {
		// Custom codes are namespaced so they can never collide with a builtin code
		// shipped later — which would silently attach an operator's regex to a
		// built-in checker, or hide the builtin behind it.
		code = "custom." + code
	}
	if _, err := s.Repo.GetSQLReviewRuleByCode(code); err == nil {
		return nil, fmt.Errorf("规则编码 %s 已存在", code)
	}
	if strings.TrimSpace(req.Name) == "" {
		return nil, fmt.Errorf("规则名称不能为空")
	}
	if !json.Valid([]byte(orDefault(req.Params, "{}"))) {
		return nil, fmt.Errorf("规则参数不是合法 JSON")
	}
	row := &model.SQLReviewRule{
		Code: code, Name: clip(strings.TrimSpace(req.Name), 60),
		Dialect:  orDefault(normalizeDialect(req.Dialect), review.DialectAll),
		Category: orDefault(strings.TrimSpace(req.Category), review.CatDML),
		Level:    req.Level,
		Kind:     model.ReviewKindRegex, // everything an operator creates is pattern-based
		Enabled:  req.Enabled, Params: req.Params, Message: clip(req.Message, 200),
		Spec:      strDeref(req.Spec),
		SpecRef:   clip(strDeref(req.SpecRef), 128),
		SortOrder: req.SortOrder, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := s.Repo.CreateSQLReviewRule(row); err != nil {
		return nil, err
	}
	return row, nil
}

// DeleteReviewRule removes a custom rule. A builtin one can only be DISABLED:
// deleting it would drop it out of the library entirely, and the next boot's
// seed would put it back enabled — a toggle that undoes itself is worse than no
// toggle at all.
// validateReviewSpec keeps the classification to the vocabulary the four
// standards use. Empty is legal and means "平台内置,规范未覆盖" — the library
// deliberately holds guards no document demands, and forcing every rule to
// claim a citation would only produce fake ones.
func validateReviewSpec(spec *string) error {
	if spec == nil {
		return nil
	}
	switch strings.TrimSpace(*spec) {
	case "", review.SpecCritical, review.SpecMandatory, review.SpecRecommended:
		return nil
	}
	return fmt.Errorf("规范分级只能是 critical / mandatory / recommended,或留空表示平台内置")
}

func (s *Services) DeleteReviewRule(id int64) error {
	row, err := s.Repo.GetSQLReviewRule(id)
	if err != nil {
		return ErrNotFound
	}
	if row.Kind != model.ReviewKindRegex {
		return fmt.Errorf("内置规则不可删除,可将其停用")
	}
	return s.Repo.DeleteSQLReviewRule(id)
}

// CheckSQL runs the library against a script. The dialect comes from the target
// connection when one is given — reviewing an Oracle change under MySQL rules
// would report violations the target cannot commit and miss the ones it will.
func (s *Services) CheckSQL(u *model.User, connID int64, dialect, sql string) (*dto.ReviewCheckResp, error) {
	if strings.TrimSpace(sql) == "" {
		return nil, ErrBadRequest
	}
	if len(sql) > maxStoredSQLBytes {
		return nil, storedSQLTooLong(len(sql))
	}
	instance := ""
	if connID > 0 {
		conn, err := s.Repo.GetConnection(connID)
		if err != nil {
			return nil, ErrNotFound
		}
		// The review reads no data, but naming an instance in a report is still a
		// statement about a database this user may not be scoped to see.
		if !s.canAccessConn(u, conn) {
			return nil, ErrForbidden
		}
		dialect = review.DialectFor(conn.Engine)
		instance = conn.Name
	}
	if dialect == "" || dialect == review.DialectAll {
		dialect = review.DialectGeneric
	}
	res := review.Check(dialect, sql, s.Repo.ReviewRules())
	return &dto.ReviewCheckResp{Instance: instance, Result: res}, nil
}

func validateReviewLevel(l string) error {
	switch l {
	case model.ReviewError, model.ReviewWarn, model.ReviewInfo:
		return nil
	}
	return fmt.Errorf("规则级别只能是 error / warn / info")
}

// normalizeDialect keeps only codes the engine knows, so a typo cannot produce a
// rule scoped to a dialect that never matches anything.
func normalizeDialect(d string) string {
	d = strings.ToLower(strings.TrimSpace(d))
	if d == "" || d == review.DialectAll {
		return review.DialectAll
	}
	known := map[string]bool{}
	for _, k := range review.Dialects {
		known[k] = true
	}
	var out []string
	for _, part := range strings.Split(d, ",") {
		p := strings.TrimSpace(part)
		if known[p] {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return review.DialectAll
	}
	return strings.Join(out, ",")
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}
