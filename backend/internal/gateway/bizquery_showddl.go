package gateway

// 数据查询里看表的 DDL(2026-11-12)。
//
// MySQL 系自带 SHOW CREATE TABLE,数据查询早已放行(bizquery_forms.go 的白名单)。PostgreSQL 系
// (含 DWS / GaussDB)与 Oracle 没有这条语句:PG 的建表语句只有 pg_dump 在客户端拼,Oracle 要调
// DBMS_METADATA —— 而判定层不让业务方调任何 DBMS_ 包。于是业务方在 DWS 上写 SHOW CREATE TABLE,
// 得到的是"数据查询只允许只读查询 …,不能执行 SHOW"(2026-11-12 UAT-DWS 报障)。
//
// 这里与 Oracle 的 DESC 同一个做法:判定层只认最朴素的一种写法 `SHOW CREATE TABLE [schema.]表`,
// 执行层不把它发给库,而是由网关用库树「查看 DDL」那一套(pg_tableddl.go / oracle_tableddl.go)
// 渲染出建表语句,结果形状与 MySQL 的 SHOW CREATE TABLE 一致:两列 Table / Create Table、一行。
//
// 跨 schema 的边界不放松:PG 系仍过 bizPGCatalog(系统目录一律拒),Oracle 仍过"别的 schema"检查
// (限定符是别的用户就拒)—— 两者都在 BizReadOnlyCheck 里、对原文做,和 SELECT 同一道闸。

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"

	"aegisdb/internal/apimsg"
	"aegisdb/internal/model"
)

var bizShowCreateTableRe = regexp.MustCompile(`(?is)^SHOW\s+CREATE\s+TABLE\s+(?P<a>` + bizIdent + `)(?:\s*\.\s*(?P<b>` + bizIdent + `))?$`)

// BizShowCreateTable 认 PG 系 / Oracle 数据查询里的 `SHOW CREATE TABLE [schema.]表`,返回 schema
// (空 = 当前)与表名,均已换成该引擎数据字典里的写法(PG 不加引号转小写,Oracle 转大写,
// 加了引号的照原样)。MySQL 系返回 false:那边的 SHOW CREATE TABLE 是库自己的语句,照常下发。
func BizShowCreateTable(engine, query string) (schema, name string, ok bool) {
	fam := engineFamily(engine)
	if fam != familyPostgres && fam != familyOracle {
		return "", "", false
	}
	m := bizShowCreateTableRe.FindStringSubmatch(bizStmtText(query))
	if m == nil {
		return "", "", false
	}
	norm := pgDictName
	if fam == familyOracle {
		norm = oracleDictName
	}
	a, b := m[bizShowCreateTableRe.SubexpIndex("a")], m[bizShowCreateTableRe.SubexpIndex("b")]
	if b == "" {
		return "", norm(a), true
	}
	return norm(a), norm(b), true
}

// pgDictName:标识符在 PG 系统目录里的写法。双引号(或顺手写的反引号)里的照原样,裸的转小写。
func pgDictName(raw string) string {
	if len(raw) >= 2 && (raw[0] == '"' || raw[0] == '`') {
		return raw[1 : len(raw)-1]
	}
	return strings.ToLower(raw)
}

// bizShowCreate 执行一条已通过判定的 PG 系 / Oracle SHOW CREATE TABLE:由网关渲染建表语句。
func bizShowCreate(ctx context.Context, conn *model.Connection, schema, name string) (BizQueryResult, error) {
	var out BizQueryResult
	var ddl string
	var err error
	switch engineFamily(conn.Engine) {
	case familyPostgres, familyOracle:
		if schema == "" {
			if schema, err = defaultTableScope(ctx, conn, name); err != nil {
				return out, err
			}
		}
		if engineFamily(conn.Engine) == familyPostgres {
			ddl, err = pgTableDDL(conn, schema, name)
		} else {
			ddl, err = oracleTableDDL(conn, schema, name)
		}
	default:
		return out, apimsg.Errorf("不支持的对象类型 %q", "table")
	}
	if err != nil {
		return out, err
	}
	out.Columns = []string{"Table", "Create Table"}
	out.Data = [][]string{{name, ddl}}
	return out, nil
}

// defaultTableScope:不带 schema 的表名在 PG 系 / Oracle 上落在哪个 schema —— 和这条连接上
// 执行 SELECT * FROM 表 找到的是同一张。PG 系按 search_path;Oracle 先看连接上配的目标 schema,
// 没配就用账号自己的当前 schema。
func defaultTableScope(ctx context.Context, conn *model.Connection, name string) (string, error) {
	switch engineFamily(conn.Engine) {
	case familyPostgres:
		return pgVisibleSchema(ctx, conn, name)
	case familyOracle:
		if s := oracleTargetSchema(conn); s != "" {
			return s, nil
		}
		return oracleCurrentSchema(ctx, conn)
	}
	return "", nil
}

// pgVisibleSchema:不带 schema 的表名按 search_path 落在哪个 schema(和 SELECT * FROM 表 找到的是同一张)。
func pgVisibleSchema(ctx context.Context, conn *model.Connection, name string) (string, error) {
	db, release, err := openConn(conn)
	if err != nil {
		return "", err
	}
	defer release()
	var s string
	err = db.QueryRowContext(ctx,
		`SELECT n.nspname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE c.relname = $1 AND c.relkind IN ('r','p','v','m','f') AND pg_table_is_visible(c.oid) LIMIT 1`, name).Scan(&s)
	if errors.Is(err, sql.ErrNoRows) {
		return "", apimsg.Errorf("对象不存在(数据库 %s, schema %s)", conn.Database, "search_path")
	}
	return s, err
}

// oracleCurrentSchema:连接上没配目标 schema 时,账号自己的当前 schema。
func oracleCurrentSchema(ctx context.Context, conn *model.Connection) (string, error) {
	db, release, err := openConn(conn)
	if err != nil {
		return "", err
	}
	defer release()
	var s string
	err = db.QueryRowContext(ctx, `SELECT SYS_CONTEXT('USERENV','CURRENT_SCHEMA') FROM dual`).Scan(&s)
	return s, err
}
