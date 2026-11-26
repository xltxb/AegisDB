package gateway

// 表结构对比的读取端:把一个库的系统目录翻成 schemadiff.Snapshot(见 .scratch/schema-diff/PRD.md)。
//
// 只读目录(information_schema / ALL_* / pg_catalog),不碰业务数据。每类对象一条
// 查询取全库,不逐表查 —— 几千张表的库逐表查要几千次往返。
//
// 默认值、类型都取目录里的原文,不在这里归一化:归一化在 schemadiff 里做,报告要能显示两边的原文。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"aegisdb/internal/apimsg"
	"aegisdb/internal/model"
	"aegisdb/internal/schemadiff"
)

// 读目录的时限与表数上限。导出给 service:读失败的提示里要说出这两个数,不能各写一份。
const (
	SnapshotTimeoutSec = 60
	MaxSnapshotTables  = 5000
	snapshotTimeout    = SnapshotTimeoutSec * time.Second
	maxSnapshotTables  = MaxSnapshotTables
)

// ErrSnapshotTooLarge:库里的表超过上限,一次对比读不完。
var ErrSnapshotTooLarge = apimsg.Errorf("库里超过 %d 张表,一次对比读不完,请缩小范围", maxSnapshotTables)

// ErrSnapshotTimeout:读目录超过时限。
var ErrSnapshotTimeout = apimsg.Errorf("读取表结构超过 %d 秒", int(snapshotTimeout.Seconds()))

// RealSchemaSnapshot 读一个库的结构快照。database:MySQL 库名 / Oracle owner / PG 库名。
// schema 只对 PG / DWS 有意义:空 = 库内全部非系统 schema(非 public 的表名带 schema 前缀);
// 指定了就只读这一个 schema,表名不带前缀 —— 这样 app 与 app_uat 两个 schema 也能互比。
//
// 时限:读目录 60 秒;建连不在这 60 秒里,新建连接池时另有 8 秒的 ping 上限(openAndPing)。
func realSchemaSnapshotOnce(conn *model.Connection, database, schema string) (*schemadiff.Snapshot, error) {
	target := *conn
	fam := engineFamily(conn.Engine)
	switch fam {
	case familyMySQL:
		target.Database = "" // information_schema 是全局的,不绑默认库
	case familyPostgres:
		target.Database = database // PG 的目录按库隔离:连到目标库
	}
	db, release, err := openConn(&target)
	if err != nil {
		return nil, err
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), snapshotTimeout)
	defer cancel()

	b := &snapBuilder{tables: map[string]*schemadiff.Table{}}
	switch fam {
	case familyMySQL:
		err = mysqlSnapshot(ctx, db, database, b)
	case familyOracle:
		err = oracleSnapshot(ctx, db, strings.ToUpper(strings.TrimSpace(database)), b)
	case familyPostgres:
		err = pgSnapshot(ctx, db, strings.TrimSpace(schema), b)
	default:
		return nil, apimsg.Errorf("引擎 %q 暂不支持表结构对比", conn.Engine)
	}
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			return nil, fmt.Errorf("%w: %v", ErrSnapshotTimeout, err)
		}
		return nil, err
	}
	return b.snapshot(conn.Engine, database), nil
}

// maxDiffDatabases 是结构对比库名下拉最多列出的个数:再多的实例也只是下拉里多翻几页,
// 列不全的由页面上直接输入库名兜住。
const maxDiffDatabases = 2000

