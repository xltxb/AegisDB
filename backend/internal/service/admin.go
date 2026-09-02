package service

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"velagateway/internal/dto"
	"velagateway/internal/gateway"
	"velagateway/internal/model"
	"velagateway/internal/repository"
	"velagateway/pkg/crypto"
)

// ---------------------------------------------------------------- Connections

// connEnvMeta resolves an environment code to the display layer and default
// connection role its tier prescribes, and doubles as the validity check: an
// environment that does not resolve has no tier, and a connection stored against
// it would be governed by no rules at all (see the ErrBadRequest cases below and
// the note on model.EnvTier).
//
// This replaces a hardcoded env→layer map and the validEnvs whitelist; the
// values now live in tbl_env_tier, seeded from exactly what that map contained.
func (s *Services) connEnvMeta(envCode string) (layer, role string, err error) {
	t, err := s.TierOfEnvironment(envCode)
	if err != nil {
		return "", "", ErrBadRequest
	}
	return t.ConnLayer, t.DefaultRole, nil
}

func (s *Services) CreateConnection(req dto.ConnectionCreateReq) (*model.Connection, error) {
	host, port := splitHostPort(req.Host)
	env := strings.ToLower(strings.TrimSpace(req.Env))
	layer, role, err := s.connEnvMeta(env)
	if err != nil {
		return nil, err
	}
	// Encrypt the DB password at rest (AES-256-GCM). It must stay reversible
	// because the gateway needs it to open the real connection.
	encPw, err := crypto.EncryptSecret(req.Password)
	if err != nil {
		return nil, err
	}
	c := &model.Connection{
		Name: strings.TrimSpace(req.Name), Engine: req.Engine, Host: host, Port: port,
		Env: env, Policy: req.Policy, DefaultRole: role, Layer: layer, Status: "online",
		Username: strings.TrimSpace(req.Username), Password: encPw, Database: strings.TrimSpace(req.Database),
	}
	if err := s.Repo.CreateConnection(c); err != nil {
		return nil, err
	}
	return c, nil
}

// UpdateConnection edits an existing instance's config (address, engine, env,
// policy, credentials, database). An empty password keeps the stored one so the
// admin needn't re-enter it on every edit.
func (s *Services) UpdateConnection(id int64, req dto.ConnectionUpdateReq) (*model.Connection, error) {
	c, err := s.Repo.GetConnection(id)
	if err != nil {
		return nil, ErrNotFound
	}
	if !validPolicies[req.Policy] {
		return nil, ErrBadRequest
	}
	env := strings.ToLower(strings.TrimSpace(req.Env))
	layer, role, err := s.connEnvMeta(env)
	if err != nil {
		return nil, err
	}
	c.Name = strings.TrimSpace(req.Name)
	c.Engine = strings.TrimSpace(req.Engine)
	c.Host, c.Port = splitHostPort(req.Host)
	c.Env = env
	c.Layer, c.DefaultRole = layer, role
	c.Policy = req.Policy
	c.Username = strings.TrimSpace(req.Username)
	c.Database = strings.TrimSpace(req.Database)
	if strings.TrimSpace(req.Password) != "" { // empty = keep the stored password
		encPw, encErr := crypto.EncryptSecret(req.Password)
		if encErr != nil {
			return nil, encErr
		}
		c.Password = encPw
	}
	if err := s.Repo.UpdateConnection(c); err != nil {
		return nil, err
	}
	return c, nil
}

// ConnectionSchema returns a connection's database→table tree. When the connection
// has real credentials it introspects the live target; otherwise it falls back to
// the seeded (simulated) tree. Live-introspection failures are reported in the
// response's Error field (not as a transport error) so the UI can show why the tree
// is empty. Access is tag-gated just like execution.
func tableDTOs(names []string) []dto.SchemaTableDTO {
	out := make([]dto.SchemaTableDTO, 0, len(names))
	for _, n := range names {
		out = append(out, dto.SchemaTableDTO{Name: n})
	}
	return out
}

