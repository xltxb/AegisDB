package bootstrap

import "testing"

// B6: MySQL's schema is owned by the versioned SQL migrations (migrations/*.sql).
// Boot-time GORM AutoMigrate must never run against MySQL — a second source of
// truth would silently ALTER prod tables to match model inference (H13 drift).
// It stays available for the sqlite dev/test path.
func TestShouldAutoMigrate_NeverForMySQL(t *testing.T) {
	cases := []struct {
		driver string
		want   bool
		expect bool
	}{
		{"mysql", true, false},  // requested but refused
		{"mysql", false, false}, // not requested
		{"sqlite", true, true},  // dev/test convenience
		{"sqlite", false, false},
	}
	for _, c := range cases {
		if got := shouldAutoMigrate(c.driver, c.want); got != c.expect {
			t.Errorf("shouldAutoMigrate(%q, %v) = %v, want %v", c.driver, c.want, got, c.expect)
		}
	}
}
