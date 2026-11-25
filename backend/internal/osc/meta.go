package osc

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"velagateway/internal/apimsg"
)

// column is what the copier and the replayer need to know about one column.
type column struct {
	Name      string
	DataType  string // information_schema DATA_TYPE, lower case: int, varchar, datetime ...
	Charset   string // 仅字符串列有
	Collation string
	Unsigned  bool
	Generated bool // 生成列不能被写入,拷贝与重放都跳过它,由影子表自己算
	Nullable  bool
	// HasDefault / AutoInc:新加的列在拷贝、重放时不会被写到,靠它们取值(见 newColMap)
	HasDefault bool
	AutoInc    bool
	Precision  int // DECIMAL 的 M
	Scale     int // DECIMAL 的 D;DATETIME/TIME 的小数秒位数也放这里
}

type index struct {
	Name    string
	Unique  bool
	Cols    []string
	Partial bool // 前缀索引或函数索引:唯一性不落在"整列取值"上,不能拿来分块
}

// tableMeta is one table's shape, loaded once when a migration starts. 迁移期间表
// 结构若被改,重放按下标对列就会错位 —— 所以一旦在 binlog 里看到针对本表的 DDL,
// 迁移直接中止,而不是去"适应"新结构。
type tableMeta struct {
	Schema, Table string
	Engine        string
	EstRows       int64
	DataBytes     int64
	Columns       []column
	Indexes       []index
	ChunkKey      string // "" = 没有可用的分块键
	keyIdx        []int  // 分块键各列在 Columns 中的下标
}

type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// loadMeta reads the table's shape from information_schema. exists=false (and no
// error) means there is no such base table.
func loadMeta(ctx context.Context, q queryer, schema, table string) (m *tableMeta, exists bool, err error) {
	m = &tableMeta{Schema: schema, Table: table}
	var rows, dataLen, idxLen sql.NullInt64
	var engine sql.NullString
	err = q.QueryRowContext(ctx, `SELECT ENGINE, TABLE_ROWS, DATA_LENGTH, INDEX_LENGTH FROM information_schema.TABLES
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? AND TABLE_TYPE = 'BASE TABLE'`, schema, table).
		Scan(&engine, &rows, &dataLen, &idxLen)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, apimsg.Errorf("读取表信息: %w", err)
	}
	m.Engine, m.EstRows, m.DataBytes = engine.String, rows.Int64, dataLen.Int64+idxLen.Int64

	cr, err := q.QueryContext(ctx, `SELECT COLUMN_NAME, DATA_TYPE, COLUMN_TYPE, IS_NULLABLE,
			IFNULL(CHARACTER_SET_NAME, ''), IFNULL(COLLATION_NAME, ''),
			IFNULL(NUMERIC_PRECISION, 0), IFNULL(NUMERIC_SCALE, 0), IFNULL(DATETIME_PRECISION, 0),
			IFNULL(GENERATION_EXPRESSION, ''), COLUMN_DEFAULT IS NOT NULL, LOWER(EXTRA)
		FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? ORDER BY ORDINAL_POSITION`, schema, table)
	if err != nil {
		return nil, false, apimsg.Errorf("读取列信息: %w", err)
	}
	defer cr.Close()
	for cr.Next() {
		var c column
		var colType, nullable, genExpr, extra string
		var prec, scale, dtPrec int
		if err := cr.Scan(&c.Name, &c.DataType, &colType, &nullable, &c.Charset, &c.Collation, &prec, &scale, &dtPrec, &genExpr, &c.HasDefault, &extra); err != nil {
			return nil, false, err
		}
		c.DataType = strings.ToLower(c.DataType)
		c.Unsigned = strings.Contains(strings.ToLower(colType), "unsigned")
		c.Nullable = nullable == "YES"
		// EXTRA 里的 DEFAULT_GENERATED 是"默认值是表达式",不是生成列;只认生成表达式。
		c.Generated = genExpr != ""
		c.AutoInc = strings.Contains(extra, "auto_increment")
		c.Precision, c.Scale = prec, scale
		if isTemporal(c.DataType) {
			c.Scale = dtPrec
		}
		m.Columns = append(m.Columns, c)
	}
	if err := cr.Err(); err != nil {
		return nil, false, err
	}

	ir, err := q.QueryContext(ctx, `SELECT INDEX_NAME, NON_UNIQUE, IFNULL(COLUMN_NAME, ''), SUB_PART
		FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?
		ORDER BY INDEX_NAME = 'PRIMARY' DESC, INDEX_NAME, SEQ_IN_INDEX`, schema, table)
	if err != nil {
		return nil, false, apimsg.Errorf("读取索引信息: %w", err)
	}
	defer ir.Close()
	for ir.Next() {
		var name, col string
		var nonUnique int
		var subPart sql.NullInt64
		if err := ir.Scan(&name, &nonUnique, &col, &subPart); err != nil {
			return nil, false, err
		}
		if n := len(m.Indexes); n == 0 || m.Indexes[n-1].Name != name {
			m.Indexes = append(m.Indexes, index{Name: name, Unique: nonUnique == 0})
		}
		ix := &m.Indexes[len(m.Indexes)-1]
		if col == "" || subPart.Valid {
			ix.Partial = true
		}
		ix.Cols = append(ix.Cols, col)
	}
	if err := ir.Err(); err != nil {
		return nil, false, err
	}
	m.pickChunkKey()
	return m, true, nil
}