func (s *Services) ConnectionSchema(u *model.User, connID int64, database string) dto.ConnectionSchemaResp {
	out := dto.ConnectionSchemaResp{ConnectionID: connID, Databases: []dto.SchemaDBDTO{}}
	conn, err := s.Repo.GetConnection(connID)
	if err != nil {
		out.Error = "连接不存在"
		return out
	}
	if !s.canAccessConn(u, conn) {
		out.Error = "无权访问该连接"
		return out
	}
	// A caller may target a specific database (e.g. a PostgreSQL database picked
	// from the list) to load that database's tables.
	applyTargetDatabase(conn, database)
	if gateway.RealExecSupported(conn) {
		groups, err := gateway.RealSchema(conn)
		if err != nil {
			out.Error = "无法连接目标数据库(检查网络/主机/端口/账号密码/数据库名): " + err.Error()
			return out
		}
		for _, g := range groups {
			db := dto.SchemaDBDTO{Name: g.Database, Tables: tableDTOs(g.Tables)}
			for _, sc := range g.Schemas {
				db.Schemas = append(db.Schemas, dto.SchemaSchemaDTO{Name: sc.Name, Tables: tableDTOs(sc.Tables)})
			}
			out.Databases = append(out.Databases, db)
		}
		return s.labelProjects(connID, out)
	}
	// No credentials → simulated connection: return the seeded tree (if any).
	objs, _ := s.Repo.SchemaForConnection(connID)
	order := []string{}
	byDB := map[string][]dto.SchemaTableDTO{}
	for _, o := range objs {
		if _, seen := byDB[o.Database]; !seen {
			order = append(order, o.Database)
		}
		byDB[o.Database] = append(byDB[o.Database], dto.SchemaTableDTO{Name: o.Tbl})
	}
	for _, name := range order {
		out.Databases = append(out.Databases, dto.SchemaDBDTO{Name: name, Tables: byDB[name]})
	}
	return s.labelProjects(connID, out)
}

// labelProjects tags each discovered database with the project it is filed
// under. Filing is stored by NAME (databases are discovered, never
// registered), so this is a map lookup, not a join — and a filing naming a
// database that no longer exists simply matches nothing.
//
// A filing pointing at a deleted project degrades to unlabelled rather than
// erroring: the tree is how people reach their data, and it must not go dark
// over a bookkeeping detail.
func (s *Services) labelProjects(connID int64, out dto.ConnectionSchemaResp) dto.ConnectionSchemaResp {
	filed := s.Repo.DatabaseProjectsForConnection(connID)
	if len(filed) == 0 {
		return out
	}
	names := map[int64]string{}
	if ps, err := s.Repo.ListProjects(); err == nil {
		for _, p := range ps {
			names[p.ID] = p.Name
		}
	}
	for i, db := range out.Databases {
		if id := filed[db.Name]; id != 0 {
			out.Databases[i].ProjectID = id
			out.Databases[i].ProjectName = names[id]
		}
	}
	return out
}

// ConnectionObjects lists a database's programmable objects (functions /
// procedures / packages / triggers) for the terminal tree. scope is the
// database (MySQL), schema (PostgreSQL family) or owner (Oracle); database,
// when non-empty, targets that database — the PostgreSQL family's catalogs are
// PER database, so browsing a database other than the connection's default one
// otherwise introspects the wrong catalog and reports every object missing
// (same contract as ConnectionSchema). Same access stance as ConnectionSchema;
// a simulated (credential-less) connection returns a small synthetic set.
func (s *Services) ConnectionObjects(u *model.User, connID int64, scope, database string) dto.DbObjectsResp {
	out := dto.DbObjectsResp{Functions: []string{}, Procedures: []string{}, Packages: []string{}, Triggers: []string{}}
	conn, err := s.Repo.GetConnection(connID)
	if err != nil {
		out.Error = "连接不存在"
		return out
	}
	if !s.canAccessConn(u, conn) {
		out.Error = "无权访问该连接"
		return out
	}
	applyTargetDatabase(conn, database)
	if !gateway.RealExecSupported(conn) {
		return simulatedObjects(conn.Engine)
	}
	objs, err := gateway.RealObjects(conn, strings.TrimSpace(scope))
	if err != nil {
		out.Error = "无法加载对象列表: " + err.Error()
		return out
	}
	if objs.Functions != nil {
		out.Functions = objs.Functions
	}
	if objs.Procedures != nil {
		out.Procedures = objs.Procedures
	}
	if objs.Packages != nil {
		out.Packages = objs.Packages
	}
	if objs.Triggers != nil {
		out.Triggers = objs.Triggers
	}
	return out
}

// ConnectionObjectSource returns one programmable object's source text. The
// read is metadata-only (catalog views), gated by the same connection access
// check as the schema tree. database, when non-empty, targets that database —
// see ConnectionObjects on why the PostgreSQL family needs it.
func (s *Services) ConnectionObjectSource(u *model.User, connID int64, scope, typ, name, database string) (*dto.ObjectSourceResp, error) {
	conn, err := s.Repo.GetConnection(connID)
	if err != nil {
		return nil, ErrNotFound
	}
	if !s.canAccessConn(u, conn) {
		return nil, ErrForbidden
	}
	applyTargetDatabase(conn, database)
	if !gateway.RealExecSupported(conn) {
		return &dto.ObjectSourceResp{Name: name, Type: typ, Source: simulatedObjectSource(conn.Engine, typ, name)}, nil
	}
	src, err := gateway.RealObjectSource(conn, strings.TrimSpace(scope), typ, name)
	if err != nil {
		return nil, fmt.Errorf("无法读取对象源码: %w", err)
	}
	return &dto.ObjectSourceResp{Name: name, Type: typ, Source: src}, nil
}

