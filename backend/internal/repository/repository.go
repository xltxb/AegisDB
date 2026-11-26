// Package repository is the GORM data-access layer. Relational integrity is kept
// here (no physical FKs), per backend doc §9.
package repository

import (
	"errors"
	"log/slog"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"aegisdb/internal/apimsg"
	"aegisdb/internal/model"
	"aegisdb/internal/presence"
	"aegisdb/pkg/crypto"
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

// GetUserByEmailFold 按邮箱查,不分大小写。SSO 回来的邮箱与网关里存的大小写不一定一致
// (CreateUser 会转小写,但早期种子与邀请路径不会)—— 大小写不同就当成两个人,结果是
// 一个已经开通的人经 SSO 进来时被当成没有账号而拒绝。
func (r *Repo) GetUserByEmailFold(email string) (*model.User, error) {
	var u model.User
	if err := r.db.Where("LOWER(email) = ?", strings.ToLower(strings.TrimSpace(email))).First(&u).Error; err != nil {
		return nil, err
	}
	return &u, nil
}

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
	// 他的 token 全部作废了,在线列表里也该立刻没有他 —— 不等 3 分钟的窗口过去。
	presence.Default.DropUser(userID)
	return r.db.Model(&model.User{}).Where("id = ?", userID).
		Update("token_version", gorm.Expr("token_version + 1")).Error
}

// TouchLastActive 记下一个人最近一次在网关上操作的时间(RFC3339)。从前这一列只在建号时写一个
// "—" 或"刚刚",之后再没动过。
func (r *Repo) TouchLastActive(userID int64, at time.Time) error {
	return r.db.Model(&model.User{}).Where("id = ?", userID).Update("last_active", at.Format(time.RFC3339)).Error
}

// UpdateUserMFA sets a user's TOTP secret and enabled flag (map form so the
// false/empty values are persisted).
func (r *Repo) UpdateUserMFA(userID int64, enabled bool, secret string) error {
	// 入库前加密(L-5)。这里走的是 Updates(map),绕开了模型钩子,所以显式做。
	// 加密失败就不写:宁可这一次绑定失败让人重试,也不要把明文种子落进库里。
	enc, err := crypto.EncryptSecret(secret)
	if err != nil {
		return err
	}
	return r.db.Model(&model.User{}).Where("id = ?", userID).
		Updates(map[string]any{"mfa_enabled": enabled, "mfa_secret": enc, "mfa_last_ctr": 0}).Error
}

// UpdateUserMFAIf 是 UpdateUserMFA 的比较后写入版:只有库里的 MFA 状态仍是 (wantEnabled, wantSecret)
// —— 也就是调用方做判断时看到的那一份 —— 才写。返回是否写成。
//
// 本人自助领取 / 启用 / 停用 MFA 用它。这三条路都是"先按请求开始时读到的状态判断,再整体覆盖",
// 管理员在这中间替他绑定的新因子会被冲掉(领取密钥甚至会把刚启用的 MFA 重新关掉)。密文是随机
// 加密的,不能拿明文去 WHERE 比,所以先读出当前这一行的密文、解开比对,再用这段密文做条件更新:
// 读与写之间有人改过,密文或开关就变了,更新落空。
func (r *Repo) UpdateUserMFAIf(userID int64, wantEnabled bool, wantSecret string, enabled bool, secret string) (bool, error) {
	var cur struct {
		MFAEnabled bool
		MFASecret  *string // 列可为 NULL(0001 起就是)
	}
	if err := r.db.Table("tbl_user").Select("mfa_enabled", "mfa_secret").Where("id = ?", userID).Take(&cur).Error; err != nil {
		return false, err
	}
	plain := ""
	if cur.MFASecret != nil && *cur.MFASecret != "" {
		// 解不开按"没有密钥"算,与 User.AfterFind 同一口径:调用方看到的也是空。
		if p, err := crypto.DecryptSecret(*cur.MFASecret); err == nil {
			plain = p
		}
	}
	if cur.MFAEnabled != wantEnabled || plain != wantSecret {
		return false, nil
	}
	enc, err := crypto.EncryptSecret(secret)
	if err != nil {
		return false, err
	}
	q := r.db.Model(&model.User{}).Where("id = ? AND mfa_enabled = ?", userID, cur.MFAEnabled)
	if cur.MFASecret == nil {
		q = q.Where("mfa_secret IS NULL")
	} else {
		q = q.Where("mfa_secret = ?", *cur.MFASecret)
	}
	res := q.Updates(map[string]any{"mfa_enabled": enabled, "mfa_secret": enc, "mfa_last_ctr": 0})
	return res.RowsAffected == 1, res.Error
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

// CountRoleUsers:以这个角色为主角色、或是它成员的用户数(去重)。
func (r *Repo) CountRoleUsers(roleID int64) (int64, error) {
	var n int64
	err := r.db.Model(&model.User{}).
		Where("role_id = ? OR id IN (?)", roleID, r.db.Model(&model.RoleMember{}).Select("user_id").Where("role_id = ?", roleID)).
		Count(&n).Error
	return n, err
}

// StageConfigsForRoleCheck:所有流程模板的阶段,以及未结束发布单的阶段(删角色前查它是否还被引用)。
func (r *Repo) StageConfigsForRoleCheck() (pipe []model.PipelineStage, live []model.ReleaseStage, err error) {
	if err = r.db.Find(&pipe).Error; err != nil {
		return nil, nil, err
	}
	err = r.db.Where("status IN ?", []string{model.RunPending, model.RunWaiting, model.RunRunning}).Find(&live).Error
	return pipe, live, err
}

// DeleteRoleCascade 删一个角色,连同它的菜单、能力矩阵、实例标签、成员关系(同一个事务)。
// 调用方先确认它没有成员、没被流程引用(见 service.DeleteRole)。
func (r *Repo) DeleteRoleCascade(roleID int64) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		for _, m := range []any{&model.RoleMenu{}, &model.RoleCapability{}, &model.RoleTag{}, &model.RoleMember{}} {
			if err := tx.Where("role_id = ?", roleID).Delete(m).Error; err != nil {
				return err
			}
		}
		return tx.Delete(&model.Role{}, roleID).Error
	})
}

// CreateRoleDenyAll 建一个新角色,并在同一个事务里给它每个分层 × 每项能力都写上 deny。
//
// 必须写:能力矩阵里查不到行的格子按 allow 算(见 CapabilityLevel),一个空的新角色会在所有分层上
// 全放行。新建分层时从模板分层复制矩阵行(见 CreateEnvTier),这些 deny 行也会被带过去。
// 实例范围同理:没有标签的角色不限实例,所以写一行占位标签(model.RoleNoInstanceTag)。
func (r *Repo) CreateRoleDenyAll(role *model.Role, tiers, caps []string) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(role).Error; err != nil {
			return err
		}
		for _, tier := range tiers {
			for _, c := range caps {
				if err := tx.Create(&model.RoleCapability{RoleID: role.ID, Capability: c, TierCode: tier, Level: model.LevelDeny}).Error; err != nil {
					return err
				}
			}
		}
		return tx.Create(&model.RoleTag{RoleID: role.ID, Tag: model.RoleNoInstanceTag}).Error
	})
}

// CustomRolesWithoutTags 列出没有任何标签(= 不限实例)的自定义角色。这些角色建于新建角色会写
// 占位标签之前,分不清是有意不限还是漏配,启动时列出来让管理员确认。
func (r *Repo) CustomRolesWithoutTags() ([]model.Role, error) {
	var roles []model.Role
	err := r.db.Where("code LIKE ? ESCAPE '!' AND id NOT IN (?)", "r!_%", r.db.Model(&model.RoleTag{}).Select("role_id")).
		Order("id asc").Find(&roles).Error
	return roles, err
}

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

