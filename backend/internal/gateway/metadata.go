package gateway

// 远端库的表清单与表结构:一次探查,拿走全部。
//
// 与 RealSchema 的区别不是"多取了几列",而是**往返次数**。RealSchema 只要表名,一台
// 实例一两条查询就够;而"每张表的每一列"如果按表去问,843 张表就是 843 次往返 ——
// 定时任务这么跑,等于周期性地对生产库发起一轮扫描。
//
// 所以这里的原则是:**一台实例尽可能少的查询**。MySQL / Oracle 的数据字典本来就是
// 全库一张视图,两条查询(表 + 列)就能覆盖整台实例。PostgreSQL 家族是唯一的例外,理由见 realMetadataPG。
//
// 取回来的东西是**只读副本**。判定、执行、导出一律仍然走实时的目标库 —— 一份可能
// 过期的结构如果被拿去做判断,它就从"快"变成了"错"。

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"aegisdb/internal/apimsg"
	"aegisdb/internal/model"
)

// MetaTableRow / MetaColumnRow 是探查的产物,与 model.MetaTable / MetaColumn 一一对应。
//
// 在 gateway 包里另立一份而不是直接用 model:这一层不该知道数据怎么落库(主键、
// 索引、同步时间都是仓储的事),它只负责把远端的样子说清楚。
type MetaTableRow struct {
	Database string
	Schema   string
	Table    string
	Kind     string // table | view
	Comment  string
}

type MetaColumnRow struct {
	Database string
	Schema   string
	Table    string
	Ordinal  int
	Name     string
	DataType string
	Nullable bool
	Default  string
	Comment  string
	IsPK     bool
}

// metadataTimeout 一台实例一次全量探查的上限。
//
// 比 objectQueryTimeout 宽得多:这是全库的字典查询,几千张表的实例上它本来就慢。
// 但必须有上限 —— 一个卡住的探查会一直占着连接,而定时任务下一轮又会再来一个。
const metadataTimeout = 5 * time.Minute

// maxMetaRows 一台实例最多收多少行列信息。
//
// 与 maxSchemaRows 同一个理由,但门槛高得多:表清单是给人看的树,列信息是给机器
// 检索的索引。超出就截断并如实说 —— 悄悄少收一半,表现是"某些表没有列",而那看起来
// 和"这张表真的没有列"一模一样。
const maxMetaRows = 200000

// ErrMetaTruncated 说明这台实例的列信息超过了 maxMetaRows,收下的是前一部分。
var ErrMetaTruncated = apimsg.Errorf("列信息超过上限 %d 行,已截断", maxMetaRows)

// RealMetadata 探查一台实例的全部表与列。
//
// 返回的两组行已经配套:每一张表都在表清单里,列的四元组指回它。engine 不支持时
// 报错而不是返回空 —— 空清单读起来是"这台库很干净",那是这里最坏的一种谎。
func realMetadataOnce(conn *model.Connection) ([]MetaTableRow, []MetaColumnRow, error) {
	if !RealExecSupported(conn) {
		return nil, nil, apimsg.Errorf("实例未配置真实执行凭据,无法探查元数据")
	}
	db, release, err := openConn(conn)
	if err != nil {
		return nil, nil, err
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), metadataTimeout)
	defer cancel()

	switch engineFamily(conn.Engine) {
	case familyMySQL:
		return realMetadataMySQL(ctx, db)
	case familyPostgres:
		return realMetadataPG(ctx, db, conn)
	case familyOracle:
		return realMetadataOracle(ctx, db, conn)
	}
	return nil, nil, apimsg.Errorf("引擎 %q 暂不支持元数据探查", conn.Engine)
}

// ---------------------------------------------------------------- MySQL 家族

// mysqlSystemSchemas 是不入清单的库。它们是服务器自己的东西,不是用户的数据 ——
// 收进来只会让搜索结果里永远混着几百张 performance_schema 的表。metrics_schema 是 TiDB 的。
//
// 比较前要先 LOWER:TiDB 的系统库名是大写的,库名比较又区分大小写,不转就一个也滤不掉
// (issue #92)。
const mysqlSystemSchemas = `('information_schema','performance_schema','mysql','sys','metrics_schema')`

