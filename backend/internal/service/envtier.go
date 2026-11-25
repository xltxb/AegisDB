package service

import (
	"errors"
	"regexp"
	"strings"

	"gorm.io/gorm"

	"velagateway/internal/apimsg"
	"velagateway/internal/dto"
	"velagateway/internal/model"
)

// Tier / environment administration.
//
// A tier owns the rules (capability matrix + risk dictionary are keyed by it); an
// environment groups instances and binds to exactly one tier. One tier backs many
// environments, so a second production cluster is a new environment on the
// existing prod tier — it inherits the whole rule set with nothing copied.
//
// Every guard below exists because both rule lookups treat a missing row as
// permission granted (repository.CapabilityLevel → allow, matchCommand → off).
// The failure mode is silent: an unregulated instance looks exactly like a
// regulated one until someone drops a table on it.

// Errors specific to tier/environment administration. They carry the reason in
// the message because the console shows it verbatim to the operator.
var (
	ErrTierInUse         = apimsg.Errorf("仍有环境绑定该分层标签,请先迁移或删除这些环境")
	ErrTierIsScanBase    = apimsg.Errorf("该分层标签是脚本扫描基准,请先把基准转移到其他标签")
	ErrLastTier          = apimsg.Errorf("至少保留一个分层标签")
	ErrLastEnvironment   = apimsg.Errorf("至少保留一个环境")
	ErrNoScanBaseline    = apimsg.Errorf("必须有且仅有一个分层标签作为脚本扫描基准")
	ErrEnvMoveTargetSame = apimsg.Errorf("迁移目标不能是被删除的环境本身")
)

// codeRe constrains codes to what can be embedded in a display label and matched
// case-insensitively in the rule tables without escaping surprises.
var codeRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

func normCode(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// ---------------------------------------------------------------- Tiers

func (s *Services) ListEnvTiers() ([]model.EnvTier, error) { return s.Repo.ListEnvTiers() }

// CreateEnvTier creates a tier by cloning an existing one's rules.
//
// A template is mandatory. There is no "empty tier" option: the empty state is
// not a blank slate but an environment with no rules at all, and it would go
// live the moment an instance is pointed at it.
func (s *Services) CreateEnvTier(req dto.EnvTierCreateReq) (*model.EnvTier, error) {
	code := normCode(req.Code)
	tmpl := normCode(req.TemplateCode)
	if !codeRe.MatchString(code) || strings.TrimSpace(req.DisplayName) == "" {
		return nil, ErrBadRequest
	}
	if _, err := s.Repo.GetEnvTier(code); err == nil {
		return nil, ErrBadRequest // already exists
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	tmplTier, err := s.Repo.GetEnvTier(tmpl)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrBadRequest // unknown template — nothing to clone from
		}
		return nil, err
	}
	mask, export := tmplTier.MaskSensitive, tmplTier.BizExportRestrict
	if req.MaskSensitive != nil {
		mask = *req.MaskSensitive
	}
	if req.BizExportRestrict != nil {
		export = *req.BizExportRestrict
	}
	auto := tmplTier.AutoExecute
	if req.AutoExecute != nil {
		auto = *req.AutoExecute
	}

	t := &model.EnvTier{
		Code:            code,
		DisplayName:     strings.TrimSpace(req.DisplayName),
		SortOrder:       req.SortOrder,
		RequireMFA:      req.RequireMFA,
		DangerBanner:    req.DangerBanner,
		CountsInPending: req.CountsInPending,
		ScanBaseline:    req.ScanBaseline,
		StrictNoWhere:   req.StrictNoWhere,
		MaskSensitive:     mask,
		BizExportRestrict: export,
		AutoExecute:       auto,
		ConnLayer:       strings.TrimSpace(req.ConnLayer),
		DefaultRole:     strings.TrimSpace(req.DefaultRole),
	}
	if err := s.Repo.CreateEnvTierFrom(t, tmpl); err != nil {
		return nil, err
	}
	return t, nil
}

