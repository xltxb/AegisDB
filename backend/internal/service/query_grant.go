package service

// 查询授权(业务方数据查询,见 .scratch/biz-data-query/PRD.md)。
//
// 授权由外部审批系统审批,审批完经开放接口下发(scope grant:write)。网关不审批,只负责:
//
//   - 只允许授权到**业务查询源**(标记过的从库连接)上**真实存在**的库;
//   - 行数上限、超时、有效期不能超过全局上限 —— 外部系统填多大都没用;
//   - 被授权人没有网关账号就建一个,只挂「业务查询」角色;
//   - 每一次下发、更新、撤销、到期都写审计,记下是哪个外部系统、哪张外部单、谁批的。
//
// 一个人对一个库只有一条授权,再次下发即更新(见 model.QueryGrant)。

import (
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"aegisdb/internal/apimsg"
	"aegisdb/internal/dto"
	"aegisdb/internal/gateway"
	"aegisdb/internal/model"
	"aegisdb/internal/repository"
)

// 业务查询的全局设置(tbl_setting)。
const (
	SettingBizDefaultMaxRows = "bizQuery.defaultMaxRows"
	SettingBizMaxRowsCap     = "bizQuery.maxRowsCap"
	SettingBizDefaultTimeout = "bizQuery.defaultTimeoutSec"
	SettingBizTimeoutCap     = "bizQuery.timeoutCapSec"
	SettingBizMaxGrantDays   = "bizQuery.maxGrantDays"
	SettingBizExportEnabled  = "bizQuery.exportEnabled"
	// 每人每天最多执行多少次查询 / 导出(PRD 5.7);0 = 不限,也是默认值。
	SettingBizMaxDailyPerUser = "bizQuery.maxDailyPerUser"
	// 申请方式(#173):internal = 在网关里申请,走网关的审批链;external = 去外部审批系统申请,
	// 授权由它经开放接口下发。没设过时是 internal。
	SettingBizApplyMode = "bizQuery.applyMode"
	// 外部申请页:申请方式为 external 时,拦截卡片上的「去申请权限」跳到这里(2026-10-22)。
	// 空串 = 不给链接,只说去审批系统申请。
	SettingBizApplyURL = "bizQuery.applyUrl"
)

// 申请方式的两个取值。
const (
	BizApplyInternal = "internal"
	BizApplyExternal = "external"
)

// 全局值的硬边界:设置界面里能填的范围。再往外就不是"调参",而是换了一个功能。
const (
	bizHardMaxRows    = 100000
	bizHardTimeoutSec = 3600
	bizHardGrantDays  = 365
	bizHardPerUser    = 20
	bizHardPerSource  = 100
	bizHardDaily      = 100000
)

// BizQueryLimits 是一份全局上限的快照。
type BizQueryLimits struct {
	DefaultMaxRows int  `json:"defaultMaxRows"`
	MaxRowsCap     int  `json:"maxRowsCap"`
	DefaultTimeout int  `json:"defaultTimeoutSec"`
	TimeoutCap     int  `json:"timeoutCapSec"`
	MaxGrantDays   int  `json:"maxGrantDays"`
	MaxPerUser     int  `json:"maxConcurrentPerUser"`
	MaxPerSource   int  `json:"maxConcurrentPerSource"`
	ExportEnabled  bool `json:"exportEnabled"`
	MaxDailyPerUser int `json:"maxDailyPerUser"` // 0 = 不限
	ApplyMode       string `json:"applyMode"` // internal | external
	ApplyURL        string `json:"applyUrl"`  // 外部申请页;空 = 不给链接(只在 external 时有意义)
}

