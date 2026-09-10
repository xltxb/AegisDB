package gateway

// Oracle 表结构:完整的一份,而不是一串列名。
//
// 权威来源是 DBMS_METADATA.GET_DDL —— SQLcl 的 `ddl` 命令就是它。但它有两个必须
// 补的缺口:GET_DDL('TABLE') **不含索引、也不含注释**(两者都是"依赖对象",要走
// GET_DEPENDENT_DDL),而约束、表空间、存储参数、分区它是带的。
//
// 更要紧的是另一条路。生产上的账号常常连 DBMS_METADATA 都执行不了(它要
// SELECT_CATALOG_ROLE 或对象自身的属主权限),这时候只能从数据字典重建 —— 而重建
// 出来的东西如果只有列名和类型,那它回答不了任何一个真正会被问的问题:这张表有没有
// 主键、外键指向谁、哪几个索引、这列是干什么的、落在哪个表空间。所以这里把
// ALL_TAB_COLUMNS / ALL_CONSTRAINTS / ALL_CONS_COLUMNS / ALL_INDEXES /
// ALL_IND_COLUMNS / ALL_IND_EXPRESSIONS / ALL_TAB_COMMENTS / ALL_COL_COMMENTS /
// ALL_TABLES / ALL_PART_* 一并读了。
//
// **每一段都是可失败的。** 一个只被授予了部分字典视图的账号很常见,读不到约束不该
// 让整张表的结构一起消失 —— 那样人看到的是"查不到",而真相是"少了一块"。所以失败
// 的段落降级成一行说明留在输出里,让缺口是**看得见**的。这跟规则判定层的"查不到就
// 拒绝"方向相反,是故意的:这里是一个只读的展示接口,不是闸。
//
// 取数与渲染是分开的:renderOracleTable 是纯函数,不碰数据库。没有真实 Oracle 可以
// 跑测试的情况下,这是唯一能把"输出到底长什么样"钉住的办法(见 oracle_tableddl_test.go)。

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// ddlQueryTimeout 比 objectQueryTimeout 宽,因为重建这一路要发十来条字典查询,
// 而 ALL_* 视图在对象很多的库上并不快。权威路径只有一两条,用不满这个预算。
const ddlQueryTimeout = 30 * time.Second

type oraColumn struct {
	Name     string
	Type     string
	Length   sql.NullInt64
	Prec     sql.NullInt64
	Scale    sql.NullInt64
	CharLen  sql.NullInt64
	CharUsed string // C = 按字符, B = 按字节
	Nullable string // N = NOT NULL
	Default  string
}

type oraConstraint struct {
	Name       string
	Type       string // P / U / R / C
	Cols       []string
	RefOwner   string
	RefTable   string
	RefCols    []string
	DeleteRule string
	Condition  string
	Status     string
}

type oraIndex struct {
	Name       string
	Owner      string
	Unique     bool
	Type       string // NORMAL / BITMAP / FUNCTION-BASED NORMAL / ...
	Tablespace string
	Status     string
	Cols       []string // 已按位置排好;函数索引里是表达式原文
}

type oraColComment struct {
	Column  string
	Comment string
}

// oraTable 是渲染需要的全部事实。
type oraTable struct {
	Owner       string
	Name        string
	Tablespace  string
	Partitioned bool
	PartType    string
	PartKeys    []string
	PartCount   int64
	Temporary   bool
	IOTType     string
	Comment     string
	Columns     []oraColumn
	Constraints []oraConstraint
	Indexes     []oraIndex
	ColComments []oraColComment
	// Notes 是"这一段没读到"的说明,原样出现在输出顶部。缺口要看得见。
	Notes []string
}

// sqlIdent / sqlLit / qualifiedName 是 ANSI 的引用规则,Oracle 与 PostgreSQL 家族
// 共用一份 —— 两边的 DDL 重建都要用,各写一遍就会各错一遍。
//
// 全程加双引号是跟着 DBMS_METADATA 和 pg_dump 走的:字典里存的是标识符的真实大小写,
// 不加引号重放会被 Oracle 折成大写、被 PostgreSQL 折成小写。
func sqlIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

