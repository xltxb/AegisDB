package bootstrap

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"

	"velagateway/migrations"
)

var (
	createTableRe = regexp.MustCompile(`(?i)CREATE TABLE (?:IF NOT EXISTS )?` + "`?" + `(\w+)`)
	alterTableRe  = regexp.MustCompile(`(?i)ALTER TABLE\s+` + "`?" + `(\w+)`)
)

// Migrations run only against MySQL, so the sqlite-backed suite never executes
// them — a typo'd table name ships silently and dies on the operator's box
// ("Table 'vela_gateway.tbl_audit' doesn't exist", migration 0018: the audit
// table is tbl_audit_log). Cheap static guard: every ALTER TABLE target across
// all migration files must be a table some migration CREATEs.
func TestMigrations_AlterTargetsExistingTables(t *testing.T) {
	created := map[string]bool{}
	altered := map[string][]string{} // table → files that alter it

	err := fs.WalkDir(migrations.FS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".sql") {
			return err
		}
		b, rerr := fs.ReadFile(migrations.FS, path)
		if rerr != nil {
			return rerr
		}
		for _, m := range createTableRe.FindAllStringSubmatch(string(b), -1) {
			created[strings.ToLower(m[1])] = true
		}
		for _, m := range alterTableRe.FindAllStringSubmatch(string(b), -1) {
			tbl := strings.ToLower(m[1])
			altered[tbl] = append(altered[tbl], path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk migrations: %v", err)
	}
	if len(created) == 0 {
		t.Fatal("no CREATE TABLE found — the parser is broken, not the schema")
	}
	for tbl, files := range altered {
		if !created[tbl] {
			t.Errorf("ALTER TABLE %s in %v targets a table no migration creates", tbl, files)
		}
	}
}