// BizQueryLimits 读全局上限,并把明显不合理的值拉回来:默认值不能超过上限。
func (s *Services) BizQueryLimits() BizQueryLimits {
	l := BizQueryLimits{
		DefaultMaxRows: s.settingInt(SettingBizDefaultMaxRows, 1000),
		MaxRowsCap:     s.settingInt(SettingBizMaxRowsCap, 10000),
		DefaultTimeout: s.settingInt(SettingBizDefaultTimeout, 30),
		TimeoutCap:     s.settingInt(SettingBizTimeoutCap, 300),
		MaxGrantDays:   s.settingInt(SettingBizMaxGrantDays, 90),
		MaxPerUser:     s.settingInt(SettingBizMaxPerUser, 2),
		MaxPerSource:   s.settingInt(SettingBizMaxPerSource, 5),
		ExportEnabled:  s.settingBool(SettingBizExportEnabled, false),
		MaxDailyPerUser: s.settingInt(SettingBizMaxDailyPerUser, 0),
		ApplyMode:       s.settingString(SettingBizApplyMode, BizApplyInternal),
		ApplyURL:        strings.TrimSpace(s.settingString(SettingBizApplyURL, "")),
	}
	if l.ApplyMode != BizApplyExternal {
		l.ApplyMode = BizApplyInternal // 认不出的值(被人直接改了库)按默认的网关内申请
	}
	if l.MaxRowsCap < 1 {
		l.MaxRowsCap = 10000
	}
	if l.DefaultMaxRows < 1 || l.DefaultMaxRows > l.MaxRowsCap {
		l.DefaultMaxRows = min(1000, l.MaxRowsCap)
	}
	if l.TimeoutCap < 1 {
		l.TimeoutCap = 300
	}
	if l.DefaultTimeout < 1 || l.DefaultTimeout > l.TimeoutCap {
		l.DefaultTimeout = min(30, l.TimeoutCap)
	}
	if l.MaxGrantDays < 1 {
		l.MaxGrantDays = 90
	}
	if l.MaxPerUser < 1 {
		l.MaxPerUser = 2
	}
	if l.MaxPerSource < 1 {
		l.MaxPerSource = 5
	}
	if l.MaxDailyPerUser < 0 {
		l.MaxDailyPerUser = 0
	}
	return l
}

// ValidateBizQueryLimits 在**保存那一刻**把不合理的全局值挡住(与 SSO 设置同一个做法:任何一项
// 不对,这次一项都不存)。
func ValidateBizQueryLimits(l BizQueryLimits) error {
	chk := func(label apimsg.M, v, lo, hi int) error {
		if v < lo || v > hi {
			return apimsg.Errorf("%s 必须在 %d ~ %d 之间,当前 %d", label, lo, hi, v)
		}
		return nil
	}
	for _, e := range []error{
		chk(apimsg.T("行数上限"), l.MaxRowsCap, 1, bizHardMaxRows),
		chk(apimsg.T("默认行数"), l.DefaultMaxRows, 1, l.MaxRowsCap),
		chk(apimsg.T("超时上限(秒)"), l.TimeoutCap, 1, bizHardTimeoutSec),
		chk(apimsg.T("默认超时(秒)"), l.DefaultTimeout, 1, l.TimeoutCap),
		chk(apimsg.T("授权最长有效期(天)"), l.MaxGrantDays, 1, bizHardGrantDays),
		chk(apimsg.T("每人并发"), l.MaxPerUser, 1, bizHardPerUser),
		chk(apimsg.T("每实例并发"), l.MaxPerSource, 1, bizHardPerSource),
		chk(apimsg.T("每人每天次数(0 = 不限)"), l.MaxDailyPerUser, 0, bizHardDaily),
	} {
		if e != nil {
			return e
		}
	}
	return nil
}

// CheckBizQuerySettings 把一次设置保存里的 bizQuery.* 叠到当前值上整体校验:任何一项不对,调用方
// 这次一项都不该存。返回变更摘要(写审计用;没碰 bizQuery.* 时为空)。
func (s *Services) CheckBizQuerySettings(body map[string]any) (string, error) {
	touched := false
	for k := range body {
		if strings.HasPrefix(k, "bizQuery.") {
			touched = true
			break
		}
	}
	if !touched {
		return "", nil
	}
	next := s.BizQueryLimits()
	num := func(key string, dst *int) error {
		v, ok := body[key]
		if !ok {
			return nil
		}
		switch x := v.(type) {
		case float64:
			if x != float64(int(x)) {
				return apimsg.Errorf("%s 必须是整数", key)
			}
			*dst = int(x)
		case string:
			n, err := strconv.Atoi(strings.TrimSpace(x))
			if err != nil {
				return apimsg.Errorf("%s 必须是整数", key)
			}
			*dst = n
		default:
			return apimsg.Errorf("%s 必须是整数", key)
		}
		return nil
	}
	for key, dst := range map[string]*int{
		SettingBizDefaultMaxRows: &next.DefaultMaxRows, SettingBizMaxRowsCap: &next.MaxRowsCap,
		SettingBizDefaultTimeout: &next.DefaultTimeout, SettingBizTimeoutCap: &next.TimeoutCap,
		SettingBizMaxGrantDays: &next.MaxGrantDays, SettingBizMaxPerUser: &next.MaxPerUser,
		SettingBizMaxPerSource: &next.MaxPerSource, SettingBizMaxDailyPerUser: &next.MaxDailyPerUser,
	} {
		if err := num(key, dst); err != nil {
			return "", err
		}
	}
	if v, ok := body[SettingBizExportEnabled]; ok {
		b, isBool := v.(bool)
		if !isBool {
			return "", errors.New("导出开关必须是布尔值")
		}
		next.ExportEnabled = b
	}
	if err := ValidateBizQueryLimits(next); err != nil {
		return "", err
	}
	return fmt.Sprintf("rows=%d/%d timeout=%d/%ds days=%d concurrent=%d/%d daily=%d export=%v",
		next.DefaultMaxRows, next.MaxRowsCap, next.DefaultTimeout, next.TimeoutCap, next.MaxGrantDays,
		next.MaxPerUser, next.MaxPerSource, next.MaxDailyPerUser, next.ExportEnabled), nil
}

