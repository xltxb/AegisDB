package bootstrap

import (
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"velagateway/internal/model"
)

// The rule tables' `env` column became `tier_code`. AutoMigrate does not rename
// anything: shown a model whose field no longer matches the column, it ADDS the
// new one and leaves the old in place, holding the data and still part of the
// primary key.
//
// What makes that specific failure so dangerous is what comes after. Every rule
// lookup would read an empty tier_code and find nothing, and both lookups —
// CapabilityLevel and matchCommand — spell "no rows" as permission granted. The
// database comes up with no error anywhere and every instance ungoverned. It is
// not a hypothetical: it happened on a live dev database during this change,
// because boot ran GORM's AutoMigrate directly and never reached the rename.

// The rule models as they were BEFORE the rename. The old schema is built by
// AutoMigrating these rather than by hand-written DDL, so the starting point is
// byte-for-byte what a real installation has — hand-written column types differ
// just enough (TEXT vs varchar(16)) to send GORM down a different path and prove
// something other than what is being tested.
type oldRoleCapability struct {
	RoleID     int64  `gorm:"primaryKey"`
	Capability string `gorm:"primaryKey;size:32"`
	Env        string `gorm:"primaryKey;size:16"`
	Level      string `gorm:"size:16;not null"`
}

func (oldRoleCapability) TableName() string { return "tbl_role_capability" }

type oldRiskCommand struct {
	Command string `gorm:"primaryKey;size:32"`
	Env     string `gorm:"primaryKey;size:16"`
	Level   string `gorm:"size:16;not null;default:high"`
}

func (oldRiskCommand) TableName() string { return "tbl_risk_command" }

// oldSchemaDB builds a database carrying the PRE-rename rule tables, with rows.
func oldSchemaDB(t *testing.T) *Config {
	t.Helper()
	cfg := &Config{}
	cfg.Database.Driver = "sqlite"
	cfg.Database.SQLitePath = filepath.Join(t.TempDir(), "old.db")
	cfg.Database.AutoMigrate = true

	// OpenDB migrates on the way in, so build the old shape on a raw handle first.
	db, err := gorm.Open(sqlite.Open(cfg.Database.SQLitePath), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(&oldRoleCapability{}, &oldRiskCommand{}); err != nil {
		t.Fatalf("build old schema: %v", err)
	}
	rows := []any{
		&oldRoleCapability{RoleID: 1, Capability: "select", Env: "prod", Level: "deny"},
		&oldRoleCapability{RoleID: 1, Capability: "write", Env: "staging", Level: "approve"},
		&oldRiskCommand{Command: "DROP", Env: "prod", Level: "high"},
		&oldRiskCommand{Command: "DROP", Env: "dev", Level: "off"},
	}
	for _, r := range rows {
		if err := db.Create(r).Error; err != nil {
			t.Fatalf("seed old rows: %v", err)
		}
	}
	if sqlDB, derr := db.DB(); derr == nil {
		_ = sqlDB.Close()
	}
	return cfg
}

func TestTierColumnRename_CarriesTheDataNotJustTheColumn(t *testing.T) {
	cfg := oldSchemaDB(t)

	db, err := OpenDB(cfg) // migrates on the way in — the path boot actually takes
	if err != nil {
		t.Fatalf("open/migrate: %v", err)
	}
	if sqlDB, derr := db.DB(); derr == nil {
		t.Cleanup(func() { sqlDB.Close() })
	}

	// The old column is gone, and every row kept its tier.
	var caps []model.RoleCapability
	if err := db.Order("capability").Find(&caps).Error; err != nil {
		t.Fatalf("read capabilities: %v", err)
	}
	if len(caps) != 2 {
		t.Fatalf("capability rows = %d, want 2", len(caps))
	}
	for _, c := range caps {
		if c.TierCode == "" {
			t.Errorf("capability %q lost its tier — an empty tier reads as ALLOW on every lookup", c.Capability)
		}
	}
	eq(t, caps[0].TierCode, "prod", "select row kept its tier")
	eq(t, caps[0].Level, "deny", "…and its level")
	eq(t, caps[1].TierCode, "staging", "write row kept its tier")

	var cmds []model.RiskCommand
	if err := db.Order("tier_code").Find(&cmds).Error; err != nil {
		t.Fatalf("read dictionary: %v", err)
	}
	if len(cmds) != 2 {
		t.Fatalf("dictionary rows = %d, want 2", len(cmds))
	}
	for _, c := range cmds {
		if c.TierCode == "" {
			t.Errorf("dictionary row %q lost its tier — an empty tier reads as OFF", c.Command)
		}
	}
	eq(t, cmds[0].TierCode, "dev", "dev row kept its tier")
	eq(t, cmds[1].TierCode, "prod", "prod row kept its tier")
	eq(t, cmds[1].Level, "high", "…and its level")
}

// Re-migrating an already-renamed database changes nothing.
func TestTierColumnRename_IsIdempotent(t *testing.T) {
	cfg := oldSchemaDB(t)

	db, err := OpenDB(cfg)
	if err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	if err := autoMigrate(db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	var n int64
	db.Model(&model.RiskCommand{}).Where("tier_code = ?", "prod").Count(&n)
	eq(t, n, int64(1), "the prod dictionary row survives a second migration")
	if sqlDB, derr := db.DB(); derr == nil {
		sqlDB.Close()
	}
}

// A half-migrated table (both columns present) must stop the process rather than
// serve traffic: tier_code would be empty and empty reads as permitted.
func TestTierColumnRename_RefusesAHalfMigratedTable(t *testing.T) {
	cfg := oldSchemaDB(t)

	db, err := gorm.Open(sqlite.Open(cfg.Database.SQLitePath), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// Exactly what a build that ran AutoMigrate without the rename leaves behind.
	if err := db.Exec(`ALTER TABLE tbl_risk_command ADD COLUMN tier_code TEXT DEFAULT ''`).Error; err != nil {
		t.Fatalf("simulate half migration: %v", err)
	}
	if sqlDB, derr := db.DB(); derr == nil {
		_ = sqlDB.Close()
	}

	if _, err := OpenDB(cfg); err == nil {
		t.Fatal("a half-migrated schema must refuse to start — serving it means serving an ungoverned gateway")
	}
}