// simulatedObjects is the demo object set for a credential-less connection,
// shaped to what the engine family would really have.
func simulatedObjects(engine string) dto.DbObjectsResp {
	e := strings.ToLower(engine)
	out := dto.DbObjectsResp{
		Functions:  []string{"fn_order_total", "fn_user_level"},
		Procedures: []string{"sp_archive_orders", "sp_rebuild_index"},
		Packages:   []string{},
		Triggers:   []string{"trg_orders_audit"},
	}
	switch {
	case strings.Contains(e, "oracle"):
		out.Packages = []string{"pkg_billing"}
		out.Triggers = []string{}
	case strings.Contains(e, "postgre"), strings.Contains(e, "dws"), strings.Contains(e, "gauss"):
		out.Triggers = []string{}
	case strings.Contains(e, "sqlite"):
		out = dto.DbObjectsResp{Functions: []string{}, Procedures: []string{}, Packages: []string{}, Triggers: []string{"trg_orders_audit"}}
	}
	return out
}

// simulatedObjectSource synthesises a plausible body for a demo object.
func simulatedObjectSource(engine, typ, name string) string {
	head := "-- 模拟连接(未配置真实凭据),以下为演示内容\n"
	e := strings.ToLower(engine)
	switch typ {
	case "table":
		return head + fmt.Sprintf("CREATE TABLE %s (\n  id BIGINT NOT NULL,\n  name VARCHAR(128),\n  status VARCHAR(16) DEFAULT 'active',\n  created_at TIMESTAMP,\n  PRIMARY KEY (id)\n);\n\nCREATE INDEX idx_%s_status ON %s (status);", name, name, name)
	case "package":
		return head + fmt.Sprintf("CREATE OR REPLACE PACKAGE %s AS\n  FUNCTION charge(p_order_id NUMBER) RETURN NUMBER;\n  PROCEDURE settle(p_day DATE);\nEND %s;\n/", name, name)
	case "trigger":
		return head + fmt.Sprintf("CREATE TRIGGER %s AFTER UPDATE ON orders\nFOR EACH ROW\nBEGIN\n  INSERT INTO orders_audit(order_id, changed_at) VALUES (OLD.id, NOW());\nEND", name)
	case "procedure":
		if strings.Contains(e, "postgre") || strings.Contains(e, "dws") || strings.Contains(e, "gauss") {
			return head + fmt.Sprintf("CREATE OR REPLACE PROCEDURE %s()\nLANGUAGE plpgsql\nAS $$\nBEGIN\n  RAISE NOTICE 'archiving…';\nEND;\n$$;", name)
		}
		return head + fmt.Sprintf("CREATE PROCEDURE %s()\nBEGIN\n  -- archive rows older than 90 days\n  DELETE FROM orders WHERE created_at < NOW() - INTERVAL 90 DAY;\nEND", name)
	default: // function
		if strings.Contains(e, "postgre") || strings.Contains(e, "dws") || strings.Contains(e, "gauss") {
			return head + fmt.Sprintf("CREATE OR REPLACE FUNCTION %s(uid BIGINT)\nRETURNS INTEGER\nLANGUAGE sql\nAS $$ SELECT 1 $$;", name)
		}
		return head + fmt.Sprintf("CREATE FUNCTION %s(uid BIGINT)\nRETURNS INT\nDETERMINISTIC\nBEGIN\n  RETURN 1;\nEND", name)
	}
}

// withTierDefaults refreshes each connection's display layer and default role
// from the tier that governs it right now.
//
// Both columns are DERIVED from the tier, and they are written when a connection
// is created or edited — so they drift the moment the tier moves out from under
// them, which the tier model made possible in three new ways: renaming a tier's
// layer, rebinding an environment to another tier, and deleting an environment so
// its instances move to one on a different tier. None of those touch the
// connection row, and the console would keep showing the layer of a tier that no
// longer governs the instance.
//
// So the stored value is not trusted for display; it is recomputed here, at the
// one place the console reads instances from. It stays in the column as the
// fallback for an environment that no longer resolves — there is no tier to ask
// then, and the last known value beats a blank.
//
// Nothing about access control reads these fields; judgement resolves the tier
// itself (see tierOf).
func (s *Services) withTierDefaults(conns []model.Connection) []model.Connection {
	for i := range conns {
		t, err := s.TierOfEnvironment(conns[i].Env)
		if err != nil {
			continue // unresolvable environment — keep the stored value
		}
		conns[i].Layer, conns[i].DefaultRole = t.ConnLayer, t.DefaultRole
	}
	return conns
}