// ErrGrantInvalid 包着一条说得出"哪里不对"的原因,handler 原样回给外部系统。
var ErrGrantInvalid = errors.New("invalid query grant")

func grantInvalid(format string, a ...any) error {
	return apimsg.Errorf("%w: %s", ErrGrantInvalid, apimsg.Errorf(format, a...))
}

// ---------------------------------------------------------------- 可授权的实例与库

// QuerySources 列出这个调用方(外部系统的服务账号)能授权的业务查询源。
func (s *Services) QuerySources(actor *model.User) []dto.QuerySourceView {
	conns, err := s.Repo.BizQuerySources()
	out := []dto.QuerySourceView{}
	if err != nil {
		return out
	}
	for i := range conns {
		c := &conns[i]
		if !s.canAccessConn(actor, c) {
			continue
		}
		out = append(out, dto.QuerySourceView{ID: c.ID, Name: c.Name, Engine: c.Engine, Env: c.Env, Status: c.Status})
	}
	return out
}

// querySource 取一个业务查询源,并确认调用方看得到它。
func (s *Services) querySource(actor *model.User, id int64) (*model.Connection, error) {
	conn, err := s.Repo.GetConnection(id)
	if err != nil {
		return nil, ErrNotFound
	}
	if !conn.BizQuery {
		// 与"不存在"分开说:外部系统填了一台真实存在、但不是业务查询源的实例(多半是主库),
		// 要让它知道是哪里错了。
		return nil, grantInvalid("实例 %s 不是业务查询源,不能授权(业务查询只开放标记过的从库)", conn.Name)
	}
	if !s.canAccessConn(actor, conn) {
		return nil, ErrForbidden
	}
	return conn, nil
}

// SourceDatabases 列出一个业务查询源上的库名,下发授权时也拿它校验 database。
//
// 真实实例现查:元数据缓存只从表清单汇总,新库要等下一轮同步(定时同步默认还是关的),没有表的库
// 永远不在里面,外部审批系统于是选不到、也授不了刚建的库。查的是查询实际要打到的那台库。
//
// 现查失败或一个库都没读到才退回缓存,并留日志:实例暂时连不上时,下拉不该整个空掉。
func (s *Services) SourceDatabases(actor *model.User, id int64) ([]string, error) {
	conn, err := s.querySource(actor, id)
	if err != nil {
		return nil, err
	}
	if gateway.RealExecSupported(conn) {
		return s.liveSourceDatabases(conn)
	}
	// 没有凭据的开发 / 演示连接:只有缓存和种子树。
	names, err := s.Repo.MetaDatabaseNames(conn.ID)
	if err == nil && len(names) > 0 {
		return names, nil
	}
	sc := s.ConnectionSchema(actor, conn.ID, "")
	if sc.Error != "" {
		return nil, apimsg.Errorf("无法读取实例 %s 的库列表: %s", conn.Name, sc.Error)
	}
	out := make([]string, 0, len(sc.Databases))
	for _, d := range sc.Databases {
		out = append(out, d.Name)
	}
	return out, nil
}

func (s *Services) liveSourceDatabases(conn *model.Connection) ([]string, error) {
	target, _, _ := s.bizTarget(conn)
	names, err := gateway.DiffDatabaseNames(target)
	if err == nil && len(names) > 0 {
		return names, nil
	}
	cached, cerr := s.Repo.MetaDatabaseNames(conn.ID)
	if cerr == nil && len(cached) > 0 {
		slog.Warn("业务查询源现查库名失败,退回元数据缓存", "conn", conn.Name, "err", err)
		return cached, nil
	}
	if err == nil {
		return []string{}, nil // 连得上,这个账号确实一个库也看不到
	}
	return nil, apimsg.Errorf("无法读取实例 %s 的库列表: %w", conn.Name, err)
}

// ---------------------------------------------------------------- 下发 / 撤销 / 查询

