package service

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"aegisdb/internal/apimsg"
	"aegisdb/internal/dto"
	"aegisdb/internal/model"
	"aegisdb/internal/review"
	"aegisdb/pkg/sqlutil"
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
		Params:     review.ParamSpecs(),
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
		return nil, apimsg.Errorf("规则参数不是合法 JSON")
	}
	if id > 0 {
		row, err := s.Repo.GetSQLReviewRule(id)
		if err != nil {
			return nil, ErrNotFound
		}
		if row.Kind == model.ReviewKindRegex {
			if err := validateRegexRule(req.Params, req.Category, req.Dialect); err != nil {
				return nil, err
			}
		} else if req.Params != row.Params {
			// 内置规则的参数按说明书校验:键名写错、数字填成字母、正则编不过,保存时就说清楚,
			// 而不是存进去以后静默按默认值跑。只在参数真的改了时校验 —— 切级别、开关会把
			// 原参数原样带回来,库里早先存下的旧写法不该因此让人连开关都关不掉。
			if err := review.ValidateParams(row.Code, req.Params); err != nil {
				return nil, err
			}
		}
		fields := map[string]any{
			"level": req.Level, "enabled": req.Enabled, "params": req.Params,
			"message": clip(req.Message, 200), "sort_order": req.SortOrder,
		}
		if req.Spec != nil {
			fields["spec"] = strings.TrimSpace(*req.Spec)
		}
		if req.SpecRef != nil {
			fields["spec_ref"] = clipColumn(strings.TrimSpace(*req.SpecRef), 128)
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
		return nil, apimsg.Errorf("规则编码不能为空")
	}
	if !strings.HasPrefix(code, "custom.") {
		// Custom codes are namespaced so they can never collide with a builtin code
		// shipped later — which would silently attach an operator's regex to a
		// built-in checker, or hide the builtin behind it.
		code = "custom." + code
	}
	if _, err := s.Repo.GetSQLReviewRuleByCode(code); err == nil {
		return nil, apimsg.Errorf("规则编码 %s 已存在", code)
	}
	if strings.TrimSpace(req.Name) == "" {
		return nil, apimsg.Errorf("规则名称不能为空")
	}
	if !json.Valid([]byte(orDefault(req.Params, "{}"))) {
		return nil, apimsg.Errorf("规则参数不是合法 JSON")
	}
	if err := validateRegexRule(req.Params, req.Category, req.Dialect); err != nil {
		return nil, err
	}
	row := &model.SQLReviewRule{
		Code: code, Name: clip(strings.TrimSpace(req.Name), 60),
		Dialect:  orDefault(normalizeDialect(req.Dialect), review.DialectAll),
		Category: orDefault(strings.TrimSpace(req.Category), review.CatDML),
		Level:    req.Level,
		Kind:     model.ReviewKindRegex, // everything an operator creates is pattern-based
		Enabled:  req.Enabled, Params: req.Params, Message: clip(req.Message, 200),
		Spec:      strDeref(req.Spec),
		SpecRef:   clipColumn(strDeref(req.SpecRef), 128),
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
	return apimsg.Errorf("规范分级只能是 critical / mandatory / recommended,或留空表示平台内置")
}

func (s *Services) DeleteReviewRule(id int64) error {
	row, err := s.Repo.GetSQLReviewRule(id)
	if err != nil {
		return ErrNotFound
	}
	if row.Kind != model.ReviewKindRegex {
		return apimsg.Errorf("内置规则不可删除,可将其停用")
	}
	return s.Repo.DeleteSQLReviewRule(id)
}

// CheckSQL runs the library against a script. The dialect comes from the target
// connection when one is given — reviewing an Oracle change under MySQL rules
// would report violations the target cannot commit and miss the ones it will.
//
// changeType(dml / ddl / 空)是这段 SQL 要进的升级单类型:声明了就核对内容是否相符;不论声明与否,
// DML 与 DDL 同在一段脚本里都报一条 error —— 建单时同样会被拒(changeTypeConflict)。
func (s *Services) CheckSQL(u *model.User, connID int64, dialect, sql, changeType string) (*dto.ReviewCheckResp, error) {
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
	conflict, at, err := changeTypeConflict(changeType, sqlutil.SplitStatements(sql))
	if err != nil {
		return nil, err
	}
	if conflict != nil {
		m := apimsg.FromErr(conflict)
		res.AddFinding(review.Finding{
			Code: ReviewCodeChangeType, Name: "升级单 DML 与 DDL 不得同单", Level: review.LevelError, Category: review.CatStructure,
			Stmt: at, Line: review.StatementLine(sql, at), Message: m.S, MessageRef: m.Ref,
		})
	}
	return &dto.ReviewCheckResp{Instance: instance, Result: res}, nil
}

// validateRegexRule 在保存时把自定义规则的三样东西先验一遍(2026-10 黑盒测试,工单 02 第 6 条):
//
//   - 正则编译得过 —— 与审查时同一个写法("(?is)" 前缀,见 review.regexRule)。从前坏正则照存,
//     审查时它对每一条语句都报一条 error,于是**所有**审查都 failed;
//   - category 是认得的分类;dialect 每一项都是认得的方言 —— 从前写错的值被静默改成默认值
//     (dml / all),规则看着存上了,作用范围却不是人写的那个。
//
// 空的 category / dialect 照旧放过:新建时取默认值,编辑时表示"不改"。
func validateRegexRule(params, category, dialect string) error {
	var p struct {
		Pattern string `json:"pattern"`
	}
	if strings.TrimSpace(params) != "" {
		_ = json.Unmarshal([]byte(params), &p)
	}
	if p.Pattern != "" {
		if _, err := regexp.Compile("(?is)" + p.Pattern); err != nil {
			return apimsg.Errorf("正则无法编译:%v", err)
		}
	}
	if c := strings.TrimSpace(category); c != "" {
		ok := false
		for _, k := range review.Categories {
			if k == c {
				ok = true
				break
			}
		}
		if !ok {
			return apimsg.Errorf("分类只能是 %s:%s", strings.Join(review.Categories, " / "), c)
		}
	}
	d := strings.ToLower(strings.TrimSpace(dialect))
	if d != "" && d != review.DialectAll {
		known := map[string]bool{}
		for _, k := range review.Dialects {
			known[k] = true
		}
		for _, part := range strings.Split(d, ",") {
			if p := strings.TrimSpace(part); !known[p] {
				return apimsg.Errorf("方言只能是 all 或 %s 中的若干个:%s", strings.Join(review.Dialects, " / "), part)
			}
		}
	}
	return nil
}

func validateReviewLevel(l string) error {
	switch l {
	case model.ReviewError, model.ReviewWarn, model.ReviewInfo:
		return nil
	}
	return apimsg.Errorf("规则级别只能是 error / warn / info")
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

// ReviewCodeChangeType 是"DML 与 DDL 同单 / 与声明的变更类型不符"那条结论的规则码。它不是规则库里的
// 一条规则(运维关不掉):建单时这两种情况一定被拒,预检说"通过"只会让人白提一次。
const ReviewCodeChangeType = "release.change_type"
