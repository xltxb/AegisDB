package bootstrap

// fixtures_test.go holds the demo business data the black-box regression suite
// asserts against (extra users, connections, pending approvals, a multi-row audit
// chain, schema tree). The PRODUCT seed (seed.go) intentionally ships only the
// reference data plus a single admin account, so these fixtures live in the test
// harness instead of the shipped binary. newTestApp calls seedTestFixtures right
// after Seed().

import (
	"encoding/json"
	"testing"
	"time"

	"velagateway/internal/model"
	"velagateway/internal/repository"
	"velagateway/pkg/crypto"
)

// seedTestFixtures loads the demo dataset the tests expect. The admin account
// (Lin Wei / linwei@vela.io) already exists from the product seed; this adds the
// remaining users, connections, approvals, audit chain and schema tree.
func seedTestFixtures(t *testing.T, repo *repository.Repo) {
	t.Helper()
	db := repo.DB()

	var roles []model.Role
	if err := db.Find(&roles).Error; err != nil {
		t.Fatalf("fixtures: load roles: %v", err)
	}
	roleID := map[string]int64{}
	for _, r := range roles {
		roleID[r.Code] = r.ID
	}

	pw, _ := crypto.HashPassword("vela123")

	// Lin Wei is seeded by the product; reuse its id for approvals/audit below.
	userID := map[string]int64{}
	var admin model.User
	if err := db.Where("email = ?", "linwei@vela.io").First(&admin).Error; err != nil {
		t.Fatalf("fixtures: admin not seeded: %v", err)
	}
	userID["Lin Wei"] = admin.ID

	// ---- Users (Lin Wei already exists) ----
	type seedUser struct{ name, ini, dept, email, role, last, status string }
	sus := []seedUser{
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
	for _, s := range sus {
		u := model.User{Name: s.name, Initials: s.ini, Dept: s.dept, Email: s.email,
			RoleID: roleID[s.role], Status: s.status, LastActive: s.last, PasswordHash: pw, MFAEnabled: true}
		if err := db.Create(&u).Error; err != nil {
			t.Fatalf("fixtures: create user %s: %v", s.email, err)
		}
		userID[s.name] = u.ID
	}

	// ---- Role membership (Lin Wei's admin membership already exists) ----
	membership := map[string][]string{
		"admin": {"Wang Min"},
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
		t.Fatalf("fixtures: create connections: %v", err)
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

	// ---- Audit log (hash chained) ----
	seedTestAudit(repo, connID, userID)

	// Schema tree needs connections to exist first; product Seed() ran seedSchema
	// before these fixtures (with zero connections), so run it again now.
	if err := seedSchema(repo); err != nil {
		t.Fatalf("fixtures: seed schema: %v", err)
	}
}

func seedTestAudit(repo *repository.Repo, connID, userID map[string]int64) {
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
