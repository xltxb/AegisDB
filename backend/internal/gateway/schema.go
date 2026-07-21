package gateway

import (
	"context"
	"fmt"
	"strings"
	"time"

	"velagateway/internal/model"
)

// SchemaGroup is one database/schema and its tables, introspected from a live
// target instance.
type SchemaGroup struct {
	Database string
	Tables   []string
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
	query, singleDB := schemaIntrospectQuery(conn)
	if query == "" {
		return nil, fmt.Errorf("引擎 %q 暂不支持库表加载", conn.Engine)
	}
	db, release, err := openConn(conn)
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