// AccessibleConnections returns the connections a user's role may see: all of
// them when the role has no tags (unrestricted, e.g. admin), otherwise only the
// connections whose tags intersect the role's granted tags.
func (s *Services) AccessibleConnections(u *model.User) ([]model.Connection, error) {
	all, err := s.Repo.ListConnections()
	if err != nil {
		return nil, err
	}
	all = s.withTierDefaults(all)
	if u == nil {
		return all, nil
	}
	allow, unrestricted, err := s.Repo.ScopeForUser(u.ID, s.Repo.EffectiveRoleIDs(u))
	if err != nil {
		return nil, err // don't fall back to "unrestricted" on a failed read (ED3)
	}
	if unrestricted {
		return all, nil
	}
	allowSet := map[string]bool{}
	for _, t := range allow {
		allowSet[t] = true
	}
	out := []model.Connection{}
	for _, c := range all {
		for _, ct := range repository.SplitTags(c.Tags) {
			if allowSet[ct] {
				out = append(out, c)
				break
			}
		}
	}
	return out, nil
}

// canAccessConn reports whether a user's role may operate on a connection.
func (s *Services) canAccessConn(u *model.User, conn *model.Connection) bool {
	if u == nil || conn == nil {
		return false
	}
	allow, unrestricted, err := s.Repo.ScopeForUser(u.ID, s.Repo.EffectiveRoleIDs(u))
	if err != nil {
		slog.Error("tag access check failed — denying", "userID", u.ID, "connID", conn.ID, "err", err)
		return false // a check that cannot run denies (ED3)
	}
	if unrestricted {
		return true
	}
	allowSet := map[string]bool{}
	for _, t := range allow {
		allowSet[t] = true
	}
	for _, ct := range repository.SplitTags(conn.Tags) {
		if allowSet[ct] {
			return true
		}
	}
	return false
}

// SetConnectionTags normalizes and stores a connection's tags.
func (s *Services) SetConnectionTags(id int64, tags string) error {
	return s.Repo.SetConnectionTags(id, strings.Join(repository.SplitTags(tags), ","))
}

// SetRoleTags stores the tags granted to a role (user group).
func (s *Services) SetRoleTags(id int64, tags []string) error { return s.Repo.SetRoleTags(id, tags) }

// AllTags lists every distinct connection tag (for pickers).
func (s *Services) AllTags() []string { return s.Repo.AllConnectionTags() }

// validPolicies are the gateway policies a connection may carry.
var validPolicies = map[string]bool{"strict": true, "approve-1": true, "audit-only": true}

// The environments a connection may be stored against were a four-entry
// whitelist here (ED5). The reasoning behind it is unchanged and still load
// bearing: capability levels and dictionary rules are stored per tier, a lookup
// that finds no row falls through to "allow", so an unrecognised value is not a
// label — it is an instance with no rules at all.
//
// What changed is only where the accepted set comes from. It is now tbl_environment,
// and the check is s.connEnvMeta: an environment resolves to its tier or the
// write is refused. A typo like "uat" still cannot be stored, and an environment
// can only be created by binding it to a tier that already owns rules.

// SetConnectionPolicy updates a connection's gateway policy (strict | approve-1 |
// audit-only), rejecting an unknown value.
func (s *Services) SetConnectionPolicy(id int64, policy string) error {
	if !validPolicies[policy] {
		return ErrBadRequest
	}
	c, err := s.Repo.GetConnection(id)
	if err != nil {
		return ErrNotFound
	}
	c.Policy = policy
	return s.Repo.UpdateConnection(c)
}

func (s *Services) ToggleConnection(id int64, status string) error {
	c, err := s.Repo.GetConnection(id)
	if err != nil {
		return ErrNotFound
	}
	if status == "" {
		if c.Status == "online" {
			status = "maint"
		} else {
			status = "online"
		}
	}
	c.Status = status
	return s.Repo.UpdateConnection(c)
}

// ---------------------------------------------------------------- Roles

func (s *Services) ListRolesBrief() ([]dto.RoleBrief, error) {
	roles, err := s.Repo.ListRoles()
	if err != nil {
		return nil, err
	}
	out := []dto.RoleBrief{}
	for _, r := range roles {
		members, _ := s.Repo.MembersOfRole(r.ID)
		out = append(out, dto.RoleBrief{ID: r.ID, Code: r.Code, Name: r.Name, Layer: r.Layer, Icon: r.Icon, Count: len(members)})
	}
	return out, nil
}

// DefaultApprovers returns the approval chain shown in settings: the "owner"
// (DBA 负责人) role members, falling back to admins when no owner is assigned —
// matching the chain that defaultChainSteps actually builds.
func (s *Services) DefaultApprovers() []dto.MemberDTO {
	out := []dto.MemberDTO{}
	for _, m := range s.approverPool() {
		out = append(out, dto.MemberDTO{ID: m.ID, Name: m.Name, Initials: m.Initials, Dept: m.Dept, Kind: m.Kind})
	}
	return out
}

