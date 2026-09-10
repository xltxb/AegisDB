package gateway

// PostgreSQL / DWS / GaussDB 的表结构:完整的一份。
//
// PostgreSQL 没有 SHOW CREATE TABLE,也没有服务端函数能给出建表语句(pg_dump 是在
// 客户端拼的)。原先这里用 information_schema.columns 拼列、再补 pg_indexes 的索引,
// 于是约束、注释、表空间、存储参数一概没有 —— 而对 **DWS** 来说还少了两件更要命的:
//
//   - **分布键**(DISTRIBUTE BY HASH / REPLICATION / ROUNDROBIN)。它决定数据怎么散在
//     各 DN 上,是 DWS 上性能问题的第一来源;看不到它的"表结构"在 DWS 语境下不叫完整。
//   - **行存还是列存**(reloptions 里的 orientation),以及压缩级别。
//
// 所以改成读 pg_catalog:列的类型用 format_type(它给出的就是 numeric(10,2) 这种最终
// 写法,information_schema 要自己拼且拼不全),默认值用 pg_get_expr,约束直接用
// pg_get_constraintdef(PostgreSQL 自己渲染,不可能拼错),注释用 obj_description /
// col_description。这些在 PG 9.2 以上都有,DWS/GaussDB 也在这条线上。
//
// 两个刻意的取舍,与 Oracle 那边一致(见 oracle_tableddl.go 的开头):
//
//   - **每一段独立可失败**,失败降级成一行说明留在输出里。缺口要看得见 —— 这是只读
//     展示,不是闸。DWS 专有的 pgxc_class 在原生 PostgreSQL 上根本不存在,那条查询
//     必然失败,而那**不是**故障,所以它连说明都不写。
//   - **取数与渲染分开**,renderPGTable 是纯函数。仓库里没有 DWS 也没有 PostgreSQL
//     可跑集成测试,这是唯一能把输出钉住的办法。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

type pgColumn struct {
	AttNum  int
	Name    string
	Type    string // format_type 的输出,已含长度/精度
	NotNull bool
	Default string
	Comment string
}

type pgConstraint struct {
	Name string
	Type string // p / u / f / c
	Def  string // pg_get_constraintdef,PostgreSQL 自己渲染的
}

type pgIndex struct {
	Name string
	Def  string // pg_indexes.indexdef,同样是服务端渲染的
}

// pgTable 是渲染需要的全部事实。
type pgTable struct {
	Schema      string
	Name        string
	Tablespace  string
	RelOptions  string // orientation=column, compression=low …
	Unlogged    bool
	Temporary   bool
	Comment     string
	Distribute  string // 已渲染:HASH("id") / REPLICATION / ROUNDROBIN
	PartKeyDef  string // RANGE (created_at) 之类
	PartCount   int64
	Columns     []pgColumn
	Constraints []pgConstraint
	Indexes     []pgIndex
	Notes       []string
}

func (t *pgTable) addNote(s string) {
	if s != "" {
		t.Notes = append(t.Notes, s)
	}
}

