package service

import (
	"strconv"
	"strings"
	"time"

	"velagateway/internal/dto"
	"velagateway/internal/model"
	"velagateway/internal/repository"
	"velagateway/pkg/crypto"
)

// ---------------------------------------------------------------- Connections

func (s *Services) CreateConnection(req dto.ConnectionCreateReq) (*model.Connection, error) {
	host, port := splitHostPort(req.Host)
	env := strings.ToLower(req.Env)
	layer := map[string]string{"prod": "L1 核心 · 写", "staging": "L3 预发", "dev": "L4 沙盒"}[env]
	role := "dba_l2"
	if env == model.EnvDev {
		role = "developer"
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

// AccessibleConnections returns the connections a user's role may see: all of
// them when the role has no tags (unrestricted, e.g. admin), otherwise only the
// connections whose tags intersect the role's granted tags.
func (s *Services) AccessibleConnections(u *model.User) ([]model.Connection, error) {
	all, err := s.Repo.ListConnections()
	if err != nil {
		return nil, err
	}
	if u == nil {
		return all, nil
	}
	allow := s.Repo.TagsForRole(u.RoleID)
	if len(allow) == 0 {
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
	allow := s.Repo.TagsForRole(u.RoleID)
	if len(allow) == 0 {
		return true // unrestricted
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

// DefaultApprovers returns the fallback approval chain (DBA-owner role members).
func (s *Services) DefaultApprovers() []dto.MemberDTO {
	out := []dto.MemberDTO{}
	owner, err := s.Repo.GetRoleByCode("owner")
	if err != nil {
		return out
	}
	members, _ := s.Repo.MembersOfRole(owner.ID)
	for _, m := range members {
		out = append(out, dto.MemberDTO{ID: m.ID, Name: m.Name, Initials: m.Initials, Dept: m.Dept})
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
		mds = append(mds, dto.MemberDTO{ID: m.ID, Name: m.Name, Initials: m.Initials, Dept: m.Dept})
		ids = append(ids, m.ID)
	}
	return &dto.RoleDetailResp{
		ID: role.ID, Code: role.Code, Name: role.Name, Layer: role.Layer, Icon: role.Icon,
		Menus: menus, Matrix: matrix, Members: mds, MemberIDs: ids, Tags: s.Repo.TagsForRole(id),
	}, nil
}

// ---------------------------------------------------------------- Users

func (s *Services) UsersView() ([]dto.UserView, error) {
	users, err := s.Repo.ListUsers()
	if err != nil {
		return nil, err
	}
	out := []dto.UserView{}
	for _, u := range users {
		roles, _ := s.Repo.RolesOfUser(u.ID)
		out = append(out, dto.UserView{
			ID: u.ID, Name: u.Name, Email: u.Email, Initials: u.Initials, Dept: u.Dept,
			Roles: roles, Status: u.Status, MFAEnabled: u.MFAEnabled && u.MFASecret != "", LastActive: u.LastActive,
		})
	}
	return out, nil
}

func (s *Services) PatchUser(id int64, req dto.UserPatchReq) error {
	if _, err := s.Repo.GetUserByID(id); err != nil {
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
	}
	if req.RoleID != nil {
		fields["role_id"] = *req.RoleID
	}
	return s.Repo.UpdateUserFields(id, fields)
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
		out = append(out, dto.RiskCommandView{Command: cmd, Env: grouped[cmd]})
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
	role, err := s.Repo.GetRole(u.RoleID)
	if err != nil || role == nil {
		return false
	}
	switch role.Code {
	case "admin", "owner", "audit":
		return true
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

func (s *Services) ListAudit(u *model.User, risk string, since time.Time, limit int) ([]model.AuditLog, error) {
	return s.Repo.ListAudit(s.auditActor(u), risk, since, limit)
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

// ExportCSV renders the audit rows as CSV (scoped to the user's own commands
// unless they hold an oversight role).
func (s *Services) ExportCSV(u *model.User, risk string, since time.Time) (string, error) {
	rows, err := s.Repo.ListAudit(s.auditActor(u), risk, since, 0)
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
