// Package repository is the GORM data-access layer. Relational integrity is kept
// here (no physical FKs), per backend doc §9.
package repository

import (
	"errors"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"velagateway/internal/model"
)

// MaxApprovalSeq returns the largest numeric suffix of any AP-<n> approval number.
func (r *Repo) MaxApprovalSeq() int64 { return r.maxSeq("tbl_approval", "ap_no", "AP-") }

// MaxAuditSeq returns the largest numeric suffix of any AUD-<n> audit id.
func (r *Repo) MaxAuditSeq() int64 { return r.maxSeq("tbl_approval", "audit_id", "AUD-") }

func (r *Repo) maxSeq(table, col, prefix string) int64 {
	var vals []string
	if err := r.db.Table(table).Where(col+" LIKE ?", prefix+"%").Pluck(col, &vals).Error; err != nil {
		// Log rather than silently returning 0 — a 0 here would reseed the AP/AUD
		// counter to the demo base and collide with existing numbers (R9).
		slog.Error("maxSeq query failed", "table", table, "col", col, "err", err)
		return 0
	}
	var m int64
	for _, v := range vals {
		if n, err := strconv.ParseInt(strings.TrimPrefix(v, prefix), 10, 64); err == nil && n > m {
			m = n
		}
	}
	return m
}

// Repo aggregates data access for all entities.
type Repo struct {
	db *gorm.DB
}

func New(db *gorm.DB) *Repo { return &Repo{db: db} }

func (r *Repo) DB() *gorm.DB { return r.db }

// ----------------------------------------------------------------- Users

func (r *Repo) GetUserByEmail(email string) (*model.User, error) {
	var u model.User
	if err := r.db.Where("email = ?", email).First(&u).Error; err != nil {
		return nil, err
	}
	return &u, nil
}

func (r *Repo) GetUserByID(id int64) (*model.User, error) {
	var u model.User
	if err := r.db.First(&u, id).Error; err != nil {
		return nil, err
	}
	return &u, nil
}

func (r *Repo) ListUsers() ([]model.User, error) {
	var us []model.User
	err := r.db.Order("id asc").Find(&us).Error
	return us, err
}

func (r *Repo) CreateUser(u *model.User) error { return r.db.Create(u).Error }

func (r *Repo) UpdateUser(u *model.User) error { return r.db.Save(u).Error }

// UpdateUserFields updates only the named columns for a user. Prefer this over
// UpdateUser(Save) for partial edits so a stale in-memory snapshot can't clobber
// columns changed concurrently (e.g. token_version / mfa_last_ctr) — R8.
func (r *Repo) UpdateUserFields(id int64, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	return r.db.Model(&model.User{}).Where("id = ?", id).Updates(fields).Error
}

func (r *Repo) UpdateUserPassword(userID int64, hash string) error {
	return r.db.Model(&model.User{}).Where("id = ?", userID).Update("password_hash", hash).Error
}

// BumpTokenVersion increments a user's session generation, revoking every token
// issued before now (logout / disable / password reset / role change) — M1.
func (r *Repo) BumpTokenVersion(userID int64) error {
	return r.db.Model(&model.User{}).Where("id = ?", userID).
		Update("token_version", gorm.Expr("token_version + 1")).Error
}

// UpdateUserMFA sets a user's TOTP secret and enabled flag (map form so the
// false/empty values are persisted).
func (r *Repo) UpdateUserMFA(userID int64, enabled bool, secret string) error {
	return r.db.Model(&model.User{}).Where("id = ?", userID).
		Updates(map[string]any{"mfa_enabled": enabled, "mfa_secret": secret, "mfa_last_ctr": 0}).Error
}

// ConsumeMFACounter records a just-used TOTP counter, but only if it advances
// past the last one — RowsAffected==1 means this code had not been used yet.
// This makes a captured step-up code single-use within its validity window (M3).
func (r *Repo) ConsumeMFACounter(userID int64, counter int64) bool {
	res := r.db.Model(&model.User{}).
		Where("id = ? AND mfa_last_ctr < ?", userID, counter).
		Update("mfa_last_ctr", counter)
	return res.RowsAffected == 1
}

// SetConnectionTags replaces a connection's tag string (comma-separated).
func (r *Repo) SetConnectionTags(connID int64, tags string) error {
	return r.db.Model(&model.Connection{}).Where("id = ?", connID).Update("tags", tags).Error
}