// UpsertQueryGrant 按 (邮箱, 实例, 库) 创建或更新一条授权。created 表示这次是新建。
func (s *Services) UpsertQueryGrant(client *model.APIClient, actor *model.User, req dto.QueryGrantReq) (*dto.QueryGrantView, bool, error) {
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email == "" || !strings.Contains(email, "@") || len(email) > 128 {
		return nil, false, grantInvalid("email 不是有效的邮箱")
	}
	ext := strings.TrimSpace(req.ExternalID)
	if ext == "" || len(ext) > 128 {
		return nil, false, grantInvalid("externalId 必填,且不超过 128 个字符(外部审批单号)")
	}
	conn, err := s.querySource(actor, req.SourceID)
	if err != nil {
		return nil, false, err
	}
	dbName := strings.TrimSpace(req.Database)
	if dbName == "" {
		return nil, false, grantInvalid("database 必填")
	}
	dbs, err := s.SourceDatabases(actor, conn.ID)
	if err != nil {
		return nil, false, err
	}
	if !containsStr(dbs, dbName) {
		return nil, false, grantInvalid("实例 %s 上没有库 %s(以 GET /open/query-sources/%d/databases 为准,区分大小写)", conn.Name, dbName, conn.ID)
	}

	lim := s.BizQueryLimits()
	now := time.Now()
	starts := now
	if req.StartsAt != nil {
		starts = *req.StartsAt
	}
	if req.EndsAt == nil {
		return nil, false, grantInvalid("endsAt 必填:授权必须有到期时间")
	}
	ends := *req.EndsAt
	if err := checkGrantLimits(req.MaxRows, req.TimeoutSec, starts, ends, now, lim); err != nil {
		return nil, false, err
	}

	user, err := s.grantee(email, req.Name, req.Dept, actor, client)
	if err != nil {
		return nil, false, err
	}

	g, gerr := s.Repo.GetQueryGrantByKey(user.ID, conn.ID, dbName)
	created := false
	switch {
	case repository.IsNotFound(gerr):
		g = &model.QueryGrant{UserID: user.ID, ConnectionID: conn.ID, DBName: dbName}
		created = true
	case gerr != nil:
		return nil, false, gerr
	case g.ClientID == client.ID && g.ExternalID == ext:
		// 同一张审批单重发(网络重试):幂等,原样返回。不改条件,也不让已撤销的授权复活 ——
		// 撤销之后要恢复,得有一张新的审批单。
		_ = s.Repo.RecordQueryGrantApproval(client.ID, ext, g.ID) // 旧数据补记
		v := s.grantView(g, user, conn, lim, now)
		return &v, false, nil
	default:
		// 别的凭据下发的授权:原凭据还在且启用,就不许覆盖 —— 否则条件被改、归属也改成这把,
		// 原系统此后既列不出也撤不掉它(2026-10 安全审计 M3)。原凭据已删 / 已停用时允许接管,
		// 这是凭据轮换的正常路径。
		if g.ClientID != client.ID && g.ClientID != 0 {
			if owner, oerr := s.Repo.GetAPIClient(g.ClientID); oerr == nil && owner.Enabled {
				return nil, false, grantInvalid("该授权(#%d)由外部系统 %s 下发,只能由它修改或撤销", g.ID, owner.Name)
			}
		}
		used, err := s.Repo.QueryGrantApprovalUsed(client.ID, ext, g.ID)
		if err != nil {
			return nil, false, err
		}
		if used {
			// 这张单早先作用过这条授权,后来被别的单取代了:多半是一次延迟到达的重试。
			// 照做就会把新批的条件改回旧的。
			by := apimsg.T("更新的审批")
			if g.ClientID == client.ID {
				by = apimsg.Sprintf("审批单 %s", g.ExternalID)
			}
			return nil, false, grantInvalid("审批单 %s 已被%s取代,不能再用它修改授权 #%d", ext, by, g.ID)
		}
		// 迁移 0044 之前的授权没有历史:先把它当前的单号记上,之后它再发过来就认得出是旧单。
		if g.ExternalID != "" {
			_ = s.Repo.RecordQueryGrantApproval(g.ClientID, g.ExternalID, g.ID)
		}
	}
	g.MaxRows, g.TimeoutSec, g.AllowExport = req.MaxRows, req.TimeoutSec, req.AllowExport
	g.StartsAt, g.EndsAt = starts, ends
	g.Status, g.RevokedAt, g.RevokedBy = model.GrantActive, nil, ""
	g.ClientID, g.ClientName, g.ExternalID = client.ID, fitRunes(client.Name, 64), ext
	g.Approver, g.Reason = fitRunes(strings.TrimSpace(req.Approver), 128), fitRunes(strings.TrimSpace(req.Reason), 512)
	if err := s.Repo.SaveQueryGrant(g); err != nil {
		return nil, false, err
	}
	if err := s.Repo.RecordQueryGrantApproval(client.ID, ext, g.ID); err != nil {
		return nil, false, err
	}

	verb := "更新"
	if created {
		verb = "下发"
	}
	s.recordAuditBy(actor, apiOperator(client), conn, fmt.Sprintf(
		"bizquery.grant %s查询授权 #%d:%s → %s/%s,行数上限 %s,超时 %s,导出 %v,有效期 %s ~ %s · 外部系统 %s · 外部单号 %s · 审批人 %s",
		verb, g.ID, email, conn.Name, dbName, limitText(g.MaxRows, lim.DefaultMaxRows), limitText(g.TimeoutSec, lim.DefaultTimeout)+"s",
		g.AllowExport, starts.Format("2006-01-02 15:04"), ends.Format("2006-01-02 15:04"),
		client.Name, ext, orDash(g.Approver)), model.RiskMid, model.ResultExecuted, "", "")
	v := s.grantView(g, user, conn, lim, now)
	return &v, created, nil
}