// CapabilityLevel implements gateway.Store: role × capability × TIER -> level
// (default allow). `tier` is an EnvTier.Code — passing an environment code finds
// no row, and no row reads as allow.
func (r *Repo) CapabilityLevel(roleID int64, capability, tier string) (string, error) {
	var row model.RoleCapability
	// Case-insensitive on capability/tier so matching is consistent across the
	// PostgreSQL and MySQL stores regardless of stored/input casing.
	err := r.db.Where("role_id = ? AND LOWER(capability) = ? AND LOWER(tier_code) = ?",
		roleID, strings.ToLower(strings.TrimSpace(capability)), strings.ToLower(strings.TrimSpace(tier))).First(&row).Error
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

// MatrixForRole returns capability -> tier -> level.
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
		out[c.Capability][c.TierCode] = c.Level
	}
	return out, nil
}

func (r *Repo) SetMatrix(roleID int64, matrix map[string]map[string]string) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		for cap, tiers := range matrix {
			for tier, level := range tiers {
				row := model.RoleCapability{RoleID: roleID, Capability: cap, TierCode: tier, Level: level}
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
	ids, _ := r.EffectiveRoles(u)
	return ids
}

// EffectiveRoles 是 EffectiveRoleIDs 再带上「读不出来」:出错时的列表仍是 EffectiveRoleIDs 会给的那一份,
// 可能缺了成员关系,也可能留着已经不存在的角色。
func (r *Repo) EffectiveRoles(u *model.User) ([]int64, error) {
	if u == nil {
		return nil, nil
	}
	seen := map[int64]bool{}
	out := []int64{}
	if u.RoleID != 0 {
		seen[u.RoleID] = true
		out = append(out, u.RoleID)
	}
	ids, merr := r.RoleIDsOfUser(u.ID)
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	out, err := r.existingRoles(out)
	return out, errors.Join(merr, err)
}

// existingRoles drops ids that name no role.
//
// 这是兜底,不是入口 —— 入口(加成员、改主角色)已经各自校验了。但"查无规则行 =
// 放行"是这套能力矩阵的基础语义,而一个**不存在的角色**永远查无规则行:它会让持有
// 它的用户每一格都读成放行、标签也不受限。历史数据里若已经躺着这样一个 id,光补
// 入口是追不回来的,所以在权限计算的入口再滤一次。
//
// 查询失败时保留原样:一次数据库抖动不该让所有人瞬间失去全部角色。这个方向是安全
// 的 —— 保留的是**真实存在过的** id 集合,而不是把未知当成放行。
func (r *Repo) existingRoles(ids []int64) ([]int64, error) {
	if len(ids) == 0 {
		return ids, nil
	}
	var found []int64
	if err := r.db.Model(&model.Role{}).Where("id IN ?", ids).Pluck("id", &found).Error; err != nil {
		return ids, err
	}
	if len(found) == len(ids) {
		return ids, nil // 常态:一个都没少,不必重建切片
	}
	ok := map[int64]bool{}
	for _, id := range found {
		ok[id] = true
	}
	out := ids[:0:0]
	for _, id := range ids {
		if ok[id] {
			out = append(out, id)
		}
	}
	slog.Warn("用户持有不存在的角色 id,已从权限计算中剔除", "kept", out, "asked", ids)
	return out, nil
}

// SetUserRoles replaces a user's role membership set atomically. The first id is
// also written to tbl_user.role_id as the primary role (drives display/JWT).
// ClearUserRoles strips every role from a user: the membership rows and the
// primary role both go, in one transaction.
//
// SetUserRoles(id, nil) is NOT the same thing — it deletes the membership rows
// but leaves tbl_user.role_id standing, so the account keeps the role that
// actually drives its permissions.
//
// role_id is set to 0 rather than NULL: the column is NOT NULL, and
// EffectiveRoleIDs already skips a zero id, so the account resolves to an empty
// role set. What an empty set MEANS is decided in gateway.capabilityLevelUnion,
// and it means deny — see the note there, because it did not always.
func (r *Repo) ClearUserRoles(userID int64) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("user_id = ?", userID).Delete(&model.RoleMember{}).Error; err != nil {
			return err
		}
		return tx.Model(&model.User{}).Where("id = ?", userID).Update("role_id", 0).Error
	})
}

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

// ActiveAdminIDs lists the users who can still administer the platform: kind
// human, status active, and holding the admin role through ANY of their roles
// (primary or membership — union semantics, same as the AdminOnly guard).
//
// 服务账号不算在内:它进不了控制台(Login 直接拒绝 kind=service),所以它救不了
// "最后一个管理员被停用"的场面。把它算进去,等于用一个永远打不开的门证明屋子有出口。
func (r *Repo) ActiveAdminIDs() []int64 {
	var role model.Role
	if err := r.db.Where("code = ?", "admin").First(&role).Error; err != nil {
		return nil
	}
	var us []model.User
	if err := r.db.Where("status = ? AND kind = ?", "active", model.UserKindHuman).Order("id asc").Find(&us).Error; err != nil {
		return nil
	}
	// 一次取齐管理员角色的成员,不再逐人查角色(黑盒 2026-10 工单 05 第 4 条:用户列表每行都
	// 调这里,逐人查让 GET /users 成了 N² 次查询)。admin 角色本身存在,所以"主角色或成员
	// 关系之一是 admin"与 EffectiveRoleIDs 含 admin 等价。
	var memberIDs []int64
	if err := r.db.Model(&model.RoleMember{}).Where("role_id = ?", role.ID).Pluck("user_id", &memberIDs).Error; err != nil {
		return nil
	}
	isMember := make(map[int64]bool, len(memberIDs))
	for _, id := range memberIDs {
		isMember[id] = true
	}
	out := []int64{}
	for i := range us {
		if us[i].RoleID == role.ID || isMember[us[i].ID] {
			out = append(out, us[i].ID)
		}
	}
	return out
}