func realMetadataMySQL(ctx context.Context, db *sql.DB) ([]MetaTableRow, []MetaColumnRow, error) {
	started := time.Now()
	trows, err := db.QueryContext(ctx,
		`SELECT table_schema, table_name, table_type, IFNULL(table_comment,'')
		 FROM information_schema.tables
		 WHERE LOWER(table_schema) NOT IN `+mysqlSystemSchemas+`
		 ORDER BY table_schema, table_name`)
	if err != nil {
		return nil, nil, err
	}
	tables := []MetaTableRow{}
	for trows.Next() {
		var schema, name, typ, comment string
		if err := trows.Scan(&schema, &name, &typ, &comment); err != nil {
			trows.Close()
			return nil, nil, err
		}
		// MySQL 把库叫 schema,而它没有 schema 这一层 —— 落在 Database 上,与
		// RealSchema 给树的形状一致。
		tables = append(tables, MetaTableRow{
			Database: schema, Table: name, Kind: mysqlTableKind(typ), Comment: comment,
		})
	}
	if err := trows.Err(); err != nil {
		trows.Close()
		return nil, nil, err
	}
	trows.Close()
	tookTables := time.Since(started)

	crows, err := db.QueryContext(ctx,
		`SELECT table_schema, table_name, ordinal_position, column_name, column_type,
		        is_nullable, IFNULL(column_default,''), IFNULL(column_comment,''), column_key
		 FROM information_schema.columns
		 WHERE LOWER(table_schema) NOT IN `+mysqlSystemSchemas+`
		 ORDER BY table_schema, table_name, ordinal_position`)
	if err != nil {
		return nil, nil, err
	}
	defer crows.Close()
	cols := []MetaColumnRow{}
	truncated := false
	for crows.Next() {
		if len(cols) >= maxMetaRows {
			truncated = true
			break
		}
		var schema, table, name, typ, nullable, def, comment, key string
		var ord int
		if err := crows.Scan(&schema, &table, &ord, &name, &typ, &nullable, &def, &comment, &key); err != nil {
			return nil, nil, err
		}
		cols = append(cols, MetaColumnRow{
			Database: schema, Table: table, Ordinal: ord, Name: name, DataType: typ,
			Nullable: strings.EqualFold(nullable, "YES"), Default: def, Comment: comment,
			IsPK: key == "PRI",
		})
	}
	if err := crows.Err(); err != nil {
		return nil, nil, err
	}
	// 大实例同步慢时,看这一行分清是表清单还是列清单(information_schema.columns)在耗时。
	slog.Info("metadata probe mysql", "tables", len(tables), "columns", len(cols),
		"tables_took", tookTables.String(), "columns_took", (time.Since(started) - tookTables).String())
	if truncated {
		return tables, cols, ErrMetaTruncated
	}
	return tables, cols, nil
}

func mysqlTableKind(t string) string {
	if strings.Contains(strings.ToUpper(t), "VIEW") {
		return "view"
	}
	return "table"
}

// ---------------------------------------------------------------- PostgreSQL 家族

// maxMetaDatabases 一台 PostgreSQL 家族实例最多探查多少个库。
//
// 每个库都要单独登录一次 —— 这是 PG 的目录按库隔离带来的,不是这里的选择。上限存在
// 的理由是:一台挂着几百个库的实例会把一次同步变成几百次登录,那已经不是"抓元数据",
// 是对着自己的生产集群做一轮登录压测。超出就停,并在同步结果里如实说。
const maxMetaDatabases = 50

// MetaIncompleteError 说明收下的不完整 —— 但**不是失败**:拿到的那部分仍然有用。
//
// 和 ErrMetaTruncated 同一类。两种情形都用它:某几个库读不到(权限是按库授的),
// 或者库数超过了 maxMetaDatabases。合成一个类型是因为调用方要做的事完全一样 ——
// 收下这部分,并把原因留在同步结果里。
//
// 必须留痕:悄悄少收一个库,表现是"那个库一张表都没有",而那看起来和"那个库真的是
// 空的"一模一样。
type MetaIncompleteError struct{ Reason string }

func (e *MetaIncompleteError) Error() string { return e.Reason }

