package service

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"aegisdb/internal/apimsg"
	"aegisdb/internal/dto"
	"aegisdb/internal/gateway"
	"aegisdb/internal/model"
	"aegisdb/internal/repository"
	"aegisdb/pkg/crypto"
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
	// 新建与编辑同一套校验(工单 02 第 10 条):从前编辑挡非法 policy,新建却照单全收。
	if !validPolicies[req.Policy] {
		return nil, invalidf("网关策略无效:%s(只能是 strict / approve-1 / audit-only)", req.Policy)
	}
	if !gateway.IsSupportedEngine(req.Engine) {
		return nil, invalidf("不支持的数据库引擎:%s", req.Engine)
	}
	host, port, err := createHostPort(req.Host, req.Port)
	if err != nil {
		return nil, err
	}
	if err := checkConnHost(req.Engine, host); err != nil {
		return nil, err
	}
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
		Env: env, Policy: req.Policy, DefaultRole: role, Layer: layer, Status: model.ConnStatusOnline,
		Username: strings.TrimSpace(req.Username), Password: encPw, Database: strings.TrimSpace(req.Database),
		Tags: strings.Join(repository.SplitTags(req.Tags), ","),
	}
	if err := s.Repo.CreateConnection(c); err != nil {
		return nil, err
	}
	return c, nil
}

// createHostPort 把新建表单的 host(可带 :port)与单独的 port 合成一份。两处都给且不一致时
// 拒绝 —— 猜哪个是对的,猜错了就是连到另一台库上。
//
// 没给 port(或给的是 0,即"没填")时完全照旧走 splitHostPort(SQLite 的 "localhost:0"
// 之类存量写法不受影响)。
// checkConnHost 校验连接地址的格式(2026-11 审计 V-07):连接串是拼出来的,地址里混进 / ? # 就能带上
// 驱动选项。空地址不在这里管,原有的必填校验会说清楚。
func checkConnHost(engine, host string) error {
	if host != "" && gateway.HostChecked(engine) && !gateway.ValidHost(host) {
		return invalidf("连接地址格式不对:%s(只能是域名或 IP)", host)
	}
	return nil
}

func createHostPort(raw string, port *int) (string, int, error) {
	if port == nil || *port == 0 {
		h, p := splitHostPort(raw)
		return h, p, nil
	}
	if *port <= 0 || *port > 65535 {
		return "", 0, invalidf("端口超出范围:%d", *port)
	}
	raw = strings.TrimSpace(raw)
	if i := strings.LastIndex(raw, ":"); i > 0 {
		if p, err := strconv.Atoi(raw[i+1:]); err == nil {
			if p != 0 && p != *port {
				return "", 0, invalidf("地址里的端口 %d 与 port %d 不一致", p, *port)
			}
			return raw[:i], *port, nil
		}
	}
	return raw, *port, nil
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
	// 引擎只在**改了**的时候把关:存量连接上可能有历史写法的引擎标签,编辑别的字段不该因此被挡。
	if strings.TrimSpace(req.Engine) != c.Engine && !gateway.IsSupportedEngine(req.Engine) {
		return nil, invalidf("不支持的数据库引擎:%s", req.Engine)
	}
	env := strings.ToLower(strings.TrimSpace(req.Env))
	layer, role, err := s.connEnvMeta(env)
	if err != nil {
		return nil, err
	}
	// 拨号要素变了,上一次的巡检结论说的就是另一台库了 —— 清掉。不清的话,改好密码
	// 的实例会带着旧的"认证失败"被定时巡检永久跳过。
	host, port := splitHostPort(req.Host)
	if err := checkConnHost(req.Engine, host); err != nil {
		return nil, err
	}
	// 口令留空 = 沿用已存的口令,但只在地址没变时。地址变了还沿用,网关就会把存着的口令发往
	// 新地址 —— 能改连接配置、却看不到口令的人把地址指向自己的机器就能收到它(2026-10 安全审计 M4)。
	pwKept := strings.TrimSpace(req.Password) == ""
	if pwKept && c.Password != "" && (!strings.EqualFold(c.Host, host) || c.Port != port) {
		return nil, invalidf("连接地址改了,请重新输入口令(已保存的口令不会发往新地址)")
	}
	dialChanged := c.Host != host || c.Port != port ||
		c.Username != strings.TrimSpace(req.Username) ||
		c.Database != strings.TrimSpace(req.Database) ||
		strings.TrimSpace(req.Password) != ""
	c.Name = strings.TrimSpace(req.Name)
	c.Engine = strings.TrimSpace(req.Engine)
	c.Host, c.Port = host, port
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
	if err := applyReplicaConfig(c, req, pwKept); err != nil {
		return nil, err
	}
	if err := s.Repo.UpdateConnection(c); err != nil {
		return nil, err
	}
	if dialChanged {
		if err := s.Repo.ClearConnCheck(c.ID); err != nil {
			return nil, err
		}
		c.LastCheckAt, c.LastCheckOK, c.LastCheckErr, c.LastCheckKind = nil, nil, "", ""
	}
	return c, nil
}