// AllConnectionTags returns the distinct, sorted set of tags across all connections.
func (r *Repo) AllConnectionTags() []string {
	var cs []model.Connection
	r.db.Find(&cs)
	set := map[string]bool{}
	for _, c := range cs {
		for _, t := range SplitTags(c.Tags) {
			set[t] = true
		}
	}
	out := make([]string, 0, len(set))
	for t := range set {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// ----------------------------------------------------------------- Role tags

// TagsForRole returns the tags granted to a role (empty = unrestricted).
func (r *Repo) TagsForRole(roleID int64) []string {
	tags, err := r.tagsForRole(roleID)
	if err != nil {
		slog.Error("role tag read failed", "roleID", roleID, "err", err)
	}
	return tags
}

// tagsForRole is TagsForRole with the error kept, so access checks can tell a
// role with no restrictions apart from a tag table it could not read (ED3).
func (r *Repo) tagsForRole(roleID int64) ([]string, error) {
	var rows []model.RoleTag
	if err := r.db.Where("role_id = ?", roleID).Order("tag asc").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, t := range rows {
		out = append(out, t.Tag)
	}
	return out, nil
}

// SetRoleTags replaces a role's granted tags.
func (r *Repo) SetRoleTags(roleID int64, tags []string) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("role_id = ?", roleID).Delete(&model.RoleTag{}).Error; err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, raw := range tags {
			t := strings.ToLower(strings.TrimSpace(raw))
			if t == "" || seen[t] {
				continue
			}
			seen[t] = true
			if err := tx.Create(&model.RoleTag{RoleID: roleID, Tag: t}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// SplitTags normalizes a comma-separated tag string to a lower-cased slice.
func SplitTags(s string) []string {
	out := []string{}
	for _, t := range strings.Split(s, ",") {
		if t = strings.ToLower(strings.TrimSpace(t)); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// ----------------------------------------------------------------- Roles

func (r *Repo) ListRoles() ([]model.Role, error) {
	var rs []model.Role
	err := r.db.Order("id asc").Find(&rs).Error
	return rs, err
}

func (r *Repo) GetRole(id int64) (*model.Role, error) {
	var role model.Role
	if err := r.db.First(&role, id).Error; err != nil {
		return nil, err
	}
	return &role, nil
}

func (r *Repo) UpdateRole(role *model.Role) error { return r.db.Save(role).Error }

func (r *Repo) GetRoleByCode(code string) (*model.Role, error) {
	var role model.Role
	if err := r.db.Where("code = ?", code).First(&role).Error; err != nil {
		return nil, err
	}
	return &role, nil
}

// ----------------------------------------------------------------- Menus

// MenusForRole returns menuKey -> enabled.
func (r *Repo) MenusForRole(roleID int64) (map[string]bool, error) {
	var rows []model.RoleMenu
	if err := r.db.Where("role_id = ?", roleID).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, m := range rows {
		out[m.MenuKey] = m.Enabled
	}
	return out, nil
}

func (r *Repo) SetMenus(roleID int64, menus map[string]bool) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		for k, v := range menus {
			row := model.RoleMenu{RoleID: roleID, MenuKey: k, Enabled: v}
			if err := tx.Save(&row).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// ----------------------------------------------------------------- Capabilities

// CapabilityLevel implements gateway.Store: role × capability × env -> level (default allow).
func (r *Repo) CapabilityLevel(roleID int64, capability, env string) (string, error) {
	var row model.RoleCapability
	// Case-insensitive on capability/env so matching is consistent across the
	// SQLite (dev) and MySQL (prod) drivers regardless of stored/input casing.
	err := r.db.Where("role_id = ? AND LOWER(capability) = ? AND LOWER(env) = ?",
		roleID, strings.ToLower(strings.TrimSpace(capability)), strings.ToLower(strings.TrimSpace(env))).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.LevelAllow, nil // no rule configured for this cell = allow
	}
	if err != nil {
		// A failed QUERY is not an absent rule. Reporting allow here disabled the
		// capability matrix during any transient database fault (ED3).
		return "", err
	}
	return row.Level, nil
}

// MatrixForRole returns capability -> env -> level.
func (r *Repo) MatrixForRole(roleID int64) (map[string]map[string]string, error) {
	var rows []model.RoleCapability
	if err := r.db.Where("role_id = ?", roleID).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := map[string]map[string]string{}
	for _, c := range rows {
		if out[c.Capability] == nil {
			out[c.Capability] = map[string]string{}
		}
		out[c.Capability][c.Env] = c.Level
	}
	return out, nil
}

func (r *Repo) SetMatrix(roleID int64, matrix map[string]map[string]string) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		for cap, envs := range matrix {
			for env, level := range envs {
				row := model.RoleCapability{RoleID: roleID, Capability: cap, Env: env, Level: level}
				if err := tx.Save(&row).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// ----------------------------------------------------------------- Role members

func (r *Repo) MembersOfRole(roleID int64) ([]model.User, error) {
	var ids []int64
	if err := r.db.Model(&model.RoleMember{}).Where("role_id = ?", roleID).Pluck("user_id", &ids).Error; err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return []model.User{}, nil
	}
	var us []model.User
	err := r.db.Where("id IN ?", ids).Order("id asc").Find(&us).Error
	return us, err
}

// RolesOfUser returns the role names a user is a member of.
func (r *Repo) RolesOfUser(userID int64) ([]string, error) {
	var roleIDs []int64
	if err := r.db.Model(&model.RoleMember{}).Where("user_id = ?", userID).Pluck("role_id", &roleIDs).Error; err != nil {
		return nil, err
	}
	if len(roleIDs) == 0 {
		return []string{}, nil
	}
	var names []string
	if err := r.db.Model(&model.Role{}).Where("id IN ?", roleIDs).Order("id asc").Pluck("name", &names).Error; err != nil {
		return nil, err
	}
	return names, nil
}

func (r *Repo) AddMember(roleID, userID int64) error {
	return r.db.Where(model.RoleMember{RoleID: roleID, UserID: userID}).
		FirstOrCreate(&model.RoleMember{RoleID: roleID, UserID: userID}).Error
}

func (r *Repo) RemoveMember(roleID, userID int64) error {
	return r.db.Where("role_id = ? AND user_id = ?", roleID, userID).Delete(&model.RoleMember{}).Error
}

// RoleIDsOfUser returns the role ids a user is a member of (tbl_role_member).
func (r *Repo) RoleIDsOfUser(userID int64) ([]int64, error) {
	var ids []int64
	err := r.db.Model(&model.RoleMember{}).Where("user_id = ?", userID).Order("role_id asc").Pluck("role_id", &ids).Error
	return ids, err
}

// EffectiveRoleIDs is the deduplicated set of roles that govern a user's
// permissions: their primary role (tbl_user.role_id) unioned with every role
// they are a member of. All permission resolution (menus, capability matrix,
// tags, oversight) is computed over this set so multiple roles compose (union).
func (r *Repo) EffectiveRoleIDs(u *model.User) []int64 {
	if u == nil {
		return nil
	}
	seen := map[int64]bool{}
	out := []int64{}
	if u.RoleID != 0 {
		seen[u.RoleID] = true
		out = append(out, u.RoleID)
	}
	ids, _ := r.RoleIDsOfUser(u.ID)
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// SetUserRoles replaces a user's role membership set atomically. The first id is
// also written to tbl_user.role_id as the primary role (drives display/JWT).
func (r *Repo) SetUserRoles(userID int64, roleIDs []int64) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("user_id = ?", userID).Delete(&model.RoleMember{}).Error; err != nil {
			return err
		}
		seen := map[int64]bool{}
		for _, rid := range roleIDs {
			if rid == 0 || seen[rid] {
				continue
			}
			seen[rid] = true
			if err := tx.Create(&model.RoleMember{RoleID: rid, UserID: userID}).Error; err != nil {
				return err
			}
		}
		if len(roleIDs) > 0 {
			if err := tx.Model(&model.User{}).Where("id = ?", userID).Update("role_id", roleIDs[0]).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// RoleCodesForIDs returns the role codes for a set of role ids (for oversight /
// admin checks that must consider every role a user holds).
func (r *Repo) RoleCodesForIDs(ids []int64) []string {
	if len(ids) == 0 {
		return nil
	}
	var codes []string
	r.db.Model(&model.Role{}).Where("id IN ?", ids).Pluck("code", &codes)
	return codes
}

// MenusForRoles returns the union of menu access across several roles: a menu is
// visible if ANY of the roles enables it.
func (r *Repo) MenusForRoles(ids []int64) (map[string]bool, error) {
	out := map[string]bool{}
	for _, id := range ids {
		m, err := r.MenusForRole(id)
		if err != nil {
			return nil, err
		}
		for k, v := range m {
			if v {
				out[k] = true
			}
		}
	}
	return out, nil
}

// levelRank orders capability levels from most to least permissive.
var levelRank = map[string]int{model.LevelAllow: 0, model.LevelApprove: 1, model.LevelDeny: 2}

// MatrixForRoles merges several roles' capability matrices, keeping the MOST
// permissive level per capability×env cell (union semantics for the /me view).
func (r *Repo) MatrixForRoles(ids []int64) (map[string]map[string]string, error) {
	out := map[string]map[string]string{}
	for _, id := range ids {
		m, err := r.MatrixForRole(id)
		if err != nil {
			return nil, err
		}
		for cap, envs := range m {
			if out[cap] == nil {
				out[cap] = map[string]string{}
			}
			for env, level := range envs {
				if cur, ok := out[cap][env]; !ok || levelRank[level] < levelRank[cur] {
					out[cap][env] = level
				}
			}
		}
	}
	return out, nil
}

// TagsForRoles returns the union of DB tags across roles. A role with no tags is
// unrestricted; if ANY of the user's roles is unrestricted the user sees every
// connection, so unrestricted=true is returned and the tag list is irrelevant.
func (r *Repo) TagsForRoles(ids []int64) (allow []string, unrestricted bool, err error) {
	seen := map[string]bool{}
	out := []string{}
	for _, id := range ids {
		tags, terr := r.tagsForRole(id)
		if terr != nil {
			// Never report unrestricted on a failed read: an empty result means
			// "no restrictions", so swallowing the error would hand a restricted
			// role the whole estate during a database blip (ED3).
			return nil, false, terr
		}
		if len(tags) == 0 {
			return nil, true, nil
		}
		for _, t := range tags {
			if !seen[t] {
				seen[t] = true
				out = append(out, t)
			}
		}
	}
	return out, false, nil
}

// ------------------------------------------------------- Env tiers / environments

func (r *Repo) ListEnvTiers() ([]model.EnvTier, error) {
	var ts []model.EnvTier
	err := r.db.Order("sort_order asc, code asc").Find(&ts).Error
	return ts, err
}

func (r *Repo) GetEnvTier(code string) (*model.EnvTier, error) {
	var t model.EnvTier
	if err := r.db.First(&t, "code = ?", code).Error; err != nil {
		return nil, err
	}
	return &t, nil
}

// ScanBaselineTier returns the tier whose dictionary script scanning judges
// against. A missing baseline is an error, never a zero value: handing back ""
// would make matchCommand match nothing and report every uploaded script as
// clean — the exact silent-failure mode unavailableVerdict exists to prevent.
func (r *Repo) ScanBaselineTier() (*model.EnvTier, error) {
	var t model.EnvTier
	if err := r.db.First(&t, "scan_baseline = ?", true).Error; err != nil {
		return nil, err
	}
	return &t, nil
}

func (r *Repo) ListEnvironments() ([]model.Environment, error) {
	var es []model.Environment
	err := r.db.Order("sort_order asc, code asc").Find(&es).Error
	return es, err
}

func (r *Repo) GetEnvironment(code string) (*model.Environment, error) {
	var e model.Environment
	if err := r.db.First(&e, "code = ?", code).Error; err != nil {
		return nil, err
	}
	return &e, nil
}

// CountEnvironmentsOfTier reports how many environments are bound to a tier —
// the check that stops a tier from being deleted out from under live instances.
func (r *Repo) CountEnvironmentsOfTier(tierCode string) (int64, error) {
	var n int64
	err := r.db.Model(&model.Environment{}).Where("tier_code = ?", tierCode).Count(&n).Error
	return n, err
}

// CountConnectionsInEnvironment reports how many instances live in an
// environment, so a delete can tell the operator what it is about to move.
func (r *Repo) CountConnectionsInEnvironment(code string) (int64, error) {
	var n int64
	err := r.db.Model(&model.Connection{}).Where("env = ?", code).Count(&n).Error
	return n, err
}

// CreateEnvTierFrom writes a tier AND clones every rule row of the template tier
// in ONE transaction: each (role × capability) level from tbl_role_capability
// and each command level from tbl_risk_command.
//
// The clone is not a convenience. Both lookups read a missing row as permission
// granted, so a tier that exists without its rules is an environment where any
// role may DROP TABLE unreviewed. Committing the tier and copying afterwards
// would open exactly that window, so a failed copy rolls the tier back with it.
//
// This generalises what backfillGliEnv did by hand when GLI was introduced.
func (r *Repo) CreateEnvTierFrom(t *model.EnvTier, templateCode string) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		var tmpl model.EnvTier
		if err := tx.First(&tmpl, "code = ?", templateCode).Error; err != nil {
			return err
		}
		if err := tx.Create(t).Error; err != nil {
			return err
		}

		var caps []model.RoleCapability
		if err := tx.Where("env = ?", templateCode).Find(&caps).Error; err != nil {
			return err
		}
		for _, row := range caps {
			row.Env = t.Code
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		}

		var cmds []model.RiskCommand
		if err := tx.Where("env = ?", templateCode).Find(&cmds).Error; err != nil {
			return err
		}
		for _, row := range cmds {
			row.Env = t.Code
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		}

		// A tier is created without the baseline; moving it is an explicit edit.
		if t.ScanBaseline {
			if err := clearOtherBaselines(tx, t.Code); err != nil {
				return err
			}
		}
		return nil
	})
}

// UpdateEnvTier saves a tier, keeping the single-baseline invariant: setting the
// flag clears it everywhere else in the same transaction.
func (r *Repo) UpdateEnvTier(t *model.EnvTier) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(t).Error; err != nil {
			return err
		}
		if t.ScanBaseline {
			return clearOtherBaselines(tx, t.Code)
		}
		return nil
	})
}

func clearOtherBaselines(tx *gorm.DB, keep string) error {
	return tx.Model(&model.EnvTier{}).
		Where("code <> ? AND scan_baseline = ?", keep, true).
		Update("scan_baseline", false).Error
}

// DeleteEnvTier removes a tier and every rule row keyed by it. Callers must have
// verified that no environment still binds it (service layer).
func (r *Repo) DeleteEnvTier(code string) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("env = ?", code).Delete(&model.RoleCapability{}).Error; err != nil {
			return err
		}
		if err := tx.Where("env = ?", code).Delete(&model.RiskCommand{}).Error; err != nil {
			return err
		}
		return tx.Where("code = ?", code).Delete(&model.EnvTier{}).Error
	})
}

