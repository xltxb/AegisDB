// Package migrations embeds the canonical SQL migration files so the server
// binary is self-contained (no external migrations/ directory needed at deploy).
package migrations

import "embed"

// FS holds every *.sql migration, applied in lexical filename order by the
// migration runner (internal/bootstrap.Migrate).
//
//go:embed *.sql
var FS embed.FS
