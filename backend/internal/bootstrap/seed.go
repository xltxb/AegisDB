package bootstrap

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

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
	return seedSchema(repo)
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
		Events: "intercept,approve,exec", RetryMax: cfg.Webhook.RetryMax, Enabled: cfg.Webhook.Enabled,
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
		"security.ipAllowlist":    "10.20.0.0/16",
		"notify.larkChannel":      "#dba-oncall",
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

// seedFreshData writes the full demo dataset (reference data + demo users,
// connections, approvals, audit) — only ever called on an empty DB (dev).
func seedFreshData(repo *repository.Repo, cfg *Config) error {
	roleID, err := seedReference(repo, cfg)
	if err != nil {
		return err
	}
	db := repo.DB()
	pw, _ := crypto.HashPassword("vela123")

	// ---- Users (primary role drives auth) ----
	type seedUser struct{ name, ini, dept, email, role, last, status string }
	sus := []seedUser{
		{"Lin Wei", "LW", "DBA 组", "linwei@vela.io", "admin", "2 分钟前", "active"},
		{"Chen Hao", "CH", "DBA 组", "chenhao@vela.io", "l2", "14 分钟前", "active"},
		{"Wang Min", "WM", "平台工程", "wangmin@vela.io", "admin", "1 小时前", "active"},
		{"Zhang Wei", "ZW", "DBA 组", "zhangwei@vela.io", "owner", "刚刚", "active"},
		{"Li Na", "LN", "DBA 组", "lina@vela.io", "owner", "3 小时前", "active"},
		{"Sun Qi", "SQ", "数据平台", "sunqi@vela.io", "l2", "昨天", "active"},
		{"Zhao Lei", "ZL", "研发一部", "zhaolei@vela.io", "ro", "2 天前", "active"},
		{"Qian Yi", "QY", "研发二部", "qianyi@vela.io", "ro", "30 天前", "disabled"},
		{"Hu Jun", "HJ", "安全合规", "hujun@vela.io", "audit", "5 小时前", "active"},
		{"Audit Bot", "AB", "安全合规", "auditbot@vela.io", "audit", "实时", "active"},
	}
	userID := map[string]int64{}
	for _, s := range sus {
		u := model.User{Name: s.name, Initials: s.ini, Dept: s.dept, Email: s.email,
			RoleID: roleID[s.role], Status: s.status, LastActive: s.last, PasswordHash: pw, MFAEnabled: true}
		if err := db.Create(&u).Error; err != nil {
			return err
		}
		userID[s.name] = u.ID
	}

	// ---- Role membership (many-to-many) ----
	// Membership now GRANTS permissions (a user's effective rights are the union
	// across every role they belong to), so the seed keeps each user in exactly
	// their primary role — no decorative cross-role rows that would silently
	// escalate. Multi-role is a deliberate admin action via the perms page.
	membership := map[string][]string{
		"admin": {"Lin Wei", "Wang Min"},
		"owner": {"Zhang Wei", "Li Na"},
		"l2":    {"Chen Hao", "Sun Qi"},
		"ro":    {"Zhao Lei", "Qian Yi"},
		"audit": {"Hu Jun", "Audit Bot"},
	}
	for code, names := range membership {
		for _, n := range names {
			db.Create(&model.RoleMember{RoleID: roleID[code], UserID: userID[n]})
		}
	}

	// ---- Connections ----
	conns := []model.Connection{
		{Name: "order-cluster", Layer: "L1 核心 · 写", Engine: "MySQL 8.0", Host: "10.20.3.11", Port: 3306, Env: "prod", Policy: "strict", DefaultRole: "dba_l2", Tags: "orders,core", Status: "online"},
		{Name: "user-cluster", Layer: "L1 核心 · 写", Engine: "PostgreSQL 15", Host: "10.20.3.18", Port: 5432, Env: "prod", Policy: "strict", DefaultRole: "dba_l2", Tags: "users,core", Status: "online"},
		{Name: "analytics-ro", Layer: "L2 只读副本", Engine: "ClickHouse", Host: "10.20.4.6", Port: 8123, Env: "prod", Policy: "audit-only", DefaultRole: "readonly", Tags: "analytics,readonly", Status: "online"},
		{Name: "order-cluster-stg", Layer: "L3 预发", Engine: "MySQL 8.0", Host: "10.30.1.11", Port: 3306, Env: "staging", Policy: "approve-1", DefaultRole: "dba_l2", Tags: "orders,staging", Status: "online"},
		{Name: "sandbox-dev", Layer: "L4 沙盒", Engine: "MySQL 8.0", Host: "10.40.0.5", Port: 3306, Env: "dev", Policy: "audit-only", DefaultRole: "developer", Tags: "sandbox,dev", Status: "online"},
	}
	if err := db.Create(&conns).Error; err != nil {
		return err
	}
	connID := map[string]int64{}
	for _, c := range conns {
		connID[c.Name] = c.ID
	}

	// ---- Approvals (pending) ----
	seedApprovals := []model.Approval{
		{ApNo: "AP-2294", ConnectionID: connID["order-cluster"], Env: "prod", Instance: "order-cluster", Command: "TRUNCATE TABLE orders_2024_q3;", Keyword: "TRUNCATE", InitiatorID: userID["Lin Wei"], Initiator: "Lin Wei", Reason: "Q3 归档表清理,已确认无服务引用,变更窗口 23:00–23:30", RiskLevel: "high", Status: "pending", AuditID: "AUD-77310"},
		{ApNo: "AP-2293", ConnectionID: connID["order-cluster"], Env: "prod", Instance: "order-cluster", Command: "ALTER TABLE orders DROP COLUMN legacy_ref;", Keyword: "ALTER", InitiatorID: userID["Chen Hao"], Initiator: "Chen Hao", Reason: "下线已废弃字段 legacy_ref", RiskLevel: "high", Status: "pending", AuditID: "AUD-77309"},
		{ApNo: "AP-2292", ConnectionID: connID["analytics-ro"], Env: "prod", Instance: "analytics-ro", Command: "GRANT SELECT ON analytics.* TO reporter;", Keyword: "GRANT", InitiatorID: userID["Wang Min"], Initiator: "Wang Min", Reason: "报表平台只读授权", RiskLevel: "mid", Status: "pending", AuditID: "AUD-77308"},
	}
	owner := []string{"Zhang Wei", "Li Na"}
	for _, a := range seedApprovals {
		ap := a
		steps := []model.ApprovalStep{}
		for i, n := range owner {
			st := model.ApprovalStep{StepOrder: i + 1, ApproverID: userID[n], Approver: n, Status: "waiting"}
			if i == 0 {
				st.Status = "active"
			}
			steps = append(steps, st)
		}
		_ = repo.CreateApproval(&ap, steps)
	}

	// ---- Audit log (hash chained, newest first in UI but inserted oldest first) ----
	seedAudit(repo, connID, userID)

	slog.Info("seed data inserted", "users", len(sus), "connections", len(conns))
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

func seedAudit(repo *repository.Repo, connID, userID map[string]int64) {
	now := time.Now()
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	type ar struct {
		h, m, s            int
		who, inst, cmd     string
		risk, result, apNo string
	}
	rows := []ar{
		{22, 19, 13, "Lin Wei", "analytics-ro", "SELECT * FROM events;", "low", "warn", ""},
		{22, 24, 40, "Zhang Wei", "order-cluster", "DELETE FROM orders;", "high", "rejected", "AP-2288"},
		{22, 31, 2, "Wang Min", "user-cluster", "UPDATE users SET tier='vip' WHERE id=88;", "mid", "executed", ""},
		{22, 38, 55, "Chen Hao", "order-cluster", "DROP TABLE tmp_export;", "high", "executed", "AP-2290"},
		{22, 41, 7, "Lin Wei", "order-cluster", "TRUNCATE TABLE orders_2024_q3;", "high", "pending", "AP-2294"},
	}
	prev := ""
	for _, r := range rows {
		occurred := day.Add(time.Duration(r.h)*time.Hour + time.Duration(r.m)*time.Minute + time.Duration(r.s)*time.Second)
		payload, _ := json.Marshal(map[string]any{
			"time": occurred.Format(time.RFC3339), "actor": r.who, "instance": r.inst,
			"command": r.cmd, "risk": r.risk, "result": r.result, "ap": r.apNo,
		})
		h := crypto.ChainHash(prev, payload)
		a := &model.AuditLog{
			OccurredAt: occurred, ActorID: userID[r.who], ActorName: r.who,
			ConnectionID: connID[r.inst], Instance: r.inst, Command: r.cmd,
			Risk: r.risk, Result: r.result, ApprovalNo: r.apNo, PrevHash: prev, Hash: h,
		}
		_ = repo.InsertAudit(a)
		prev = h
	}
}