func (r *Repo) CreateEnvironment(e *model.Environment) error { return r.db.Create(e).Error }

func (r *Repo) UpdateEnvironment(e *model.Environment) error { return r.db.Save(e).Error }

// DeleteEnvironmentMoving reassigns every instance of `code` to `moveTo` and then
// drops the environment, in one transaction — an instance must never be left
// pointing at an environment that no longer exists, since its tier is what
// decides whether the instance is regulated at all.
func (r *Repo) DeleteEnvironmentMoving(code, moveTo string) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		var dst model.Environment
		if err := tx.First(&dst, "code = ?", moveTo).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.Connection{}).
			Where("env = ?", code).Update("env", moveTo).Error; err != nil {
			return err
		}
		return tx.Where("code = ?", code).Delete(&model.Environment{}).Error
	})
}

// ----------------------------------------------------------------- Connections

func (r *Repo) ListConnections() ([]model.Connection, error) {
	var cs []model.Connection
	err := r.db.Order("id asc").Find(&cs).Error
	return cs, err
}

func (r *Repo) GetConnection(id int64) (*model.Connection, error) {
	var c model.Connection
	if err := r.db.First(&c, id).Error; err != nil {
		return nil, err
	}
	return &c, nil
}

func (r *Repo) CreateConnection(c *model.Connection) error { return r.db.Create(c).Error }

