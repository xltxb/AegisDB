package gateway

// TableColumns —— 终端 Tab 补全用的"这张表有哪些列"。
//
// 实时查目标库,不走元数据缓存:缓存默认关着,开了也可能是昨天的,而补全补出一个昨天就删掉的
// 列名,比不补更糟。与 LiveColumnsOf(发布单收单检查用)的区别:这里按**库 / schema 过滤**(同名表
// 在别的库里的列不能混进来),**保留原始大小写**(补全插进去的就是它),**按建表顺序**返回。
//
// 表名、限定名都走参数绑定,不拼进语句;中文表名因此也能用(safeIdent 为了能拼进语句而挡掉它们)。

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"

	"aegisdb/internal/apimsg"
	"aegisdb/internal/model"
)

// ErrBadTableName:不像一个表名(含空白、引号、分号、控制字符,或过长)。
var ErrBadTableName = errors.New("表名不合法")

const columnsTimeout = 8 * time.Second

// SplitQualified 把 `a.b` 拆成限定名与表名,去掉两侧的反引号 / 双引号。没有点时限定名为空。
func SplitQualified(name string) (qualifier, table string) {
	name = strings.TrimSpace(name)
	if i := strings.LastIndex(name, "."); i >= 0 {
		qualifier, table = name[:i], name[i+1:]
	} else {
		table = name
	}
	unq := func(s string) string { return strings.Trim(strings.TrimSpace(s), "`\"") }
	return unq(qualifier), unq(table)
}

func plausibleIdent(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) || strings.ContainsRune("'\"`;\\", r) {
			return false
		}
	}
	return true
}

// TableColumns 返回一张表的列名(建表顺序、原始大小写)。name 可以带限定名:MySQL 系是库,
// PostgreSQL 系是 schema,Oracle 是 owner。没有这张表返回空切片。
func tableColumnsOnce(conn *model.Connection, name string) ([]string, error) {
	qual, table := SplitQualified(name)
	if !plausibleIdent(table) || (qual != "" && !plausibleIdent(qual)) {
		return nil, ErrBadTableName
	}
	var q string
	var args []any
	switch engineFamily(conn.Engine) {
	case familyMySQL:
		q = `SELECT column_name FROM information_schema.columns
		     WHERE table_schema = COALESCE(NULLIF(?, ''), DATABASE()) AND table_name = ?
		     ORDER BY ordinal_position`
		args = []any{qual, table}
	case familyPostgres:
		// 不加引号的标识符在 PostgreSQL 里折成小写:敲 Orders 指的是 orders。
		q = `SELECT column_name FROM information_schema.columns
		     WHERE table_schema = COALESCE(NULLIF($1, ''), current_schema())
		       AND (table_name = $2 OR table_name = LOWER($2))
		     ORDER BY ordinal_position`
		args = []any{qual, table}
	case familyOracle:
		// 不加引号的标识符在 Oracle 里折成大写;空串在 Oracle 里就是 NULL。
		q = `SELECT column_name FROM all_tab_columns
		     WHERE owner = NVL(UPPER(:1), SYS_CONTEXT('USERENV', 'CURRENT_SCHEMA'))
		       AND (table_name = :2 OR table_name = UPPER(:3))
		     ORDER BY column_id`
		args = []any{qual, table, table}
	default:
		return nil, apimsg.Errorf("引擎 %q 暂不支持取列", conn.Engine)
	}

	db, release, err := openConn(conn)
	if err != nil {
		return nil, err
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), columnsTimeout)
	defer cancel()
	var runner sqlRunner = db
	// Oracle 选了 schema:和执行同一个会话口径(CURRENT_SCHEMA),不限定 owner 时查的就是它。
	if schema := oracleTargetSchema(conn); schema != "" {
		sc, cerr := db.Conn(ctx)
		if cerr != nil {
			return nil, cerr
		}
		defer sc.Close()
		defer oracleSchemaRestorer(ctx, sc)() // 先于 Close:切回原 schema 再还给池子
		if _, aerr := sc.ExecContext(ctx, `ALTER SESSION SET CURRENT_SCHEMA = "`+schema+`"`); aerr != nil {
			return nil, aerr
		}
		runner = sc
	}
	rows, err := runner.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