// UpdateEnvTier edits a tier's display fields and property flags. The code is
// immutable: it is the foreign key the rule rows and every environment carry.
func (s *Services) UpdateEnvTier(code string, req dto.EnvTierUpdateReq) (*model.EnvTier, error) {
	t, err := s.Repo.GetEnvTier(normCode(code))
	if err != nil {
		return nil, ErrNotFound
	}
	// 局部更新:缺席的字段保持原值(见 dto.EnvTierUpdateReq)。名称传了就不能是空的。
	if req.DisplayName != nil {
		if strings.TrimSpace(*req.DisplayName) == "" {
			return nil, ErrBadRequest
		}
		t.DisplayName = strings.TrimSpace(*req.DisplayName)
	}
	// Clearing the baseline is only allowed by moving it elsewhere (which sets
	// the flag on another tier and clears this one). Turning it off outright
	// would leave script scanning with no dictionary to judge against.
	if req.ScanBaseline != nil {
		if t.ScanBaseline && !*req.ScanBaseline {
			return nil, ErrNoScanBaseline
		}
		t.ScanBaseline = *req.ScanBaseline
	}
	setInt := func(dst *int, v *int) {
		if v != nil {
			*dst = *v
		}
	}
	setBool := func(dst *bool, v *bool) {
		if v != nil {
			*dst = *v
		}
	}
	setStr := func(dst *string, v *string) {
		if v != nil {
			*dst = strings.TrimSpace(*v)
		}
	}
	setInt(&t.SortOrder, req.SortOrder)
	setBool(&t.RequireMFA, req.RequireMFA)
	setBool(&t.DangerBanner, req.DangerBanner)
	setBool(&t.CountsInPending, req.CountsInPending)
	setBool(&t.StrictNoWhere, req.StrictNoWhere)
	setBool(&t.MaskSensitive, req.MaskSensitive)
	setBool(&t.BizExportRestrict, req.BizExportRestrict)
	setBool(&t.AutoExecute, req.AutoExecute)
	setStr(&t.ConnLayer, req.ConnLayer)
	setStr(&t.DefaultRole, req.DefaultRole)
	if err := s.Repo.UpdateEnvTier(t); err != nil {
		return nil, err
	}
	return t, nil
}

// DeleteEnvTier drops a tier and its rule rows, refusing every case that would
// leave instances ungoverned or scanning blind.
func (s *Services) DeleteEnvTier(code string) error {
	code = normCode(code)
	t, err := s.Repo.GetEnvTier(code)
	if err != nil {
		return ErrNotFound
	}
	if t.ScanBaseline {
		return ErrTierIsScanBase
	}
	n, err := s.Repo.CountEnvironmentsOfTier(code)
	if err != nil {
		return err
	}
	if n > 0 {
		return ErrTierInUse
	}
	tiers, err := s.Repo.ListEnvTiers()
	if err != nil {
		return err
	}
	if len(tiers) <= 1 {
		return ErrLastTier
	}
	return s.Repo.DeleteEnvTier(code)
}

// ---------------------------------------------------------------- Environments

func (s *Services) ListEnvironments() ([]model.Environment, error) { return s.Repo.ListEnvironments() }

// EnvironmentUsage reports the instance count per environment, so the console can
// tell an operator how many instances a delete is about to move.
func (s *Services) EnvironmentUsage() (map[string]int64, error) {
	envs, err := s.Repo.ListEnvironments()
	if err != nil {
		return nil, err
	}
	out := make(map[string]int64, len(envs))
	for _, e := range envs {
		n, err := s.Repo.CountConnectionsInEnvironment(e.Code)
		if err != nil {
			return nil, err
		}
		out[e.Code] = n
	}
	return out, nil
}