// DiffDatabaseNames 列出一个实例上可以拿来做结构对比的"库",给页面的库名下拉:MySQL 系是库,
// Oracle 是用户(schema),PG / DWS 是可连接的非模板库。系统库不列。
func diffDatabaseNamesOnce(conn *model.Connection) ([]string, error) {
	fam := engineFamily(conn.Engine)
	target := *conn
	var q, fallback string
	switch fam {
	case familyMySQL:
		target.Database = "" // information_schema 是全局的,配的默认库不存在也不该让这一步失败
		q = "SELECT schema_name FROM information_schema.schemata WHERE LOWER(schema_name) NOT IN " + mysqlSystemSchemas + " ORDER BY schema_name"
	case familyOracle:
		// oracle_maintained 是 12c 起才有的列;老版本回退到全部用户、去掉常见的系统用户。
		q = "SELECT username FROM all_users WHERE oracle_maintained = 'N' ORDER BY username"
		fallback = "SELECT username FROM all_users ORDER BY username"
	case familyPostgres:
		q = "SELECT datname FROM pg_database WHERE datistemplate = false AND datallowconn = true ORDER BY datname"
	default:
		return nil, apimsg.Errorf("引擎 %q 暂不支持表结构对比", conn.Engine)
	}
	db, release, err := openConn(&target)
	if err != nil {
		return nil, err
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out := []string{}
	collect := func(query string) error {
		out = out[:0]
		return queryEach(ctx, db, query, nil, func(rows *sql.Rows) error {
			var n string
			if err := rows.Scan(&n); err != nil {
				return err
			}
			if len(out) < maxDiffDatabases {
				out = append(out, n)
			}
			return nil
		})
	}
	if err = collect(q); err != nil && fallback != "" {
		if err = collect(fallback); err == nil {
			out = dropOracleSystemUsers(out)
		}
	}
	// Oracle 默认比的是登录账号自己的 schema(见 service.defaultDiffDB):它就算是系统维护的
	// 用户,也得在下拉里。
	if err == nil && fam == familyOracle {
		if me := strings.ToUpper(strings.TrimSpace(conn.Username)); me != "" && !containsStr(out, me) {
			out = append([]string{me}, out...)
		}
	}
	return out, err
}

func containsStr(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// DiffSchemaNames 列出 PG / DWS 一个库里的用户 schema,给页面的 schema 下拉。口径与读快照相同
// (pgUserNS):下拉里有的,对比时就读得到。只对 PG 系有意义。
func diffSchemaNamesOnce(conn *model.Connection, database string) ([]string, error) {
	if engineFamily(conn.Engine) != familyPostgres {
		return nil, apimsg.Errorf("引擎 %q 没有库下的 schema 这一层", conn.Engine)
	}
	target := *conn
	if d := strings.TrimSpace(database); d != "" {
		target.Database = d // PG 的目录按库隔离:连到这个库
	}
	db, release, err := openConn(&target)
	if err != nil {
		return nil, err
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out := []string{}
	err = queryEach(ctx, db, `SELECT n.nspname FROM pg_namespace n WHERE `+pgUserNS+` ORDER BY n.nspname`, nil, func(rows *sql.Rows) error {
		var n string
		if err := rows.Scan(&n); err != nil {
			return err
		}
		if len(out) < maxDiffDatabases {
			out = append(out, n)
		}
		return nil
	})
	return out, err
}

func dropOracleSystemUsers(names []string) []string {
	sys := map[string]bool{}
	for _, n := range bizSystemSchemas[familyOracle] {
		sys[strings.ToUpper(n)] = true
	}
	out := names[:0]
	for _, n := range names {
		if !sys[n] {
			out = append(out, n)
		}
	}
	return out
}

// snapBuilder 按表名收拢几条查询的结果。
type snapBuilder struct {
	tables map[string]*schemadiff.Table
}

func (b *snapBuilder) addTable(name, kind, comment string) error {
	if _, ok := b.tables[name]; ok {
		return nil
	}
	if len(b.tables) >= maxSnapshotTables {
		return ErrSnapshotTooLarge
	}
	b.tables[name] = &schemadiff.Table{Name: name, Kind: kind, Comment: comment,
		Columns: []schemadiff.Column{}, PrimaryKey: []string{}, Indexes: []schemadiff.Index{}}
	return nil
}

func (b *snapBuilder) addColumn(table string, c schemadiff.Column) {
	if t, ok := b.tables[table]; ok {
		t.Columns = append(t.Columns, c)
	}
}

// addIndexColumn 把索引的一列按顺序接上;同名索引的后续列追加到同一个索引上。
// primary:这是支撑主键的那个索引(见 schemadiff.Index.Primary)。
func (b *snapBuilder) addIndexColumn(table, index, column string, unique, system, primary bool) {
	t, ok := b.tables[table]
	if !ok {
		return
	}
	for i := range t.Indexes {
		if t.Indexes[i].Name == index {
			t.Indexes[i].Columns = append(t.Indexes[i].Columns, column)
			return
		}
	}
	t.Indexes = append(t.Indexes, schemadiff.Index{Name: index, Columns: []string{column}, Unique: unique, SystemNamed: system, Primary: primary})
}

func (b *snapBuilder) addPK(table, column string) {
	if t, ok := b.tables[table]; ok {
		t.PrimaryKey = append(t.PrimaryKey, column)
	}
}

func (b *snapBuilder) snapshot(engine, database string) *schemadiff.Snapshot {
	s := &schemadiff.Snapshot{Engine: engine, Database: database, ReadAt: time.Now(), Tables: make([]schemadiff.Table, 0, len(b.tables))}
	for _, t := range b.tables {
		s.Tables = append(s.Tables, *t)
	}
	sort.Slice(s.Tables, func(i, j int) bool { return s.Tables[i].Name < s.Tables[j].Name })
	return s
}

// querier 是 *sql.DB 或 *sql.Tx(PG 按 schema 读时在事务里读,见 pgSnapshot)。
type querier interface {
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
}

func queryEach(ctx context.Context, db querier, q string, args []any, scan func(*sql.Rows) error) error {
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := scan(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

// ---------------------------------------------------------------- MySQL 系

func mysqlSnapshot(ctx context.Context, db *sql.DB, schema string, b *snapBuilder) error {
	args := []any{schema}
	if err := queryEach(ctx, db, `SELECT table_name, table_type, COALESCE(table_comment, '') FROM information_schema.tables
		WHERE table_schema = ? ORDER BY table_name`, args, func(r *sql.Rows) error {
		var name, typ, comment string
		if err := r.Scan(&name, &typ, &comment); err != nil {
			return err
		}
		kind := "table"
		if strings.EqualFold(typ, "VIEW") {
			kind, comment = "view", "" // 视图的 table_comment 恒为 "VIEW"
		}
		return b.addTable(name, kind, comment)
	}); err != nil {
		return err
	}
	if err := queryEach(ctx, db, `SELECT table_name, column_name, ordinal_position, column_type, is_nullable,
		column_default, COALESCE(column_comment, ''), COALESCE(extra, '')
		FROM information_schema.columns WHERE table_schema = ? ORDER BY table_name, ordinal_position`, args, func(r *sql.Rows) error {
		var table, name, typ, nullable, comment, extra string
		var ord int
		var def sql.NullString
		if err := r.Scan(&table, &name, &ord, &typ, &nullable, &def, &comment, &extra); err != nil {
			return err
		}
		b.addColumn(table, schemadiff.Column{Name: name, DataType: typ + mysqlExtra(extra), Nullable: nullable == "YES",
			HasDefault: def.Valid, Default: def.String, Comment: comment, Ordinal: ord})
		return nil
	}); err != nil {
		return err
	}
	scanIndex := func(r *sql.Rows) error {
		var table, index string
		var nonUnique int
		var column, expr sql.NullString
		if err := r.Scan(&table, &index, &nonUnique, &column, &expr); err != nil {
			return err
		}
		c := column.String
		if !column.Valid {
			c = expr.String // 函数索引:列名为 NULL,表达式在 expression 里
		}
		if index == "PRIMARY" {
			b.addPK(table, c)
		}
		b.addIndexColumn(table, index, c, nonUnique == 0, false, index == "PRIMARY")
		return nil
	}
	err := queryEach(ctx, db, `SELECT table_name, index_name, non_unique, column_name, expression FROM information_schema.statistics
		WHERE table_schema = ? ORDER BY table_name, index_name, seq_in_index`, args, scanIndex)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "unknown column") {
		// 没有 expression 列的版本也没有函数索引,取列名就够了
		return queryEach(ctx, db, `SELECT table_name, index_name, non_unique, column_name, NULL FROM information_schema.statistics
			WHERE table_schema = ? ORDER BY table_name, index_name, seq_in_index`, args, scanIndex)
	}
	return err
}

// mysqlExtra 把 extra 里改变列行为的部分并进类型比较:auto_increment、on update CURRENT_TIMESTAMP、
// 生成列。DEFAULT_GENERATED 只是"默认值是表达式"的标记,默认值本身已经在比了。
func mysqlExtra(extra string) string {
	var parts []string
	for _, p := range strings.Fields(strings.ToLower(extra)) {
		if p != "default_generated" {
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return " " + strings.Join(parts, " ")
}

// ---------------------------------------------------------------- Oracle

func oracleSnapshot(ctx context.Context, db *sql.DB, owner string, b *snapBuilder) error {
	if err := queryEach(ctx, db, `SELECT t.table_name, 'table', NVL(c.comments, ' ') FROM all_tables t
		LEFT JOIN all_tab_comments c ON c.owner = t.owner AND c.table_name = t.table_name
		WHERE t.owner = :1 AND t.dropped = 'NO' AND t.nested = 'NO' AND t.secondary = 'N'
		UNION ALL
		SELECT v.view_name, 'view', NVL(c.comments, ' ') FROM all_views v
		LEFT JOIN all_tab_comments c ON c.owner = v.owner AND c.table_name = v.view_name
		WHERE v.owner = :2`, []any{owner, owner}, func(r *sql.Rows) error {
		var name, kind, comment string
		if err := r.Scan(&name, &kind, &comment); err != nil {
			return err
		}
		return b.addTable(name, kind, strings.TrimSpace(comment))
	}); err != nil {
		return err
	}
	if err := queryEach(ctx, db, `SELECT c.table_name, c.column_name, c.column_id, c.data_type,
		c.data_length, c.data_precision, c.data_scale, c.char_length, NVL(c.char_used, ' '), c.nullable,
		c.data_default, NVL(cc.comments, ' ')
		FROM all_tab_columns c
		LEFT JOIN all_col_comments cc ON cc.owner = c.owner AND cc.table_name = c.table_name AND cc.column_name = c.column_name
		WHERE c.owner = :1 ORDER BY c.table_name, c.column_id`, []any{owner}, func(r *sql.Rows) error {
		var table, name, typ, charUsed, nullable, comment string
		var ord int
		var length, charLen sql.NullInt64
		var prec, scale sql.NullInt64
		var def sql.NullString
		if err := r.Scan(&table, &name, &ord, &typ, &length, &prec, &scale, &charLen, &charUsed, &nullable, &def, &comment); err != nil {
			return err
		}
		d := strings.TrimSpace(def.String)
		b.addColumn(table, schemadiff.Column{Name: name, DataType: oracleType(typ, length, prec, scale, charLen, charUsed),
			Nullable: nullable == "Y", HasDefault: def.Valid && d != "", Default: d, Comment: strings.TrimSpace(comment), Ordinal: ord})
		return nil
	}); err != nil {
		return err
	}
	if err := queryEach(ctx, db, `SELECT c.table_name, cc.column_name FROM all_constraints c
		JOIN all_cons_columns cc ON cc.owner = c.owner AND cc.constraint_name = c.constraint_name
		WHERE c.owner = :1 AND c.constraint_type = 'P' ORDER BY c.table_name, cc.position`, []any{owner}, func(r *sql.Rows) error {
		var table, column string
		if err := r.Scan(&table, &column); err != nil {
			return err
		}
		b.addPK(table, column)
		return nil
	}); err != nil {
		return err
	}
	pkIndex := map[string]bool{}
	if err := queryEach(ctx, db, `SELECT index_name FROM all_constraints WHERE owner = :1 AND constraint_type = 'P' AND index_name IS NOT NULL`,
		[]any{owner}, func(r *sql.Rows) error {
			var n string
			if err := r.Scan(&n); err != nil {
				return err
			}
			pkIndex[n] = true
			return nil
		}); err != nil {
		return err
	}
	// 索引属主 + 索引名 + 位置 → 表达式。按**表**的属主取:索引可以建在别的用户名下,按索引属主
	// 过滤会漏掉它们,列名就留成 SYS_NC…$ —— 两个环境里不同,报出假差异。
	exprs := map[string]string{}
	if err := queryEach(ctx, db, `SELECT index_owner, index_name, column_position, column_expression FROM all_ind_expressions WHERE table_owner = :1`,
		[]any{owner}, func(r *sql.Rows) error {
			var o, n string
			var pos int
			var e sql.NullString
			if err := r.Scan(&o, &n, &pos, &e); err != nil {
				return err
			}
			exprs[o+"\x00"+n+"\x00"+strconv.Itoa(pos)] = strings.TrimSpace(e.String)
			return nil
		}); err != nil {
		return err
	}
	return queryEach(ctx, db, `SELECT i.owner, i.table_name, i.index_name, i.uniqueness, ic.column_name, i.generated, ic.column_position
		FROM all_indexes i
		JOIN all_ind_columns ic ON ic.index_owner = i.owner AND ic.index_name = i.index_name
		WHERE i.table_owner = :1 AND i.index_type <> 'LOB'
		ORDER BY i.table_name, i.index_name, ic.column_position`, []any{owner}, func(r *sql.Rows) error {
		var idxOwner, table, index, uniq, column, generated string
		var pos int
		if err := r.Scan(&idxOwner, &table, &index, &uniq, &column, &generated, &pos); err != nil {
			return err
		}
		if e, ok := exprs[idxOwner+"\x00"+index+"\x00"+strconv.Itoa(pos)]; ok && e != "" {
			column = e
		}
		b.addIndexColumn(table, index, column, uniq == "UNIQUE", generated == "Y" || strings.HasPrefix(index, "SYS_"), pkIndex[index])
		return nil
	})
}

// oracleType 按 Oracle 的写法把类型拼回来:VARCHAR2(20 CHAR)、NUMBER(10,2)、NUMBER(*,0)。
func oracleType(typ string, length, prec, scale, charLen sql.NullInt64, charUsed string) string {
	switch typ {
	case "VARCHAR2", "NVARCHAR2", "CHAR", "NCHAR":
		n := charLen.Int64
		if n == 0 {
			n = length.Int64
		}
		if charUsed == "C" && (typ == "VARCHAR2" || typ == "CHAR") {
			return fmt.Sprintf("%s(%d CHAR)", typ, n)
		}
		return fmt.Sprintf("%s(%d)", typ, n)
	case "RAW":
		return fmt.Sprintf("RAW(%d)", length.Int64)
	case "NUMBER":
		switch {
		case !prec.Valid && !scale.Valid:
			return "NUMBER"
		case !prec.Valid:
			return fmt.Sprintf("NUMBER(*,%d)", scale.Int64)
		case !scale.Valid || scale.Int64 == 0:
			return fmt.Sprintf("NUMBER(%d)", prec.Int64)
		default:
			return fmt.Sprintf("NUMBER(%d,%d)", prec.Int64, scale.Int64)
		}
	case "FLOAT":
		if prec.Valid {
			return fmt.Sprintf("FLOAT(%d)", prec.Int64)
		}
	}
	return typ
}

// ---------------------------------------------------------------- PostgreSQL / DWS

// 走 pg_catalog 而不是 information_schema:后者的类型是 "character varying" 这类不带长度的说法,
// 要自己拼;pg_catalog 的 format_type 直接给出建表时的写法。也不用 relispartition 这类新字段 ——
// DWS 基于老版本,没有它们。
const pgUserNS = `n.nspname NOT IN ('pg_catalog', 'information_schema', 'pg_toast') AND n.nspname NOT LIKE 'pg\_temp%'
	AND n.nspname NOT LIKE 'pg\_toast%' AND n.nspname NOT IN ('cstore', 'dbms_om', 'dbms_job', 'dbms_lob', 'dbms_output', 'dbms_random', 'utl_raw', 'pkg_service', 'snapshot', 'sys')`

// pgNamer 决定表名写不写 schema:只读一个 schema 时一律不写;读全库时 public 不写、其余写。
func pgNamer(only string) func(schema, table string) string {
	return func(schema, table string) string {
		if only != "" || schema == "public" {
			return table
		}
		return schema + "." + table
	}
}

func pgSnapshot(ctx context.Context, pool *sql.DB, only string, b *snapBuilder) error {
	ns, args := pgUserNS, []any(nil)
	var db querier = pool
	if only != "" {
		ns, args = "n.nspname = $1", []any{only}
		// format_type / pg_get_expr / pg_get_indexdef 会给不在 search_path 上的对象带 schema 前缀
		// (nextval('app.x_seq'::regclass)、app.status 枚举)—— 比 app 与 app_uat 时,每个自增列、
		// 每个枚举列都会因为这个前缀报出假差异。在只读事务里把 search_path 设成这个 schema,
		// 它的对象就不带前缀了;set_config 的第三个参数 true 让设置随事务结束,不会带回连接池。
		tx, err := pool.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err := tx.ExecContext(ctx, `SELECT set_config('search_path', $1, true)`,
			`"`+strings.ReplaceAll(only, `"`, `""`)+`", pg_catalog`); err != nil {
			return err
		}
		db = tx
	}
	pgName := pgNamer(only)
	if err := queryEach(ctx, db, `SELECT n.nspname, c.relname, c.relkind, COALESCE(obj_description(c.oid, 'pg_class'), '')
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relkind IN ('r', 'p', 'v', 'm') AND `+ns+` ORDER BY 1, 2`, args, func(r *sql.Rows) error {
		var schema, name, kind, comment string
		if err := r.Scan(&schema, &name, &kind, &comment); err != nil {
			return err
		}
		k := "table"
		if kind == "v" || kind == "m" {
			k = "view"
		}
		return b.addTable(pgName(schema, name), k, comment)
	}); err != nil {
		return err
	}
	if err := queryEach(ctx, db, `SELECT n.nspname, c.relname, a.attname, a.attnum, format_type(a.atttypid, a.atttypmod),
		a.attnotnull, pg_get_expr(d.adbin, d.adrelid), COALESCE(col_description(c.oid, a.attnum), '')
		FROM pg_attribute a
		JOIN pg_class c ON c.oid = a.attrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
		WHERE a.attnum > 0 AND NOT a.attisdropped AND c.relkind IN ('r', 'p', 'v', 'm') AND `+ns+`
		ORDER BY 1, 2, a.attnum`, args, func(r *sql.Rows) error {
		var schema, table, name, typ, comment string
		var num int
		var notNull bool
		var def sql.NullString
		if err := r.Scan(&schema, &table, &name, &num, &typ, &notNull, &def, &comment); err != nil {
			return err
		}
		t := pgName(schema, table)
		b.addColumn(t, schemadiff.Column{Name: name, DataType: typ, Nullable: !notNull,
			HasDefault: def.Valid, Default: def.String, Comment: comment, Ordinal: num})
		return nil
	}); err != nil {
		return err
	}
	// 索引逐列取 pg_get_indexdef(索引, 第 k 列, true):普通列是列名,表达式列是表达式原文 ——
	// 两边表达式不同也比得出来。用一个常量序列与 indnatts 相比,不用 LATERAL / WITH ORDINALITY,
	// DWS(基于老版本 PG)没有它们。
	return queryEach(ctx, db, `SELECT n.nspname, t.relname, i.relname, ix.indisunique, ix.indisprimary, pg_get_indexdef(ix.indexrelid, k.k, true)
		FROM pg_index ix
		JOIN pg_class i ON i.oid = ix.indexrelid
		JOIN pg_class t ON t.oid = ix.indrelid
		JOIN pg_namespace n ON n.oid = t.relnamespace
		CROSS JOIN generate_series(1, 32) AS k(k)
		WHERE k.k <= ix.indnatts AND `+ns+` ORDER BY 1, 2, 3, k.k`, args, func(r *sql.Rows) error {
		var schema, table, index, col string
		var unique, primary bool
		if err := r.Scan(&schema, &table, &index, &unique, &primary, &col); err != nil {
			return err
		}
		t := pgName(schema, table)
		if primary {
			b.addPK(t, col)
		}
		b.addIndexColumn(t, index, col, unique, false, primary)
		return nil
	})
}