// sqlLit 把一段文本变成 SQL 字符串字面量(注释里的单引号必须转义)。
func sqlLit(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

func qualifiedName(owner, name string) string {
	if owner == "" {
		return sqlIdent(name)
	}
	return sqlIdent(owner) + "." + sqlIdent(name)
}

// oraColumnType 还原列的类型写法。
//
// 原来的实现只对 CHAR / RAW 补长度,于是 NUMBER(10,2) 显示成 NUMBER、VARCHAR2(50)
// 显示成 VARCHAR2 —— 一个没有精度的数值列和一个没有长度的字符列,在"看表结构"这件
// 事上等于没说。
func oraColumnType(c oraColumn) string {
	t := strings.ToUpper(strings.TrimSpace(c.Type))
	switch t {
	case "NUMBER":
		if !c.Prec.Valid {
			return "NUMBER" // 浮动精度,写出 (38) 反而是错的
		}
		if !c.Scale.Valid || c.Scale.Int64 == 0 {
			return fmt.Sprintf("NUMBER(%d)", c.Prec.Int64)
		}
		return fmt.Sprintf("NUMBER(%d,%d)", c.Prec.Int64, c.Scale.Int64)
	case "FLOAT":
		if c.Prec.Valid {
			return fmt.Sprintf("FLOAT(%d)", c.Prec.Int64)
		}
		return t
	case "CHAR", "NCHAR", "VARCHAR", "VARCHAR2", "NVARCHAR2":
		n := int64(0)
		if c.CharLen.Valid && c.CharLen.Int64 > 0 {
			n = c.CharLen.Int64
		} else if c.Length.Valid {
			n = c.Length.Int64
		}
		if n <= 0 {
			return t
		}
		// 只在按字符计算时标出来:BYTE 是默认语义,写出来是噪音。
		if strings.EqualFold(c.CharUsed, "C") {
			return fmt.Sprintf("%s(%d CHAR)", t, n)
		}
		return fmt.Sprintf("%s(%d)", t, n)
	case "RAW":
		if c.Length.Valid && c.Length.Int64 > 0 {
			return fmt.Sprintf("RAW(%d)", c.Length.Int64)
		}
		return t
	}
	// DATE / CLOB / BLOB / TIMESTAMP(6) / INTERVAL … 字典里存的就是完整写法。
	return t
}

// renderOracleTable 把事实渲染成一份可读、且基本可重放的 DDL。
//
// reason 非空时说明这是重建出来的,原因写在最上面 —— 人要能一眼看出自己看的不是
// 权威 DDL,以及为什么。
func renderOracleTable(t *oraTable, reason string) string {
	var b strings.Builder

	if reason != "" {
		b.WriteString("-- DBMS_METADATA.GET_DDL 不可用(" + reason + "),以下由 ALL_* 数据字典重建。\n")
		b.WriteString("-- 重建覆盖列/约束/索引/注释/表空间;存储参数、LOB 段、IOT 溢出段等未还原。\n")
	}
	for _, n := range t.Notes {
		b.WriteString("-- " + n + "\n")
	}

	// 表级事实压成一行:它们回答的是"这张表放在哪、是不是分区表"。
	facts := []string{}
	if t.Tablespace != "" {
		facts = append(facts, "表空间:"+t.Tablespace)
	}
	if t.Temporary {
		facts = append(facts, "临时表:是")
	}
	if t.IOTType != "" {
		facts = append(facts, "索引组织表:"+t.IOTType)
	}
	if t.Partitioned {
		p := "分区表:是"
		if t.PartType != "" {
			p = "分区:" + t.PartType
			if len(t.PartKeys) > 0 {
				p += "(" + strings.Join(quoteAll(t.PartKeys), ", ") + ")"
			}
		}
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

	full := qualifiedName(t.Owner, t.Name)
	b.WriteString("CREATE TABLE " + full + " (\n")
	lines := make([]string, 0, len(t.Columns))
	for _, c := range t.Columns {
		l := "  " + sqlIdent(c.Name) + " " + oraColumnType(c)
		if d := strings.TrimSpace(c.Default); d != "" {
			l += " DEFAULT " + d
		}
		if strings.EqualFold(c.Nullable, "N") {
			l += " NOT NULL"
		}
		lines = append(lines, l)
	}
	b.WriteString(strings.Join(lines, ",\n"))
	b.WriteString("\n)")
	if t.Tablespace != "" {
		b.WriteString(" TABLESPACE " + sqlIdent(t.Tablespace))
	}
	b.WriteString(";\n")

	if s := renderOracleConstraints(t); s != "" {
		b.WriteString("\n-- 约束\n" + s)
	}
	if s := renderOracleIndexes(t); s != "" {
		b.WriteString("\n-- 索引\n" + s)
	}
	if s := renderOracleComments(t); s != "" {
		b.WriteString("\n-- 注释\n" + s)
	}
	return strings.TrimRight(b.String(), "\n")
}

func quoteAll(names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, sqlIdent(n))
	}
	return out
}

