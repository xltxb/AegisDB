package gateway

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"aegisdb/internal/apimsg"
	"aegisdb/internal/model"
)

// maxBizTables bounds the table dropdown on the data-query page. It lists ONE
// database, so the cap is a guard against a pathological schema, not a sampling
// of the instance the way maxSchemaRows is for the terminal tree.
const maxBizTables = 5000

// RealBizTables lists the tables and views of one database, for the data-query
// page's table picker.
//
// It deliberately does not reuse RealSchema: that sweeps every schema on a MySQL
// or Oracle instance and stops at maxSchemaRows, so on a large instance the
// granted database could be cut off entirely. This asks for that one database.
//
// Names come back as the query would write them from inside the database the
// grant covers: bare on MySQL / Oracle (the session is switched to that database
// / schema), schema-qualified on PostgreSQL unless in `public`.
// truncated reports that the list stopped at maxBizTables.
func RealBizTables(conn *model.Connection, database string) (names []string, truncated bool, err error) {
	e := strings.ToLower(conn.Engine)
	target := conn
	var q string
	var args []any
	switch {
	case strings.Contains(e, "tidb"), strings.Contains(e, "mysql"), strings.Contains(e, "mariadb"), strings.Contains(e, "polardb"):
		// information_schema is global: connect without a default database so a
		// database the account cannot USE still fails as "no rows", not as a login error.
		c := *conn
		c.Database = ""
		target = &c
		q = `SELECT '', table_name FROM information_schema.tables ` +
			`WHERE table_schema = ? AND table_type IN ('BASE TABLE','VIEW') ORDER BY table_name`
		args = []any{database}
	case strings.Contains(e, "postgre"), strings.Contains(e, "dws"), strings.Contains(e, "gauss"):
		// PostgreSQL's information_schema is per database: connect to the granted one.
		c := *conn
		c.Database = database
		target = &c
		q = `SELECT table_schema, table_name FROM information_schema.tables ` +
			`WHERE table_type IN ('BASE TABLE','VIEW') AND table_schema NOT IN ('pg_catalog','information_schema') ` +
			`ORDER BY CASE WHEN table_schema = 'public' THEN 0 ELSE 1 END, table_schema, table_name`
	case strings.Contains(e, "oracle"):
		q = `SELECT '', table_name FROM all_tables WHERE owner = :1 ` +
			`UNION SELECT '', view_name FROM all_views WHERE owner = :2 ORDER BY 2`
		owner := strings.ToUpper(strings.TrimSpace(database))
		args = []any{owner, owner}
	default:
		return nil, false, apimsg.Errorf("引擎 %q 暂不支持列出表", conn.Engine)
	}

	db, release, err := openConn(target)
	if err != nil {
		return nil, false, err
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	names = []string{}
	for rows.Next() {
		if len(names) >= maxBizTables {
			return names, true, nil
		}
		var schema sql.NullString
		var name string
		if err := rows.Scan(&schema, &name); err != nil {
			return nil, false, err
		}
		if s := schema.String; s != "" && s != "public" {
			name = s + "." + name
		}
		names = append(names, name)
	}
	return names, false, rows.Err()
}
