package bootstrap

import (
	"testing"

	"velagateway/internal/testsupport"
)

// The rule tables' `env` column became `tier_code`, and the API client's `key`
// became `api_key`. Those renames used to live in migrations 0016 / 0038–0044;
// the PostgreSQL baseline folds them in, so there is no longer a pre-rename
// schema to migrate — what is left to guard is the RESULT.
//
// What made that specific failure so dangerous is what came after. Every rule
// lookup read an empty tier_code and found nothing, and both lookups —
// CapabilityLevel and matchCommand — spell "no rows" as permission granted. The
// database came up with no error anywhere and every instance ungoverned. It is
// not a hypothetical: it happened on a live dev database during that change.
//
// So: the renamed columns must be there. If a name ever goes back, the queries
// in repository/ are written against the new one and silently find nothing.
func TestBaselineUsesRenamedColumns(t *testing.T) {
	db := testsupport.NewDB(t)
	for _, c := range []struct{ table, col string }{
		{"tbl_role_capability", "tier_code"},
		{"tbl_api_client", "api_key"},
	} {
		var n int64
		err := db.Raw(`SELECT count(*) FROM information_schema.columns
		               WHERE table_schema = current_schema() AND table_name = ? AND column_name = ?`,
			c.table, c.col).Scan(&n).Error
		if err != nil {
			t.Fatalf("query columns: %v", err)
		}
		if n != 1 {
			t.Errorf("%s.%s 不存在", c.table, c.col)
		}
	}
}