func renderOracleConstraints(t *oraTable) string {
	full := qualifiedName(t.Owner, t.Name)
	var b strings.Builder
	for _, c := range t.Constraints {
		head := "ALTER TABLE " + full + " ADD CONSTRAINT " + sqlIdent(c.Name) + " "
		var body string
		switch strings.ToUpper(c.Type) {
		case "P":
			body = "PRIMARY KEY (" + strings.Join(quoteAll(c.Cols), ", ") + ")"
		case "U":
			body = "UNIQUE (" + strings.Join(quoteAll(c.Cols), ", ") + ")"
		case "R":
			body = "FOREIGN KEY (" + strings.Join(quoteAll(c.Cols), ", ") + ") REFERENCES " +
				qualifiedName(c.RefOwner, c.RefTable)
			if len(c.RefCols) > 0 {
				body += " (" + strings.Join(quoteAll(c.RefCols), ", ") + ")"
			}
			// NO ACTION 是默认,写出来是噪音;CASCADE / SET NULL 必须写 —— 它们决定
			// 删父行时子行会发生什么。
			switch strings.ToUpper(strings.TrimSpace(c.DeleteRule)) {
			case "CASCADE":
				body += " ON DELETE CASCADE"
			case "SET NULL":
				body += " ON DELETE SET NULL"
			}
		case "C":
			cond := strings.TrimSpace(c.Condition)
			if cond == "" {
				continue // 读不到条件的检查约束,写出一个空 CHECK 只会误导
			}
			body = "CHECK (" + cond + ")"
		default:
			continue
		}
		b.WriteString(head + body + ";")
		// 失效的约束什么也不保证,这件事必须跟着约束一起出现。
		if st := strings.ToUpper(strings.TrimSpace(c.Status)); st != "" && st != "ENABLED" {
			b.WriteString("  -- 状态:" + st)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func renderOracleIndexes(t *oraTable) string {
	var b strings.Builder
	for _, idx := range t.Indexes {
		kind := "INDEX"
		switch {
		case idx.Unique:
			kind = "UNIQUE INDEX"
		case strings.Contains(strings.ToUpper(idx.Type), "BITMAP"):
			kind = "BITMAP INDEX"
		}
		owner := idx.Owner
		if owner == "" {
			owner = t.Owner
		}
		b.WriteString("CREATE " + kind + " " + qualifiedName(owner, idx.Name) +
			" ON " + qualifiedName(t.Owner, t.Name) +
			" (" + strings.Join(idx.Cols, ", ") + ")")
		if idx.Tablespace != "" {
			b.WriteString(" TABLESPACE " + sqlIdent(idx.Tablespace))
		}
		b.WriteString(";")
		// UNUSABLE 的索引优化器不会用,而它看起来和正常索引一模一样。
		if st := strings.ToUpper(strings.TrimSpace(idx.Status)); st != "" && st != "VALID" && st != "N/A" {
			b.WriteString("  -- 状态:" + st)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func renderOracleComments(t *oraTable) string {
	full := qualifiedName(t.Owner, t.Name)
	var b strings.Builder
	if c := strings.TrimSpace(t.Comment); c != "" {
		b.WriteString("COMMENT ON TABLE " + full + " IS " + sqlLit(c) + ";\n")
	}
	for _, cc := range t.ColComments {
		if strings.TrimSpace(cc.Comment) == "" {
			continue
		}
		b.WriteString("COMMENT ON COLUMN " + full + "." + sqlIdent(cc.Column) + " IS " + sqlLit(cc.Comment) + ";\n")
	}
	return b.String()
}

// ------------------------------------------------------------ 取数

// oracleTableStructure 读回一张表的全部结构。
//
// 只有列读不到才算失败 —— 没有列就没有表可言。其余每一段读不到都降级成一条 Note。
func oracleTableStructure(ctx context.Context, db *sql.DB, owner, name string) (*oraTable, error) {
	t := &oraTable{Owner: owner, Name: name}

	cols, note, err := oracleColumns(ctx, db, owner, name)
	if err != nil {
		return nil, err
	}
	if len(cols) == 0 {
		return nil, fmt.Errorf("对象不存在(属主 %s)", owner)
	}
	t.Columns = cols
	t.addNote(note)

	t.addNote(oracleTableFacts(ctx, db, t))
	t.addNote(oracleTableConstraints(ctx, db, t))
	t.addNote(oracleTableIndexes(ctx, db, t))
	t.addNote(oracleTableComments(ctx, db, t))
	return t, nil
}

func (t *oraTable) addNote(s string) {
	if s != "" {
		t.Notes = append(t.Notes, s)
	}
}

// oracleColumns 读列。DATA_DEFAULT 是 LONG 类型,某些驱动/版本组合上会读失败 ——
// 那时退一步只读其余字段,而不是让整张表的结构跟着一起没有。
func oracleColumns(ctx context.Context, db *sql.DB, owner, name string) ([]oraColumn, string, error) {
	const withDefault = `SELECT column_name, data_type, data_length, data_precision, data_scale,
	        char_length, char_used, nullable, data_default
	 FROM all_tab_columns WHERE owner = :1 AND table_name = :2 ORDER BY column_id`
	const noDefault = `SELECT column_name, data_type, data_length, data_precision, data_scale,
	        char_length, char_used, nullable
	 FROM all_tab_columns WHERE owner = :1 AND table_name = :2 ORDER BY column_id`

	scan := func(q string, wantDefault bool) ([]oraColumn, error) {
		rows, err := db.QueryContext(ctx, q, owner, name)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []oraColumn{}
		for rows.Next() {
			var c oraColumn
			var charUsed, def sql.NullString
			dest := []any{&c.Name, &c.Type, &c.Length, &c.Prec, &c.Scale, &c.CharLen, &charUsed, &c.Nullable}
			if wantDefault {
				dest = append(dest, &def)
			}
			if err := rows.Scan(dest...); err != nil {
				return nil, err
			}
			c.CharUsed = charUsed.String
			c.Default = strings.TrimSpace(def.String)
			out = append(out, c)
		}
		return out, rows.Err()
	}

	cols, err := scan(withDefault, true)
	if err == nil {
		return cols, "", nil
	}
	cols, err2 := scan(noDefault, false)
	if err2 != nil {
		return nil, "", err // 两次都失败,报第一次的原因(它更接近真实缘由)
	}
	return cols, "列默认值未列出:读取 DATA_DEFAULT(LONG)失败(" + errBrief(err) + ")", nil
}

func oracleTableFacts(ctx context.Context, db *sql.DB, t *oraTable) string {
	var ts, part, temp, iot sql.NullString
	err := db.QueryRowContext(ctx,
		`SELECT tablespace_name, partitioned, temporary, iot_type
		 FROM all_tables WHERE owner = :1 AND table_name = :2`, t.Owner, t.Name).
		Scan(&ts, &part, &temp, &iot)
	if err != nil {
		return "表空间/分区信息未列出:读取 ALL_TABLES 失败(" + errBrief(err) + ")"
	}
	t.Tablespace = strings.TrimSpace(ts.String)
	t.Partitioned = strings.EqualFold(strings.TrimSpace(part.String), "YES")
	t.Temporary = strings.EqualFold(strings.TrimSpace(temp.String), "Y")
	t.IOTType = strings.TrimSpace(iot.String)
	if !t.Partitioned {
		return ""
	}

	// 分区表再补一层:分区方式、分区键、分区数。三条都是"读到就写、读不到就算了" ——
	// 它们是补充信息,不值得为此让整段结构降级。
	var ptype, subtype sql.NullString
	if err := db.QueryRowContext(ctx,
		`SELECT partitioning_type, subpartitioning_type FROM all_part_tables
		 WHERE owner = :1 AND table_name = :2`, t.Owner, t.Name).Scan(&ptype, &subtype); err == nil {
		t.PartType = strings.TrimSpace(ptype.String)
		if s := strings.TrimSpace(subtype.String); s != "" && !strings.EqualFold(s, "NONE") {
			t.PartType += "-" + s
		}
	}
	if rows, err := db.QueryContext(ctx,
		`SELECT column_name FROM all_part_key_columns
		 WHERE owner = :1 AND name = :2 ORDER BY column_position`, t.Owner, t.Name); err == nil {
		defer rows.Close()
		for rows.Next() {
			var c string
			if err := rows.Scan(&c); err == nil {
				t.PartKeys = append(t.PartKeys, c)
			}
		}
	}
	var n int64
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM all_tab_partitions WHERE table_owner = :1 AND table_name = :2`,
		t.Owner, t.Name).Scan(&n); err == nil {
		t.PartCount = n
	}
	return ""
}

// oracleTableConstraints 读主键/唯一/外键/检查约束及其列。
func oracleTableConstraints(ctx context.Context, db *sql.DB, t *oraTable) string {
	// 本表各约束的列。一次查完,避免每个约束发一条。
	colsOf := map[string][]string{}
	if rows, err := db.QueryContext(ctx,
		`SELECT constraint_name, column_name FROM all_cons_columns
		 WHERE owner = :1 AND table_name = :2 ORDER BY constraint_name, position`,
		t.Owner, t.Name); err == nil {
		for rows.Next() {
			var cn, col string
			if err := rows.Scan(&cn, &col); err == nil {
				colsOf[cn] = append(colsOf[cn], col)
			}
		}
		rows.Close()
	}

	// 外键指向的表与列:一条查询解决所有外键,而不是每个外键两条。
	type ref struct {
		table string
		owner string
		cols  []string
	}
	refs := map[string]ref{} // key: r_owner + "." + r_constraint_name
	if rows, err := db.QueryContext(ctx,
		`SELECT rc.owner, rc.constraint_name, rc.table_name, cc.column_name
		 FROM all_constraints rc
		 JOIN all_cons_columns cc
		   ON cc.owner = rc.owner AND cc.constraint_name = rc.constraint_name
		 WHERE (rc.owner, rc.constraint_name) IN (
		         SELECT r_owner, r_constraint_name FROM all_constraints
		         WHERE owner = :1 AND table_name = :2 AND constraint_type = 'R')
		 ORDER BY rc.owner, rc.constraint_name, cc.position`, t.Owner, t.Name); err == nil {
		for rows.Next() {
			var ro, rcn, rt, col string
			if err := rows.Scan(&ro, &rcn, &rt, &col); err == nil {
				k := ro + "." + rcn
				r := refs[k]
				r.owner, r.table = ro, rt
				r.cols = append(r.cols, col)
				refs[k] = r
			}
		}
		rows.Close()
	}

	// SEARCH_CONDITION 同样是 LONG,同样准备好读不到时退一步。
	const withCond = `SELECT constraint_name, constraint_type, r_owner, r_constraint_name,
	        delete_rule, status, search_condition
	 FROM all_constraints WHERE owner = :1 AND table_name = :2
	   AND constraint_type IN ('P','U','R','C')
	 ORDER BY DECODE(constraint_type,'P',1,'U',2,'R',3,4), constraint_name`
	const noCond = `SELECT constraint_name, constraint_type, r_owner, r_constraint_name,
	        delete_rule, status
	 FROM all_constraints WHERE owner = :1 AND table_name = :2
	   AND constraint_type IN ('P','U','R')
	 ORDER BY DECODE(constraint_type,'P',1,'U',2,'R',3,4), constraint_name`

	scan := func(q string, wantCond bool) ([]oraConstraint, error) {
		rows, err := db.QueryContext(ctx, q, t.Owner, t.Name)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []oraConstraint{}
		for rows.Next() {
			var c oraConstraint
			var rOwner, rName, delRule, status, cond sql.NullString
			dest := []any{&c.Name, &c.Type, &rOwner, &rName, &delRule, &status}
			if wantCond {
				dest = append(dest, &cond)
			}
			if err := rows.Scan(dest...); err != nil {
				return nil, err
			}
			c.DeleteRule, c.Status = delRule.String, status.String
			c.Condition = strings.TrimSpace(cond.String)
			c.Cols = colsOf[c.Name]
			if strings.EqualFold(c.Type, "R") {
				if r, ok := refs[rOwner.String+"."+rName.String]; ok {
					c.RefOwner, c.RefTable, c.RefCols = r.owner, r.table, r.cols
				}
			}
			// NOT NULL 是 Oracle 用一条系统命名的检查约束实现的。它已经写在列定义
			// 上了,再列一遍 CHECK ("X" IS NOT NULL) 只会把真正的业务约束淹掉。
			if strings.EqualFold(c.Type, "C") && oraIsNotNullCheck(c.Condition) {
				continue
			}
			out = append(out, c)
		}
		return out, rows.Err()
	}

	cs, err := scan(withCond, true)
	if err == nil {
		t.Constraints = cs
		return ""
	}
	cs, err2 := scan(noCond, false)
	if err2 != nil {
		return "约束未列出:读取 ALL_CONSTRAINTS 失败(" + errBrief(err2) + ")"
	}
	t.Constraints = cs
	return "检查约束未列出:读取 SEARCH_CONDITION(LONG)失败(" + errBrief(err) + ")"
}

// oraIsNotNullCheck 认出 Oracle 为 NOT NULL 自动生成的检查约束。
func oraIsNotNullCheck(cond string) bool {
	s := strings.ToUpper(strings.TrimSpace(cond))
	s = strings.TrimSuffix(strings.TrimPrefix(s, "("), ")")
	s = strings.TrimSpace(s)
	return strings.HasSuffix(s, " IS NOT NULL") && !strings.Contains(s, " AND ") && !strings.Contains(s, " OR ")
}

func oracleTableIndexes(ctx context.Context, db *sql.DB, t *oraTable) string {
	rows, err := db.QueryContext(ctx,
		`SELECT index_name, owner, uniqueness, index_type, tablespace_name, status
		 FROM all_indexes WHERE table_owner = :1 AND table_name = :2 ORDER BY index_name`,
		t.Owner, t.Name)
	if err != nil {
		return "索引未列出:读取 ALL_INDEXES 失败(" + errBrief(err) + ")"
	}
	byName := map[string]*oraIndex{}
	order := []string{}
	for rows.Next() {
		var idx oraIndex
		var uniq string
		var ts, status sql.NullString
		if err := rows.Scan(&idx.Name, &idx.Owner, &uniq, &idx.Type, &ts, &status); err != nil {
			rows.Close()
			return "索引未列出:读取 ALL_INDEXES 失败(" + errBrief(err) + ")"
		}
		idx.Unique = strings.EqualFold(uniq, "UNIQUE")
		idx.Tablespace = strings.TrimSpace(ts.String)
		idx.Status = strings.TrimSpace(status.String)
		byName[idx.Name] = &idx
		order = append(order, idx.Name)
	}
	rows.Close()
	if len(order) == 0 {
		return ""
	}

	// 列。DESC 列在 ALL_IND_COLUMNS 里是有标记的,漏掉它等于把一个反向索引说成正向。
	pos := map[string]map[int64]string{} // index -> position -> 列或表达式
	if crows, err := db.QueryContext(ctx,
		`SELECT index_name, column_position, column_name, descend FROM all_ind_columns
		 WHERE table_owner = :1 AND table_name = :2 ORDER BY index_name, column_position`,
		t.Owner, t.Name); err == nil {
		for crows.Next() {
			var iname, col, desc string
			var p int64
			if err := crows.Scan(&iname, &p, &col, &desc); err != nil {
				continue
			}
			s := sqlIdent(col)
			if strings.EqualFold(strings.TrimSpace(desc), "DESC") {
				s += " DESC"
			}
			if pos[iname] == nil {
				pos[iname] = map[int64]string{}
			}
			pos[iname][p] = s
		}
		crows.Close()
	}

	// 函数索引:ALL_IND_COLUMNS 里是 SYS_NC00007$ 这样的隐藏列名,对人毫无意义,
	// 真正的表达式在 ALL_IND_EXPRESSIONS(也是 LONG,读不到就保持原样)。
	fbNote := ""
	if erows, err := db.QueryContext(ctx,
		`SELECT index_name, column_position, column_expression FROM all_ind_expressions
		 WHERE table_owner = :1 AND table_name = :2 ORDER BY index_name, column_position`,
		t.Owner, t.Name); err == nil {
		for erows.Next() {
			var iname, expr string
			var p int64
			if err := erows.Scan(&iname, &p, &expr); err != nil {
				continue
			}
			if pos[iname] == nil {
				pos[iname] = map[int64]string{}
			}
			pos[iname][p] = strings.TrimSpace(expr)
		}
		erows.Close()
	} else {
		for _, n := range order {
			if strings.Contains(strings.ToUpper(byName[n].Type), "FUNCTION-BASED") {
				fbNote = "函数索引的表达式未还原:读取 ALL_IND_EXPRESSIONS 失败(" + errBrief(err) + ")"
				break
			}
		}
	}

	for _, n := range order {
		m := pos[n]
		cols := make([]string, 0, len(m))
		for p := int64(1); p <= int64(len(m)); p++ {
			if s, ok := m[p]; ok {
				cols = append(cols, s)
			}
		}
		if len(cols) == 0 {
			continue // 列都读不到的索引,写出一个空括号没有意义
		}
		byName[n].Cols = cols
		t.Indexes = append(t.Indexes, *byName[n])
	}
	return fbNote
}

func oracleTableComments(ctx context.Context, db *sql.DB, t *oraTable) string {
	var c sql.NullString
	if err := db.QueryRowContext(ctx,
		`SELECT comments FROM all_tab_comments WHERE owner = :1 AND table_name = :2`,
		t.Owner, t.Name).Scan(&c); err == nil {
		t.Comment = strings.TrimSpace(c.String)
	}
	rows, err := db.QueryContext(ctx,
		`SELECT column_name, comments FROM all_col_comments
		 WHERE owner = :1 AND table_name = :2 AND comments IS NOT NULL ORDER BY column_name`,
		t.Owner, t.Name)
	if err != nil {
		return "列注释未列出:读取 ALL_COL_COMMENTS 失败(" + errBrief(err) + ")"
	}
	defer rows.Close()
	for rows.Next() {
		var col string
		var cm sql.NullString
		if err := rows.Scan(&col, &cm); err == nil && strings.TrimSpace(cm.String) != "" {
			t.ColComments = append(t.ColComments, oraColComment{Column: col, Comment: strings.TrimSpace(cm.String)})
		}
	}
	return ""
}

// errBrief 把驱动的错误压成一行。这些字符串会出现在页面上,而驱动的错误里
// 常带换行和栈式的上下文 —— 整段贴进 DDL 会把输出冲散。
func errBrief(err error) string {
	if err == nil {
		return ""
	}
	s := strings.TrimSpace(strings.SplitN(err.Error(), "\n", 2)[0])
	const max = 160
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}
