package repository

import (
	"testing"

	"velagateway/internal/model"
	"velagateway/internal/testsupport"
)

// ED3: "this role has no tag restrictions" and "the tag query failed" must not
// look alike. TagsForRoles reports unrestricted when a role yields zero tags, so
// swallowing a query error turns a tag-restricted role into one that can reach
// every connection in the estate — exactly backwards for a transient fault.
func TestTagsForRoles_QueryFailureIsNotUnrestricted(t *testing.T) {
	db := testsupport.NewDB(t)
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