// checkGrantLimits 是一条授权的条件要过的全局上限与有效期检查。开放接口、网关内审批、控制台
// 直接开通三条路都过它:哪条路批出来的授权,都不能比全局上限更宽。
func checkGrantLimits(maxRows, timeoutSec int, starts, ends, now time.Time, lim BizQueryLimits) error {
	if maxRows < 0 || maxRows > lim.MaxRowsCap {
		return grantInvalid("maxRows 不能超过全局上限 %d(0 或不填表示用默认值 %d)", lim.MaxRowsCap, lim.DefaultMaxRows)
	}
	if timeoutSec < 0 || timeoutSec > lim.TimeoutCap {
		return grantInvalid("timeoutSec 不能超过全局上限 %d 秒(0 或不填表示用默认值 %d)", lim.TimeoutCap, lim.DefaultTimeout)
	}
	if !ends.After(starts) {
		return grantInvalid("endsAt 必须晚于 startsAt")
	}
	if !ends.After(now) {
		return grantInvalid("endsAt 已经过去了")
	}
	if ends.Sub(starts) > time.Duration(lim.MaxGrantDays)*24*time.Hour {
		return grantInvalid("有效期不能超过 %d 天", lim.MaxGrantDays)
	}
	return nil
}

// ErrBizNoLocalPassword:只挂业务查询角色的账号不设本地口令(PRD 决策 10)。
var ErrBizNoLocalPassword = apimsg.Errorf("%w: 业务查询账号不设本地口令,业务方通过 SSO 登录进入网关", ErrBadRequest)

// bizQueryOnly 报告这个账号是不是**只有**业务查询角色(业务方)。已有别的角色、因授权追加了
// 业务查询角色的人不算 —— 他们原来怎么登录还怎么登录。
func (s *Services) bizQueryOnly(u *model.User) bool {
	return u != nil && s.onlyBizQueryRole(s.Repo.EffectiveRoleIDs(u))
}

func (s *Services) onlyBizQueryRole(ids []int64) bool {
	role, err := s.Repo.GetRoleByCode(model.RoleCodeBizQuery)
	if err != nil || len(ids) == 0 {
		return false
	}
	for _, id := range ids {
		if id != role.ID {
			return false
		}
	}
	return true
}

// grantee 按邮箱找被授权人;没有就建一个只挂业务查询角色的账号。已有账号没有这个角色就加上。
func (s *Services) grantee(email, name, dept string, actor *model.User, client *model.APIClient) (*model.User, error) {
	role, err := s.Repo.GetRoleByCode(model.RoleCodeBizQuery)
	if err != nil {
		return nil, apimsg.Errorf("业务查询角色不存在,请重启网关以完成初始化: %w", err)
	}
	u, err := s.Repo.FindUserByEmailCI(email)
	if repository.IsNotFound(err) {
		display := strings.TrimSpace(name)
		if display == "" {
			display = email[:strings.Index(email, "@")]
		}
		display = fitRunes(display, 64)
		d := strings.TrimSpace(dept)
		if d == "" {
			d = "业务方"
		}
		u = &model.User{
			Name: display, Email: email, Status: "active", Kind: model.UserKindHuman,
			RoleID: role.ID,
			// 没有本地口令,也设不上(bizQueryOnly):业务方经 SSO 登录(PRD 决策 10)。
			PasswordHash: "",
			Initials:     initials(display), Dept: fitRunes(d, 64), LastActive: "—",
		}
		if err := s.Repo.CreateUser(u); err != nil {
			// 并发下发同一个人:另一边已经建好了,拿它就是。
			if existing, gerr := s.Repo.FindUserByEmailCI(email); gerr == nil {
				u = existing
			} else {
				return nil, err
			}
		} else {
			if err := s.Repo.SetUserRoles(u.ID, []int64{role.ID}); err != nil {
				return nil, err
			}
			s.recordAuditBy(actor, apiOperator(client), nil, fmt.Sprintf("bizquery.provision 为查询授权自动开通账号 %s(只有业务查询角色)· 外部系统 %s", email, client.Name),
				model.RiskMid, model.ResultExecuted, "", "")
			return u, nil
		}
	} else if err != nil {
		return nil, err
	}
	return u, s.ensureBizQueryRole(u, role, actor, apiOperator(client), "外部系统 "+client.Name)
}

