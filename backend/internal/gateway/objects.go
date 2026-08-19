package gateway

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"velagateway/internal/model"
)

// DbObjects lists a database's (or schema's / owner's) programmable objects,
// grouped by kind. Only the kinds an engine actually has are populated:
// MySQL has functions/procedures/triggers, PostgreSQL functions/procedures,
// Oracle functions/procedures/packages, SQLite triggers.
type DbObjects struct {
	Functions  []string
	Procedures []string
	Packages   []string
	Triggers   []string
}

// objectIdentRe restricts an object/schema name used inside SHOW CREATE (where
// the driver offers no parameter binding) to a safe identifier. Same stance as
// dbNameRe; `#` is included for Oracle identifiers.
var objectIdentRe = regexp.MustCompile(`^[A-Za-z0-9_$#.-]+$`)

// objectQueryTimeout bounds one catalog introspection, same as RealSchema's.
const objectQueryTimeout = 15 * time.Second

// RealObjects introspects the live target's programmable objects under one
// database (MySQL), schema (PostgreSQL family) or owner (Oracle). SQLite has a
// single namespace, so scope is ignored there. Requires real credentials.
func RealObjects(conn *model.Connection, scope string) (*DbObjects, error) {
	e := strings.ToLower(conn.Engine)
	switch {
	case strings.Contains(e, "sqlite"):
		return sqliteObjects(conn)
	case strings.Contains(e, "mysql"), strings.Contains(e, "mariadb"),
		strings.Contains(e, "tidb"), strings.Contains(e, "polardb"):
		return mysqlObjects(conn, scope)
	case strings.Contains(e, "postgre"), strings.Contains(e, "dws"), strings.Contains(e, "gauss"):
		return pgObjects(conn, scope)
	case strings.Contains(e, "oracle"):
		return oracleObjects(conn, scope)
	}
	return nil, fmt.Errorf("引擎 %q 暂不支持对象浏览", conn.Engine)
}

// RealObjectSource returns one object's source text (its CREATE statement or
// catalog-stored definition). What SQLcl's `ddl` does for Oracle, done here via
// the same catalog views every client tool ultimately reads (ALL_SOURCE,
// information_schema, pg_get_functiondef, sqlite_master) — the gateway ships as
// a single Go binary and shells out to nothing.
func RealObjectSource(conn *model.Connection, scope, typ, name string) (string, error) {
	if !objectIdentRe.MatchString(name) || (scope != "" && !objectIdentRe.MatchString(scope)) {
		return "", fmt.Errorf("非法的对象名")
	}
	e := strings.ToLower(conn.Engine)
	switch {
	case strings.Contains(e, "sqlite"):
		return sqliteObjectSource(conn, typ, name)
	case strings.Contains(e, "mysql"), strings.Contains(e, "mariadb"),
		strings.Contains(e, "tidb"), strings.Contains(e, "polardb"):
		return mysqlObjectSource(conn, scope, typ, name)
	case strings.Contains(e, "postgre"), strings.Contains(e, "dws"), strings.Contains(e, "gauss"):
		return pgObjectSource(conn, scope, typ, name)
	case strings.Contains(e, "oracle"):
		return oracleObjectSource(conn, scope, typ, name)
	}
	return "", fmt.Errorf("引擎 %q 暂不支持对象浏览", conn.Engine)
}

// ---------------------------------------------------------------- MySQL family

func mysqlObjects(conn *model.Connection, database string) (*DbObjects, error) {
	// information_schema is global — introspect without binding a default
	// database, same reasoning as RealSchema.
	c := *conn
	c.Database = ""
	db, release, err := openConn(&c)
	if err != nil {
		return nil, err
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), objectQueryTimeout)
	defer cancel()

	out := &DbObjects{}
	rows, err := db.QueryContext(ctx,
		`SELECT routine_name, routine_type FROM information_schema.routines WHERE routine_schema = ? ORDER BY routine_name`, database)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var name, typ string
		if err := rows.Scan(&name, &typ); err != nil {
			return nil, err
		}
		if strings.EqualFold(typ, "PROCEDURE") {
			out.Procedures = append(out.Procedures, name)
		} else {
			out.Functions = append(out.Functions, name)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	trows, err := db.QueryContext(ctx,
		`SELECT trigger_name FROM information_schema.triggers WHERE trigger_schema = ? ORDER BY trigger_name`, database)
	if err != nil {
		return nil, err
	}
	defer trows.Close()
	for trows.Next() {
		var name string
		if err := trows.Scan(&name); err != nil {
			return nil, err
		}
		out.Triggers = append(out.Triggers, name)
	}
	return out, trows.Err()
}