func (r *Repo) UpdateConnection(c *model.Connection) error { return r.db.Save(c).Error }

// ----------------------------------------------------------------- Risk dictionary

// RiskCommands implements gateway.Store. The error is propagated rather than
// swallowed: an unreadable dictionary is not an empty one, and the engine must
// be able to tell them apart to fail closed (ED3).
func (r *Repo) RiskCommands() ([]model.RiskCommand, error) {
	var rc []model.RiskCommand
	if err := r.db.Find(&rc).Error; err != nil {
		return nil, err
	}
	return rc, nil
}

// RiskCommandsGrouped returns command -> env -> level, preserving insertion order of commands.
func (r *Repo) RiskCommandsGrouped() ([]string, map[string]map[string]string) {
	rows, err := r.RiskCommands()
	if err != nil {
		slog.Error("risk dictionary read failed", "err", err)
	}
	grouped := map[string]map[string]string{}
	order := []string{}
	// stable command order: by a fixed priority then alpha
	priority := map[string]int{"DROP": 0, "TRUNCATE": 1, "DELETE": 2, "ALTER": 3, "RENAME": 4, "GRANT": 5, "REVOKE": 6}
	for _, c := range rows {
		if grouped[c.Command] == nil {
			grouped[c.Command] = map[string]string{}
			order = append(order, c.Command)
		}
		grouped[c.Command][c.Env] = c.Level
	}
	sort.SliceStable(order, func(i, j int) bool {
		pi, oki := priority[order[i]]
		pj, okj := priority[order[j]]
		if oki && okj {
			return pi < pj
		}
		if oki != okj {
			return oki
		}
		return order[i] < order[j]
	})
	return order, grouped
}