// ensureBizQueryRole 确认一个已有账号可以被授予查询权,并在需要时追加业务查询角色。
// via 写进审计,说明是谁让它追加的(外部系统 / 网关审批 / 控制台)。
func (s *Services) ensureBizQueryRole(u *model.User, role *model.Role, actor *model.User, operator, via string) error {
	if u.Kind == model.UserKindService {
		return grantInvalid("%s 是服务账号,不能被授予查询权", u.Email)
	}
	if u.Status == "disabled" {
		return grantInvalid("%s 的网关账号已停用,不能授权", u.Email)
	}
	if role == nil {
		r, err := s.Repo.GetRoleByCode(model.RoleCodeBizQuery)
		if err != nil {
			return apimsg.Errorf("业务查询角色不存在,请重启网关以完成初始化: %w", err)
		}
		role = r
	}
	ids := s.Repo.EffectiveRoleIDs(u)
	for _, id := range ids {
		if id == role.ID {
			return nil
		}
	}
	// 主角色保持不变(排在第一个),业务查询角色追加在后。
	if err := s.Repo.SetUserRoles(u.ID, append(ids, role.ID)); err != nil {
		return err
	}
	s.recordAuditBy(actor, operator, nil, fmt.Sprintf("bizquery.role 为 %s 追加业务查询角色(因查询授权)· %s", u.Email, via),
		model.RiskMid, model.ResultExecuted, "", "")
	return nil
}

// RevokeQueryGrant 外部系统撤销授权。按 id,或按外部单号 —— 两种都只在这个外部系统**自己下发**
// 的授权里找:一把 grant:write 凭据不能撤掉别的系统批出去的授权。别人的授权回"不存在",
// 不透露它在不在。
func (s *Services) RevokeQueryGrant(client *model.APIClient, actor *model.User, id int64, externalID, reason string) ([]dto.QueryGrantView, error) {
	var targets []model.QueryGrant
	switch {
	case id > 0:
		g, err := s.Repo.GetQueryGrant(id)
		if err != nil || g.ClientID != client.ID {
			return nil, ErrNotFound
		}
		targets = []model.QueryGrant{*g}
	case strings.TrimSpace(externalID) != "":
		gs, err := s.Repo.ListQueryGrants(repository.QueryGrantFilter{ExternalID: strings.TrimSpace(externalID), ClientID: client.ID})
		if err != nil {
			return nil, err
		}
		if len(gs) == 0 {
			return nil, ErrNotFound
		}
		targets = gs
	default:
		return nil, grantInvalid("请指定授权 id 或 externalId")
	}
	return s.revokeGrants(actor, apiOperator(client), targets, client.Name, "外部系统 "+client.Name, reason)
}

// apiOperator 是开放接口写审计时的 operator:"API:<凭据名>"(ADR-0006)。actor 是凭据绑定的
// 服务账号,operator 说明是哪个外部系统在做 —— 同一个服务账号可以绑好几把凭据。
func apiOperator(client *model.APIClient) string {
	if client == nil || client.Name == "" {
		return ""
	}
	return "API:" + client.Name
}

// AdminRevokeQueryGrant 管理员在控制台撤销一条授权(授权总表上的「撤销」)。
//
// 授权的记录方是外部审批系统,但离职、误授、安全事件时网关这边必须有一个立即生效的开关,
// 不能等外部系统。撤销会写审计;外部系统之后再下发同一个人同一个库,会重新生效。
func (s *Services) AdminRevokeQueryGrant(actor *model.User, id int64, reason string) (*dto.QueryGrantView, error) {
	g, err := s.Repo.GetQueryGrant(id)
	if err != nil {
		return nil, ErrNotFound
	}
	out, err := s.revokeGrants(actor, "", []model.QueryGrant{*g}, actor.Name, "管理员 "+actor.Name, reason)
	if err != nil || len(out) == 0 {
		return nil, err
	}
	return &out[0], nil
}