func (m *tableMeta) colIndex(name string) int {
	for i, c := range m.Columns {
		if strings.EqualFold(c.Name, name) {
			return i
		}
	}
	return -1
}

func (m *tableMeta) hasPK() bool {
	return len(m.Indexes) > 0 && m.Indexes[0].Name == "PRIMARY"
}

// uniqueNotNull lists the non-primary unique indexes that could stand in for a
// primary key: every column NOT NULL (两行 NULL 在唯一索引里不算冲突), and the
// uniqueness is over whole column values.
func (m *tableMeta) uniqueNotNull() []index {
	var out []index
	for _, ix := range m.Indexes {
		if ix.Name == "PRIMARY" || !ix.Unique || ix.Partial {
			continue
		}
		ok := true
		for _, c := range ix.Cols {
			if i := m.colIndex(c); i < 0 || m.Columns[i].Nullable {
				ok = false
			}
		}
		if ok {
			out = append(out, ix)
		}
	}
	return out
}

// pickChunkKey prefers the primary key, then the first usable unique-not-null
// index — in both cases only if every key column has a type whose ORDER BY
// order and comparison order agree.
func (m *tableMeta) pickChunkKey() {
	cands := m.uniqueNotNull()
	if m.hasPK() && !m.Indexes[0].Partial {
		cands = append([]index{m.Indexes[0]}, cands...)
	}
	for _, ix := range cands {
		idx := make([]int, 0, len(ix.Cols))
		ok := true
		for _, c := range ix.Cols {
			i := m.colIndex(c)
			if i < 0 || !chunkKeyTypeOK(m.Columns[i].DataType) {
				ok = false
				break
			}
			idx = append(idx, i)
		}
		if ok {
			m.ChunkKey, m.keyIdx = ix.Name, idx
			return
		}
	}
}

func isTemporal(t string) bool {
	switch t {
	case "date", "datetime", "timestamp", "time":
		return true
	}
	return false
}

func isInteger(t string) bool {
	switch t {
	case "tinyint", "smallint", "mediumint", "int", "integer", "bigint", "year":
		return true
	}
	return false
}

func isText(t string) bool {
	switch t {
	case "char", "varchar", "tinytext", "text", "mediumtext", "longtext":
		return true
	}
	return false
}

