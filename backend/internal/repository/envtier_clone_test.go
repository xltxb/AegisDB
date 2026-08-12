package repository

import (
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"velagateway/internal/model"
)

func newTierDB(t *testing.T) (*gorm.DB, *Repo) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tier.db")
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	// Close the handle before t.TempDir() removal runs (cleanups are LIFO), or
	// Windows refuses to delete the still-open database file.
	if sqlDB, derr := db.DB(); derr == nil {
		t.Cleanup(func() { sqlDB.Close() })
	}
	if err := db.AutoMigrate(&model.EnvTier{}, &model.Environment{},
		&model.RoleCapability{}, &model.RiskCommand{}, &model.Connection{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db, New(db)
}

// seedTemplate creates a tier with one capability row and one dictionary row.
func seedTemplate(t *testing.T, db *gorm.DB, code string) {
	t.Helper()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	must(db.Create(&model.EnvTier{Code: code, DisplayName: code}).Error)
	must(db.Create(&model.RoleCapability{RoleID: 1, Capability: "ddl", Env: code, Level: "approve"}).Error)
	must(db.Create(&model.RiskCommand{Command: "DROP", Env: code, Level: "high"}).Error)
}

// A half-cloned tier is the worst possible outcome: it exists, instances can be
// pointed at it, and every rule row it is missing reads as permission granted.
// So a failure anywhere in the clone must take the tier row down with it.
//
// The failure is provoked the way it could really happen — a dictionary row for
// the new tier already exists (left over from a previous life of that code), so
// the cloned INSERT collides on the (command, env) primary key.
func TestCreateEnvTierFrom_RollsBackWhenCloneFails(t *testing.T) {
	db, repo := newTierDB(t)
	seedTemplate(t, db, "prod")

	// A stray row occupying (DROP, prod-hk) — the clone will collide with it.
	if err := db.Create(&model.RiskCommand{Command: "DROP", Env: "prod-hk", Level: "off"}).Error; err != nil {
		t.Fatalf("seed stray row: %v", err)
	}

	err := repo.CreateEnvTierFrom(&model.EnvTier{Code: "prod-hk", DisplayName: "香港生产"}, "prod")
	if err == nil {
		t.Fatal("clone collided with an existing row but reported success")
	}

	// The tier must not exist…
	var tiers int64
	db.Model(&model.EnvTier{}).Where("code = ?", "prod-hk").Count(&tiers)
	if tiers != 0 {
		t.Error("failed clone left the tier row behind — instances could be pointed at a tier with incomplete rules")
	}
	// …and neither may the partially copied capability rows.
	var caps int64
	db.Model(&model.RoleCapability{}).Where("env = ?", "prod-hk").Count(&caps)
	if caps != 0 {
		t.Errorf("failed clone left %d capability rows behind", caps)
	}
}

// An unknown template is refused before anything is written.
func TestCreateEnvTierFrom_UnknownTemplateWritesNothing(t *testing.T) {
	db, repo := newTierDB(t)
	seedTemplate(t, db, "prod")

	if err := repo.CreateEnvTierFrom(&model.EnvTier{Code: "ghost", DisplayName: "x"}, "nope"); err == nil {
		t.Fatal("cloning from a non-existent template must fail")
	}
	var n int64
	db.Model(&model.EnvTier{}).Where("code = ?", "ghost").Count(&n)
	if n != 0 {
		t.Error("a tier was created from a template that does not exist")
	}
}

// The happy path copies every rule row, and only the template's.
func TestCreateEnvTierFrom_ClonesExactlyTheTemplateRows(t *testing.T) {
	db, repo := newTierDB(t)
	seedTemplate(t, db, "prod")
	seedTemplate(t, db, "dev") // a second tier whose rows must NOT be copied

	if err := repo.CreateEnvTierFrom(&model.EnvTier{Code: "prod-hk", DisplayName: "香港生产"}, "prod"); err != nil {
		t.Fatalf("clone: %v", err)
	}

	var caps []model.RoleCapability
	db.Where("env = ?", "prod-hk").Find(&caps)
	if len(caps) != 1 || caps[0].Level != "approve" || caps[0].Capability != "ddl" {
		t.Errorf("capability rows not cloned faithfully: %+v", caps)
	}
	var cmds []model.RiskCommand
	db.Where("env = ?", "prod-hk").Find(&cmds)
	if len(cmds) != 1 || cmds[0].Command != "DROP" || cmds[0].Level != "high" {
		t.Errorf("dictionary rows not cloned faithfully: %+v", cmds)
	}
}

// Setting the baseline on one tier clears it everywhere else, in one statement,
// so there is never a moment with two baselines or none.
func TestUpdateEnvTier_BaselineMovesAtomically(t *testing.T) {
	db, repo := newTierDB(t)
	if err := db.Create(&model.EnvTier{Code: "prod", DisplayName: "prod", ScanBaseline: true}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := db.Create(&model.EnvTier{Code: "gli", DisplayName: "gli"}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	gli, err := repo.GetEnvTier("gli")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	gli.ScanBaseline = true
	if err := repo.UpdateEnvTier(gli); err != nil {
		t.Fatalf("update: %v", err)
	}

	var holders []model.EnvTier
	db.Where("scan_baseline = ?", true).Find(&holders)
	if len(holders) != 1 || holders[0].Code != "gli" {
		t.Errorf("expected exactly gli to hold the baseline, got %+v", holders)
	}

	base, err := repo.ScanBaselineTier()
	if err != nil || base.Code != "gli" {
		t.Errorf("ScanBaselineTier = %+v, %v", base, err)
	}
}

// ScanBaselineTier must report an error when no tier holds it. Returning a zero
// value would let script scanning run against an empty dictionary and pronounce
// every statement safe without raising anything.
func TestScanBaselineTier_MissingIsAnError(t *testing.T) {
	db, repo := newTierDB(t)
	if err := db.Create(&model.EnvTier{Code: "prod", DisplayName: "prod"}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := repo.ScanBaselineTier(); err == nil {
		t.Fatal("a missing scan baseline must be an error, not an empty tier")
	}
}

// Deleting an environment moves its instances in the same transaction, and a
// missing target aborts the whole thing rather than stranding them.
func TestDeleteEnvironmentMoving_IsAtomic(t *testing.T) {
	db, repo := newTierDB(t)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	must(db.Create(&model.EnvTier{Code: "prod", DisplayName: "prod"}).Error)
	must(db.Create(&model.Environment{Code: "prod-sh", DisplayName: "上海", TierCode: "prod"}).Error)
	must(db.Create(&model.Environment{Code: "prod", DisplayName: "生产", TierCode: "prod"}).Error)
	must(db.Create(&model.Connection{Name: "c1", Engine: "mysql", Host: "h", Port: 3306, Env: "prod-sh", Policy: "strict", Status: "online"}).Error)

	// A non-existent target aborts: the environment stays and so does the instance.
	if err := repo.DeleteEnvironmentMoving("prod-sh", "nowhere"); err == nil {
		t.Fatal("moving to a non-existent environment must fail")
	}
	var still int64
	db.Model(&model.Environment{}).Where("code = ?", "prod-sh").Count(&still)
	if still != 1 {
		t.Error("a failed move deleted the environment anyway")
	}
	var c model.Connection
	db.First(&c, "name = ?", "c1")
	if c.Env != "prod-sh" {
		t.Errorf("a failed move relocated the instance to %q", c.Env)
	}

	// The real move relocates the instance and drops the environment together.
	if err := repo.DeleteEnvironmentMoving("prod-sh", "prod"); err != nil {
		t.Fatalf("move: %v", err)
	}
	db.First(&c, "name = ?", "c1")
	if c.Env != "prod" {
		t.Errorf("instance env = %q, want prod", c.Env)
	}
	db.Model(&model.Environment{}).Where("code = ?", "prod-sh").Count(&still)
	if still != 0 {
		t.Error("environment should be gone after a successful move")
	}
}
