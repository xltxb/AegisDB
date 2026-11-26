package sqlmigrate

// Exposed to the external test package (sqlmigrate_test), which cannot live in
// this package: it uses testpg, and testpg imports sqlmigrate.

const PGMigrationLockKey = pgMigrationLockKey

var SplitSQLStatements = splitSQLStatements