func (s *Services) RoleDetail(id int64) (*dto.RoleDetailResp, error) {
	role, err := s.Repo.GetRole(id)
	if err != nil {
		return nil, ErrNotFound
	}
	menus, _ := s.Repo.MenusForRole(id)
	matrix, _ := s.Repo.MatrixForRole(id)
	members, _ := s.Repo.MembersOfRole(id)
	mds := []dto.MemberDTO{}
	ids := []int64{}
	for _, m := range members {
		mds = append(mds, dto.MemberDTO{ID: m.ID, Name: m.Name, Initials: m.Initials, Dept: m.Dept, Kind: m.Kind})
		ids = append(ids, m.ID)
	}
	return &dto.RoleDetailResp{
		ID: role.ID, Code: role.Code, Name: role.Name, Layer: role.Layer, Icon: role.Icon,
		Menus: menus, Matrix: matrix, Members: mds, MemberIDs: ids, Tags: s.Repo.TagsForRole(id),
	}, nil
}

// ---------------------------------------------------------------- Users

func (s *Services) UsersView(actor *model.User) ([]dto.UserView, error) {
	users, err := s.Repo.ListUsers()
	if err != nil {
		return nil, err
	}
	out := []dto.UserView{}
	for i := range users {
		u := &users[i]
		roles, _ := s.Repo.RolesOfUser(u.ID)
		ids := s.Repo.EffectiveRoleIDs(u)
		// 停用方向才有拦的必要 —— 启用永远允许。
		block := ""
		if err := s.guardDisable(actor, u); err != nil {
			block = err.Error()
		}
		out = append(out, dto.UserView{
			ID: u.ID, Name: u.Name, Email: u.Email, Initials: u.Initials, Dept: u.Dept,
			Roles: roles, RoleIDs: ids, PrimaryRoleID: u.RoleID,
			Status: u.Status, Kind: u.Kind, MFAEnabled: u.MFAEnabled && u.MFASecret != "", LastActive: u.LastActive,
			CanDisable: block == "", DisableBlock: block,
		})
	}
	return out, nil
}

func (s *Services) PatchUser(actor *model.User, id int64, req dto.UserPatchReq) error {
	target, err := s.Repo.GetUserByID(id)
	if err != nil {
		return ErrNotFound
	}
	// Write ONLY the edited columns — never Save() a full snapshot, which would
	// roll back token_version / mfa_last_ctr changed by a concurrent logout /
	// password reset / TOTP use (R8).
	fields := map[string]any{}
	if req.Status != "" {
		// Reject out-of-vocabulary states: a stray value leaves a user in limbo that
		// login (only "active" passes) and the disabled-session check don't cover (C3).
		switch req.Status {
		case "active", "disabled", "invited":
			fields["status"] = req.Status
		default:
			return ErrBadRequest
		}
		if req.Status != "active" {
			if err := s.guardDisable(actor, target); err != nil {
				return err
			}
		}
	}
	if req.RoleID != nil {
		// 多角色路径(RoleIDs)一直有 validateRoleIDs,单角色这条漏了 —— 同一个洞
		// 的两个入口,补上另一个。
		if err := s.validateRoleIDs([]int64{*req.RoleID}); err != nil {
			return err
		}
		fields["role_id"] = *req.RoleID
	}
	if len(fields) > 0 {
		if err := s.Repo.UpdateUserFields(id, fields); err != nil {
			return err
		}
	}
	// A full multi-role assignment replaces the membership set and repoints the
	// primary role to the first id (union permissions follow from the set).
	if req.RoleIDs != nil {
		if len(req.RoleIDs) == 0 {
			return ErrBadRequest // a user must keep at least one role
		}
		if err := s.validateRoleIDs(req.RoleIDs); err != nil {
			return err
		}
		return s.Repo.SetUserRoles(id, req.RoleIDs)
	}
	// Keep membership in sync when only the single primary role changed, so the
	// role view and union permissions reflect the new role.
	if req.RoleID != nil {
		_ = s.Repo.AddMember(*req.RoleID, id)
	}
	s.auditPatchUser(actor, target, req)
	return nil
}

// auditPatchUser records which account attributes an administrator changed.
// Status and role edits are privilege changes, so they belong in the immutable
// log alongside command execution (EU4).
func (s *Services) auditPatchUser(actor, target *model.User, req dto.UserPatchReq) {
	if target == nil {
		return
	}
	changed := []string{}
	if req.Status != "" {
		changed = append(changed, "status="+req.Status)
	}
	if req.RoleID != nil {
		changed = append(changed, "roleId="+strconv.FormatInt(*req.RoleID, 10))
	}
	if req.RoleIDs != nil {
		ids := make([]string, 0, len(req.RoleIDs))
		for _, id := range req.RoleIDs {
			ids = append(ids, strconv.FormatInt(id, 10))
		}
		changed = append(changed, "roleIds="+strings.Join(ids, "|"))
	}
	if len(changed) == 0 {
		return
	}
	s.auditAdminAction(actor, "admin.user.patch "+strings.Join(changed, " ")+" user="+target.Email)
}

