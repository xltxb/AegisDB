package bootstrap

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"velagateway/internal/model"
	"velagateway/internal/repository"
	"velagateway/pkg/crypto"
)

// Seed inserts demo data mirroring the prototype. The full dataset is written
// only on an empty DB, but additive blocks (e.g. the schema tree) are backfilled
// idempotently on every boot so DBs created before a seed addition self-heal.
// Default login: linwei@vela.io / vela123 (platform admin).
func Seed(repo *repository.Repo, cfg *Config) error {
	if repo.Count(&model.Role{}) == 0 {
		if err := seedFreshData(repo, cfg); err != nil {
			return err
		}
	}
	if err := seedGliEnv(repo); err != nil {
		return err
	}
	return seedSchema(repo)
}

// seedGliEnv backfills the GLI (灰度) connection environment. GLI is a later
// addition; its capability-matrix and risk-dictionary rows mirror staging
// (演练UAT tier) so grey-release instances are gated coherently from day one.
// Idempotent: clones every staging row to a gli counterpart only when absent,
// so DBs seeded before GLI existed self-heal on boot (like seedSchema).
func seedGliEnv(repo *repository.Repo) error {
	db := repo.DB()

	// Capability matrix: mirror each staging (role × capability) level to gli.
	var caps []model.RoleCapability
	if err := db.Where("env = ?", model.EnvStaging).Find(&caps).Error; err != nil {
		return err
	}
	for _, row := range caps {
		var n int64
		db.Model(&model.RoleCapability{}).
			Where("role_id = ? AND capability = ? AND env = ?", row.RoleID, row.Capability, model.EnvGli).
			Count(&n)
		if n == 0 {
			if err := db.Create(&model.RoleCapability{
				RoleID: row.RoleID, Capability: row.Capability, Env: model.EnvGli, Level: row.Level,
			}).Error; err != nil {
				return err
			}
		}
	}

	// Risk dictionary: mirror each staging command level to gli.
	var cmds []model.RiskCommand
	if err := db.Where("env = ?", model.EnvStaging).Find(&cmds).Error; err != nil {
		return err
	}
	for _, r := range cmds {
		var n int64
		db.Model(&model.RiskCommand{}).
			Where("command = ? AND env = ?", r.Command, model.EnvGli).
			Count(&n)
		if n == 0 {
			if err := db.Create(&model.RiskCommand{Command: r.Command, Env: model.EnvGli, Level: r.Level}).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

// seedReference inserts the reference data every deployment needs: roles, menus,
// the capability matrix, default role tags, the risk dictionary, webhook and
// gateway settings. Shared by the dev demo seed and the production `init`.
func seedReference(repo *repository.Repo, cfg *Config) (map[string]int64, error) {
	db := repo.DB()

	// ---- Roles ----
	roles := []model.Role{
		{Code: "admin", Name: "平台管理员", Layer: "L0 · 全局", Icon: "crown", CanApprove: true, DefaultConnRole: "dba_l2", Description: "管理连接配置、规则、用户与系统设置;可审批高危命令。"},
		{Code: "owner", Name: "DBA 负责人", Layer: "L1 · 终审", Icon: "shield", CanApprove: true, DefaultConnRole: "dba_l2", Description: "高危命令审批链终审节点。"},
		{Code: "l2", Name: "DBA L2", Layer: "L2 · 执行(需审批)", Icon: "user-cog", CanApprove: false, DefaultConnRole: "dba_l2", Description: "可在 PROD 执行常规操作,高危命令需走审批。"},
		{Code: "ro", Name: "研发只读", Layer: "L3 · 只读", Icon: "code", CanApprove: false, DefaultConnRole: "readonly", Description: "仅 Web 命令行查询与审计日志。"},
		{Code: "audit", Name: "审计员", Layer: "L3 · 只看日志", Icon: "eye", CanApprove: false, DefaultConnRole: "readonly", Description: "仅审计日志查看与导出。"},
	}
	if err := db.Create(&roles).Error; err != nil {
		return nil, err
	}
	roleID := map[string]int64{}
	for _, r := range roles {
		roleID[r.Code] = r.ID
	}

	// create routes every reference-row insert through a sticky error so a failed
	// write is surfaced (not silently dropped) and aborts the seed (M13).
	var seedErr error
	create := func(v any) {
		if seedErr == nil {
			seedErr = db.Create(v).Error
		}
	}

	// ---- Menus ----
	menuKeys := []string{"terminal", "approve", "db", "rules", "perms", "audit", "settings"}
	// Instance config (db), rules, permissions (perms) and settings are all
	// platform-admin only — non-admins don't even see these pages.
	menuMatrix := map[string][]bool{
		//        terminal approve  db    rules  perms audit settings
		"admin": {true, true, true, true, true, true, true},
		"owner": {true, true, false, false, false, true, false},
		"l2":    {true, true, false, false, false, true, false},
		"ro":    {true, false, false, false, false, true, false},
		"audit": {false, false, false, false, false, true, false},
	}
	for code, vals := range menuMatrix {
		for i, k := range menuKeys {
			create(&model.RoleMenu{RoleID: roleID[code], MenuKey: k, Enabled: vals[i]})
		}
	}

	// ---- Capability matrix (caps × [prod,staging,dev]) ----
	caps := []string{"select", "write", "ddl", "grant", "conn", "approve"}
	envs := []string{"prod", "staging", "dev"}
	matrices := map[string][][]string{
		"admin": {{"allow", "allow", "allow"}, {"approve", "allow", "allow"}, {"approve", "approve", "allow"}, {"approve", "approve", "approve"}, {"allow", "allow", "allow"}, {"allow", "allow", "deny"}},
		"owner": {{"allow", "allow", "allow"}, {"approve", "allow", "allow"}, {"approve", "approve", "allow"}, {"approve", "approve", "approve"}, {"allow", "allow", "allow"}, {"allow", "allow", "allow"}},
		"l2":    {{"allow", "allow", "allow"}, {"approve", "approve", "allow"}, {"approve", "approve", "allow"}, {"deny", "deny", "deny"}, {"deny", "deny", "deny"}, {"deny", "deny", "deny"}},
		"ro":    {{"allow", "allow", "allow"}, {"deny", "deny", "allow"}, {"deny", "deny", "allow"}, {"deny", "deny", "deny"}, {"deny", "deny", "deny"}, {"deny", "deny", "deny"}},
		"audit": {{"allow", "allow", "allow"}, {"deny", "deny", "deny"}, {"deny", "deny", "deny"}, {"deny", "deny", "deny"}, {"deny", "deny", "deny"}, {"deny", "deny", "deny"}},
	}
	for code, rows := range matrices {
		for ci, capName := range caps {
			for ei, env := range envs {
				create(&model.RoleCapability{RoleID: roleID[code], Capability: capName, Env: env, Level: rows[ci][ei]})
			}
		}
	}

	// ---- Role → tag grants (admin/owner unrestricted = no tags) ----
	roleTags := map[string][]string{
		"l2":    {"orders", "users"},
		"ro":    {"analytics", "readonly"},
		"audit": {"analytics"},
	}
	for code, tags := range roleTags {
		for _, tg := range tags {
			create(&model.RoleTag{RoleID: roleID[code], Tag: tg})
		}
	}

	// ---- Risk command dictionary ----
	type rc struct {
		cmd                string
		prod, staging, dev string
	}
	dict := []rc{
		{"DROP", "high", "mid", "off"}, {"TRUNCATE", "high", "mid", "off"}, {"DELETE", "high", "mid", "off"},
		{"ALTER", "high", "mid", "off"}, {"RENAME", "high", "mid", "off"},
		{"GRANT", "mid", "mid", "off"}, {"REVOKE", "mid", "mid", "off"},
	}
	for _, d := range dict {
		create(&model.RiskCommand{Command: d.cmd, Env: "prod", Level: d.prod})
		create(&model.RiskCommand{Command: d.cmd, Env: "staging", Level: d.staging})
		create(&model.RiskCommand{Command: d.cmd, Env: "dev", Level: d.dev})
	}

	// ---- Webhook + settings ----
	create(&model.WebhookConfig{
		Endpoint: cfg.Webhook.Endpoint, Secret: cfg.Webhook.Secret,
		// Forward command-execution events by default; login events are opt-in
		// (usually high-volume, low-value for the Event Center) — toggle in
		// Settings › Webhook.
		Events: "exec", RetryMax: cfg.Webhook.RetryMax, Enabled: cfg.Webhook.Enabled,
	})
	settings := map[string]any{
		"gateway.defaultPolicy":   cfg.Gateway.DefaultPolicy,
		"gateway.execTimeout":     cfg.Gateway.ExecTimeoutSeconds,
		"approval.onTimeout":      "auto-escalate",
		"approval.timeoutMinutes": 720,
		"security.sessionTTL":     "8h",
		"security.idleLock":       true,
		"security.idleMinutes":    15,
		"security.requireMFA":     true,
		"security.ipAllowlist":    "",
		"notify.larkChannel":      "",
	}
	for k, v := range settings {
		b, _ := json.Marshal(v)
		create(&model.Setting{K: k, V: string(b)})
	}
	if seedErr != nil {
		return nil, fmt.Errorf("seed reference data: %w", seedErr)
	}
	return roleID, nil
}

// seedFreshData bootstraps a fresh DB with just the reference data plus a single
// platform-admin account for first login. All business data (users beyond the
// admin, connections, approvals, audit) starts empty so the console reflects real
// usage rather than prototype fixtures. Only ever called on an empty DB (dev).
// Default login: linwei@vela.io / vela123.
func seedFreshData(repo *repository.Repo, cfg *Config) error {
	roleID, err := seedReference(repo, cfg)
	if err != nil {
		return err
	}
	db := repo.DB()
	pw, _ := crypto.HashPassword("vela123")

	admin := model.User{
		Name: "Lin Wei", Initials: "LW", Dept: "DBA 组", Email: "linwei@vela.io",
		RoleID: roleID["admin"], Status: "active", LastActive: "刚刚", PasswordHash: pw,
	}
	if err := db.Create(&admin).Error; err != nil {
		return err
	}
	if err := db.Create(&model.RoleMember{RoleID: roleID["admin"], UserID: admin.ID}).Error; err != nil {
		return err
	}

	slog.Info("seed data inserted", "users", 1, "connections", 0)
	return nil
}

// seedSchema backfills the simulated db.table tree (US#11). Idempotent: it runs
// on every boot but writes only when no schema objects exist yet, so DBs created
// before schema seeding existed self-heal without a wipe.
func seedSchema(repo *repository.Repo) error {
	if repo.Count(&model.SchemaObject{}) > 0 {
		return nil
	}
	conns, err := repo.ListConnections()
	if err != nil {
		return err
	}
	idByName := map[string]int64{}
	for _, c := range conns {
		idByName[c.Name] = c.ID
	}
	schema := map[string]map[string][]string{
		"order-cluster":     {"orders_db": {"orders", "orders_2024_q3", "payments"}},
		"user-cluster":      {"users_db": {"users", "sessions", "profiles"}},
		"analytics-ro":      {"analytics": {"events", "funnels", "retention"}},
		"order-cluster-stg": {"orders_db": {"orders", "orders_2024_q3"}},
		"sandbox-dev":       {"sandbox": {"scratch", "tmp_import"}},
	}
	db := repo.DB()
	for connName, dbs := range schema {
		id, ok := idByName[connName]
		if !ok {
			continue
		}
		for dbName, tables := range dbs {
			for _, tbl := range tables {
				if err := db.Create(&model.SchemaObject{ConnectionID: id, Database: dbName, Tbl: tbl}).Error; err != nil {
					return err
				}
			}
		}
	}
	return nil
}

