package repository

import (
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"velagateway/internal/model"
)

// ED3: "this role has no tag restrictions" and "the tag query failed" must not
// look alike. TagsForRoles reports unrestricted when a role yields zero tags, so
// swallowing a query error turns a tag-restricted role into one that can reach
// every connection in the estate — exactly backwards for a transient fault.
func TestTagsForRoles_QueryFailureIsNotUnrestricted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tags.db")
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.RoleTag{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	repo := New(db)

	// A restricted role reads back its grants normally.
	if err := db.Create(&model.RoleTag{RoleID: 7, Tag: "orders"}).Error; err != nil {
		t.Fatalf("seed tag: %v", err)
	}
	allow, unrestricted, err := repo.TagsForRoles([]int64{7})
	if err != nil || unrestricted || len(allow) != 1 {
		t.Fatalf("healthy read: allow=%v unrestricted=%v err=%v", allow, unrestricted, err)
	}

	// Now make every query fail the way a dropped connection or exhausted pool
	// would, and check the restriction does not evaporate.
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	sqlDB.Close()

	allow, unrestricted, err = repo.TagsForRoles([]int64{7})
	if err == nil {
		t.Fatalf("tag query failed but no error was reported (allow=%v unrestricted=%v)", allow, unrestricted)
	}
	if unrestricted {
		t.Error("a failed tag query granted unrestricted access to every connection")
	}
}