// validateRoleIDs ensures every id refers to an existing role.
func (s *Services) validateRoleIDs(ids []int64) error {
	for _, id := range ids {
		if _, err := s.Repo.GetRole(id); err != nil {
			return ErrBadRequest
		}
	}
	return nil
}

// guardDisable refuses the two ways disabling an account locks the platform.
//
// 停用是一把没有回程的闸:停掉之后登录被拒(Login 只放行 active),在手的 token 每个
// 请求都被中间件按库里的最新状态挡下 —— 这是对的,停用就该立刻生效。但正因为这么
// 彻底,把最后一个管理员停掉就没有路走回来了:重新启用要调管理员接口,而调它需要一个
// 还能登录的管理员。剩下的唯一办法是有人直接去改数据库。
//
// 闸可以关,但不能把钥匙一起关在里面。
func (s *Services) guardDisable(actor, target *model.User) error {
	if actor != nil && actor.ID == target.ID {
		// 这一条不是为了防死锁(下一条才是),而是因为它几乎总是误点:状态徽章就在
		// 用户列表里自己那一行上,点下去的后果是当场把自己踢出控制台。要停用自己,
		// 得请另一个管理员来做 —— 那时至少有人看着。
		return &DisableRefusal{Reason: "不能停用自己的账户:停用会立刻生效,你会当场退出控制台。请让另一位管理员操作。"}
	}
	admins := s.Repo.ActiveAdminIDs()
	if len(admins) != 1 || admins[0] != target.ID {
		return nil // 目标不是最后一个管理员(或压根不是管理员)
	}
	return &DisableRefusal{Reason: "不能停用最后一位管理员:停用之后没有人能再启用任何账户,只能直接改数据库。请先指派另一位管理员。"}
}

// DisableRefusal carries the reason through to the console: 两种拒绝要人做的事不同
// —— 一个是"换个人来点",一个是"先指派一位管理员"。
type DisableRefusal struct{ Reason string }

func (e *DisableRefusal) Error() string { return e.Reason }

// CreateUser provisions an account directly from the admin console: it sets an
// initial password and marks the account active so the user can sign in
// immediately (distinct from Invite, which leaves a passwordless "invited" row).
// The user may be granted several roles at once; the first is the primary role.
func (s *Services) CreateUser(req dto.UserCreateReq) (*model.User, error) {
	email := strings.TrimSpace(strings.ToLower(req.Email))
	if email == "" || !strings.Contains(email, "@") {
		return nil, ErrBadRequest
	}
	if len(req.Password) < 8 { // keep in sync with AdminSetPassword / frontend
		return nil, ErrBadRequest
	}
	if len(req.RoleIDs) == 0 {
		return nil, ErrBadRequest
	}
	if err := s.validateRoleIDs(req.RoleIDs); err != nil {
		return nil, err
	}
	if _, err := s.Repo.GetUserByEmail(email); err == nil {
		return nil, ErrBadRequest // email already exists
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = email[:strings.Index(email, "@")]
	}
	hash, err := crypto.HashPassword(req.Password)
	if err != nil {
		return nil, err
	}
	u := &model.User{
		Name: name, Email: email, RoleID: req.RoleIDs[0], Status: "active",
		PasswordHash: hash, Initials: initials(name), Dept: "—", LastActive: "—",
	}
	if err := s.Repo.CreateUser(u); err != nil {
		return nil, err
	}
	if err := s.Repo.SetUserRoles(u.ID, req.RoleIDs); err != nil {
		return nil, err
	}
	return u, nil
}

func (s *Services) Invite(req dto.InviteReq) (*model.User, error) {
	name := req.Email
	if at := strings.Index(req.Email, "@"); at > 0 {
		name = req.Email[:at]
	}
	u := &model.User{
		Name: name, Email: req.Email, RoleID: req.RoleID, Status: "invited",
		Initials: initials(name), Dept: "—", LastActive: "—",
	}
	if err := s.Repo.CreateUser(u); err != nil {
		return nil, err
	}
	_ = s.Repo.AddMember(req.RoleID, u.ID)
	return u, nil
}

// ---------------------------------------------------------------- Risk dictionary

func (s *Services) RiskDictView() []dto.RiskCommandView {
	order, grouped := s.Repo.RiskCommandsGrouped()
	out := []dto.RiskCommandView{}
	for _, cmd := range order {
		out = append(out, dto.RiskCommandView{Command: cmd, Tiers: grouped[cmd]})
	}
	return out
}

// ---------------------------------------------------------------- Audit

