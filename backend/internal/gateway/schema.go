package gateway

import (
	"context"
	"fmt"
	"strings"
	"time"

	"velagateway/internal/model"
)

// SchemaGroup is one database and its contents, introspected from a live target.
// Engines with a schema layer (PostgreSQL/GaussDB) populate Schemas; flat engines
// (MySQL/SQLite/Oracle-by-owner) populate Tables directly.
type SchemaGroup struct {
	Database string
	Schemas  []SchemaEntry // database → schema → tables (PostgreSQL)
	Tables   []string      // database → tables (flat engines)
}

// SchemaEntry is a schema within a database and its tables.
type SchemaEntry struct {
	Name   string
	Tables []string
}

// maxSchemaRows bounds a single introspection so a huge instance can't build an
// unwieldy tree (or hold the connection open indefinitely).
const maxSchemaRows = 5000

// RealSchema introspects the live target's databases/schemas and their tables.
// Engine-specific: MySQL/TiDB list every user schema via information_schema;
// PostgreSQL/GaussDB(DWS) list the schemas within the connected database; Oracle
// groups by owner via all_tables; SQLite reads sqlite_master (a single database
// named after the file). Requires real credentials (see RealExecSupported).
func RealSchema(conn *model.Connection) ([]SchemaGroup, error) {
	// PostgreSQL/GaussDB is scoped to a single database and information_schema is
	// per-database, so it can't be introspected in one pass like MySQL. Without a
	// database, list the SERVER's databases (like SHOW DATABASES); with one, list
	// that database's tables — giving the tree a database → tables shape.
	e := strings.ToLower(conn.Engine)
	if strings.Contains(e, "postgre") || strings.Contains(e, "dws") || strings.Contains(e, "gauss") {
		if strings.TrimSpace(conn.Database) == "" {
			return pgListDatabases(conn)
		}
		return pgListTables(conn)
	}

	query, singleDB := schemaIntrospectQuery(conn)
	if query == "" {
		return nil, fmt.Errorf("引擎 %q 暂不支持库表加载", conn.Engine)
	}
	// For MySQL/TiDB, introspect against the server WITHOUT binding a default
	// database: information_schema is global, so a missing/empty/nonexistent
	// conn.Database must not make the connect fail with "Unknown database".
	target := conn
	if strings.Contains(e, "mysql") || strings.Contains(e, "tidb") || strings.Contains(e, "mariadb") {
		c := *conn
		c.Database = ""
		target = &c
	}
	db, release, err := openConn(target)
	if err != nil {
		return nil, err
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	order := []string{}
	byDB := map[string][]string{}
	n := 0
	for rows.Next() {
		var dbName, tbl string
		if singleDB {
			if err := rows.Scan(&tbl); err != nil {
				return nil, err
			}
			dbName = singleDBName(conn)
		} else if err := rows.Scan(&dbName, &tbl); err != nil {
			return nil, err
		}
		if _, seen := byDB[dbName]; !seen {
			order = append(order, dbName)
		}
		byDB[dbName] = append(byDB[dbName], tbl)
		if n++; n >= maxSchemaRows {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]SchemaGroup, 0, len(order))
	for _, d := range order {
		out = append(out, SchemaGroup{Database: d, Tables: byDB[d]})
	}
	return out, nil
}

// pgListDatabases lists the connectable, non-template databases on a PostgreSQL /
// GaussDB server. The connection defaults to the "postgres" maintenance database
// (see engineDriver) just to run this catalog query; each result is returned as a
// database the user can then target (tables load once a database is configured).
func pgListDatabases(conn *model.Connection) ([]SchemaGroup, error) {
	db, release, err := openConn(conn)
	if err != nil {
		return nil, err
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	rows, err := db.QueryContext(ctx,
		`SELECT datname FROM pg_database WHERE datistemplate = false AND datallowconn = true ORDER BY datname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SchemaGroup{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, SchemaGroup{Database: name}) // tables load after the db is selected
		if len(out) >= maxSchemaRows {
			break
		}
	}
	return out, rows.Err()
}

// pgListTables lists the connected PostgreSQL/GaussDB database's tables grouped by
// schema, so the tree shows database → schema → tables.
func pgListTables(conn *model.Connection) ([]SchemaGroup, error) {
	db, release, err := openConn(conn) // conn.Database is set
	if err != nil {
		return nil, err
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	rows, err := db.QueryContext(ctx,
		`SELECT table_schema, table_name FROM information_schema.tables `+
			`WHERE table_type IN ('BASE TABLE','VIEW') `+
			`AND table_schema NOT IN ('pg_catalog','information_schema') `+
			`ORDER BY table_schema, table_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	order := []string{}
	bySchema := map[string][]string{}
	n := 0
	for rows.Next() {
		var schema, tbl string
		if err := rows.Scan(&schema, &tbl); err != nil {
			return nil, err
		}
		if _, seen := bySchema[schema]; !seen {
			order = append(order, schema)
		}
		bySchema[schema] = append(bySchema[schema], tbl)
		if n++; n >= maxSchemaRows {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	schemas := make([]SchemaEntry, 0, len(order))
	for _, s := range order {
		schemas = append(schemas, SchemaEntry{Name: s, Tables: bySchema[s]})
	}
	return []SchemaGroup{{Database: conn.Database, Schemas: schemas}}, nil
}

// schemaIntrospectQuery returns the engine-appropriate introspection SQL and
// whether it yields a single-column (table-only) result set.
func schemaIntrospectQuery(conn *model.Connection) (query string, singleDB bool) {
	e := strings.ToLower(conn.Engine)
	switch {
	case strings.Contains(e, "sqlite"):
		return `SELECT name FROM sqlite_master WHERE type IN ('table','view') AND name NOT LIKE 'sqlite_%' ORDER BY name`, true
	case strings.Contains(e, "tidb") || strings.Contains(e, "mysql") || strings.Contains(e, "mariadb"):
		return `SELECT table_schema, table_name FROM information_schema.tables ` +
			`WHERE table_type IN ('BASE TABLE','VIEW') ` +
			`AND table_schema NOT IN ('mysql','information_schema','performance_schema','sys') ` +
			`ORDER BY table_schema, table_name`, false
	case strings.Contains(e, "postgre") || strings.Contains(e, "dws") || strings.Contains(e, "gauss"):
		return `SELECT table_schema, table_name FROM information_schema.tables ` +
			`WHERE table_type IN ('BASE TABLE','VIEW') ` +
			`AND table_schema NOT IN ('pg_catalog','information_schema') ` +
			`ORDER BY table_schema, table_name`, false
	case strings.Contains(e, "oracle"):
		return `SELECT owner, table_name FROM all_tables ` +
			`WHERE owner NOT IN ('SYS','SYSTEM','OUTLN','XDB','MDSYS','CTXSYS','DBSNMP','APPQOSSYS','ORDSYS','WMSYS','LBACSYS','DVSYS','GSMADMIN_INTERNAL') ` +
			`ORDER BY owner, table_name`, false
	}
	return "", false
}

// singleDBName is the display name for engines with a single database (SQLite):
// the file's base name, else the connection name, else "main".
func singleDBName(conn *model.Connection) string {
	if conn.Database != "" {
		base := conn.Database
		if i := strings.LastIndexAny(base, `/\`); i >= 0 {
			base = base[i+1:]
		}
		return base
	}
	if conn.Name != "" {
		return conn.Name
	}
	return "main"
}