func mysqlObjectSource(conn *model.Connection, database, typ, name string) (string, error) {
	var kw string
	switch typ {
	case "function":
		kw = "FUNCTION"
	case "procedure":
		kw = "PROCEDURE"
	case "trigger":
		kw = "TRIGGER"
	case "table":
		kw = "TABLE" // SHOW CREATE TABLE also answers for views
	default:
		return "", fmt.Errorf("不支持的对象类型 %q", typ)
	}
	c := *conn
	c.Database = ""
	db, release, err := openConn(&c)
	if err != nil {
		return "", err
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), objectQueryTimeout)
	defer cancel()

	// SHOW CREATE takes no bind parameters; both identifiers were validated
	// against objectIdentRe by the caller, and are additionally backtick-quoted.
	q := fmt.Sprintf("SHOW CREATE %s `%s`.`%s`", kw, database, name)
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return "", err
	}
	if !rows.Next() {
		return "", fmt.Errorf("对象不存在")
	}
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return "", err
	}
	// The definition column moves between SHOW CREATE variants (index 1 for
	// TABLE/VIEW, index 2 for FUNCTION/PROCEDURE/TRIGGER), so find it by NAME:
	// "Create Table" / "Create View" / "Create Function" / "Create Procedure" /
	// "SQL Original Statement". It is NULL when the account lacks the privilege
	// to see the body.
	for i, col := range cols {
		if strings.HasPrefix(col, "Create ") || col == "SQL Original Statement" {
			if src := cellString(vals[i]); strings.TrimSpace(src) != "" {
				return src, nil
			}
		}
	}
	return "", fmt.Errorf("无法读取对象定义(账号可能缺少查看权限)")
}

// ---------------------------------------------------------------- PostgreSQL family

func pgObjects(conn *model.Connection, schema string) (*DbObjects, error) {
	db, release, err := openConn(conn)
	if err != nil {
		return nil, err
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), objectQueryTimeout)
	defer cancel()

	out := &DbObjects{}
	// prokind arrived in PG 11; GaussDB/DWS descend from older PostgreSQL, so
	// fall back to "everything in pg_proc is a function" when it is missing.
	rows, err := db.QueryContext(ctx,
		`SELECT DISTINCT p.proname, CASE p.prokind WHEN 'p' THEN 'procedure' ELSE 'function' END
		 FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
		 WHERE n.nspname = $1 AND p.prokind IN ('f','p') ORDER BY 1`, schema)
	if err != nil {
		rows, err = db.QueryContext(ctx,
			`SELECT DISTINCT p.proname, 'function'
			 FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
			 WHERE n.nspname = $1 ORDER BY 1`, schema)
		if err != nil {
			return nil, err
		}
	}
	defer rows.Close()
	for rows.Next() {
		var name, typ string
		if err := rows.Scan(&name, &typ); err != nil {
			return nil, err
		}
		if typ == "procedure" {
			out.Procedures = append(out.Procedures, name)
		} else {
			out.Functions = append(out.Functions, name)
		}
	}
	return out, rows.Err()
}

func pgObjectSource(conn *model.Connection, schema, typ, name string) (string, error) {
	if typ == "table" {
		return pgTableDDL(conn, schema, name)
	}
	if typ != "function" && typ != "procedure" {
		return "", fmt.Errorf("不支持的对象类型 %q", typ)
	}
	db, release, err := openConn(conn)
	if err != nil {
		return "", err
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), objectQueryTimeout)
	defer cancel()

	// One name may be several overloads; return them all, separated.
	rows, err := db.QueryContext(ctx,
		`SELECT pg_get_functiondef(p.oid)
		 FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
		 WHERE n.nspname = $1 AND p.proname = $2 ORDER BY p.oid`, schema, name)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	defs := []string{}
	for rows.Next() {
		var def string
		if err := rows.Scan(&def); err != nil {
			return "", err
		}
		defs = append(defs, def)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(defs) == 0 {
		return "", fmt.Errorf("对象不存在")
	}
	return strings.Join(defs, "\n\n"), nil
}