// canSeeAllActivity reports whether a role has cross-user oversight (audit/admin
// functions). Others only ever see their own commands and files.
func (s *Services) canSeeAllActivity(u *model.User) bool {
	if u == nil {
		return false
	}
	// Any of the user's roles granting oversight is enough (union semantics).
	for _, code := range s.Repo.RoleCodesForIDs(s.Repo.EffectiveRoleIDs(u)) {
		switch code {
		case "admin", "owner", "audit":
			return true
		}
	}
	return false
}

// auditActor returns the actor filter for a user: 0 (all) for oversight roles,
// otherwise the user's own id so they only see their own commands.
func (s *Services) auditActor(u *model.User) int64 {
	if u == nil || s.canSeeAllActivity(u) {
		return 0
	}
	return u.ID
}

func (s *Services) ListAudit(u *model.User, risk string, since, until time.Time, limit int) ([]model.AuditLog, error) {
	return s.Repo.ListAudit(s.auditActor(u), risk, since, until, limit)
}

// AuditPage is one page of audit rows plus the total matching the filters.
type AuditPage struct {
	Items    []model.AuditLog `json:"items"`
	Total    int64            `json:"total"`
	Page     int              `json:"page"`
	PageSize int              `json:"pageSize"`
}

// ListAuditPaged returns page `page` (1-based) of `pageSize` audit rows matching
// the risk + time filters, scoped to the caller's visibility.
func (s *Services) ListAuditPaged(u *model.User, risk string, since, until time.Time, page, pageSize int) (AuditPage, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 100
	}
	rows, total, err := s.Repo.ListAuditPaged(s.auditActor(u), risk, since, until, (page-1)*pageSize, pageSize)
	if rows == nil {
		rows = []model.AuditLog{}
	}
	return AuditPage{Items: rows, Total: total, Page: page, PageSize: pageSize}, err
}

// RangeSince maps a range key (24h|7d|30d) to a cutoff time (zero = all-time).
func RangeSince(key string) time.Time {
	switch key {
	case "24h":
		return time.Now().Add(-24 * time.Hour)
	case "7d":
		return time.Now().Add(-7 * 24 * time.Hour)
	case "30d":
		return time.Now().Add(-30 * 24 * time.Hour)
	default:
		return time.Time{}
	}
}

// AuditWindow resolves the audit time filter into a [since, until] window. An
// explicit absolute `from`/`to` (accepted as RFC3339, "2006-01-02T15:04[:05]"
// or "2006-01-02", interpreted in server-local time) takes precedence for the
// lower bound; otherwise the relative `rangeKey` (24h|7d|30d) sets `since`. `to`
// sets the upper bound when present. A zero time means "unbounded" on that side.
func AuditWindow(rangeKey, from, to string) (since, until time.Time) {
	if t, ok := parseAuditTime(from); ok {
		since = t
	} else {
		since = RangeSince(rangeKey)
	}
	if t, ok := parseAuditTime(to); ok {
		until = t
	}
	return
}

func parseAuditTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// ExportCSV renders the audit rows as CSV (scoped to the user's own commands
// unless they hold an oversight role).
func (s *Services) ExportCSV(u *model.User, risk string, since, until time.Time) (string, error) {
	rows, err := s.Repo.ListAudit(s.auditActor(u), risk, since, until, 0)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("time,actor,instance,command,risk,result,approval_no,hash\n")
	for _, r := range rows {
		b.WriteString(strings.Join([]string{
			r.OccurredAt.Format("2006-01-02 15:04:05"),
			csvCell(r.ActorName), csvCell(r.Instance), csvCell(r.Command),
			r.Risk, r.Result, r.ApprovalNo, r.Hash,
		}, ","))
		b.WriteByte('\n')
	}
	return b.String(), nil
}

// ---------------------------------------------------------------- helpers

func splitHostPort(hp string) (string, int) {
	hp = strings.TrimSpace(hp)
	if i := strings.LastIndex(hp, ":"); i > 0 {
		port, _ := strconv.Atoi(hp[i+1:])
		if port == 0 {
			port = 3306
		}
		return hp[:i], port
	}
	return hp, 3306
}

func initials(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "?"
	}
	parts := strings.Fields(name)
	if len(parts) >= 2 {
		return strings.ToUpper(string([]rune(parts[0])[:1]) + string([]rune(parts[1])[:1]))
	}
	r := []rune(name)
	if len(r) >= 2 {
		return strings.ToUpper(string(r[:2]))
	}
	return strings.ToUpper(string(r))
}

func csvCell(s string) string {
	s = csvSanitize(s)
	if strings.ContainsAny(s, ",\"\n") {
		return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}
	return s
}