// CreateEnvironment adds an instance group on an existing tier. Nothing is
// cloned: the tier already holds the rules, which is the point of the split.
func (s *Services) CreateEnvironment(req dto.EnvironmentCreateReq) (*model.Environment, error) {
	code := normCode(req.Code)
	tier := normCode(req.TierCode)
	if !codeRe.MatchString(code) || strings.TrimSpace(req.DisplayName) == "" {
		return nil, ErrBadRequest
	}
	if _, err := s.Repo.GetEnvironment(code); err == nil {
		return nil, ErrBadRequest // already exists
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if _, err := s.Repo.GetEnvTier(tier); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrBadRequest // an environment with no tier has no rules
		}
		return nil, err
	}
	e := &model.Environment{
		Code:        code,
		DisplayName: strings.TrimSpace(req.DisplayName),
		TierCode:    tier,
		SortOrder:   req.SortOrder,
	}
	if err := s.Repo.CreateEnvironment(e); err != nil {
		return nil, err
	}
	return e, nil
}

// UpdateEnvironment edits an environment, including rebinding it to another tier.
//
// Rebinding changes how every instance in it is governed from that moment on. It
// does NOT rewrite history: audit and approval rows keep the tier they were
// judged under, because editing those would forge the audit trail.
func (s *Services) UpdateEnvironment(code string, req dto.EnvironmentUpdateReq) (*model.Environment, error) {
	e, err := s.Repo.GetEnvironment(normCode(code))
	if err != nil {
		return nil, ErrNotFound
	}
	tier := normCode(req.TierCode)
	if strings.TrimSpace(req.DisplayName) == "" {
		return nil, ErrBadRequest
	}
	if _, err := s.Repo.GetEnvTier(tier); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrBadRequest
		}
		return nil, err
	}
	e.DisplayName = strings.TrimSpace(req.DisplayName)
	e.TierCode = tier
	e.SortOrder = req.SortOrder
	if err := s.Repo.UpdateEnvironment(e); err != nil {
		return nil, err
	}
	return e, nil
}

// DeleteEnvironment removes an environment after moving its instances elsewhere.
// moveTo is mandatory even when the environment looks empty: a concurrent create
// would otherwise strand an instance on a dangling code, and an instance whose
// environment does not resolve has no tier and therefore no rules.
func (s *Services) DeleteEnvironment(code, moveTo string) error {
	code, moveTo = normCode(code), normCode(moveTo)
	if _, err := s.Repo.GetEnvironment(code); err != nil {
		return ErrNotFound
	}
	if moveTo == code {
		return ErrEnvMoveTargetSame
	}
	if _, err := s.Repo.GetEnvironment(moveTo); err != nil {
		return ErrBadRequest
	}
	envs, err := s.Repo.ListEnvironments()
	if err != nil {
		return err
	}
	if len(envs) <= 1 {
		return ErrLastEnvironment
	}
	return s.Repo.DeleteEnvironmentMoving(code, moveTo)
}

// ---------------------------------------------------------------- Resolution

// TierOfEnvironment resolves an environment code to its tier. Used wherever a
// rule decision is made, so the caller judges by control tier rather than by the
// instance's group.
//
// An unresolvable environment is an error, never a default: falling back to a
// zero tier would find no rule rows and read as "allowed" everywhere.
func (s *Services) TierOfEnvironment(envCode string) (*model.EnvTier, error) {
	e, err := s.Repo.GetEnvironment(normCode(envCode))
	if err != nil {
		return nil, err
	}
	return s.Repo.GetEnvTier(e.TierCode)
}

// tierOf resolves the tier governing a connection, following
// connection → environment → tier.
//
// Read straight through to the database on every call, matching how the rules
// themselves are read (gateway.matchCommand queries per evaluation). A cache
// here would mean a tier edit takes effect on the rules immediately but on these
// property checks only after eviction, so the same command could be judged under
// one tier and step up MFA under another.
//
// Every caller must treat an error as a refusal. There is no sensible zero
// value: a connection whose tier is unknown has no rules keyed to it, and both
// rule lookups spell "no rows" as "permitted".
func (s *Services) tierOf(conn *model.Connection) (*model.EnvTier, error) {
	if conn == nil {
		return nil, ErrNotFound
	}
	return s.TierOfEnvironment(conn.Env)
}

// tierCodeOf is tierOf for the callers that only need the lookup key.
func (s *Services) tierCodeOf(conn *model.Connection) (string, error) {
	t, err := s.tierOf(conn)
	if err != nil {
		return "", err
	}
	return t.Code, nil
}