// chunkKeyTypeOK: 键的"排序顺序"必须等于"比较顺序",范围切分才不会漏行。浮点(文本往返
// 有精度损失)、ENUM/SET(排序按下标、与字符串比较按字面)、JSON/BLOB/空间类型都不行。
func chunkKeyTypeOK(t string) bool {
	switch {
	case isInteger(t), isText(t), isTemporal(t):
		return true
	case t == "decimal", t == "binary", t == "varbinary":
		return true
	}
	return false
}

// replayTypeOK: 行镜像里的值能否原样写回。空间类型的内部格式、将来版本新增的类型,
// 我们都没有验证过 —— 没验证过的就不做。
func replayTypeOK(t string) bool {
	switch {
	case isInteger(t), isText(t), isTemporal(t):
		return true
	}
	switch t {
	case "decimal", "float", "double", "bit", "enum", "set", "json",
		"binary", "varbinary", "tinyblob", "blob", "mediumblob", "longblob":
		return true
	}
	return false
}

// Every SQL statement in this package runs on a session opened by prepareSession:
// SET NAMES binary 让字符串列的字节原样往返 —— 读出来是列自己字符集的字节,写回去用
// CONVERT(? USING <列字符集>) 重新贴上字符集标签,不经过任何转码。否则 latin1 列里
// 的 0xE9 会被当作 utf8mb4 解释,悄悄变成另一个字符。
//
// time_zone 固定 UTC:TIMESTAMP 在读、比较、写回时都按会话时区换算,同一个会话里
// 读写一致即可;固定成 UTC 是为了和 binlog 解码出来的 TIMESTAMP 文本对上。
func prepareSession(ctx context.Context, c *sql.Conn) error {
	for _, s := range []string{
		"SET NAMES binary",
		"SET SESSION time_zone = '+00:00'",
		// 严格模式:值放不进新类型(改列类型 / 收窄)、NOT NULL 遇到 NULL 都报错,而不是截断、填 0
		// 之后继续 —— 那是静默改数据。不带 NO_ZERO_DATE:存量数据里的零日期照常能拷。
		"SET SESSION sql_mode = 'STRICT_ALL_TABLES,NO_ENGINE_SUBSTITUTION'",
		// 拷贝块之后要读全 SHOW WARNINGS(每块至多 ChunkSize 条"已存在"),默认上限 1024 不够
		"SET SESSION max_error_count = 65535",
	} {
		if _, err := c.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("%s: %w", s, err)
		}
	}
	return nil
}

// valueExpr is the placeholder for writing a value into column c.
func valueExpr(c column) string {
	switch {
	case isText(c.DataType):
		return "CONVERT(? USING " + c.Charset + ")"
	case c.DataType == "json":
		return "CONVERT(? USING utf8mb4)"
	}
	return "?"
}

// keyExpr is the placeholder for comparing a value against key column c. 显式
// COLLATE 是为了让比较走列自己的排序规则(从而走得上索引),而不是被连接的
// binary 字符集拖成逐字节比较。
func keyExpr(c column) string {
	switch {
	case isText(c.DataType):
		return fmt.Sprintf("CONVERT(? USING %s) COLLATE %s", c.Charset, c.Collation)
	case c.DataType == "decimal":
		return fmt.Sprintf("CAST(? AS DECIMAL(%d,%d))", c.Precision, c.Scale)
	case c.DataType == "date":
		return "CAST(? AS DATE)"
	case c.DataType == "datetime", c.DataType == "timestamp":
		return fmt.Sprintf("CAST(? AS DATETIME(%d))", c.Scale)
	case c.DataType == "time":
		return fmt.Sprintf("CAST(? AS TIME(%d))", c.Scale)
	}
	return "?"
}

// writable returns the non-generated columns, in table order.
func (m *tableMeta) writable() []int {
	var out []int
	for i, c := range m.Columns {
		if !c.Generated {
			out = append(out, i)
		}
	}
	return out
}

func (m *tableMeta) quotedCols(idx []int) string {
	s := make([]string, len(idx))
	for i, j := range idx {
		s[i] = quoteIdent(m.Columns[j].Name)
	}
	return strings.Join(s, ", ")
}