// pgTableDDL reconstructs a readable CREATE TABLE for the PostgreSQL family.
// PostgreSQL has no SHOW CREATE TABLE and no server function for it (pg_dump
// builds the DDL client-side), so this composes the honest equivalent from
// information_schema plus the real index definitions from pg_indexes.
func pgTableDDL(conn *model.Connection, schema, name string) (string, error) {
	db, release, err := openConn(conn)
	if err != nil {
		return "", err
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), objectQueryTimeout)
	defer cancel()

	rows, err := db.QueryContext(ctx,
		`SELECT column_name, data_type,
		        COALESCE(character_maximum_length, -1),
		        is_nullable, COALESCE(column_default, '')
		 FROM information_schema.columns
		 WHERE table_schema = $1 AND table_name = $2 ORDER BY ordinal_position`, schema, name)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	lines := []string{}
	for rows.Next() {
		var col, dtype, nullable, def string
		var maxLen int64
		if err := rows.Scan(&col, &dtype, &maxLen, &nullable, &def); err != nil {
			return "", err
		}
		l := fmt.Sprintf("  %s %s", col, dtype)
		if maxLen > 0 {
			l += fmt.Sprintf("(%d)", maxLen)
		}
		if def != "" {
			l += " DEFAULT " + def
		}
		if nullable == "NO" {
			l += " NOT NULL"
		}
		lines = append(lines, l)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(lines) == 0 {
		return "", fmt.Errorf("对象不存在")
	}
	ddl := fmt.Sprintf("-- 由 information_schema 重建(PostgreSQL 无 SHOW CREATE TABLE)\nCREATE TABLE %s.%s (\n%s\n);",
		schema, name, strings.Join(lines, ",\n"))

	irows, err := db.QueryContext(ctx,
		`SELECT indexdef FROM pg_indexes WHERE schemaname = $1 AND tablename = $2 ORDER BY indexname`, schema, name)
	if err == nil {
		defer irows.Close()
		for irows.Next() {
			var def string
			if err := irows.Scan(&def); err == nil {
				ddl += "\n\n" + def + ";"
			}
		}
	}
	return ddl, nil
}

// ---------------------------------------------------------------- Oracle

func oracleObjects(conn *model.Connection, owner string) (*DbObjects, error) {
	db, release, err := openConn(conn)
	if err != nil {
		return nil, err
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), objectQueryTimeout)
	defer cancel()

	out := &DbObjects{}
	rows, err := db.QueryContext(ctx,
		`SELECT object_name, object_type FROM all_objects
		 WHERE owner = :1 AND object_type IN ('FUNCTION','PROCEDURE','PACKAGE')
		 ORDER BY object_type, object_name`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var name, typ string
		if err := rows.Scan(&name, &typ); err != nil {
			return nil, err
		}
		switch typ {
		case "PROCEDURE":
			out.Procedures = append(out.Procedures, name)
		case "PACKAGE":
			out.Packages = append(out.Packages, name)
		default:
			out.Functions = append(out.Functions, name)
		}
	}
	return out, rows.Err()
}

func oracleObjectSource(conn *model.Connection, owner, typ, name string) (string, error) {
	var types []string
	switch typ {
	case "table":
		return oracleTableDDL(conn, owner, name)
	case "function":
		types = []string{"FUNCTION"}
	case "procedure":
		types = []string{"PROCEDURE"}
	case "package":
		// spec first, then body — the order a reader wants them in.
		types = []string{"PACKAGE", "PACKAGE BODY"}
	default:
		return "", fmt.Errorf("不支持的对象类型 %q", typ)
	}
	db, release, err := openConn(conn)
	if err != nil {
		return "", err
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), objectQueryTimeout)
	defer cancel()

	parts := []string{}
	for _, ot := range types {
		rows, err := db.QueryContext(ctx,
			`SELECT text FROM all_source WHERE owner = :1 AND name = :2 AND type = :3 ORDER BY line`,
			owner, name, ot)
		if err != nil {
			return "", err
		}
		var b strings.Builder
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				rows.Close()
				return "", err
			}
			b.WriteString(line)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return "", err
		}
		rows.Close()
		if b.Len() > 0 {
			// ALL_SOURCE stores the text without the CREATE preamble.
			parts = append(parts, "CREATE OR REPLACE "+b.String())
		}
	}
	if len(parts) == 0 {
		return "", fmt.Errorf("对象不存在")
	}
	return strings.Join(parts, "\n/\n\n"), nil
}