// csvSanitize defuses CSV/spreadsheet formula injection: a cell starting with
// = + - @ (or a Tab/CR that spreadsheets treat as a formula lead-in) is prefixed
// with a single quote so Excel/WPS render it as text instead of executing it
// (e.g. =HYPERLINK / =WEBSERVICE data exfiltration). See M9.
func csvSanitize(s string) string {
	if s == "" {
		return s
	}
	// Spreadsheets may strip leading whitespace before parsing a formula, so
	// " =cmd" is still live. Trigger on either the raw first byte or the first
	// non-whitespace byte (C9).
	if isFormulaLead(s[0]) {
		return "'" + s
	}
	if t := strings.TrimLeft(s, " \t\r\n"); t != "" && isFormulaLead(t[0]) {
		return "'" + s
	}
	return s
}

func isFormulaLead(b byte) bool {
	switch b {
	case '=', '+', '-', '@', '\t', '\r':
		return true
	}
	return false
}

// RemoveRoleMember revokes a role from a user.
//
// Effective permissions are the union of tbl_user.role_id and the user's
// tbl_role_member rows, and every account-creation path writes BOTH for the
// primary role. Deleting only the membership row therefore removed the user from
// the role's member list while leaving every permission it granted in place —
// an administrator revoking platform-admin was told it worked and was wrong
// (EU2). So when the revoked role is also the primary one, re-point role_id at a
// role the user still holds.
//
// A user must keep at least one role: role_id is dereferenced when building the
// session (an id pointing at nothing fails the whole /auth/me call), so dropping
// someone's last role would lock the account rather than downgrade it. Removing
// it is refused — assign the replacement role first, then revoke.
func (s *Services) RemoveRoleMember(roleID, userID int64) error {
	u, err := s.Repo.GetUserByID(userID)
	if err != nil {
		return ErrNotFound
	}
	if u.RoleID == roleID {
		held, herr := s.Repo.RoleIDsOfUser(userID)
		if herr != nil {
			return herr
		}
		replacement := int64(0)
		for _, id := range held {
			if id != roleID {
				replacement = id
				break
			}
		}
		if replacement == 0 {
			return ErrBadRequest // would leave the user with no role at all
		}
		if err := s.Repo.UpdateUserFields(userID, map[string]any{"role_id": replacement}); err != nil {
			return err
		}
	}
	if err := s.Repo.RemoveMember(roleID, userID); err != nil {
		return err
	}
	// Revocation must apply to sessions already issued, not just future ones.
	return s.Repo.BumpTokenVersion(userID)
}

// auditAdminAction records an administrative change to an account or role in the
// same hash-chained log as command execution.
//
// These actions are the ones an attacker with an admin session would use to
// borrow another user's identity — reset their password, bind a fresh MFA secret
// — and they were the only privileged operations writing no audit at all, so
// that impersonation left no trace while the resulting approval was faithfully
// recorded against the impersonated approver (EU4). detail should be a stable,
// greppable key plus the target, e.g. `admin.password.reset user=x@y.io`.
//
// No connection is involved, and no webhook event type is emitted: the webhook
// vocabulary is exec/login/intercept/approve, so admin events stay audit-only
// until a subscriber vocabulary exists for them.
func (s *Services) auditAdminAction(actor *model.User, detail string) {
	if actor == nil {
		return
	}
	s.recordAudit(actor, nil, detail, model.RiskMid, model.ResultExecuted, "", "")
}

// AuditRoleChange records a change to what a role may do — the capability
// matrix, menu grants, tag grants or membership. These grants decide every
// permission check in the system, so editing them is a privilege change and
// belongs in the same immutable log as the actions they authorise (EU4).
// Without it, someone could widen a role, act under it, and narrow it back with
// the chain showing only the action and never the grant that permitted it.
func (s *Services) AuditRoleChange(actor *model.User, roleID int64, what string, value any) {
	name := strconv.FormatInt(roleID, 10)
	if r, err := s.Repo.GetRole(roleID); err == nil && r != nil {
		name = r.Code
	}
	detail, _ := json.Marshal(value)
	s.auditAdminAction(actor, "admin.role."+what+" role="+name+" value="+clip(string(detail), 400))
}

// UserTags returns the tags granted directly to a user (empty = the role scope
// applies).
func (s *Services) UserTags(id int64) ([]string, error) {
	if _, err := s.Repo.GetUserByID(id); err != nil {
		return nil, ErrNotFound
	}
	return s.Repo.TagsForUser(id)
}

// SetUserTags scopes one user's data access. This is a privilege change — it
// decides which instances that person can see and execute against — so it is
// audited like the other administrative mutations (EU4).
func (s *Services) SetUserTags(actor *model.User, id int64, tags []string) error {
	target, err := s.Repo.GetUserByID(id)
	if err != nil {
		return ErrNotFound
	}
	if err := s.Repo.SetUserTags(id, tags); err != nil {
		return err
	}
	scope := strings.Join(tags, "|")
	if scope == "" {
		scope = "(role default)"
	}
	s.auditAdminAction(actor, "admin.user.tags scope="+scope+" user="+target.Email)
	return nil
}