// renderPGTable 把事实渲染成一份可读、且基本可重放的 DDL。
func renderPGTable(t *pgTable) string {
	var b strings.Builder
	for _, n := range t.Notes {
		b.WriteString("-- " + n + "\n")
	}

	facts := []string{}
	if t.Tablespace != "" {
		facts = append(facts, "表空间:"+t.Tablespace)
	}
	if t.RelOptions != "" {
		facts = append(facts, "存储:"+t.RelOptions)
	}
	if t.Distribute != "" {
		facts = append(facts, "分布:"+t.Distribute)
	}
	if t.Temporary {
		facts = append(facts, "临时表:是")
	}
	if t.Unlogged {
		facts = append(facts, "UNLOGGED:是")
	}
	if t.PartKeyDef != "" {
		p := "分区:" + t.PartKeyDef
		if t.PartCount > 0 {
			p += fmt.Sprintf(" · %d 个分区", t.PartCount)
		}
		facts = append(facts, p)
	}
	if len(facts) > 0 {
		b.WriteString("-- " + strings.Join(facts, "   ") + "\n")
	}
	if b.Len() > 0 {
		b.WriteString("\n")
	}

	full := qualifiedName(t.Schema, t.Name)
	kind := "CREATE TABLE"
	switch {
	case t.Temporary:
		kind = "CREATE TEMPORARY TABLE"
	case t.Unlogged:
		kind = "CREATE UNLOGGED TABLE"
	}
	b.WriteString(kind + " " + full + " (\n")
	lines := make([]string, 0, len(t.Columns))
	for _, c := range t.Columns {
		l := "  " + sqlIdent(c.Name) + " " + c.Type
		if d := strings.TrimSpace(c.Default); d != "" {
			l += " DEFAULT " + d
		}
		if c.NotNull {
			l += " NOT NULL"
		}
		lines = append(lines, l)
	}
	b.WriteString(strings.Join(lines, ",\n"))
	b.WriteString("\n)")
	// WITH / TABLESPACE / DISTRIBUTE BY 的顺序按 DWS 的语法来,好让这段能直接重放。
	if t.RelOptions != "" {
		b.WriteString("\nWITH (" + t.RelOptions + ")")
	}
	if t.Tablespace != "" {
		b.WriteString("\nTABLESPACE " + sqlIdent(t.Tablespace))
	}
	if t.Distribute != "" {
		b.WriteString("\nDISTRIBUTE BY " + t.Distribute)
	}
	b.WriteString(";\n")

	if s := renderPGConstraints(t); s != "" {
		b.WriteString("\n-- 约束\n" + s)
	}
	if s := renderPGIndexes(t); s != "" {
		b.WriteString("\n-- 索引\n" + s)
	}
	if s := renderPGComments(t); s != "" {
		b.WriteString("\n-- 注释\n" + s)
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderPGConstraints(t *pgTable) string {
	full := qualifiedName(t.Schema, t.Name)
	var b strings.Builder
	for _, c := range t.Constraints {
		if strings.TrimSpace(c.Def) == "" {
			continue
		}
		b.WriteString("ALTER TABLE " + full + " ADD CONSTRAINT " + sqlIdent(c.Name) + " " + c.Def + ";\n")
	}
	return b.String()
}

func renderPGIndexes(t *pgTable) string {
	var b strings.Builder
	for _, idx := range t.Indexes {
		if strings.TrimSpace(idx.Def) == "" {
			continue
		}
		b.WriteString(strings.TrimRight(idx.Def, "; \t") + ";\n")
	}
	return b.String()
}

func renderPGComments(t *pgTable) string {
	full := qualifiedName(t.Schema, t.Name)
	var b strings.Builder
	if c := strings.TrimSpace(t.Comment); c != "" {
		b.WriteString("COMMENT ON TABLE " + full + " IS " + sqlLit(c) + ";\n")
	}
	for _, c := range t.Columns {
		if strings.TrimSpace(c.Comment) == "" {
			continue
		}
		b.WriteString("COMMENT ON COLUMN " + full + "." + sqlIdent(c.Name) + " IS " + sqlLit(c.Comment) + ";\n")
	}
	return b.String()
}

// pgDistributeClause 把 pgxc_class 的定位类型和分布列拼成 DISTRIBUTE BY 子句。
//
// 字母的含义来自 GaussDB/DWS 的 locator type:H 哈希、R 复制、N 轮询、M 取模、
// L 列表、G 范围。认不出来的原样带出去 —— 猜一个错的分布方式比承认不认识更糟。
func pgDistributeClause(locator string, cols []string) string {
	switch strings.ToUpper(strings.TrimSpace(locator)) {
	case "H":
		if len(cols) == 0 {
			return "" // 哈希却没有分布列,写出一个空括号没有意义
		}
		return "HASH(" + strings.Join(quoteAll(cols), ", ") + ")"
	case "R":
		return "REPLICATION"
	case "N":
		return "ROUNDROBIN"
	case "M":
		if len(cols) == 0 {
			return ""
		}
		return "MODULO(" + strings.Join(quoteAll(cols), ", ") + ")"
	case "L", "G":
		if len(cols) == 0 {
			return ""
		}
		name := map[string]string{"L": "LIST", "G": "RANGE"}[strings.ToUpper(strings.TrimSpace(locator))]
		return name + "(" + strings.Join(quoteAll(cols), ", ") + ")"
	case "":
		return ""
	}
	return locator
}

// ------------------------------------------------------------ 取数

// errPGRelNotFound 与"查询失败"区分开:表不存在时不该再去跑一遍退化路径。
var errPGRelNotFound = fmt.Errorf("pg: relation not found")

// pgTableStructure 从 pg_catalog 读回一张表的全部结构。
//
// 只有拿不到 relation 或列为空才算失败,其余每一段读不到都降级成一条 Note。
func pgTableStructure(ctx context.Context, db *sql.DB, schema, name string) (*pgTable, error) {
	t := &pgTable{Schema: schema, Name: name}

	var oid int64
	var relPersist string
	var spc, opts, comment sql.NullString
	err := db.QueryRowContext(ctx,
		`SELECT c.oid::bigint, c.relpersistence, ts.spcname,
		        array_to_string(c.reloptions, ', '),
		        obj_description(c.oid, 'pg_class')
		 FROM pg_class c
		 JOIN pg_namespace n ON n.oid = c.relnamespace
		 LEFT JOIN pg_tablespace ts ON ts.oid = c.reltablespace
		 WHERE n.nspname = $1 AND c.relname = $2`, schema, name).
		Scan(&oid, &relPersist, &spc, &opts, &comment)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errPGRelNotFound
	}
	if err != nil {
		return nil, err
	}
	t.Tablespace = strings.TrimSpace(spc.String)
	t.RelOptions = strings.TrimSpace(opts.String)
	t.Comment = strings.TrimSpace(comment.String)
	t.Temporary = relPersist == "t"
	t.Unlogged = relPersist == "u"

	cols, err := pgColumns(ctx, db, oid)
	if err != nil {
		return nil, err
	}
	if len(cols) == 0 {
		return nil, errPGRelNotFound
	}
	t.Columns = cols

	t.addNote(pgTableConstraints(ctx, db, t, oid))
	t.addNote(pgTableIndexes(ctx, db, t))
	pgTableDistribution(ctx, db, t, oid) // DWS 专有,原生 PG 上必然查不到,不记 Note
	pgTablePartition(ctx, db, t, oid)
	return t, nil
}

func pgColumns(ctx context.Context, db *sql.DB, oid int64) ([]pgColumn, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT a.attnum, a.attname, format_type(a.atttypid, a.atttypmod), a.attnotnull,
		        pg_get_expr(d.adbin, d.adrelid),
		        col_description(a.attrelid, a.attnum)
		 FROM pg_attribute a
		 LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
		 WHERE a.attrelid = $1::oid AND a.attnum > 0 AND NOT a.attisdropped
		 ORDER BY a.attnum`, oid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []pgColumn{}
	for rows.Next() {
		var c pgColumn
		var def, cm sql.NullString
		if err := rows.Scan(&c.AttNum, &c.Name, &c.Type, &c.NotNull, &def, &cm); err != nil {
			return nil, err
		}
		c.Default = strings.TrimSpace(def.String)
		c.Comment = strings.TrimSpace(cm.String)
		out = append(out, c)
	}
	return out, rows.Err()
}

// pgTableConstraints 读主键/唯一/外键/检查约束。定义直接问 PostgreSQL 要 ——
// pg_get_constraintdef 渲染的就是它自己认的那份,没有拼错的余地。
func pgTableConstraints(ctx context.Context, db *sql.DB, t *pgTable, oid int64) string {
	rows, err := db.QueryContext(ctx,
		`SELECT conname, contype, pg_get_constraintdef(oid)
		 FROM pg_constraint WHERE conrelid = $1::oid
		 ORDER BY CASE contype WHEN 'p' THEN 1 WHEN 'u' THEN 2 WHEN 'f' THEN 3 ELSE 4 END, conname`, oid)
	if err != nil {
		return "约束未列出:读取 pg_constraint 失败(" + errBrief(err) + ")"
	}
	defer rows.Close()
	for rows.Next() {
		var c pgConstraint
		var def sql.NullString
		if err := rows.Scan(&c.Name, &c.Type, &def); err != nil {
			return "约束未列出:读取 pg_constraint 失败(" + errBrief(err) + ")"
		}
		c.Def = strings.TrimSpace(def.String)
		t.Constraints = append(t.Constraints, c)
	}
	return ""
}

// pgTableIndexes 读索引,并**跳过给约束做后盾的那些**。
//
// 主键和唯一约束在 PostgreSQL 里各自带一个同名索引。约束段已经把它们说清楚了,索引段
// 再列一遍会让人以为这张表上的索引比实际多一倍。pg_dump 也是这么分的。
func pgTableIndexes(ctx context.Context, db *sql.DB, t *pgTable) string {
	byConstraint := map[string]bool{}
	for _, c := range t.Constraints {
		if c.Type == "p" || c.Type == "u" {
			byConstraint[c.Name] = true
		}
	}
	rows, err := db.QueryContext(ctx,
		`SELECT indexname, indexdef FROM pg_indexes
		 WHERE schemaname = $1 AND tablename = $2 ORDER BY indexname`, t.Schema, t.Name)
	if err != nil {
		return "索引未列出:读取 pg_indexes 失败(" + errBrief(err) + ")"
	}
	defer rows.Close()
	for rows.Next() {
		var idx pgIndex
		if err := rows.Scan(&idx.Name, &idx.Def); err != nil {
			return "索引未列出:读取 pg_indexes 失败(" + errBrief(err) + ")"
		}
		if byConstraint[idx.Name] {
			continue
		}
		t.Indexes = append(t.Indexes, idx)
	}
	return ""
}

// pgTableDistribution 读 DWS/GaussDB 的分布方式。
//
// pgxc_class 只存在于 DWS/GaussDB;在原生 PostgreSQL 上这条查询必然报"关系不存在",
// 那不是故障,所以静默跳过 —— 给原生 PG 的用户看一行"分布信息读取失败"是纯粹的噪音。
func pgTableDistribution(ctx context.Context, db *sql.DB, t *pgTable, oid int64) {
	var locator string
	var attnums sql.NullString
	// pcattnum 是 int2vector,文本形态是空格分隔的属性号("1 3")。转成文本再在 Go 里
	// 拆,比在 SQL 里跟 int2vector 较劲稳妥。
	if err := db.QueryRowContext(ctx,
		`SELECT pclocatortype, pcattnum::text FROM pgxc_class WHERE pcrelid = $1::oid`, oid).
		Scan(&locator, &attnums); err != nil {
		return
	}
	nameOf := map[int]string{}
	for _, c := range t.Columns {
		nameOf[c.AttNum] = c.Name
	}
	cols := []string{}
	for _, f := range strings.Fields(strings.TrimSpace(attnums.String)) {
		var n int
		if _, err := fmt.Sscanf(f, "%d", &n); err != nil || n <= 0 {
			continue
		}
		if nm, ok := nameOf[n]; ok {
			cols = append(cols, nm)
		}
	}
	t.Distribute = pgDistributeClause(locator, cols)
}

// pgTablePartition 读分区信息。两套机制都试:PostgreSQL 10+ 的声明式分区,以及
// DWS/GaussDB 的 pg_partition。都读不到就什么也不写 —— 绝大多数表本来就不是分区表。
func pgTablePartition(ctx context.Context, db *sql.DB, t *pgTable, oid int64) {
	var def sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT pg_get_partkeydef($1::oid)`, oid).Scan(&def); err == nil {
		t.PartKeyDef = strings.TrimSpace(def.String)
	}
	var n int64
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM pg_partition WHERE parentid = $1::oid AND parttype = 'p'`, oid).Scan(&n); err == nil {
		t.PartCount = n
		if t.PartKeyDef == "" && n > 0 {
			t.PartKeyDef = "是" // DWS:知道它分区了,但分区键的还原留给下一步
		}
	}
}