func (s *Services) revokeGrants(actor *model.User, operator string, targets []model.QueryGrant, by, byText, reason string) ([]dto.QueryGrantView, error) {
	lim := s.BizQueryLimits()
	now := time.Now()
	out := []dto.QueryGrantView{}
	for i := range targets {
		g := &targets[i]
		conn, _ := s.Repo.GetConnection(g.ConnectionID)
		user, _ := s.Repo.GetUserByID(g.UserID)
		if g.Status == model.GrantActive {
			g.Status, g.RevokedAt, g.RevokedBy = model.GrantRevoked, &now, fitRunes(by, 128)
			if err := s.Repo.SaveQueryGrant(g); err != nil {
				return nil, err
			}
			email, inst := "?", "?"
			if user != nil {
				email = user.Email
			}
			if conn != nil {
				inst = conn.Name
			}
			s.recordAuditBy(actor, operator, conn, fmt.Sprintf("bizquery.revoke 撤销查询授权 #%d:%s → %s/%s · %s · 原因 %s",
				g.ID, email, inst, g.DBName, byText, orDash(fitRunes(strings.TrimSpace(reason), 200))),
				model.RiskMid, model.ResultExecuted, "", "")
		}
		out = append(out, s.grantView(g, user, conn, lim, now))
	}
	return out, nil
}

// QueryGrantQuery 是查询授权列表的筛选条件(开放接口与控制台共用)。
type QueryGrantQuery struct {
	Dept       string // 部门(包含匹配),控制台总表用
	Email      string
	SourceID   int64
	Database   string
	Status     string // active / revoked / expired(按生效状态,不是存下来的状态)
	ExternalID string
	ClientID   int64 // 开放接口:只列这把凭据下发的授权(0 = 不限,控制台用)
}

func (s *Services) ListQueryGrants(q QueryGrantQuery) ([]dto.QueryGrantView, error) {
	f := repository.QueryGrantFilter{ConnectionID: q.SourceID, DBName: strings.TrimSpace(q.Database),
		ExternalID: strings.TrimSpace(q.ExternalID), ClientID: q.ClientID}
	out := []dto.QueryGrantView{}
	if e := strings.TrimSpace(q.Email); e != "" {
		u, err := s.Repo.FindUserByEmailCI(e)
		if err != nil {
			return out, nil // 没有这个人,自然没有授权
		}
		f.UserID = u.ID
	}
	gs, err := s.Repo.ListQueryGrants(f)
	if err != nil {
		return nil, err
	}
	lim := s.BizQueryLimits()
	now := time.Now()
	users := map[int64]*model.User{}
	conns := map[int64]*model.Connection{}
	for i := range gs {
		g := &gs[i]
		if _, ok := users[g.UserID]; !ok {
			users[g.UserID], _ = s.Repo.GetUserByID(g.UserID)
		}
		if _, ok := conns[g.ConnectionID]; !ok {
			conns[g.ConnectionID], _ = s.Repo.GetConnection(g.ConnectionID)
		}
		v := s.grantView(g, users[g.UserID], conns[g.ConnectionID], lim, now)
		if q.Status != "" && v.Status != q.Status {
			continue
		}
		if d := strings.TrimSpace(q.Dept); d != "" && !strings.Contains(strings.ToLower(v.Dept), strings.ToLower(d)) {
			continue
		}
		out = append(out, v)
	}
	return out, nil
}

// LiveQueryGrant 返回某人此刻对某实例某库生效的授权;没有返回 nil。数据查询的执行判定用它。
func (s *Services) LiveQueryGrant(userID, connID int64, db string) *model.QueryGrant {
	g, err := s.Repo.GetQueryGrantByKey(userID, connID, db)
	if err != nil || !g.LiveAt(time.Now()) {
		return nil
	}
	return g
}

