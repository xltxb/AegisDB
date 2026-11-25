// Package migrations embeds the canonical SQL migration files so the server
// binary is self-contained (no external migrations/ directory needed at deploy).
package migrations

import (
	"embed"
	"io/fs"
)

// FS holds every *.sql migration, applied in lexical filename order by the
// migration runner (internal/bootstrap.Migrate).
//
//go:embed *.sql
var FS embed.FS

// pgFS 是 PostgreSQL 存储库的迁移(pg/*.sql)。起点 pg/0064_baseline.sql 等价于 MySQL
// 0001-0064;之后两边各写一份、编号相同,见 internal/bootstrap/pgschema_test.go。
//
//go:embed pg/*.sql
var pgFS embed.FS

// PGFS 以 pg/ 为根,与 FS 一样直接是 *.sql。
var PGFS, _ = fs.Sub(pgFS, "pg")