// realMetadataPG 探查 PostgreSQL 家族(含 DWS / GaussDB)的表与列。
//
// PostgreSQL 的目录是按库隔离的:要读另一个库的表,必须重新连到那个库上 —— 所以这里
// 是整个文件里唯一一处"一台实例多次往返"的地方。
//
// 连接上配了库,就只探那个库;**没配,就把这台实例上能连的库都探一遍**。
//
// 后半句原来是直接报错的("需要在连接上指定数据库"),而树在同样的情况下会列出服务器
// 上的库(见 RealSchema 的 pg 分支)。两边口径不一致的后果在生产上出现过:两台 DWS
// 连接没填库名,树里能看见它们的库,元数据同步却一条都不收,日志里只留下一句那样的
// 报错 —— 而凭据、网络、默认库其实全是通的(openConn 是 ping 过的)。
func realMetadataPG(ctx context.Context, db *sql.DB, conn *model.Connection) ([]MetaTableRow, []MetaColumnRow, error) {
	if dbName := strings.TrimSpace(conn.Database); dbName != "" {
		return pgMetadataOfDB(ctx, db, dbName, 0)
	}
	return pgMetadataAllDBs(ctx, db, conn)
}

// pgMetadataAllDBs 枚举这台实例上能连的库,逐个探查。
//
// 单个库失败不中断:权限是按库授的,一个读不到的库不该让其余的也丢掉。失败的库名
// 收进 MetaPartialError 带回去。
func pgMetadataAllDBs(ctx context.Context, db *sql.DB, conn *model.Connection) ([]MetaTableRow, []MetaColumnRow, error) {
	names, err := pgDatabaseNames(ctx, db)
	if err != nil {
		return nil, nil, apimsg.Errorf("列出实例上的库失败: %w", err)
	}
	if len(names) == 0 {
		// 一个库都列不出来是个真实的答案,但它更可能是权限问题。说出来,不要返回空清单 ——
		// 空清单读起来是"这台实例很干净"。
		return nil, nil, apimsg.Errorf("这台实例上没有可连接的库(或当前账号看不到任何库)")
	}

	tables := []MetaTableRow{}
	cols := []MetaColumnRow{}
	failed := []string{}
	truncated := false
	capped := len(names) > maxMetaDatabases
	if capped {
		names = names[:maxMetaDatabases]
	}
	for _, name := range names {
		if truncated {
			break
		}
		t, c, err := pgMetadataOfOneDB(ctx, conn, name, len(cols))
		if err != nil && err != ErrMetaTruncated {
			failed = append(failed, name)
			continue
		}
		if err == ErrMetaTruncated {
			truncated = true
		}
		tables = append(tables, t...)
		cols = append(cols, c...)
	}

	if truncated {
		return tables, cols, ErrMetaTruncated
	}
	// 两件事可能同时发生(库太多,而且其中几个还读不到),所以是拼接而不是二选一。
	reasons := []string{}
	if capped {
		reasons = append(reasons, fmt.Sprintf("这台实例的库超过 %d 个,只探查了前 %d 个", maxMetaDatabases, maxMetaDatabases))
	}
	if len(failed) > 0 {
		reasons = append(reasons, fmt.Sprintf("%d 个库没能读取:%s", len(failed), strings.Join(failed, ", ")))
	}
	if len(reasons) > 0 {
		return tables, cols, &MetaIncompleteError{Reason: strings.Join(reasons, ";")}
	}
	return tables, cols, nil
}