// UpsertRiskCommand writes a dictionary command across EVERY control tier, using
// the caller's level where one was supplied and defaultRiskLevel elsewhere.
//
// The tier list comes from the database, not from the caller. It used to write
// only the keys the request contained, and the console sent a hardcoded
// {prod, staging, dev} — so every command an operator added through the UI was
// missing its GLI row, and a missing row reads as RiskOff. The dictionary looked
// correct on the page while GLI (法务) instances were ungoverned by it.
//
// Trusting the caller to enumerate the tiers is what made that possible, and a
// tier being data now means any client could fall behind the same way. Rows for
// tiers the caller omitted are created here rather than left absent, because
// absent is not neutral — it is permissive.
func (r *Repo) UpsertRiskCommand(command string, levels map[string]string) error {
	command = strings.ToUpper(strings.TrimSpace(command))
	// Normalise the caller's keys the way PatchRiskLevel does, so a request
	// carrying "PROD" still lands on the prod row instead of creating a second.
	want := make(map[string]string, len(levels))
	for k, v := range levels {
		want[strings.ToLower(strings.TrimSpace(k))] = v
	}
	return r.db.Transaction(func(tx *gorm.DB) error {
		var tiers []model.EnvTier
		if err := tx.Find(&tiers).Error; err != nil {
			return err
		}
		for _, t := range tiers {
			if lvl, ok := want[t.Code]; ok && strings.TrimSpace(lvl) != "" {
				if err := tx.Save(&model.RiskCommand{Command: command, Env: t.Code, Level: lvl}).Error; err != nil {
					return err
				}
				continue
			}
			// No level given for this tier. A row that ALREADY exists is left
			// exactly as it is: editing one tier from the rules page must not
			// silently rewrite the others, or setting staging to `high` would drag
			// PROD's own level along with it (R11).
			//
			// Only a tier with no row at all gets one, because that is the case
			// that is not neutral: absent reads as `off`, so the command an
			// operator just added would not apply there and the page would give no
			// sign of it.
			var existing int64
			if err := tx.Model(&model.RiskCommand{}).
				Where("command = ? AND env = ?", command, t.Code).Count(&existing).Error; err != nil {
				return err
			}
			if existing > 0 {
				continue
			}
			if err := tx.Save(&model.RiskCommand{Command: command, Env: t.Code, Level: defaultRiskLevel(t)}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// defaultRiskLevel decides what a newly added command means on a tier the caller
// did not mention. Tiers that gate production work — the script-scan baseline
// and any tier demanding an MFA step-up — get `high`, so a command added to the
// dictionary is gated there from the moment it exists. Everywhere else it is
// `off`, matching how the built-in dictionary is seeded.
//
// Erring towards `high` is deliberate: the wrong guess costs an approval that
// was not needed, while the other direction lets a command the operator just
// classified as dangerous run unreviewed on a production tier.
func defaultRiskLevel(t model.EnvTier) string {
	if t.ScanBaseline || t.RequireMFA {
		return model.RiskHigh
	}
	return model.RiskOff
}

func (r *Repo) PatchRiskLevel(command, env, level string) error {
	// normalize like UpsertRiskCommand so keys stay canonical (command upper, env lower)
	command = strings.ToUpper(strings.TrimSpace(command))
	env = strings.ToLower(strings.TrimSpace(env))
	row := model.RiskCommand{Command: command, Env: env, Level: level}
	return r.db.Save(&row).Error
}

func (r *Repo) DeleteRiskCommand(command string) error {
	return r.db.Where("command = ?", strings.ToUpper(command)).Delete(&model.RiskCommand{}).Error
}

// ----------------------------------------------------------------- Approvals

func (r *Repo) CreateApproval(a *model.Approval, steps []model.ApprovalStep) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(a).Error; err != nil {
			return err
		}
		for i := range steps {
			steps[i].ApprovalID = a.ID
		}
		if len(steps) > 0 {
			if err := tx.Create(&steps).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// ListApprovals returns approvals the user is allowed to see. A ticket is only
// visible to its initiator and to the members on its approval chain:
//   - scope "mine": approvals the user must act on (they are a chain approver)
//   - scope "all":  approvals the user initiated OR is a chain approver of
// approvalScope builds the visibility predicate for a caller: tickets they raised,
// plus tickets they sit on the approval chain of. "mine" narrows to the latter —
// the queue awaiting this person.
//
// The chain membership is a SQL subquery rather than a Pluck into Go: the old
// version loaded every approval id the caller had ever been an approver on and
// pasted them into an IN(...) list, which grows with history and eventually
// exceeds the driver's parameter limit (ED13).
func (r *Repo) approvalScope(scope string, userID int64) *gorm.DB {
	sub := r.db.Model(&model.ApprovalStep{}).Select("approval_id").Where("approver_id = ?", userID)
	if scope == "mine" {
		return r.db.Model(&model.Approval{}).Where("id IN (?)", sub)
	}
	return r.db.Model(&model.Approval{}).Where("initiator_id = ? OR id IN (?)", userID, sub)
}

// ListApprovalsPaged returns one page of approvals the caller may see (newest
// first) plus the total matching that same visibility.
//
// apNo, when set, looks a single ticket up by number REGARDLESS of paging — the
// audit log links tickets by number, and such a ticket may sit on any page. The
// visibility predicate still applies, so a number cannot be used to read someone
// else's ticket.
func (r *Repo) ListApprovalsPaged(scope string, userID int64, apNo string, offset, limit int) ([]model.Approval, int64, error) {
	count := r.approvalScope(scope, userID)
	rows := r.approvalScope(scope, userID).Order("id desc")
	if apNo != "" {
		count = count.Where("ap_no = ?", apNo)
		rows = rows.Where("ap_no = ?", apNo)
	}
	var total int64
	if err := count.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if limit > 0 {
		rows = rows.Limit(limit).Offset(offset)
	}
	var as []model.Approval
	err := rows.Find(&as).Error
	if as == nil {
		as = []model.Approval{}
	}
	return as, total, err
}

// ListApprovals returns every approval the caller may see. Retained for callers
// that genuinely need the whole set (the approval-chain sweep); the console uses
// the paged form.
func (r *Repo) ListApprovals(scope string, userID int64) ([]model.Approval, error) {
	as, _, err := r.ListApprovalsPaged(scope, userID, "", 0, 0)
	return as, err
}

func (r *Repo) GetApproval(id int64) (*model.Approval, error) {
	var a model.Approval
	if err := r.db.First(&a, id).Error; err != nil {
		return nil, err
	}
	return &a, nil
}

// GetApprovalByApNo finds an approval by its human ApNo (e.g. "AP-000123"). The
// external审批魔方 callback echoes our ApNo back as `external_task_id`, so this is
// the callback correlation key.
func (r *Repo) GetApprovalByApNo(apNo string) (*model.Approval, error) {
	var a model.Approval
	if err := r.db.Where("ap_no = ?", apNo).First(&a).Error; err != nil {
		return nil, err
	}
	return &a, nil
}

// SetApprovalExternalTask stores the vendor's task_id (for the timeout PATCH).
func (r *Repo) SetApprovalExternalTask(id int64, extID string) error {
	return r.db.Model(&model.Approval{}).Where("id = ?", id).Update("external_task_id", extID).Error
}

func (r *Repo) StepsOf(approvalID int64) ([]model.ApprovalStep, error) {
	var ss []model.ApprovalStep
	err := r.db.Where("approval_id = ?", approvalID).Order("step_order asc").Find(&ss).Error
	return ss, err
}

func (r *Repo) UpdateApprovalStatus(id int64, status string) error {
	return r.db.Model(&model.Approval{}).Where("id = ?", id).Update("status", status).Error
}

// ClaimApproval atomically transitions an approval from `from` to `to`, but only
// if it is still in the `from` state. Returns true iff this caller won the race
// (RowsAffected == 1). This closes the decide/expire TOCTOU where two callers
// could both see "pending" and both execute the command.
func (r *Repo) ClaimApproval(id int64, from, to string) (bool, error) {
	res := r.db.Model(&model.Approval{}).
		Where("id = ? AND status = ?", id, from).
		Update("status", to)
	return res.RowsAffected == 1, res.Error
}

// ClaimEscalation atomically marks an approval as escalated, but only if it was
// not already. Returns true iff this caller flipped it (RowsAffected == 1), so a
// timeout sweep escalates each overdue ticket exactly once (R13).
func (r *Repo) ClaimEscalation(id int64) (bool, error) {
	res := r.db.Model(&model.Approval{}).
		Where("id = ? AND escalated = ?", id, false).
		Update("escalated", true)
	return res.RowsAffected == 1, res.Error
}

// SetApprovalResult records the post-decision execution result + decided time.
func (r *Repo) SetApprovalResult(id int64, output string, rows int, at time.Time) error {
	return r.db.Model(&model.Approval{}).Where("id = ?", id).Updates(map[string]any{
		"result": output, "result_rows": rows, "decided_at": at,
	}).Error
}

// DecideActiveStep records a decision on the currently-active chain step of an
// approval: it sets the step status (approved|rejected) and stamps actedAt.
// A single decision finalizes the whole ticket (approve-1 policy), so any later
// steps that never acted are left as-is rather than promoted to active.
func (r *Repo) DecideActiveStep(approvalID int64, status string, actedAt time.Time) error {
	var step model.ApprovalStep
	err := r.db.Where("approval_id = ? AND status = ?", approvalID, "active").
		Order("step_order asc").First(&step).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil // no active step (e.g. chainless approval) — nothing to record
		}
		return err
	}
	return r.db.Model(&model.ApprovalStep{}).Where("id = ?", step.ID).
		Updates(map[string]any{"status": status, "acted_at": actedAt}).Error
}

// ----------------------------------------------------------------- Audit

// LastAuditHash returns the hash of the most recent audit row ("" if none).
func (r *Repo) LastAuditHash() string {
	var a model.AuditLog
	if err := r.db.Order("id desc").First(&a).Error; err != nil {
		return ""
	}
	return a.Hash
}

func (r *Repo) InsertAudit(a *model.AuditLog) error { return r.db.Create(a).Error }

// ListAudit lists audit rows; actorID > 0 restricts to that actor's own commands.
func (r *Repo) ListAudit(actorID int64, risk string, since, until time.Time, limit int) ([]model.AuditLog, error) {
	var rows []model.AuditLog
	q := r.auditQuery(actorID, risk, since, until).Order("occurred_at desc, id desc")
	if limit > 0 {
		q = q.Limit(limit)
	}
	err := q.Find(&rows).Error
	return rows, err
}

// ListAuditPaged returns one page of audit rows (newest first) plus the total
// row count matching the same filters, for paginated display.
func (r *Repo) ListAuditPaged(actorID int64, risk string, since, until time.Time, offset, limit int) ([]model.AuditLog, int64, error) {
	var total int64
	if err := r.auditQuery(actorID, risk, since, until).Model(&model.AuditLog{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []model.AuditLog
	q := r.auditQuery(actorID, risk, since, until).Order("occurred_at desc, id desc")
	if limit > 0 {
		q = q.Limit(limit).Offset(offset)
	}
	err := q.Find(&rows).Error
	return rows, total, err
}

// auditQuery builds the shared WHERE clause for audit listing/counting.
func (r *Repo) auditQuery(actorID int64, risk string, since, until time.Time) *gorm.DB {
	q := r.db.Model(&model.AuditLog{})
	if actorID > 0 {
		q = q.Where("actor_id = ?", actorID)
	}
	if risk != "" && risk != "all" {
		q = q.Where("risk = ?", risk)
	}
	if !since.IsZero() {
		q = q.Where("occurred_at >= ?", since)
	}
	if !until.IsZero() {
		q = q.Where("occurred_at <= ?", until)
	}
	return q
}

// ListPendingApprovalsOlderThan returns pending approvals created before cutoff (timeout sweep).
func (r *Repo) ListPendingApprovalsOlderThan(cutoff time.Time) ([]model.Approval, error) {
	var as []model.Approval
	err := r.db.Where("status = ? AND created_at < ?", model.StatusPending, cutoff).Find(&as).Error
	return as, err
}

// ----------------------------------------------------------------- Webhook / settings

func (r *Repo) GetWebhook() (*model.WebhookConfig, error) {
	var w model.WebhookConfig
	if err := r.db.Order("id asc").First(&w).Error; err != nil {
		return nil, err
	}
	return &w, nil
}

func (r *Repo) SaveWebhook(w *model.WebhookConfig) error { return r.db.Save(w).Error }

// SchemaForConnection returns the (simulated) db.table objects of a connection,
// ordered for a stable tree (database, then table).
func (r *Repo) SchemaForConnection(connID int64) ([]model.SchemaObject, error) {
	var rows []model.SchemaObject
	// `database` is a MySQL reserved word — must be back-quoted (works on SQLite too).
	err := r.db.Where("connection_id = ?", connID).
		Order("`database` asc, table_name asc").Find(&rows).Error
	return rows, err
}

// InsertWebhookDelivery appends a delivery-outcome record.
func (r *Repo) InsertWebhookDelivery(d *model.WebhookDelivery) error { return r.db.Create(d).Error }

// ListWebhookDeliveries returns the most recent delivery records (newest first).
func (r *Repo) ListWebhookDeliveries(limit int) ([]model.WebhookDelivery, error) {
	var rows []model.WebhookDelivery
	q := r.db.Order("id desc")
	if limit > 0 {
		q = q.Limit(limit)
	}
	err := q.Find(&rows).Error
	return rows, err
}

// ----------------------------------------------------------------- Notifications

func (r *Repo) CreateNotification(n *model.Notification) error { return r.db.Create(n).Error }

// ListNotifications returns a user's notifications, newest first.
func (r *Repo) ListNotifications(userID int64, limit int) ([]model.Notification, error) {
	var ns []model.Notification
	q := r.db.Where("user_id = ?", userID).Order("id desc")
	if limit > 0 {
		q = q.Limit(limit)
	}
	err := q.Find(&ns).Error
	return ns, err
}

func (r *Repo) CountUnreadNotifications(userID int64) (int64, error) {
	var n int64
	err := r.db.Model(&model.Notification{}).
		Where("user_id = ? AND is_read = ?", userID, false).Count(&n).Error
	return n, err
}

// MarkNotificationsRead marks the user's notifications read; empty ids marks all.
func (r *Repo) MarkNotificationsRead(userID int64, ids []int64) error {
	q := r.db.Model(&model.Notification{}).Where("user_id = ?", userID)
	if len(ids) > 0 {
		q = q.Where("id IN ?", ids)
	}
	return q.Update("is_read", true).Error
}

// ----------------------------------------------------------------- Export jobs

func (r *Repo) CreateExportJob(j *model.ExportJob) error { return r.db.Create(j).Error }

func (r *Repo) GetExportJob(id int64) (*model.ExportJob, error) {
	var j model.ExportJob
	if err := r.db.First(&j, id).Error; err != nil {
		return nil, err
	}
	return &j, nil
}

func (r *Repo) UpdateExportJob(id int64, fields map[string]any) error {
	return r.db.Model(&model.ExportJob{}).Where("id = ?", id).Updates(fields).Error
}

// ClaimExportJob atomically transitions a job from pending → running, returning
// whether this caller won the claim. A job that is not pending (already failed,
// done, or claimed by another worker) is left untouched and reports false, so a
// failed/reconciled job is never re-scheduled and two workers never run the same job.
func (r *Repo) ClaimExportJob(id int64) (bool, error) {
	res := r.db.Model(&model.ExportJob{}).
		Where("id = ? AND status = ?", id, model.ExportPending).
		Updates(map[string]any{"status": model.ExportRunning})
	return res.RowsAffected == 1, res.Error
}

// FailStuckExportJobs marks jobs left pending/running by a previous process (the
// in-memory queue doesn't survive a restart) as failed, so they don't hang in the
// UI forever. Returns the number reconciled (R24).
func (r *Repo) FailStuckExportJobs() (int64, error) {
	res := r.db.Model(&model.ExportJob{}).
		Where("status IN ?", []string{model.ExportPending, model.ExportRunning}).
		Updates(map[string]any{"status": model.ExportFailed, "error": "服务重启导致任务中断,请重新提交"})
	return res.RowsAffected, res.Error
}

func (r *Repo) ListExportJobs(userID int64, limit int) ([]model.ExportJob, error) {
	var js []model.ExportJob
	q := r.db.Where("user_id = ?", userID).Order("id desc")
	if limit > 0 {
		q = q.Limit(limit)
	}
	err := q.Find(&js).Error
	return js, err
}

// ----------------------------------------------------------------- Async jobs

func (r *Repo) CreateAsyncJob(j *model.AsyncJob) error { return r.db.Create(j).Error }

func (r *Repo) GetAsyncJob(id int64) (*model.AsyncJob, error) {
	var j model.AsyncJob
	if err := r.db.First(&j, id).Error; err != nil {
		return nil, err
	}
	return &j, nil
}

func (r *Repo) ListAsyncJobs(userID int64, limit int) ([]model.AsyncJob, error) {
	var js []model.AsyncJob
	q := r.db.Where("user_id = ?", userID).Order("id desc")
	if limit > 0 {
		q = q.Limit(limit)
	}
	err := q.Find(&js).Error
	return js, err
}

// ClaimAsyncJob atomically transitions a job pending → running, returning true iff
// this caller won the race (so a restart-reconciled or double-enqueued job runs once).
func (r *Repo) ClaimAsyncJob(id int64, startedAt time.Time) (bool, error) {
	res := r.db.Model(&model.AsyncJob{}).
		Where("id = ? AND status = ?", id, model.AsyncPending).
		Updates(map[string]any{"status": model.AsyncRunning, "started_at": startedAt})
	return res.RowsAffected == 1, res.Error
}

// UpdateAsyncJobLog overwrites the streamed log (the worker keeps the full text
// in memory and flushes it whole — dialect-safe, avoids per-notice concat).
func (r *Repo) UpdateAsyncJobLog(id int64, log string) error {
	return r.db.Model(&model.AsyncJob{}).Where("id = ?", id).Update("log", log).Error
}

// FinishAsyncJob records the terminal status + final log/rows/error.
func (r *Repo) FinishAsyncJob(id int64, status, log, errMsg string, rows int, at time.Time) error {
	return r.db.Model(&model.AsyncJob{}).Where("id = ?", id).Updates(map[string]any{
		"status": status, "log": log, "error": errMsg, "rows": rows, "finished_at": at,
	}).Error
}

// FailStuckAsyncJobs marks jobs orphaned by a previous process as failed on boot.
func (r *Repo) FailStuckAsyncJobs() (int64, error) {
	res := r.db.Model(&model.AsyncJob{}).
		Where("status IN ?", []string{model.AsyncPending, model.AsyncRunning}).
		Updates(map[string]any{"status": model.AsyncFailed, "error": "服务重启导致任务中断,请重新提交"})
	return res.RowsAffected, res.Error
}

// ----------------------------------------------------------------- Script uploads

func (r *Repo) CreateScriptUpload(u *model.ScriptUpload) error { return r.db.Create(u).Error }

func (r *Repo) ListScriptUploads(userID int64) ([]model.ScriptUpload, error) {
	var us []model.ScriptUpload
	err := r.db.Where("user_id = ?", userID).Order("id desc").Find(&us).Error
	return us, err
}

func (r *Repo) GetScriptUpload(id int64) (*model.ScriptUpload, error) {
	var u model.ScriptUpload
	if err := r.db.First(&u, id).Error; err != nil {
		return nil, err
	}
	return &u, nil
}

func (r *Repo) DeleteScriptUpload(id int64) error {
	return r.db.Delete(&model.ScriptUpload{}, id).Error
}

func (r *Repo) GetSetting(k string) (string, error) {
	var s model.Setting
	if err := r.db.First(&s, "k = ?", k).Error; err != nil {
		return "", err
	}
	return s.V, nil
}

func (r *Repo) AllSettings() (map[string]string, error) {
	var rows []model.Setting
	if err := r.db.Find(&rows).Error; err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, s := range rows {
		out[s.K] = s.V
	}
	return out, nil
}

func (r *Repo) SetSetting(k, v string) error {
	return r.db.Save(&model.Setting{K: k, V: v}).Error
}

// Count returns the row count for any model (used to decide seeding).
func (r *Repo) Count(m any) int64 {
	var n int64
	r.db.Model(m).Count(&n)
	return n
}

// CountProdInterceptions counts real risk-rule hits on the instances whose tier
// is marked CountsInPending: audit rows that were blocked (rejected) or gated to
// approval (pending). Backs the rules page's "hits" stat with live data instead
// of a demo number.
//
// The instance is joined through to its tier rather than compared against the
// literal 'prod' — with several production environments (prod-hk, prod-sh) the
// literal counted only the one that happens to be named after its tier.
func (r *Repo) CountProdInterceptions() (int64, error) {
	var n int64
	err := r.db.Model(&model.AuditLog{}).
		Joins("JOIN tbl_connection ON tbl_connection.id = tbl_audit_log.connection_id").
		Joins("JOIN tbl_environment ON tbl_environment.code = tbl_connection.env").
		Joins("JOIN tbl_env_tier ON tbl_env_tier.code = tbl_environment.tier_code").
		Where("tbl_env_tier.counts_in_pending = ? AND tbl_audit_log.result IN ?", true, []string{"rejected", "pending"}).
		Count(&n).Error
	return n, err
}

// ----------------------------------------------------------------- User tags

// TagsForUser returns the tags granted directly to a user (empty = none set, in
// which case the role-derived scope applies — see ScopeForUser).
func (r *Repo) TagsForUser(userID int64) ([]string, error) {
	var rows []model.UserTag
	if err := r.db.Where("user_id = ?", userID).Order("tag asc").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, t := range rows {
		out = append(out, t.Tag)
	}
	return out, nil
}

// SetUserTags replaces a user's directly-granted tags.
func (r *Repo) SetUserTags(userID int64, tags []string) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("user_id = ?", userID).Delete(&model.UserTag{}).Error; err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, raw := range tags {
			t := strings.ToLower(strings.TrimSpace(raw))
			if t == "" || seen[t] {
				continue
			}
			seen[t] = true
			if err := tx.Create(&model.UserTag{UserID: userID, Tag: t}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// ScopeForUser resolves which connection tags a user may reach.
//
// A grant made directly on the user is the MORE SPECIFIC statement and REPLACES
// the role-derived scope. Unioning the two instead would make a user-level grant
// meaningless for anyone whose role is already unrestricted — an administrator
// would stay unrestricted whatever tags you gave them, which is precisely the
// case per-user scoping exists for. With no user-level rows the role behaviour is
// returned unchanged.
func (r *Repo) ScopeForUser(userID int64, roleIDs []int64) (allow []string, unrestricted bool, err error) {
	own, err := r.TagsForUser(userID)
	if err != nil {
		return nil, false, err // never fall back to unrestricted on a failed read (ED3)
	}
	if len(own) > 0 {
		return own, false, nil
	}
	return r.TagsForRoles(roleIDs)
}

// CountPendingApprovals counts the pending tickets the caller may see. The
// listing is paged, so the inbox badge cannot be derived from a page — it would
// silently cap at the page size once someone has more than that.
func (r *Repo) CountPendingApprovals(scope string, userID int64) (int64, error) {
	var n int64
	err := r.approvalScope(scope, userID).Where("status = ?", model.StatusPending).Count(&n).Error
	return n, err
}