// oracleTableDDL asks DBMS_METADATA for the authoritative DDL (what SQLcl's
// `ddl` prints); when the account may not run it, fall back to a readable
// reconstruction from ALL_TAB_COLUMNS.
func oracleTableDDL(conn *model.Connection, owner, name string) (string, error) {
	db, release, err := openConn(conn)
	if err != nil {
		return "", err
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), objectQueryTimeout)
	defer cancel()

	var ddl string
	if err := db.QueryRowContext(ctx,
		`SELECT DBMS_METADATA.GET_DDL('TABLE', :1, :2) FROM dual`, name, owner).Scan(&ddl); err == nil {
		if strings.TrimSpace(ddl) != "" {
			return ddl, nil
		}
	}

	rows, err := db.QueryContext(ctx,
		`SELECT column_name, data_type, data_length, nullable
		 FROM all_tab_columns WHERE owner = :1 AND table_name = :2 ORDER BY column_id`, owner, name)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	lines := []string{}
	for rows.Next() {
		var col, dtype, nullable string
		var dlen int64
		if err := rows.Scan(&col, &dtype, &dlen, &nullable); err != nil {
			return "", err
		}
		l := fmt.Sprintf("  %s %s", col, dtype)
		if dlen > 0 && (strings.Contains(dtype, "CHAR") || strings.Contains(dtype, "RAW")) {
			l += fmt.Sprintf("(%d)", dlen)
		}
		if nullable == "N" {
			l += " NOT NULL"
		}
		lines = append(lines, l)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(lines) == 0 {
		return "", fmt.Errorf("对象不存在")
	}
	return fmt.Sprintf("-- 由 ALL_TAB_COLUMNS 重建(DBMS_METADATA.GET_DDL 不可用)\nCREATE TABLE %s.%s (\n%s\n);",
		owner, name, strings.Join(lines, ",\n")), nil
}

// ---------------------------------------------------------------- SQLite

func sqliteObjects(conn *model.Connection) (*DbObjects, error) {
	db, release, err := openConn(conn)
	if err != nil {
		return nil, err
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), objectQueryTimeout)
	defer cancel()

	out := &DbObjects{}
	rows, err := db.QueryContext(ctx,
		`SELECT name FROM sqlite_master WHERE type = 'trigger' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out.Triggers = append(out.Triggers, name)
	}
	return out, rows.Err()
}

func sqliteObjectSource(conn *model.Connection, typ, name string) (string, error) {
	if typ != "trigger" && typ != "table" {
		return "", fmt.Errorf("不支持的对象类型 %q", typ)
	}
	db, release, err := openConn(conn)
	if err != nil {
		return "", err
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), objectQueryTimeout)
	defer cancel()

	if typ == "trigger" {
		var src string
		if err := db.QueryRowContext(ctx,
			`SELECT sql FROM sqlite_master WHERE type = 'trigger' AND name = ?`, name).Scan(&src); err != nil {
			return "", fmt.Errorf("对象不存在")
		}
		return src, nil
	}
	// table (or view): the stored CREATE statement plus its named indexes.
	var src string
	if err := db.QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE type IN ('table','view') AND name = ?`, name).Scan(&src); err != nil {
		return "", fmt.Errorf("对象不存在")
	}
	rows, err := db.QueryContext(ctx,
		`SELECT sql FROM sqlite_master WHERE type = 'index' AND tbl_name = ? AND sql IS NOT NULL ORDER BY name`, name)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var idx string
			if rows.Scan(&idx) == nil && idx != "" {
				src += ";\n\n" + idx
			}
		}
	}
	return src, nil
}