// pgDatabaseNames 列出可连接的、非模板库。与 pgListDatabases 同一条查询 ——
// 树和同步看见的是同一组库,这正是原来缺的那个一致性。
func pgDatabaseNames(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT datname FROM pg_database WHERE datistemplate = false AND datallowconn = true ORDER BY datname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// pgMetadataOfOneDB 连到指定的库上探查一遍。
//
// **不走 openConn 的连接池缓存**:那份缓存按 DSN 存,永不淘汰。一台三十个库的实例
// 同步一轮就会在里面留下三十个再也用不到的连接池,每个还各占着连接数上限。这里的
// 连接是一次性的,用完就关。
func pgMetadataOfOneDB(ctx context.Context, conn *model.Connection, dbName string, have int) ([]MetaTableRow, []MetaColumnRow, error) {
	target := *conn
	target.Database = dbName
	driver, dsn, ok := engineDriver(&target)
	if !ok {
		return nil, nil, apimsg.Errorf("引擎 %q 未配置真实执行", conn.Engine)
	}
	db, err := dialPool(driver, dsn)
	if err != nil {
		return nil, nil, err
	}
	defer db.Close()
	return pgMetadataOfDB(ctx, db, dbName, have)
}

// pgMetadataOfDB 读**当前这个连接所在的库**的表与列。
//
// have 是此前已经收下的列数:上限是按整台实例算的,按单个库算会让一台有很多库的实例
// 悄悄收下远超上限的行。
func pgMetadataOfDB(ctx context.Context, db *sql.DB, dbName string, have int) ([]MetaTableRow, []MetaColumnRow, error) {
	trows, err := db.QueryContext(ctx,
		`SELECT n.nspname, c.relname,
		        CASE c.relkind WHEN 'v' THEN 'view' WHEN 'm' THEN 'view' ELSE 'table' END,
		        COALESCE(obj_description(c.oid, 'pg_class'), '')
		 FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE c.relkind IN ('r','p','v','m')
		   AND n.nspname NOT IN ('pg_catalog','information_schema')
		   AND n.nspname NOT LIKE 'pg_toast%'
		 ORDER BY n.nspname, c.relname`)
	if err != nil {
		return nil, nil, err
	}
	tables := []MetaTableRow{}
	for trows.Next() {
		var schema, name, kind, comment string
		if err := trows.Scan(&schema, &name, &kind, &comment); err != nil {
			trows.Close()
			return nil, nil, err
		}
		tables = append(tables, MetaTableRow{Database: dbName, Schema: schema, Table: name, Kind: kind, Comment: comment})
	}
	if err := trows.Err(); err != nil {
		trows.Close()
		return nil, nil, err
	}
	trows.Close()

	// 主键靠 pg_index.indisprimary 判,而不是猜列名。类型用 format_type ——
	// 它给出的就是 numeric(10,2) 这种最终写法(与表结构查看那边同一个理由)。
	crows, err := db.QueryContext(ctx,
		`SELECT n.nspname, c.relname, a.attnum, a.attname,
		        format_type(a.atttypid, a.atttypmod), a.attnotnull,
		        COALESCE(pg_get_expr(d.adbin, d.adrelid), ''),
		        COALESCE(col_description(a.attrelid, a.attnum), ''),
		        COALESCE(pk.is_pk, false)
		 FROM pg_attribute a
		 JOIN pg_class c ON c.oid = a.attrelid
		 JOIN pg_namespace n ON n.oid = c.relnamespace
		 LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
		 LEFT JOIN (
		   SELECT i.indrelid AS relid, u.attnum AS attnum, true AS is_pk
		   FROM pg_index i, unnest(i.indkey) WITH ORDINALITY AS u(attnum, ord)
		   WHERE i.indisprimary
		 ) pk ON pk.relid = a.attrelid AND pk.attnum = a.attnum
		 WHERE c.relkind IN ('r','p','v','m')
		   AND a.attnum > 0 AND NOT a.attisdropped
		   AND n.nspname NOT IN ('pg_catalog','information_schema')
		   AND n.nspname NOT LIKE 'pg_toast%'
		 ORDER BY n.nspname, c.relname, a.attnum`)
	if err != nil {
		return nil, nil, err
	}
	defer crows.Close()
	cols := []MetaColumnRow{}
	truncated := false
	for crows.Next() {
		if have+len(cols) >= maxMetaRows {
			truncated = true
			break
		}
		var schema, table, name, typ, def, comment string
		var ord int
		var notNull, isPK bool
		if err := crows.Scan(&schema, &table, &ord, &name, &typ, &notNull, &def, &comment, &isPK); err != nil {
			return nil, nil, err
		}
		cols = append(cols, MetaColumnRow{
			Database: dbName, Schema: schema, Table: table, Ordinal: ord, Name: name,
			DataType: typ, Nullable: !notNull, Default: def, Comment: comment, IsPK: isPK,
		})
	}
	if err := crows.Err(); err != nil {
		return nil, nil, err
	}
	if truncated {
		return tables, cols, ErrMetaTruncated
	}
	return tables, cols, nil
}

// ---------------------------------------------------------------- Oracle

// realMetadataOracle 按 owner 归组 —— 与树、与表结构查看的口径一致:Oracle 的
// "库"这一层在网关里就是 owner(连接上的 Database 是服务名,不是命名空间)。
func realMetadataOracle(ctx context.Context, db *sql.DB, conn *model.Connection) ([]MetaTableRow, []MetaColumnRow, error) {
	// 系统 owner 不入清单,理由同 MySQL 的系统库。
	const notSystem = `owner NOT IN ('SYS','SYSTEM','OUTLN','XDB','CTXSYS','MDSYS','ORDSYS','WMSYS',
		'APPQOSSYS','DBSNMP','OJVMSYS','ORDDATA','OLAPSYS','LBACSYS','DVSYS','AUDSYS','GSMADMIN_INTERNAL')`

	trows, err := db.QueryContext(ctx,
		`SELECT t.owner, t.table_name, 'table', NVL(c.comments,' ')
		 FROM all_tables t
		 LEFT JOIN all_tab_comments c ON c.owner = t.owner AND c.table_name = t.table_name
		 WHERE t.`+notSystem+`
		 ORDER BY t.owner, t.table_name`)
	if err != nil {
		return nil, nil, err
	}
	tables := []MetaTableRow{}
	for trows.Next() {
		var owner, name, kind, comment string
		if err := trows.Scan(&owner, &name, &kind, &comment); err != nil {
			trows.Close()
			return nil, nil, err
		}
		tables = append(tables, MetaTableRow{Database: owner, Table: name, Kind: kind, Comment: strings.TrimSpace(comment)})
	}
	if err := trows.Err(); err != nil {
		trows.Close()
		return nil, nil, err
	}
	trows.Close()

	// 主键列从 all_constraints/all_cons_columns 来。DATA_DEFAULT 是 LONG,读不回来
	// 就退到不含它的那条查询 —— 默认值丢了是遗憾,整台实例的元数据丢了是故障。
	const colsWithDefault = `SELECT c.owner, c.table_name, c.column_id, c.column_name,
	        c.data_type, c.nullable, NVL(cc.comments,' '),
	        CASE WHEN pk.column_name IS NULL THEN 0 ELSE 1 END, c.data_default
	 FROM all_tab_columns c
	 LEFT JOIN all_col_comments cc
	        ON cc.owner = c.owner AND cc.table_name = c.table_name AND cc.column_name = c.column_name
	 LEFT JOIN (
	   SELECT acc.owner, acc.table_name, acc.column_name
	   FROM all_constraints ac JOIN all_cons_columns acc
	     ON acc.owner = ac.owner AND acc.constraint_name = ac.constraint_name
	   WHERE ac.constraint_type = 'P'
	 ) pk ON pk.owner = c.owner AND pk.table_name = c.table_name AND pk.column_name = c.column_name
	 WHERE c.` + notSystem + `
	 ORDER BY c.owner, c.table_name, c.column_id`

	const colsNoDefault = `SELECT c.owner, c.table_name, c.column_id, c.column_name,
	        c.data_type, c.nullable, NVL(cc.comments,' '),
	        CASE WHEN pk.column_name IS NULL THEN 0 ELSE 1 END
	 FROM all_tab_columns c
	 LEFT JOIN all_col_comments cc
	        ON cc.owner = c.owner AND cc.table_name = c.table_name AND cc.column_name = c.column_name
	 LEFT JOIN (
	   SELECT acc.owner, acc.table_name, acc.column_name
	   FROM all_constraints ac JOIN all_cons_columns acc
	     ON acc.owner = ac.owner AND acc.constraint_name = ac.constraint_name
	   WHERE ac.constraint_type = 'P'
	 ) pk ON pk.owner = c.owner AND pk.table_name = c.table_name AND pk.column_name = c.column_name
	 WHERE c.` + notSystem + `
	 ORDER BY c.owner, c.table_name, c.column_id`

	scan := func(q string, withDefault bool) ([]MetaColumnRow, bool, error) {
		rows, err := db.QueryContext(ctx, q)
		if err != nil {
			return nil, false, err
		}
		defer rows.Close()
		out := []MetaColumnRow{}
		truncated := false
		for rows.Next() {
			if len(out) >= maxMetaRows {
				truncated = true
				break
			}
			var owner, table, name, typ, nullable, comment string
			var ord, isPK int
			var def sql.NullString
			dest := []any{&owner, &table, &ord, &name, &typ, &nullable, &comment, &isPK}
			if withDefault {
				dest = append(dest, &def)
			}
			if err := rows.Scan(dest...); err != nil {
				return nil, false, err
			}
			out = append(out, MetaColumnRow{
				Database: owner, Table: table, Ordinal: ord, Name: name, DataType: typ,
				Nullable: strings.EqualFold(nullable, "Y"), Default: strings.TrimSpace(def.String),
				Comment: strings.TrimSpace(comment), IsPK: isPK == 1,
			})
		}
		return out, truncated, rows.Err()
	}

	cols, truncated, err := scan(colsWithDefault, true)
	if err != nil {
		cols, truncated, err = scan(colsNoDefault, false)
		if err != nil {
			return nil, nil, err
		}
	}
	if truncated {
		return tables, cols, ErrMetaTruncated
	}
	return tables, cols, nil
}