// applyReplicaConfig 把编辑表单里的从库配置写到连接上(见 model.Connection.ReplicaHost)。
// mainPwKept:这次编辑主库口令留空(沿用)。从库地址变了时,它要用的口令(自己的,或沿用主库的)
// 都得重新输入,理由同 UpdateConnection(M4)。
func applyReplicaConfig(c *model.Connection, req dto.ConnectionUpdateReq, mainPwKept bool) error {
	if req.ReplicaHost == nil {
		return nil // 老客户端不带这组字段:不动
	}
	hp := strings.TrimSpace(*req.ReplicaHost)
	if hp == "" {
		c.ReplicaHost, c.ReplicaPort, c.ReplicaUsername, c.ReplicaPassword = "", 0, "", ""
		return nil
	}
	host, port := hp, 0 // 不带端口 = 沿用主库端口(不能套 splitHostPort 的 3306 默认)
	if i := strings.LastIndex(hp, ":"); i > 0 {
		p, err := strconv.Atoi(hp[i+1:])
		if err != nil || p <= 0 || p > 65535 {
			return invalidf("从库端口不对:%s", hp[i+1:])
		}
		host, port = hp[:i], p
	}
	if err := checkConnHost(req.Engine, host); err != nil {
		return err
	}
	moved := !strings.EqualFold(c.ReplicaHost, host) || c.ReplicaPort != port
	user := strings.TrimSpace(req.ReplicaUsername)
	// 与主库同一个地址:只有换了账号才有意义 —— 没有主从之分的架构(TiDB 等)用一个只读账号承接
	// 数据查询(2026-10-29)。同地址同账号就是主库本身,配了等于没配。
	if strings.EqualFold(host, c.Host) && (port == 0 || port == c.Port) {
		if user == "" || strings.EqualFold(user, c.Username) {
			return invalidf("从库地址与主库相同:没有从库的架构(如 TiDB)请另填一个只读账号,数据查询就只用这个账号")
		}
		if strings.TrimSpace(req.ReplicaPassword) == "" && (c.ReplicaPassword == "" || moved || !strings.EqualFold(c.ReplicaUsername, user)) {
			return invalidf("同一地址上的只读账号请填写它自己的口令")
		}
	}
	if moved {
		if user == "" && mainPwKept && c.Password != "" {
			return invalidf("从库地址改了且沿用主库账号,请重新输入主库口令(已保存的口令不会发往新地址)")
		}
		if user != "" && strings.TrimSpace(req.ReplicaPassword) == "" && c.ReplicaPassword != "" {
			return invalidf("从库地址改了,请重新输入从库口令(已保存的口令不会发往新地址)")
		}
	}
	c.ReplicaHost, c.ReplicaPort = host, port
	c.ReplicaUsername = user
	switch {
	case c.ReplicaUsername == "":
		c.ReplicaPassword = "" // 沿用主库账号,也就沿用主库密码
	case strings.TrimSpace(req.ReplicaPassword) != "":
		enc, err := crypto.EncryptSecret(req.ReplicaPassword)
		if err != nil {
			return err
		}
		c.ReplicaPassword = enc
	}
	return nil
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

// TableColumns 实时取一张表的列名,给终端的 Tab 补全用(见 gateway.TableColumns)。table 可带限定名。
// 访问校验与 ConnectionSchema 相同;没有真实凭据的演示连接返回空(没有可补的列,也不编一份)。
func (s *Services) TableColumns(u *model.User, connID int64, database, table string) ([]string, error) {
	conn, err := s.Repo.GetConnection(connID)
	if err != nil {
		return nil, ErrNotFound
	}
	if !s.canAccessConn(u, conn) {
		return nil, ErrForbidden
	}
	if !gateway.RealExecSupported(conn) {
		if _, t := gateway.SplitQualified(table); t == "" {
			return nil, gateway.ErrBadTableName
		}
		return []string{}, nil
	}
	applyTargetDatabase(conn, database)
	return gateway.TableColumns(conn, table)
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
			out.Error = gateway.SchemaLoadError(err) // 按原因提示:中途断开 / 超时 / 连不上
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
	// 生产环境不返回模拟数据 —— 见 gateway/simulation.go。种子树是演示数据,
	// 在生产上把它当成"这台实例的库表"显示,是把演示数据集摆进了生产的树里。
	if !gateway.SimulationAllowed() {
		out.Error = gateway.ErrSimulationDisabled.Error()
		return out
	}
	// SIMULATED-PATH: schema-tree —— 见 docs/simulated-paths.md。
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
		// 生产环境不返回模拟数据 —— 见 gateway/simulation.go。
		if !gateway.SimulationAllowed() {
			out.Error = gateway.ErrSimulationDisabled.Error()
			return out
		}
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
	src, err := objectSourceText(conn, scope, typ, name)
	if err != nil {
		return nil, err
	}
	return &dto.ObjectSourceResp{Name: name, Type: typ, Source: src}, nil
}

// objectSourceText 读一个对象的源码,访问控制由调用方先做完。查看源码与下载程序包
// 读的是同一份东西,走同一条路 —— 下载下来的文件与屏幕上看到的不会是两个版本。
func objectSourceText(conn *model.Connection, scope, typ, name string) (string, error) {
	if !gateway.RealExecSupported(conn) {
		// 生产环境不返回模拟数据 —— 见 gateway/simulation.go。
		if !gateway.SimulationAllowed() {
			return "", gateway.ErrSimulationDisabled
		}
		return simulatedObjectSource(conn.Engine, typ, name), nil
	}
	src, err := gateway.RealObjectSource(conn, strings.TrimSpace(scope), typ, name)
	if err != nil {
		return "", apimsg.Errorf("无法读取对象源码: %w", err)
	}
	return src, nil
}

// SIMULATED-PATH: objects-list —— 见 docs/simulated-paths.md。
//
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

// SIMULATED-PATH: object-source —— 见 docs/simulated-paths.md。
//
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

// AccessibleConnIDs 是这个人按标签授权够得到的实例。
//
// 逐台过 canAccessConn,而不是在别处再写一遍标签规则:那条规则只该有一个出处,
// 两份实现迟早分叉,而分叉的方向是"某处多放了一台"。实例是几十到几百台的量级,
// 这次遍历比一份会跑偏的副本便宜得多。
//
// 用途是**列表可见性**:已批准待执行的工单要让能接手的人看得见(见
// repository.approvalScope)。真正放不放行仍由执行路径上的那几道闸决定。
func (s *Services) AccessibleConnIDs(u *model.User) []int64 {
	if u == nil {
		return nil
	}
	conns, err := s.Repo.ListConnections()
	if err != nil {
		// 读不到就当"够不到任何实例":少看见一张单是件小事,多看见别人的工单不是。
		slog.Error("列可访问实例失败 —— 按够不到处理", "userID", u.ID, "err", err)
		return nil
	}
	out := make([]int64, 0, len(conns))
	for i := range conns {
		if s.canAccessConn(u, &conns[i]) {
			out = append(out, conns[i].ID)
		}
	}
	return out
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
	// 只认 online / maint(空 = 切换)。从前任意字符串都照存,一个拼错的状态既不是在线也不是
	// 维护,哪条判断都对不上它(工单 02 第 10 条)。
	switch status {
	case "", model.ConnStatusOnline, model.ConnStatusMaint:
	default:
		return invalidf("连接状态只能是 online / maint")
	}
	c, err := s.Repo.GetConnection(id)
	if err != nil {
		return ErrNotFound
	}
	if status == "" {
		if c.Status == model.ConnStatusOnline {
			status = model.ConnStatusMaint
		} else {
			status = model.ConnStatusOnline
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
	tags, noInstances := visibleRoleTags(s.Repo.TagsForRole(id))
	return &dto.RoleDetailResp{
		ID: role.ID, Code: role.Code, Name: role.Name, Layer: role.Layer, Icon: role.Icon,
		Description: role.Description, DefaultConnRole: role.DefaultConnRole, CanApprove: role.CanApprove,
		Deletable: IsDeletableRole(role.Code),
		Menus: menus, Matrix: matrix, Members: mds, MemberIDs: ids, Tags: tags, NoInstances: noInstances,
	}, nil
}

// visibleRoleTags 去掉占位标签。占位是"不开放实例"的内部写法,界面上不显示那串下划线,而是显示
// noInstances —— 否则一个空列表会被读成"未设置标签:可访问全部数据库",正好说反。
func visibleRoleTags(raw []string) (tags []string, noInstances bool) {
	tags = make([]string, 0, len(raw))
	for _, t := range raw {
		if model.IsPlaceholderTag(t) {
			noInstances = true
			continue
		}
		tags = append(tags, t)
	}
	return tags, noInstances && len(tags) == 0
}

// ---------------------------------------------------------------- Users

func (s *Services) UsersView(actor *model.User) ([]dto.UserView, error) {
	users, err := s.Repo.ListUsers()
	if err != nil {
		return nil, err
	}
	// 角色表、成员关系、活跃管理员各取一次,逐行在内存里拼(黑盒 2026-10 工单 05 第 4 条)。
	// 从前每行各调 RolesOfUser / EffectiveRoleIDs / guardDisable,而 guardDisable 每次都
	// 重算一遍全体管理员 —— 65 个用户就是上万次查询、2.5 秒。结果与逐个调用完全相同:
	// Roles = 成员关系里仍存在的角色名(按 id 升序),RoleIDs = 主角色 + 成员角色去重后、
	// 剔除已不存在的角色。
	allRoles, err := s.Repo.ListRoles()
	if err != nil {
		return nil, err
	}
	roleName := make(map[int64]string, len(allRoles))
	for _, r := range allRoles {
		roleName[r.ID] = r.Name
	}
	memberships, err := s.Repo.RoleMemberships()
	if err != nil {
		return nil, err
	}
	admins := s.Repo.ActiveAdminIDs()
	out := []dto.UserView{}
	for i := range users {
		u := &users[i]
		roles := []string{}
		for _, id := range memberships[u.ID] {
			if name, ok := roleName[id]; ok {
				roles = append(roles, name)
			}
		}
		ids := []int64{}
		seen := map[int64]bool{}
		for _, id := range append([]int64{u.RoleID}, memberships[u.ID]...) {
			if _, ok := roleName[id]; ok && !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
		// 停用方向才有拦的必要 —— 启用永远允许。
		block := ""
		if err := guardDisableAmong(actor, u, admins); err != nil {
			block = err.Error()
		}
		out = append(out, dto.UserView{
			ID: u.ID, Name: u.Name, Email: u.Email, Initials: u.Initials, Dept: u.Dept,
			Roles: roles, RoleIDs: ids, PrimaryRoleID: u.RoleID,
			Status: u.Status, Kind: u.Kind, MFAEnabled: u.MFAEnabled && u.MFASecret != "", LastActive: u.LastActive,
			SSOBound: u.SSOBound, SSOUserName: u.SSOUserName,
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
	// 被启用凭据绑定的账号 / 服务账号,加不上 admin(.scratch/blackbox-fixes 03)。
	// 在任何写入之前判:单角色路径是"在现有成员关系上追加",多角色路径是整组替换。
	if req.RoleIDs != nil {
		if err := s.GuardAdminGrant(target, req.RoleIDs); err != nil {
			return err
		}
	} else if req.RoleID != nil {
		if err := s.GuardAdminGrant(target, append(s.Repo.EffectiveRoleIDs(target), *req.RoleID)); err != nil {
			return err
		}
	}
	// 换掉最后一位管理员的角色,和停用他是同一个死局(2026-11 审计 V-15)。改的是主角色时,
	// 成员关系还在,所以算的是"换完之后还剩哪些角色"。
	if req.RoleIDs != nil {
		if err := s.guardLastAdmin(target, req.RoleIDs); err != nil {
			return err
		}
	} else if req.RoleID != nil {
		held, herr := s.Repo.RoleIDsOfUser(target.ID)
		if herr != nil {
			return herr
		}
		if err := s.guardLastAdmin(target, append(held, *req.RoleID)); err != nil {
			return err
		}
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
	// 状态一变,已发出的会话全部作废。状态检查只挡"此刻不是 active",挡不住恢复:
	// invited → active 之后,那个没过期的旧 token 又能用了。角色不在此列 —— 权限每次
	// 请求都按库里的成员关系现算,改角色当场生效,不需要把人踢下线。
	if req.Status != "" && req.Status != target.Status {
		if err := s.Repo.BumpTokenVersion(id); err != nil {
			return err
		}
	}
	// 停用只改状态,不动角色(2026-10 黑盒测试,工单 02 第 1 条)。
	//
	// 停用的语义是**临时切断**:四道状态检查(登录、JWT 中间件、WS 握手、每条命令前复核)
	// 已经把它挡在门外。从前这里会顺手清空角色,于是重新启用的账户什么都没有,服务账号
	// 停用再启用后凭据也跟着失效 —— 一个"暂停"按钮做成了"销号"。
	// A full multi-role assignment replaces the membership set and repoints the
	// primary role to the first id (union permissions follow from the set).
	if req.RoleIDs != nil {
		if len(req.RoleIDs) == 0 {
			return ErrBadRequest // a user must keep at least one role
		}
		if err := s.validateRoleIDs(req.RoleIDs); err != nil {
			return err
		}
		if err := s.Repo.SetUserRoles(id, req.RoleIDs); err != nil {
			return err
		}
		s.auditPatchUser(actor, target, req) // 改角色是权限变更,与单角色路径一样进审计(EU4)
		return nil
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
		return guardDisableAmong(actor, target, nil)
	}
	return guardDisableAmong(actor, target, s.Repo.ActiveAdminIDs())
}

// guardLastAdmin 拒绝让最后一位活跃管理员失去 admin 角色:next 是变更后他还持有的角色。
// 道理与 guardDisable 相同 —— 变更之后没有人能再把角色加回来。
func (s *Services) guardLastAdmin(target *model.User, next []int64) error {
	if s.roleIDsIncludeAdmin(next) {
		return nil
	}
	if admins := s.Repo.ActiveAdminIDs(); len(admins) != 1 || admins[0] != target.ID {
		return nil
	}
	return &DisableRefusal{Reason: apimsg.Text("不能收回最后一位管理员的管理员角色:收回之后没有人能再分配角色,只能直接改数据库。请先指派另一位管理员。")}
}

// guardDisableAmong 是 guardDisable 的判断本体,活跃管理员名单由调用方给出 —— 用户列表
// 逐行判断时只算一次名单。
func guardDisableAmong(actor, target *model.User, admins []int64) error {
	if actor != nil && actor.ID == target.ID {
		// 这一条不是为了防死锁(下一条才是),而是因为它几乎总是误点:状态徽章就在
		// 用户列表里自己那一行上,点下去的后果是当场把自己踢出控制台。要停用自己,
		// 得请另一个管理员来做 —— 那时至少有人看着。
		return &DisableRefusal{Reason: apimsg.Text("不能停用自己的账户:停用会立刻生效,你会当场退出控制台。请让另一位管理员操作。")}
	}
	if len(admins) != 1 || admins[0] != target.ID {
		return nil // 目标不是最后一个管理员(或压根不是管理员)
	}
	return &DisableRefusal{Reason: apimsg.Text("不能停用最后一位管理员:停用之后没有人能再启用任何账户,只能直接改数据库。请先指派另一位管理员。")}
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
	// 每一项说清是哪里不对(工单 02 第 11 条):从前五种失败都报"邮箱可能已存在,或密码至少 8 位"。
	email, err := normalizeEmail(req.Email)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.Name)
	if err := checkUserName(name); err != nil {
		return nil, err
	}
	if err := passwordPolicyInput(req.Password); err != nil { // keep in sync with AdminSetPassword / frontend
		return nil, err
	}
	if len(req.RoleIDs) == 0 {
		return nil, invalidf("至少选择一个角色")
	}
	if err := s.checkRolesExist(req.RoleIDs); err != nil {
		return nil, err
	}
	if s.onlyBizQueryRole(req.RoleIDs) {
		return nil, ErrBizNoLocalPassword
	}
	if _, err := s.Repo.GetUserByEmail(email); err == nil {
		return nil, invalidf("邮箱已存在:%s", email)
	}
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
	// 与 CreateUser 同一套校验:邮箱格式、角色存在、重复邮箱各说各的(工单 02 第 11 条)。
	email, err := normalizeEmail(req.Email)
	if err != nil {
		return nil, err
	}
	if err := s.checkRolesExist([]int64{req.RoleID}); err != nil {
		return nil, err
	}
	if _, err := s.Repo.GetUserByEmail(email); err == nil {
		return nil, invalidf("邮箱已存在:%s", email)
	}
	name := email[:strings.Index(email, "@")] // 邮箱上限 128,前缀不会超过姓名列宽 64 太多;超了截断
	if r := []rune(name); len(r) > maxUserNameRunes {
		name = string(r[:maxUserNameRunes])
	}
	u := &model.User{
		Name: name, Email: email, RoleID: req.RoleID, Status: "invited",
		Initials: initials(name), Dept: "—", LastActive: "—",
	}
	if err := s.Repo.CreateUser(u); err != nil {
		return nil, err
	}
	_ = s.Repo.AddMember(req.RoleID, u.ID)
	return u, nil
}

// ---------------------------------------------------------------- Risk dictionary

// validRiskLevel 是字典里一格能取的值:high = 拦截、mid = 需审批、off = 放行。
func validRiskLevel(l string) bool {
	switch l {
	case model.RiskHigh, model.RiskMid, model.RiskOff:
		return true
	}
	return false
}

// checkRiskTier 要求分层存在。写一个不存在的分层进字典不会报错,只会多出一行永远没人读的规则。
func (s *Services) checkRiskTier(tier string) error {
	if _, err := s.Repo.GetEnvTier(strings.ToLower(strings.TrimSpace(tier))); err != nil {
		return invalidf("分层标签不存在:%s", tier)
	}
	return nil
}

// UpsertRiskCommand 新增或批量改一条字典命令(工单 02 第 5 条:等级与分层先校验,一项不对整次不写)。
// 未传的分层:已有行的原样不动;没有行的取 off —— 见 Repo.UpsertRiskCommand。
func (s *Services) UpsertRiskCommand(command string, tiers map[string]string) error {
	cmd := strings.ToUpper(strings.TrimSpace(command))
	if cmd == "" || len(cmd) > 32 {
		return invalidf("命令名不能为空,且不超过 32 个字符")
	}
	for tier, lvl := range tiers {
		if err := s.checkRiskTier(tier); err != nil {
			return err
		}
		if !validRiskLevel(strings.TrimSpace(lvl)) {
			return invalidf("等级只能是 high / mid / off:%s", lvl)
		}
	}
	return s.Repo.UpsertRiskCommand(cmd, tiers)
}

// PatchRiskCommand 改一条**已有**命令在一个分层上的等级。命令不存在时返回 ErrNotFound ——
// 从前这里会顺手新建一条,一个拼错的命令名就悄悄进了字典。
func (s *Services) PatchRiskCommand(command, tier, level string) error {
	cmd := strings.ToUpper(strings.TrimSpace(command))
	if !s.Repo.RiskCommandExists(cmd) {
		return ErrNotFound
	}
	if err := s.checkRiskTier(tier); err != nil {
		return err
	}
	if !validRiskLevel(strings.TrimSpace(level)) {
		return invalidf("等级只能是 high / mid / off:%s", level)
	}
	return s.Repo.PatchRiskLevel(cmd, tier, strings.TrimSpace(level))
}

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

// auditActor returns the actor filter for a user: 0 (all) for the full-audit
// readers, otherwise the user's own id so they only see their own commands.
//
// 谁看得到所有人的审计:平台管理员(L0)、DBA 负责人(L1)、审计员 —— 与别处(发布单、后台任务、
// 执行窗口回看)同一个监督口径 canSeeAllActivity;其他人只看自己的(2026-10-14 用户确认)。
// DBA 负责人进不了管理后台,它在用户后台的「操作记录」页与总览卡片上看全员。
// 单据轨迹、导出、链校验另由路由上的 AuditSupervisor 把关(管理员、审计员,以及 DBA 负责人 ——
// 2026-10-26 回归 18)。
func (s *Services) auditActor(u *model.User) int64 {
	if u == nil || s.canSeeAllActivity(u) {
		return 0
	}
	return u.ID
}

func (s *Services) ListAudit(u *model.User, risk string, since, until time.Time, limit int) ([]model.AuditLog, error) {
	return s.Repo.ListAudit(repository.AuditFilter{
		ActorID: s.auditActor(u), Risk: risk, Since: since, Until: until,
	}, limit)
}

// AuditInstances 是筛选下拉里的实例清单 —— 只列这个人在审计里见得到的那些。
func (s *Services) AuditInstances(u *model.User) ([]string, error) {
	return s.AuditInstancesFor(u, false)
}

// AuditInstancesFor:onlyOwn 时只列自己操作过的实例(「我的操作记录」的筛选下拉)。
func (s *Services) AuditInstancesFor(u *model.User, onlyOwn bool) ([]string, error) {
	actor := s.auditActor(u)
	if onlyOwn && u != nil {
		actor = u.ID
	}
	out, err := s.Repo.AuditInstances(actor)
	if out == nil {
		out = []string{}
	}
	return out, err
}

// scopedAuditFilter 把调用方给的筛选条件套上**这个人能看见什么**,再交给仓储层。
//
// 可见性只在这一处加:列表、导出、计数都从这里拿 filter,谁也没法绕过去 —— 一份导出
// 里多出他在列表上看不见的行,是这一页最不该出的错。
func (s *Services) scopedAuditFilter(u *model.User, f repository.AuditFilter) repository.AuditFilter {
	f.ActorID = s.auditActor(u)
	if f.OnlyOwn && u != nil {
		f.ActorID = u.ID // 「我的操作记录」:能看全部的人也只要自己的
	}
	return f
}

// AuditPage is one page of audit rows plus the total matching the filters.
type AuditPage struct {
	Items    []model.AuditLog `json:"items"`
	Total    int64            `json:"total"`
	Page     int              `json:"page"`
	PageSize int              `json:"pageSize"`
	// Groups:按审批单合并时,本页每张审批单共有几条操作(审批单号 → 条数)。不合并时为空。
	Groups map[string]int64 `json:"groups,omitempty"`
}

// ListAuditGrouped 与 ListAuditPaged 相同,但同一张审批单的操作合成一个条目(显示最新的一步),
// 并在 Groups 里标出每张单共几条。点开看全部用 AuditTrail。
func (s *Services) ListAuditGrouped(u *model.User, f repository.AuditFilter, page, pageSize int) (AuditPage, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 100
	}
	scoped := s.scopedAuditFilter(u, f)
	rows, total, err := s.Repo.ListAuditGroupedByApproval(scoped, (page-1)*pageSize, pageSize)
	if rows == nil {
		rows = []model.AuditLog{}
	}
	out := AuditPage{Items: rows, Total: total, Page: page, PageSize: pageSize, Groups: map[string]int64{}}
	if err != nil {
		return out, err
	}
	var apNos []string
	for _, r := range rows {
		if r.ApprovalNo != "" {
			apNos = append(apNos, r.ApprovalNo)
		}
	}
	out.Groups, err = s.Repo.AuditApprovalCounts(scoped.ActorID, apNos)
	return out, err
}

// AuditTrail 一张审批单的全部操作记录,按时间先后。可见范围与审计列表相同:看不到全部审计的人
// 只看到自己名下的那几行。不受列表上的时间 / 风险等筛选限制 —— 点开要看的是这张单完整的经过。
func (s *Services) AuditTrail(u *model.User, apNo string) ([]model.AuditLog, error) {
	rows, err := s.Repo.AuditTrail(s.auditActor(u), strings.TrimSpace(apNo))
	if rows == nil {
		rows = []model.AuditLog{}
	}
	return rows, err
}

// ListAuditPaged returns page `page` (1-based) of `pageSize` audit rows matching
// the filters, scoped to the caller's visibility.
func (s *Services) ListAuditPaged(u *model.User, f repository.AuditFilter, page, pageSize int) (AuditPage, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 100
	}
	rows, total, err := s.Repo.ListAuditPaged(s.scopedAuditFilter(u, f), (page-1)*pageSize, pageSize)
	if rows == nil {
		rows = []model.AuditLog{}
	}
	return AuditPage{Items: rows, Total: total, Page: page, PageSize: pageSize}, err
}

// RangeSince maps a range key (1h|24h|7d|30d) to a cutoff time (zero = all-time).
func RangeSince(key string) time.Time {
	switch key {
	// 「最近一小时」是排障时最常按的一格:刚出的事故要看的就是刚才那几十分钟,而最短
	// 的一档是 24 小时的话,那几十分钟会被昨天一整天的行淹掉。
	case "1h":
		return time.Now().Add(-time.Hour)
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
// ExportCSVTo 把审计导出成 CSV 写进 w:分批读(EachAuditBatch)、encoding/csv 边读边写。
// 从前整表读进内存再拼成一个字符串,审计表大了会把进程撑爆;手写的引号规则也漏了单独的 \r
// (表格软件把它当换行)。防公式注入(csvSanitize)照旧(2026-10 安全审计 M9)。
func (s *Services) ExportCSVTo(w io.Writer, u *model.User, f repository.AuditFilter) error {
	cw := csv.NewWriter(w)
	// event_type 紧跟 result:它们回答的是同一类问题(这一行是什么、结局如何)。
	// client_ip 紧跟 actor,同一个理由:"谁"和"从哪台机器"是一句话的两半。
	// 这是一次**有意的格式变更** —— 按列号取数的下游会错位,而表头本身就是它的声明。
	if err := cw.Write([]string{"time", "actor", "client_ip", "instance", "command", "risk", "result", "event_type", "data_action", "source", "approval_no", "hash"}); err != nil {
		return err
	}
	err := s.Repo.EachAuditBatch(s.scopedAuditFilter(u, f), 1000, func(rows []model.AuditLog) error {
		for _, r := range rows {
			if err := cw.Write([]string{
				r.OccurredAt.Format("2006-01-02 15:04:05"),
				csvSanitize(r.ActorName), csvSanitize(r.ClientIP), csvSanitize(r.Instance), csvSanitize(r.Command),
				r.Risk, r.Result, r.EventType, r.DataAction, r.Source, r.ApprovalNo, r.Hash,
			}); err != nil {
				return err
			}
		}
		cw.Flush() // 每批写出一次,不在内存里攒
		return cw.Error()
	})
	if err != nil {
		return err
	}
	cw.Flush()
	return cw.Error()
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
	next := []int64{}
	for _, id := range s.Repo.EffectiveRoleIDs(u) {
		if id != roleID {
			next = append(next, id)
		}
	}
	if err := s.guardLastAdmin(u, next); err != nil {
		return err
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

// IsAdmin 说明这个用户持有 admin 角色(并集语义:任一角色是 admin 即算)。
// 与 middleware.AdminOnly 同一条规则 —— 两处一致,不然"能走管理员接口"和"能看到
// 管理员字段"会在同一个人身上给出不同答案。
func (s *Services) IsAdmin(u *model.User) bool {
	if u == nil {
		return false
	}
	for _, code := range s.Repo.RoleCodesForIDs(s.Repo.EffectiveRoleIDs(u)) {
		if code == "admin" {
			return true
		}
	}
	return false
}