// ExpireQueryGrants 把已过有效期的授权标成 expired,并写审计。判定本身不依赖它(LiveAt 会
// 比时间),它让列表和审计里的状态与事实一致。由后台每分钟调用一次。
func (s *Services) ExpireQueryGrants() {
	now := time.Now()
	gs, err := s.Repo.ActiveQueryGrantsEndedBefore(now)
	if err != nil {
		return
	}
	for i := range gs {
		g := &gs[i]
		ok, err := s.Repo.MarkQueryGrantExpired(g.ID, now)
		if err != nil || !ok {
			continue
		}
		user, _ := s.Repo.GetUserByID(g.UserID)
		conn, _ := s.Repo.GetConnection(g.ConnectionID)
		if user == nil {
			continue
		}
		inst := "?"
		if conn != nil {
			inst = conn.Name
		}
		s.recordAuditBy(user, "查询授权到期", conn, fmt.Sprintf("bizquery.expire 查询授权 #%d 到期失效:%s → %s/%s(到期时间 %s)",
			g.ID, user.Email, inst, g.DBName, g.EndsAt.Format("2006-01-02 15:04")), model.RiskLow, model.ResultExecuted, "", "")
	}
}

func (s *Services) grantView(g *model.QueryGrant, u *model.User, c *model.Connection, lim BizQueryLimits, now time.Time) dto.QueryGrantView {
	v := dto.QueryGrantView{
		ID: g.ID, SourceID: g.ConnectionID, Database: g.DBName,
		MaxRows: g.MaxRows, TimeoutSec: g.TimeoutSec, AllowExport: g.AllowExport,
		EffectiveMaxRows: effective(g.MaxRows, lim.DefaultMaxRows), EffectiveTimeoutSec: effective(g.TimeoutSec, lim.DefaultTimeout),
		StartsAt: g.StartsAt, EndsAt: g.EndsAt, Status: g.Status,
		ExternalID: g.ExternalID, Client: g.ClientName, Approver: g.Approver, Reason: g.Reason,
		RevokedAt: g.RevokedAt, RevokedBy: g.RevokedBy, UpdatedAt: g.UpdatedAt,
	}
	// 此刻能不能导出:实例所在分层受数据查询导出限制时,全局开关与授权「允许导出」都要开;
	// 不受限(默认 dev / uat)时有授权就能导。与 prepareBizQuery 的判断同一口径。
	v.CanExport = !s.tierBizExportRestricted(c) || (lim.ExportEnabled && g.AllowExport)
	// 列表上的状态是**生效状态**:记为 active 但已过期(过期任务还没跑到)也显示 expired;
	// 还没到开始时间的显示 pending。
	if g.Status == model.GrantActive {
		switch {
		case !now.Before(g.EndsAt):
			v.Status = model.GrantExpired
		case now.Before(g.StartsAt):
			v.Status = model.GrantPending
		}
	}
	if u != nil {
		v.Email, v.UserName, v.Dept = u.Email, u.Name, u.Dept
	}
	if c != nil {
		v.Source = c.Name
	}
	return v
}

// fitRunes 按字符(不是字节)截到列宽以内,不加省略号 —— 加了会比列宽多一个字符,
// MySQL 严格模式下直接写不进去。
func fitRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func effective(v, def int) int {
	if v > 0 {
		return v
	}
	return def
}

func limitText(v, def int) string {
	if v > 0 {
		return fmt.Sprint(v)
	}
	return fmt.Sprintf("默认(%d)", def)
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func containsStr(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// SetConnectionBizQuery 设置一台实例的「业务查询源」标记。
//
// 取消标记不会自动撤销这台实例上已有的授权:授权是外部审批系统的记录,网关不替它做决定。
// 但取消后这些授权**不再生效**(执行判定要求实例仍是业务查询源),并写审计说明影响了几条。
func (s *Services) SetConnectionBizQuery(actor *model.User, id int64, on bool) error {
	conn, err := s.Repo.GetConnection(id)
	if err != nil {
		return apimsg.Errorf("连接不存在")
	}
	if conn.BizQuery == on {
		return nil
	}
	if err := s.Repo.SetConnectionBizQuery(id, on); err != nil {
		return err
	}
	conn.BizQuery = on
	detail := fmt.Sprintf("bizquery.source 标记 %s 为业务查询源(应指向从库、使用只读库账号)", conn.Name)
	if !on {
		gs, _ := s.Repo.ListQueryGrants(repository.QueryGrantFilter{ConnectionID: id, Status: model.GrantActive})
		detail = fmt.Sprintf("bizquery.source 取消 %s 的业务查询源标记,其上 %d 条查询授权随之不再生效", conn.Name, len(gs))
	}
	s.recordAudit(actor, conn, detail, model.RiskMid, model.ResultExecuted, "", "")
	return nil
}

// AuditSettingsChange 记一条设置变更的管理审计(例如数据查询的全局上限)。
func (s *Services) AuditSettingsChange(actor *model.User, detail string) { s.auditAdminAction(actor, detail) }