// RoleMemberships 一次取出全部成员关系:user id → 他作为成员持有的角色 id(按 role_id 升序,
// 与 RoleIDsOfUser 同序)。给需要逐人展示角色的列表用,免得每行各查一遍。
func (r *Repo) RoleMemberships() (map[int64][]int64, error) {
	var rows []model.RoleMember
	if err := r.db.Order("user_id asc, role_id asc").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := map[int64][]int64{}
	for _, m := range rows {
		out[m.UserID] = append(out[m.UserID], m.RoleID)
	}
	return out, nil
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
// permissive level per capability×tier cell (union semantics for the /me view).
func (r *Repo) MatrixForRoles(ids []int64) (map[string]map[string]string, error) {
	out := map[string]map[string]string{}
	for _, id := range ids {
		m, err := r.MatrixForRole(id)
		if err != nil {
			return nil, err
		}
		for cap, tiers := range m {
			if out[cap] == nil {
				out[cap] = map[string]string{}
			}
			for tier, level := range tiers {
				if cur, ok := out[cap][tier]; !ok || levelRank[level] < levelRank[cur] {
					out[cap][tier] = level
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
		// The intended gate value has to be read BEFORE the insert. GORM omits a
		// field that is both zero-valued and `default`-tagged, then writes the
		// applied default back into the struct — so a tier created with the
		// no-WHERE gate switched OFF is inserted without the column, the database
		// default puts it back ON, and t itself now says ON too. The switch looked
		// saved and was not. Capture, insert, then name the column in an UPDATE.
		// 脱敏与数据查询导出限制两个开关同理(2026-10-29)。
		want, wantMask, wantExport := t.StrictNoWhere, t.MaskSensitive, t.BizExportRestrict
		if err := tx.Create(t).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.EnvTier{}).Where("code = ?", t.Code).
			Updates(map[string]any{"strict_nowhere": want, "mask_sensitive": wantMask, "biz_export_restrict": wantExport}).Error; err != nil {
			return err
		}
		t.StrictNoWhere, t.MaskSensitive, t.BizExportRestrict = want, wantMask, wantExport

		var caps []model.RoleCapability
		if err := tx.Where("tier_code = ?", templateCode).Find(&caps).Error; err != nil {
			return err
		}
		for _, row := range caps {
			row.TierCode = t.Code
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		}

		var cmds []model.RiskCommand
		if err := tx.Where("tier_code = ?", templateCode).Find(&cmds).Error; err != nil {
			return err
		}
		for _, row := range cmds {
			row.TierCode = t.Code
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
		if err := tx.Where("tier_code = ?", code).Delete(&model.RoleCapability{}).Error; err != nil {
			return err
		}
		if err := tx.Where("tier_code = ?", code).Delete(&model.RiskCommand{}).Error; err != nil {
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
		// Webhook 的推送环境里有它的话一并换成接手的环境:实例搬过去了,事件上的环境也跟着变,
		// 留着旧代码等于这批实例的事件从此**静默地**不推。
		var wh model.WebhookConfig
		if err := tx.Order("id").Limit(1).Find(&wh).Error; err != nil {
			return err
		}
		if wh.ID != 0 && wh.Envs != "" {
			if next := replaceEnvCode(wh.Envs, code, moveTo); next != wh.Envs {
				if err := tx.Model(&wh).Update("envs", next).Error; err != nil {
					return err
				}
			}
		}
		return tx.Where("code = ?", code).Delete(&model.Environment{}).Error
	})
}

// replaceEnvCode 把逗号分隔清单里的 from 换成 to,已有 to 的就只去掉 from。
func replaceEnvCode(list, from, to string) string {
	out := []string{}
	has := map[string]bool{}
	for _, e := range strings.Split(list, ",") {
		if e == from {
			e = to
		}
		if e == "" || has[e] {
			continue
		}
		has[e] = true
		out = append(out, e)
	}
	return strings.Join(out, ",")
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

// UpdateConnection 保存一条连接。
//
// **跳过巡检那四列**:它们由 SaveConnCheck 在另一条路径上写(定时巡检、批量巡检),
// 而这里的 c 是请求开头加载的快照 —— 全行 Save 会把快照里的旧结论写回去,把并发那次
// 巡检刚记下的"连不上"悄悄抹掉。
func (r *Repo) UpdateConnection(c *model.Connection) error {
	return r.db.Omit("last_check_at", "last_check_ok", "last_check_err", "last_check_kind").Save(c).Error
}

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

// StrictNoWhere implements gateway.Store: does this tier block a DELETE /
// UPDATE with no WHERE?
//
// A tier that is not there is NOT "no rule, so allowed". The other two layers
// can read a missing row as allow because a missing row means nobody wrote a
// restriction; here the row IS the tier, and its absence means the caller handed
// us a code that does not name anything. That is the same unresolvable-tier
// situation gateway.Unavailable exists for, so it comes back as an error and the
// engine refuses (ED3).
func (r *Repo) StrictNoWhere(tier string) (bool, error) {
	var t model.EnvTier
	if err := r.db.Select("strict_nowhere").First(&t, "code = ?", tier).Error; err != nil {
		return false, err
	}
	return t.StrictNoWhere, nil
}

// RiskCommandsGrouped returns command -> TIER -> level, preserving insertion order of commands.
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
		grouped[c.Command][c.TierCode] = c.Level
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
// the caller's level where one was supplied and `off` on tiers that have no row yet.
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
				if err := tx.Save(&model.RiskCommand{Command: command, TierCode: t.Code, Level: lvl}).Error; err != nil {
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
				Where("command = ? AND tier_code = ?", command, t.Code).Count(&existing).Error; err != nil {
				return err
			}
			if existing > 0 {
				continue
			}
			if err := tx.Save(&model.RiskCommand{Command: command, TierCode: t.Code, Level: defaultRiskLevel(t)}).Error; err != nil {
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
// (2026-10 黑盒测试曾把它改成一律 off,已改回:那只是"没提示"的体验问题,不值得削弱生产防护。)
func defaultRiskLevel(t model.EnvTier) string {
	if t.ScanBaseline || t.RequireMFA {
		return model.RiskHigh
	}
	return model.RiskOff
}

// RiskCommandExists 报告字典里有没有这条命令(任一分层有行即算)。
func (r *Repo) RiskCommandExists(command string) bool {
	var n int64
	r.db.Model(&model.RiskCommand{}).Where("command = ?", strings.ToUpper(strings.TrimSpace(command))).Count(&n)
	return n > 0
}

func (r *Repo) PatchRiskLevel(command, tier, level string) error {
	// normalize like UpsertRiskCommand so keys stay canonical (command upper, tier lower)
	command = strings.ToUpper(strings.TrimSpace(command))
	tier = strings.ToLower(strings.TrimSpace(tier))
	row := model.RiskCommand{Command: command, TierCode: tier, Level: level}
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
//
// approvalScope builds the visibility predicate for a caller: tickets they raised,
// plus tickets they sit on the approval chain of. "mine" narrows to the latter —
// the queue awaiting this person.
//
// The chain membership is a SQL subquery rather than a Pluck into Go: the old
// version loaded every approval id the caller had ever been an approver on and
// pasted them into an IN(...) list, which grows with history and eventually
// exceeds the driver's parameter limit (ED13).
// approvalScope —— 这个人能看见哪些工单。
//
// 三类:自己发起的、自己在审批链上的,以及**已批准待执行、且落在他够得到的实例上的**。
//
// 第三类是"代跑"必须的:执行权放开成"谁能在这台实例上跑,谁就能执行"之后,一个看
// 不见那张单的人是没法接手的。它在 SQL 里按 connection_id 收口,而不是取回来再在
// 应用层筛 —— 后者会让分页的总数和实际行数对不上,而那种列表会对着第三页的工单说
// "没有"。
//
// runnableConnIDs 由调用方按标签授权算好(service.canAccessConn 是那条规则的唯一
// 出处)。为空就不加这一条:够不到任何实例的人,这一类里本来也没有东西给他看。
// ApprovalScopeGlobal 是平台管理员的「全部」:所有工单,不按人收窄。它不是客户端能传的值 ——
// handler 只在调用者是平台管理员、请求 scope=all 时换成它。管理总览的定位是"一页看全局",
// 不在审批链上的管理员此前在那里看到的待审批是 0(2026-10 黑盒,工单 01 第 7 条)。
// 只是**看得见**:能不能批仍按审批链(service.DecideBlockFor)。
const ApprovalScopeGlobal = "global"

func (r *Repo) approvalScope(scope string, userID int64, runnableConnIDs []int64) *gorm.DB {
	if scope == ApprovalScopeGlobal {
		return r.db.Model(&model.Approval{}) // 平台管理员的「全部」:不按人收窄(见 ApprovalScopeGlobal)
	}
	sub := r.db.Model(&model.ApprovalStep{}).Select("approval_id").Where("approver_id = ?", userID)
	if scope == "mine" {
		return r.db.Model(&model.Approval{}).Where("id IN (?)", sub)
	}
	q := r.db.Model(&model.Approval{})
	if len(runnableConnIDs) == 0 {
		return q.Where("initiator_id = ? OR id IN (?)", userID, sub)
	}
	return q.Where(
		"initiator_id = ? OR id IN (?) OR (status = ? AND executed_at IS NULL AND connection_id IN ?)",
		userID, sub, model.StatusApproved, runnableConnIDs)
}

// applyApprovalType 把"只看这一类工单"加到查询上。
//
// 类型不是表上的一列,而是三个外键推出来的(见 model.ApprovalType)—— 所以这里写的是
// 同一条推断的 SQL 形态。两处必须一致:界面上那枚类型标签按 model.ApprovalType 画,
// 而筛选按这里的 WHERE 查,不一致的表现是"筛出来的行上挂着别的标签"。
//
// 认不出的类型**不加条件**。这里之所以敢这样,是因为调用方(handler)已经把取值挡在
// 白名单里了;真让一个拼错的值原样进 WHERE,查出来会是空列表 —— 而空列表和"确实没有"
// 长得一模一样,调用方看不出自己写错了。
func applyApprovalType(q *gorm.DB, typ string) *gorm.DB {
	switch typ {
	case model.ApTypeCommand:
		return q.Where("release_id = 0 AND window_id = 0 AND export_job_id = 0 AND osc_job_id = 0")
	case model.ApTypeRelease:
		return q.Where("release_id > 0")
	case model.ApTypeWindow:
		return q.Where("window_id > 0")
	case model.ApTypeExport:
		return q.Where("export_job_id > 0")
	case model.ApTypeOSC:
		return q.Where("osc_job_id > 0")
	}
	return q
}

// ListApprovalsPaged returns one page of approvals the caller may see (newest
// first) plus the total matching that same visibility.
//
// apNo, when set, looks a single ticket up by number REGARDLESS of paging — the
// audit log links tickets by number, and such a ticket may sit on any page. The
// visibility predicate still applies, so a number cannot be used to read someone
// else's ticket.
func (r *Repo) ListApprovalsPaged(scope string, userID int64, apNo, status, typ, q string, offset, limit int, runnableConnIDs []int64) ([]model.Approval, int64, error) {
	count := applyApprovalType(r.approvalScope(scope, userID, runnableConnIDs), typ)
	rows := applyApprovalType(r.approvalScope(scope, userID, runnableConnIDs), typ).Order("id desc")
	if apNo != "" {
		count = count.Where("ap_no = ?", apNo)
		rows = rows.Where("ap_no = ?", apNo)
	}
	// status 让调用方按状态取,而不是取一页回去自己筛。
	//
	// 这是"待执行"那张卡片需要的:通过之后命令并没有跑,而**通过了的工单不会过期**
	// (超时清扫只作废 pending,见 sweepStaleApprovals),所以一张等着执行的单子可以
	// 停很久,早就被新工单挤出了任何一页。客户端筛就会漏报,而这张卡片漏报等于没有。
	if status != "" {
		count = count.Where("status = ?", status)
		rows = rows.Where("status = ?", status)
	}
	// q 是控制台搜索框,匹配单号 / 实例 / 库 / 命令 / 发起人。
	//
	// 它必须在**这里**筛,不能让前端拿一页回去自己过滤:列表是分页的,只搜当前页
	// 的搜索框会对一张躺在第三页的工单回答"没有" —— 而人会据此认为它不存在。
	// 一个会说谎的搜索框比没有搜索框糟。
	//
	// LIKE 里的 % 和 _ 先转义,否则用户输入的下划线(表名里到处都是:t_order)会变成
	// 通配符,搜 t_order 连 tXorder 一起命中。
	if q != "" {
		pat := "%" + escapeLike(q) + "%"
		// 转义字符与 ESCAPE 子句的选择见 likeEscape:这里曾经写的是 ESCAPE '\',
		// MySQL 上是语法错误 —— 生产的搜索框因此一直是坏的。
		const like = likeClause
		const where = "ap_no" + like +
			" OR instance" + like +
			" OR db_name" + like +
			" OR command" + like +
			" OR initiator" + like
		count = count.Where(where, pat, pat, pat, pat, pat)
		rows = rows.Where(where, pat, pat, pat, pat, pat)
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

// escapeLike neutralises the LIKE wildcards in user input so a search term is
// matched literally. Without it "t_order" also matches "tXorder", and a single
// "%" would match every row — a search box that silently means something other
// than what was typed.
// likeEscape 是 LIKE 模式里的转义字符。**不用反斜杠**:MySQL 把字符串字面量里的反斜杠
// 当转义符,ESCAPE '\' 发过去被读成"一个转义了的引号",字符串没结束,整句语法错误 ——
// 而 PostgreSQL 没有这套语义,同一句在那边跑得好好的。换一个各个存储库都不特殊对待的
// 字符,各处就真的是同一句 SQL 了。
const likeEscape = "!"

// escapeLike 把用户输入变成可以安全放进 LIKE 的字面量:% 与 _ 不再是通配符,
// 转义字符自己也要先加倍。用法:pat := "%" + escapeLike(q) + "%",配合 likeClause。
func escapeLike(s string) string {
	// 转义字符自己先加倍,而且必须排在最前:NewReplacer 从左到右扫一遍、不回头,
	// 后面两条产生的 ! 不会被这一条再处理一次。
	r := strings.NewReplacer(likeEscape, likeEscape+likeEscape, "%", likeEscape+"%", "_", likeEscape+"_")
	return r.Replace(s)
}

// likeClause 是 " LIKE ? ESCAPE '!'":ESCAPE 必须显式写,各存储库默认的转义字符不同
// (PostgreSQL 默认是反斜杠),不写它,上面转义出来的 !_ 会被当成两个普通字符去匹配。
const likeClause = " LIKE ? ESCAPE '" + likeEscape + "'"

// ListApprovals returns every approval the caller may see. Retained for callers
// that genuinely need the whole set (the approval-chain sweep); the console uses
// the paged form.
func (r *Repo) ListApprovals(scope string, userID int64) ([]model.Approval, error) {
	// 这条路只给审批链清扫用,它关心的是自己发起/自己要批的那些 —— 代跑可见性
	// (第三类)与它无关,所以不传 runnableConnIDs。
	as, _, err := r.ListApprovalsPaged(scope, userID, "", "", "", "", 0, 0, nil)
	return as, err
}

func (r *Repo) GetApproval(id int64) (*model.Approval, error) {
	var a model.Approval
	if err := r.db.First(&a, id).Error; err != nil {
		return nil, err
	}
	return &a, nil
}

// GetApprovalByApNo finds an approval by its human ApNo (e.g. "AP-000123").
func (r *Repo) GetApprovalByApNo(apNo string) (*model.Approval, error) {
	var a model.Approval
	if err := r.db.Where("ap_no = ?", apNo).First(&a).Error; err != nil {
		return nil, err
	}
	return &a, nil
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

// ClaimApprovalDecision 是一个人的决定抢占一张待审批的单:pending → approved | rejected,连同决定的
// 渠道与拒绝原因写进**同一条** UPDATE。返回是否抢到(RowsAffected == 1)。
//
// 分成两条写不行:先抢状态再补渠道,抢输的一方补的那一笔会盖掉赢家的值,或者赢家补之前崩了,
// 留下一张没有渠道的已决定单。
func (r *Repo) ClaimApprovalDecision(id int64, to, via, rejectReason string) (bool, error) {
	res := r.db.Model(&model.Approval{}).
		Where("id = ? AND status = ?", id, model.StatusPending).
		Updates(map[string]any{"status": to, "decided_via": via, "reject_reason": rejectReason})
	return res.RowsAffected == 1, res.Error
}

// ClaimEscalation atomically marks an approval as escalated, but only if it was
// not already. Returns true iff this caller flipped it (RowsAffected == 1), so a
// timeout sweep escalates each overdue ticket exactly once (R13).
//
// 还要求单子仍是待审批:扫描先列出再逐张抢占,中间被决定掉的单不能再喊人来处理。
func (r *Repo) ClaimEscalation(id int64) (bool, error) {
	res := r.db.Model(&model.Approval{}).
		Where("id = ? AND status = ? AND escalated = ?", id, model.StatusPending, false).
		Update("escalated", true)
	return res.RowsAffected == 1, res.Error
}

// SetApprovalResult records the post-decision execution result + decided time.
func (r *Repo) SetApprovalResult(id int64, output string, outputRef *model.RuleRef, rows int, at time.Time) error {
	return r.db.Model(&model.Approval{}).Where("id = ?", id).Updates(map[string]any{
		"result": output, "result_ref": apimsg.RefJSON(outputRef), "result_rows": rows, "decided_at": at,
	}).Error
}

// DecideActiveStep records a decision on the currently-active chain step of an
// approval: it sets the step status (approved|rejected) and stamps actedAt.
// A single decision finalizes the whole ticket (approve-1 policy), so any later
// steps that never acted are left as-is rather than promoted to active.
func (r *Repo) DecideActiveStep(approvalID int64, status string, actedAt time.Time) error {
	return decideActiveStep(r.db, approvalID, status, actedAt)
}

func decideActiveStep(db *gorm.DB, approvalID int64, status string, actedAt time.Time) error {
	var step model.ApprovalStep
	err := db.Where("approval_id = ? AND status = ?", approvalID, "active").
		Order("step_order asc").First(&step).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil // no active step (e.g. chainless approval) — nothing to record
		}
		return err
	}
	return db.Model(&model.ApprovalStep{}).Where("id = ?", step.ID).
		Updates(map[string]any{"status": status, "acted_at": actedAt}).Error
}

// DecideStepBy 把一次审批决定记在**实际审批人**那一步上。
//
// 链是"任一人通过即生效"(DecideBlockFor 只问在不在链上),而 active 永远是第 1 步。
// 只记 active 那一步的后果:链上第 2 步的人批了,steps 显示成"第 1 步那个人: approved",
// 审计里的 operator 却是第 2 步的人 —— 两份记录对不上(2026-10 黑盒,工单 01 第 6 条)。
//
// 实际审批人那一步记 status / acted_at;原先 active 的那一步(没人动手)退回 waiting。
// 审批人不在链上(或 approverID 为 0)时退回旧行为:记在 active 那一步。
func (r *Repo) DecideStepBy(approvalID, approverID int64, status string, actedAt time.Time) error {
	return decideStepBy(r.db, approvalID, approverID, status, actedAt)
}

func decideStepBy(db *gorm.DB, approvalID, approverID int64, status string, actedAt time.Time) error {
	var step model.ApprovalStep
	err := db.Where("approval_id = ? AND approver_id = ?", approvalID, approverID).
		Order("step_order asc").First(&step).Error
	if approverID <= 0 || errors.Is(err, gorm.ErrRecordNotFound) {
		return decideActiveStep(db, approvalID, status, actedAt)
	}
	if err != nil {
		return err
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.ApprovalStep{}).
			Where("approval_id = ? AND status = ? AND id <> ?", approvalID, "active", step.ID).
			Update("status", "waiting").Error; err != nil {
			return err
		}
		return tx.Model(&model.ApprovalStep{}).Where("id = ?", step.ID).
			Updates(map[string]any{"status": status, "acted_at": actedAt}).Error
	})
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

// ---- 校验链用的四个读法(service.VerifyAuditChain)----
//
// 它们都**不带 actor 收窄**:链是全表一条,少看几行就验不成 —— 谁能发起校验由上层
// 判(限监督角色),而不是靠让他只看见自己那部分行。

// CountAudit 是整张表的行数。
func (r *Repo) CountAudit() (int64, error) {
	var n int64
	err := r.db.Model(&model.AuditLog{}).Count(&n).Error
	return n, err
}

// AuditRowsAsc 取 id 大于 afterID 的若干行,按 id 升序 —— 也就是链的方向。
func (r *Repo) AuditRowsAsc(afterID int64, limit int) ([]model.AuditLog, error) {
	var rows []model.AuditLog
	err := r.db.Where("id > ?", afterID).Order("id asc").Limit(limit).Find(&rows).Error
	return rows, err
}

// AuditIDAtOffsetFromNewest 从最新往回数第 offset+1 行的 id(offset=0 就是最新那一行)。
// 只验最近 N 行时,用它定出窗口的起点。数不到(表里没那么多行)返回 0。
func (r *Repo) AuditIDAtOffsetFromNewest(offset int) (int64, error) {
	var a model.AuditLog
	err := r.db.Order("id desc").Offset(offset).Limit(1).Take(&a).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, nil
	}
	return a.ID, err
}

// AuditRowBefore 取 id 小于 id 的最后一行 —— 窗口第一行要接住的就是它。没有返回 nil。
func (r *Repo) AuditRowBefore(id int64) (*model.AuditLog, error) {
	var a model.AuditLog
	err := r.db.Where("id < ?", id).Order("id desc").Take(&a).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// ListAudit lists audit rows; actorID > 0 restricts to that actor's own commands.
func (r *Repo) ListAudit(f AuditFilter, limit int) ([]model.AuditLog, error) {
	var rows []model.AuditLog
	q := r.auditQuery(f).Order("occurred_at desc, id desc")
	if limit > 0 {
		q = q.Limit(limit)
	}
	err := q.Find(&rows).Error
	return rows, err
}

// EachAuditBatch 按与列表相同的顺序(时间倒序、id 倒序)分批读出全部命中的审计行,每批交给 fn。
// 用"上一批最后一行"作游标(键集分页),不用 OFFSET —— 翻得越深越慢;也不长时间占着一个游标
// (占着存储库的一条连接)。导出用它流式写出,不把整表读进内存(2026-10 安全审计 M9)。
func (r *Repo) EachAuditBatch(f AuditFilter, batch int, fn func([]model.AuditLog) error) error {
	var lastAt time.Time
	var lastID int64
	for first := true; ; first = false {
		q := r.auditQuery(f)
		if !first {
			q = q.Where("occurred_at < ? OR (occurred_at = ? AND id < ?)", lastAt, lastAt, lastID)
		}
		var rows []model.AuditLog
		if err := q.Order("occurred_at desc, id desc").Limit(batch).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		if err := fn(rows); err != nil {
			return err
		}
		if len(rows) < batch {
			return nil
		}
		last := rows[len(rows)-1]
		lastAt, lastID = last.OccurredAt, last.ID
	}
}

// ListAuditPaged returns one page of audit rows (newest first) plus the total
// row count matching the same filters, for paginated display.
func (r *Repo) ListAuditPaged(f AuditFilter, offset, limit int) ([]model.AuditLog, int64, error) {
	var total int64
	if err := r.auditQuery(f).Model(&model.AuditLog{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []model.AuditLog
	q := r.auditQuery(f).Order("occurred_at desc, id desc")
	if limit > 0 {
		q = q.Limit(limit).Offset(offset)
	}
	err := q.Find(&rows).Error
	return rows, total, err
}

// ListAuditGroupedByApproval 与 ListAuditPaged 相同,只是同一张审批单的行合成一个条目:
// 条目 = 没有审批单号的行 + 每张审批单里 id 最大(最新)的那一行。筛选条件对"哪些行参与分组"
// 同样生效;分页与总数按条目算。
func (r *Repo) ListAuditGroupedByApproval(f AuditFilter, offset, limit int) ([]model.AuditLog, int64, error) {
	// 每张单最新的一行算成派生表再 LEFT JOIN 回来,而不是 id IN (子查询):后者在 MySQL 上是
	// DEPENDENT SUBQUERY,可能按外层每一行重算一遍,审计表一大就越翻越慢;派生表只算一次。
	// 派生表只有一列 mid,外层那些不带表名的列(approval_no、id…)不会有歧义。
	entries := func() *gorm.DB {
		latest := r.auditQuery(f).Select("MAX(id) AS mid").Where("approval_no <> ''").Group("approval_no")
		return r.auditQuery(f).
			Joins("LEFT JOIN (?) g ON tbl_audit_log.id = g.mid", latest).
			Where("(approval_no IS NULL OR approval_no = '' OR g.mid IS NOT NULL)")
	}
	var total int64
	if err := entries().Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []model.AuditLog
	q := entries().Select("tbl_audit_log.*").Order("occurred_at desc, id desc") // 计数不能带这个 Select(COUNT(t.*) 不合法)
	if limit > 0 {
		q = q.Limit(limit).Offset(offset)
	}
	err := q.Find(&rows).Error
	return rows, total, err
}

// AuditApprovalCounts 数每张审批单在审计里共有几行(actorID > 0 时只数这个人的行 —— 与
// AuditTrail 同一个可见范围,列表上标的条数与点开看到的一致)。
func (r *Repo) AuditApprovalCounts(actorID int64, apNos []string) (map[string]int64, error) {
	out := map[string]int64{}
	if len(apNos) == 0 {
		return out, nil
	}
	var rows []struct {
		ApprovalNo string
		N          int64
	}
	q := r.db.Model(&model.AuditLog{}).Select("approval_no, COUNT(*) AS n").Where("approval_no IN ?", apNos)
	if actorID > 0 {
		q = q.Where("actor_id = ?", actorID)
	}
	if err := q.Group("approval_no").Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, x := range rows {
		out[x.ApprovalNo] = x.N
	}
	return out, nil
}

// AuditTrail 一张审批单的全部审计行,按时间先后(actorID > 0 时只含这个人的行)。
func (r *Repo) AuditTrail(actorID int64, apNo string) ([]model.AuditLog, error) {
	var rows []model.AuditLog
	q := r.db.Where("approval_no = ?", apNo)
	if actorID > 0 {
		q = q.Where("actor_id = ?", actorID)
	}
	err := q.Order("occurred_at asc, id asc").Limit(500).Find(&rows).Error
	return rows, err
}

// AuditInstances 列出这个人在审计里**见得到的**实例名,去重、按字母序。
//
// 筛选下拉不该照着连接表来列:那张表上有他够不到的实例,而一个筛出来永远是空的选项,
// 读起来像"这台机器上什么都没发生过"。可见性用的是同一个 actorID 口径。
func (r *Repo) AuditInstances(actorID int64) ([]string, error) {
	var out []string
	q := r.db.Model(&model.AuditLog{}).Distinct().Where("instance <> ''")
	if actorID > 0 {
		q = q.Where("actor_id = ?", actorID)
	}
	err := q.Order("instance asc").Pluck("instance", &out).Error
	return out, err
}

// AuditFilter 是审计列表 / 导出 / 计数共用的一组筛选条件。
//
// 收成一个结构体,是因为参数已经排到第八个:再往下加,调用点就得靠数逗号来对齐,而
// 「风险」和「事件类型」都是 string、挨在一起传错了编译器也不会说话。
type AuditFilter struct {
	// ActorID > 0 = 只看这个人写下的行。可见性收窄,由 service.auditActor 决定。
	ActorID int64
	// OnlyOwn:调用方要「我的操作记录」—— 哪怕他看得见全部(管理员 / 审计员),也只要自己的。
	// 只在 service.scopedAuditFilter 里读,仓储层不看它(那时已折算进 ActorID)。
	OnlyOwn bool
	// Risk:"" / "all" 都表示不筛。
	Risk string
	// Events / Actions 是**多选**:空切片 = 不筛。取值已由 handler 挡在白名单里
	// (model.IsAuditEventType / IsAuditDataAction)。
	Events  []string
	Actions []string
	// Sources 按入口筛(迁移 0063),多选。"unknown" 指 0063 之前写下、没有来源的行
	// (entry_source 为空) —— 旧记录也得能单独筛出来,否则它们只在"不筛"时露面。
	Sources []string
	// Instance 精确匹配实例名(下拉选的是审计里真实出现过的那些,见 AuditInstances)。
	Instance string
	// Keyword 模糊匹配**操作人姓名、邮箱、来源 IP** —— 这几样是同一个问题的几半
	// ("是谁、从哪台机器"),分成几个输入框只会让人把名字打进 IP 那一格里。
	//
	// 邮箱也要匹配:审计行上存的是姓名(actor_name),而 DBA 想到一个人首先想到的是
	// 登录名。生产上一次"按 garett 搜不到"就是这么来的 —— 那个账号的姓名字段不是
	// garett,邮箱才是。审计行没存邮箱,靠 actor_id 关联用户表;用户表很小,子查询的
	// 代价可以忽略。
	Keyword      string
	Since, Until time.Time
}

// auditQuery builds the shared WHERE clause for audit listing/counting.
func (r *Repo) auditQuery(f AuditFilter) *gorm.DB {
	q := r.db.Model(&model.AuditLog{})
	if f.ActorID > 0 {
		q = q.Where("actor_id = ?", f.ActorID)
	}
	if f.Risk != "" && f.Risk != "all" {
		q = q.Where("risk = ?", f.Risk)
	}
	// 取值已由 handler 挡在白名单里(model.IsAuditEventType)。0039 之前的行这一列是空,
	// 所以按类型筛**筛不到它们** —— 那是对的:它们当时确实没有记下自己是哪一类。
	if len(f.Events) > 0 {
		q = q.Where("event_type IN (?)", f.Events)
	}
	// 同上:0040 之前的行这一列是空,按增删改查筛**筛不到它们**。
	if len(f.Sources) > 0 {
		vals := make([]string, 0, len(f.Sources))
		for _, s := range f.Sources {
			if s == model.AuditSourceUnknown {
				s = "" // 旧记录:列上是空串
			}
			vals = append(vals, s)
		}
		q = q.Where("entry_source IN (?)", vals)
	}
	if len(f.Actions) > 0 {
		q = q.Where("data_action IN (?)", f.Actions)
	}
	if f.Instance != "" {
		q = q.Where("instance = ?", f.Instance)
	}
	// 操作人姓名、邮箱、来源 IP,任意一个命中都算。前缀通配的 LIKE 用不上索引 —— 这一句
	// 总是跟在时间与实例的收窄后面跑,扫的是已经小下来的结果集。
	if kw := strings.TrimSpace(f.Keyword); kw != "" {
		like := "%" + escapeLike(kw) + "%"
		q = q.Where("actor_name"+likeClause+" OR client_ip"+likeClause+
			" OR actor_id IN (SELECT id FROM tbl_user WHERE email"+likeClause+")", like, like, like)
	}
	if !f.Since.IsZero() {
		q = q.Where("occurred_at >= ?", f.Since)
	}
	if !f.Until.IsZero() {
		q = q.Where("occurred_at <= ?", f.Until)
	}
	return q
}

// ListPendingApprovalsOlderThan returns pending approvals created before cutoff (timeout sweep).
func (r *Repo) ListPendingApprovalsOlderThan(cutoff time.Time) ([]model.Approval, error) {
	var as []model.Approval
	err := r.db.Where("status = ? AND created_at < ?", model.StatusPending, cutoff).Find(&as).Error
	return as, err
}

// PendingApprovals returns every ticket still awaiting a decision, regardless of
// who is asking. The paged/scoped listing above is for the console, where a
// ticket you are not on is none of your business; this one is for the checks
// that must see the whole queue (审批人自检).
func (r *Repo) PendingApprovals() ([]model.Approval, error) {
	var as []model.Approval
	err := r.db.Where("status = ?", model.StatusPending).Find(&as).Error
	return as, err
}

// SetApprovalExecResult writes back what the execution produced.
//
// 它**不碰 decided_at**:那是"什么时候批的",而现在批准与执行是两个时刻,
// 沿用 SetApprovalResult 会把审批时间改成执行时间 —— 一张周一批、周三执行的单子,
// 事后看就成了周三才批的。executed_at 已经在占位时盖过章了,这里只补结果。
func (r *Repo) SetApprovalExecResult(id int64, output string, outputRef *model.RuleRef, rows int, execStatus string) error {
	return r.db.Model(&model.Approval{}).Where("id = ?", id).Updates(map[string]any{
		"result": output, "result_ref": apimsg.RefJSON(outputRef), "result_rows": rows, "exec_status": execStatus,
	}).Error
}

// ClaimApprovalExecution atomically marks a ticket as executed, and reports
// whether THIS caller is the one that claimed it.
//
// 条件里带 executed_at IS NULL:并发的两个执行请求只有一个能把行改动,另一个拿到
// 0 行受影响,于是知道自己来晚了。一次批准只换一次执行,靠的就是这一行 SQL。
func (r *Repo) ClaimApprovalExecution(id int64) (bool, error) {
	res := r.db.Model(&model.Approval{}).
		Where("id = ? AND status = ? AND executed_at IS NULL", id, model.StatusApproved).
		Updates(map[string]any{"executed_at": time.Now(), "exec_status": model.ExecStatusRunning})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// UnclaimApprovalExecution 退回一次还没真正下发的认领(在线变更单:认领之后迁移没能启动)。
// 只退"执行中、且是刚认领的"那一种,把单子放回可执行状态。
func (r *Repo) UnclaimApprovalExecution(id int64) error {
	return r.db.Model(&model.Approval{}).
		Where("id = ? AND exec_status = ?", id, model.ExecStatusRunning).
		Updates(map[string]any{"executed_at": nil, "exec_status": ""}).Error
}

// ApprovalInterrupted 是网关在执行途中重启、结果无从得知的工单写在 Result 里的那句话。
var ApprovalInterrupted = apimsg.Text("网关在执行过程中重启,命令是否已在库上生效无法确认,请到目标库上核实。这张工单不会再次执行,如需重做请重新提交。")

// FailStuckApprovalExecutions 收尾上一个进程留下的"执行中"工单。
//
// 和发布流水线同一条理由(FailStuckReleases):命令可能已经到了库上,重跑是比报失败更坏的
// 结果,所以判失败、不退回待执行,并把"结果未知、请去库上核实"写在结果里 —— 不说它成功,
// 也不说它没跑。
func (r *Repo) FailStuckApprovalExecutions() (int64, error) {
	// 在线变更单的迁移若已判为 interrupted(可续跑),工单保持"执行中",由续跑 / 放弃来收尾。
	// 必须在 ReconcileStuckOSCJobs 之后调用。
	res := r.db.Model(&model.Approval{}).Where("exec_status = ?", model.ExecStatusRunning).
		Where("osc_job_id = 0 OR osc_job_id NOT IN (?)", r.db.Model(&model.OSCJob{}).Select("id").Where("status = ?", model.OSCInterrupted)).
		Updates(map[string]any{"exec_status": model.ExecStatusFailed, "result": ApprovalInterrupted})
	return res.RowsAffected, res.Error
}

// ----------------------------------------------------------------- Webhook / settings

// GetWebhook / SaveWebhook:Secret 在库里是密文(crypto.EncryptSecret),出了这两个函数就是明文 ——
// 从前明文落库,读得到库的人就拿到了推送签名口令(2026-10 安全审计 M4)。旧的明文行照常可读
// (DecryptSecret 对没有前缀的值原样返回),下次保存时变成密文。
func (r *Repo) GetWebhook() (*model.WebhookConfig, error) {
	var w model.WebhookConfig
	if err := r.db.Order("id asc").First(&w).Error; err != nil {
		return nil, err
	}
	plain, err := crypto.DecryptSecret(w.Secret)
	if err != nil {
		// 解不开(加密密钥换过):当作没配密钥,而不是把密文当口令发出去。
		slog.Warn("webhook secret undecryptable; treating as unset", "err", err)
		plain = ""
	}
	w.Secret = plain
	return &w, nil
}

func (r *Repo) SaveWebhook(w *model.WebhookConfig) error {
	row := *w // 调用方手里的仍是明文
	enc, err := crypto.EncryptSecret(w.Secret)
	if err != nil {
		return err
	}
	row.Secret = enc
	if err := r.db.Save(&row).Error; err != nil {
		return err
	}
	w.ID = row.ID
	return nil
}

// SchemaForConnection returns the (simulated) db.table objects of a connection,
// ordered for a stable tree (database, then table).
func (r *Repo) SchemaForConnection(connID int64) ([]model.SchemaObject, error) {
	var rows []model.SchemaObject
	// `database` is a MySQL reserved word — quoted by GORM per dialect (MySQL
	// backticks, PostgreSQL double quotes) via clause.OrderBy.
	err := r.db.Where("connection_id = ?", connID).
		Order(clause.OrderBy{Columns: []clause.OrderByColumn{
			{Column: clause.Column{Name: "database"}}, {Column: clause.Column{Name: "table_name"}},
		}}).Find(&rows).Error
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

// CountActiveExportJobs:这个人排队中 + 执行中的导出数(按人限额用,L-API2)。
func (r *Repo) CountActiveExportJobs(userID int64) int64 {
	var n int64
	r.db.Model(&model.ExportJob{}).Where("user_id = ? AND status IN ?", userID, []string{model.ExportPending, model.ExportRunning}).Count(&n)
	return n
}

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

// UpdateExportJobIf 只在任务仍处于 from 之一时更新,返回是否改到了。
//
// 任务的收尾有三方在抢:worker 跑完(done)、worker 出错(failed)、人按了终止(cancelled)。
// 谁先改成功算谁,后到的一方看到"没改到"就知道自己输了 —— 不会出现"已终止"被 worker 随后
// 写回"已完成"、或一份刚被终止的任务又冒出下载链接。
func (r *Repo) UpdateExportJobIf(id int64, from []string, fields map[string]any) (bool, error) {
	res := r.db.Model(&model.ExportJob{}).Where("id = ? AND status IN ?", id, from).Updates(fields)
	return res.RowsAffected == 1, res.Error
}

// LinkExportApproval records which ticket gates this job.
func (r *Repo) LinkExportApproval(jobID, approvalID int64, apNo string) error {
	return r.db.Model(&model.ExportJob{}).Where("id = ?", jobID).
		Updates(map[string]any{"approval_id": approvalID, "ap_no": apNo}).Error
}

// SetExportStatus moves a job between states.
//
// 放行含敏感字段的导出时,条件里带上**当前状态**:同一张审批单被处理两次(重复回调、
// 手快点了两下)只有一次能改动那一行,否则会产出两份带原值的归档,而人只知道有一份。
func (r *Repo) SetExportStatus(id int64, status string) error {
	res := r.db.Model(&model.ExportJob{}).
		Where("id = ? AND status = ?", id, model.ExportAwaiting).
		Updates(map[string]any{"status": status})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return apimsg.Errorf("导出任务 %d 不在待审批状态,未放行", id)
	}
	return nil
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
		Updates(map[string]any{"status": model.ExportFailed, "error": apimsg.Text("服务重启导致任务中断,请重新提交")})
	return res.RowsAffected, res.Error
}

// ExpiredExportJobs returns completed export jobs whose archive is past the
// retention cutoff and still on disk.
//
// The clock is FINISHED_AT, falling back to created_at: retention counts from
// when the file appeared, not from when the job was queued — a job that sat in
// the queue for an hour would otherwise lose an hour of its keep-time. A job
// with no `files` is already swept (or never produced one), so it is skipped
// rather than re-marked.
func (r *Repo) ExpiredExportJobs(cutoff time.Time, limit int) ([]model.ExportJob, error) {
	var js []model.ExportJob
	err := r.db.Where("status = ? AND files <> '' AND COALESCE(finished_at, created_at) < ?",
		model.ExportDone, cutoff).
		Order("id asc").Limit(limit).Find(&js).Error
	return js, err
}

// AllExportFiles returns every path any export job still claims, so the orphan
// sweep can tell "nobody's file" from "somebody's file".
func (r *Repo) AllExportFiles() ([]string, error) {
	var rows []string
	err := r.db.Model(&model.ExportJob{}).Where("files <> ''").Pluck("files", &rows).Error
	return rows, err
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

// CountActiveAsyncJobs:这个人排队中 + 执行中的后台执行数(按人限额用,L-API2)。
func (r *Repo) CountActiveAsyncJobs(userID int64) int64 {
	var n int64
	r.db.Model(&model.AsyncJob{}).Where("user_id = ? AND status IN ?", userID, []string{model.AsyncPending, model.AsyncRunning}).Count(&n)
	return n
}

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
func (r *Repo) UpdateAsyncJobLog(id int64, log, logRefs string) error {
	return r.db.Model(&model.AsyncJob{}).Where("id = ?", id).Updates(map[string]any{"log": log, "log_refs": logRefs}).Error
}

// FinishAsyncJob records the terminal status + final log/rows/error.
//
// logRefs 与终态同一次写入:先标 done 再补 refs,中间读到的人会拿到一份没有身份的日志。
func (r *Repo) FinishAsyncJob(id int64, status, log, logRefs, errMsg, errRef string, rows int, at time.Time) error {
	return r.db.Model(&model.AsyncJob{}).Where("id = ?", id).Updates(map[string]any{
		"status": status, "log": log, "log_refs": logRefs, "error": errMsg, "error_ref": errRef, "rows": rows, "finished_at": at,
	}).Error
}

// FailStuckAsyncJobs marks jobs orphaned by a previous process as failed on boot.
func (r *Repo) FailStuckAsyncJobs() (int64, error) {
	res := r.db.Model(&model.AsyncJob{}).
		Where("status IN ?", []string{model.AsyncPending, model.AsyncRunning}).
		Updates(map[string]any{"status": model.AsyncFailed, "error": apimsg.Text("服务重启导致任务中断,请重新提交")})
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

// ScriptUploadReferenced 报告这份上传是否被审批单或发布单引用(它们只存摘录与摘要,原件是执行证据)。
func (r *Repo) ScriptUploadReferenced(id int64) bool {
	var n int64
	if r.db.Model(&model.Approval{}).Where("script_upload_id = ?", id).Count(&n); n > 0 {
		return true
	}
	r.db.Model(&model.Release{}).Where("script_upload_id = ?", id).Count(&n)
	return n > 0
}

func (r *Repo) DeleteScriptUpload(id int64) error {
	return r.db.Delete(&model.ScriptUpload{}, id).Error
}

// ------------------------------------------------------- terminal snippets

// ListSnippets returns one user's snippets, bound hotkeys first (1-9 in order)
// and the unbound ones after, so the list reads in the order the keys are pressed.
func (r *Repo) ListSnippets(userID int64) ([]model.TerminalSnippet, error) {
	var ss []model.TerminalSnippet
	err := r.db.Where("user_id = ?", userID).
		Order("CASE WHEN slot > 0 THEN 0 ELSE 1 END, slot, id desc").Find(&ss).Error
	return ss, err
}

func (r *Repo) GetSnippet(id int64) (*model.TerminalSnippet, error) {
	var s model.TerminalSnippet
	if err := r.db.First(&s, id).Error; err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *Repo) CountSnippets(userID int64) (int64, error) {
	var n int64
	err := r.db.Model(&model.TerminalSnippet{}).Where("user_id = ?", userID).Count(&n).Error
	return n, err
}

// SaveSnippet creates or updates one snippet, releasing whichever OTHER snippet
// of the same user held the hotkey being claimed.
//
// The release and the write are one transaction. Done as two calls, a failure
// between them leaves the user with either two snippets answering to one key or
// none — and the hotkey would then run whichever row the database happened to
// return first, which is not a thing anyone can debug from the UI. Slot 0 means
// unbound and never displaces anything: it is the value most rows hold.
func (r *Repo) SaveSnippet(s *model.TerminalSnippet) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if s.Slot > 0 {
			q := tx.Model(&model.TerminalSnippet{}).
				Where("user_id = ? AND slot = ?", s.UserID, s.Slot)
			if s.ID > 0 {
				q = q.Where("id <> ?", s.ID)
			}
			if err := q.Update("slot", 0).Error; err != nil {
				return err
			}
		}
		// Select the columns explicitly: Slot's zero value is meaningful (unbound),
		// and GORM's Updates() skips zero-valued fields on a struct — so clearing a
		// hotkey by saving slot 0 would silently keep the old binding.
		if s.ID > 0 {
			return tx.Model(&model.TerminalSnippet{ID: s.ID}).
				Select("name", "body", "slot", "updated_at").Updates(s).Error
		}
		return tx.Create(s).Error
	})
}

func (r *Repo) DeleteSnippet(id int64) error {
	return r.db.Delete(&model.TerminalSnippet{}, id).Error
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

// SettingsByPrefix 读一组同前缀的设置。键不存在不是错误,也不会让 GORM 记一行日志(GetSetting 会)。
func (r *Repo) SettingsByPrefix(prefix string) (map[string]string, error) {
	var rows []model.Setting
	if err := r.db.Where("k LIKE ?", prefix+"%").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, s := range rows {
		out[s.K] = s.V
	}
	return out, nil
}

// SetSettings 一次写一组设置:要么全写下,要么一项不写。按键排序写,两次保存不会互相等死。
func (r *Repo) SetSettings(values map[string]string) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		in := &Repo{db: tx}
		for _, k := range slices.Sorted(maps.Keys(values)) {
			if err := in.SetSetting(k, values[k]); err != nil {
				return err
			}
		}
		return nil
	})
}

// SetSetting 写一个设置,有则改、无则加,一句写成。不用 Save:它先 UPDATE、落空再 INSERT,在 MySQL 上
// 对不存在的键做 UPDATE 会持间隙锁,两个事务各持一把,随后的 INSERT 就互相等死。
func (r *Repo) SetSetting(k, v string) error {
	return r.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "k"}}, DoUpdates: clause.AssignmentColumns([]string{"v"}),
	}).Create(&model.Setting{K: k, V: v}).Error
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
	// 徽章数的是**待审批**,而代跑可见的那一类全是已批准的 —— 传 nil 不会漏计,
	// 反倒避免了让一个数字随别人的工单变动。
	err := r.approvalScope(scope, userID, nil).Where("status = ?", model.StatusPending).Count(&n).Error
	return n, err
}

// TodoCandidates 拉出"可能在等这个人动手"的两类工单:
//
//   - 待审批、且他在审批链上(scope=mine 的那个子查询);
//   - 已批准、还没执行、他够得到的实例上的(与列表页第三类可见性同一条谓词)。
//
// 这里只做**可见性**筛,"此刻能不能动"由 service 逐张判(DecideBlockFor /
// CanExecuteApproved)—— 那两条规则全系统只有一处,这里不复制第二份。
//
// 已批准那一路把窗口单 / 升级单 / 导出单直接排掉:它们批准即生效或由别的流程执行,
// executed_at 永远是空,不排掉就会被年复一年地拉出来再逐张否掉。
//
// 不分页、不设上限:这两类都是"还没处理完"的,天然是小集合 —— 一张等着执行的单子
// 可以停很久,而分页会让它被新单子挤出去(见 api.approvals 的注释)。
func (r *Repo) TodoCandidates(userID int64, runnableConnIDs []int64) (decide, execute []model.Approval, err error) {
	if err = r.approvalScope("mine", userID, nil).
		Where("status = ?", model.StatusPending).
		Order("id desc").Find(&decide).Error; err != nil {
		return nil, nil, err
	}
	if err = r.approvalScope("all", userID, runnableConnIDs).
		Where("status = ? AND executed_at IS NULL AND window_id = 0 AND release_id = 0 AND export_job_id = 0", model.StatusApproved).
		Order("id desc").Find(&execute).Error; err != nil {
		return nil, nil, err
	}
	return decide, execute, nil
}

// SaveConnCheck 记下一次连通性巡检的结论。
//
// 只写这四列,**不碰 Status**:维护态是人标的,一次探测不该把它改掉 —— 反过来也一样,
// 一台标成维护中的实例照样可能是连得通的。
//
// 成功时把上次的错误清掉:留着它会让一台已经好了的实例永远显示成坏的。
//
// 错误文本截到 500:列是 512,MySQL 严格模式下超长会让整条 UPDATE 失败,而那意味着
// 一次真实的失败**根本记不下来** —— 行保持上一次的"通",正是这套字段要修的那个现象。
func (r *Repo) SaveConnCheck(id int64, kind string, ok bool, errMsg string) error {
	if ok {
		errMsg = ""
	}
	if len(errMsg) > 500 {
		errMsg = errMsg[:500]
	}
	return r.db.Model(&model.Connection{}).Where("id = ?", id).Updates(map[string]any{
		"last_check_at":   time.Now(),
		"last_check_ok":   ok,
		"last_check_err":  errMsg,
		"last_check_kind": kind,
	}).Error
}

// ClearConnCheck 把巡检结论清空 —— 凭据或地址改了之后,上一次的结论说的是另一台库。
//
// 不清的后果很具体:改好密码的实例带着旧的"认证失败"被定时巡检永久跳过,界面上一直
// 显示"连不上 · N 天前"。
func (r *Repo) ClearConnCheck(id int64) error {
	return r.db.Model(&model.Connection{}).Where("id = ?", id).Updates(map[string]any{
		"last_check_at":   nil,
		"last_check_ok":   nil,
		"last_check_err":  "",
		"last_check_kind": "",
	}).Error
}
