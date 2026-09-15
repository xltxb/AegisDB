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
// So: the renamed columns must be there — AND the old ones must be gone. Asserting
// only "the new column exists" would miss the exact shape of that incident: the
// filled-in `env` and the empty `tier_code` stood SIDE BY SIDE. A future migration
// that adds `env` back (for an external report, say) and backfills it from
// `tier_code` passes an existence-only check; then someone updates only `env`,
// leaves `tier_code` empty, CapabilityLevel finds no row, reads it as allow — and
// that is the original incident verbatim, waved through by its own guard.
//
// Both rule tables were renamed, so both are listed here. tbl_risk_command used to
// be missing from this table, which meant matchCommand — one of the two lookups
// that spell "no rows" as permission granted — had no guard at all.
func TestBaselineUsesRenamedColumns(t *testing.T) {
	db := testsupport.NewDB(t)
	count := func(table, col string) int64 {
		t.Helper()
		var n int64
		err := db.Raw(`SELECT count(*) FROM information_schema.columns
		               WHERE table_schema = current_schema() AND table_name = ? AND column_name = ?`,
			table, col).Scan(&n).Error
		if err != nil {
			t.Fatalf("query columns: %v", err)
		}
		return n
	}
	for _, c := range []struct{ table, newCol, oldCol string }{
		{"tbl_role_capability", "tier_code", "env"},
		{"tbl_risk_command", "tier_code", "env"},
		{"tbl_api_client", "api_key", "key"},
	} {
		if n := count(c.table, c.newCol); n != 1 {
			t.Errorf("%s.%s 不存在 —— repository/ 里的查询全写着这个名字,它们会安静地查不到任何行",
				c.table, c.newCol)
		}
		if n := count(c.table, c.oldCol); n != 0 {
			t.Errorf("%s 上旧列 %q 又回来了,和 %q 并排站着。\n"+
				"    那正是那次事故的样子:填好的旧列旁边一个空的新列,而两处规则查询\n"+
				"    (CapabilityLevel / matchCommand)都把「查不到行」读作放行 —— 库起得来、\n"+
				"    健康检查全绿、每一台实例都没人管。要么别加回来,要么把查询一起改。",
				c.table, c.oldCol, c.newCol)
		}
	}
}
