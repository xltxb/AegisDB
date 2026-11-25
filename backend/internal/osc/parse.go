package osc

// 从一条 DDL 里认出"这张表要怎么改、该不该走在线变更"(.scratch/osc-product 工单 01、08)。
//
// 认三种语句:ALTER TABLE、CREATE [UNIQUE|FULLTEXT|SPATIAL] INDEX、DROP INDEX。后两种先改写成等价
// 的 ALTER 子句。ALTER 的主体按顶层逗号拆成子句,逐条按 MySQL 8.0 的原生算法分三类:
//
//   - 原生:INSTANT 或只改元数据(加 / 删 / 改名列、改默认值、删 / 改名索引、表改名……)—— 原生秒完成,
//     走在线变更反而要整表拷贝;
//   - 在线变更:原生是 INPLACE 或 COPY 的(加索引、增删换主键、改列定义、转字符集、重建表……);
//   - 不支持:外键、分区、认不出的子句 —— 照原路径,但提示。
//
// 有的子句是不是 INSTANT 取决于版本(8.0.29 起任意位置加列、删列才是 INSTANT),所以最终归类在
// Resolve 里按实例版本做。整条的结论:有不支持的 → 不支持;否则有要走在线变更的 → 整条走在线变更
// (原生那几条一并在影子表上执行);否则原生。
//
// 表名可带库名限定、可加反引号;关键字不分大小写;注释先去掉。

import (
	"context"
	"database/sql"
	"regexp"
	"strings"

	"aegisdb/internal/apimsg"
)

// clauseKind 是一个 ALTER 子句的归类。
type clauseKind int

const (
	clauseNative      clauseKind = iota // INSTANT / 只改元数据:原生执行
	clauseOSC                           // INPLACE / COPY:走在线变更
	clauseUnsupported                   // 在线变更做不了:照原路径并提示
	clauseVersioned                     // 版本够新时 INSTANT(原生),否则 INPLACE(在线变更)
	clauseOption                        // ALGORITHM= / LOCK=:不改表,影子表上不带
)

type clause struct {
	text      string
	kind      clauseKind
	why       string // 归为在线变更 / 不支持的原因(给人看)
	instantAt [3]int // clauseVersioned:从这个版本起 INSTANT
	tableRen  bool   // RENAME TO:改表名
}

// Parsed is what ParseDDL found in a statement.
type Parsed struct {
	// Schema 为空表示没写库名(用连接当前的库)。
	Schema, Table string
	// Change 只在 Supported 时有意义:要在影子表上执行的 ALTER 子句与列改名映射。
	Change Change
	// Supported:要走在线变更(Resolve 之后才有意义)。
	Supported bool
	// Native:原生秒完成,照原路径、不提示。
	Native bool
	// Unsupported 说为什么在线变更做不了(给提示用)。
	Unsupported string
	// Why:为什么要走在线变更(第一条原生会阻塞或重建表的子句)。
	Why string

	clauses []clause
	renames map[string]string
}

// 标识符:反引号括起(内部 `` 转义)或裸的。
const identPat = "(`(?:[^`]|``)+`|[A-Za-z0-9_$\u0080-￿]+)"
const qualPat = identPat + `(?:\s*\.\s*` + identPat + `)?`

var (
	reAlter   = regexp.MustCompile(`(?is)^\s*ALTER\s+(?:ONLINE\s+|IGNORE\s+)?TABLE\s+` + qualPat + `\s+(.*)$`)
	reCreate  = regexp.MustCompile(`(?is)^\s*CREATE\s+(?:(UNIQUE|FULLTEXT|SPATIAL)\s+)?INDEX\s+` + identPat + `(\s+USING\s+\w+)?\s+ON\s+` + qualPat + `\s*(\(.*\))\s*(.*)$`)
	reDrop    = regexp.MustCompile(`(?is)^\s*DROP\s+INDEX\s+` + identPat + `\s+ON\s+` + qualPat + `\s*(.*)$`)
	reComment = regexp.MustCompile(`(?s)/\*.*?\*/|--[^\n]*|#[^\n]*`)
	// CREATE / DROP INDEX 尾部的算法 / 锁提示:不改表
	reAlgoLockWords = regexp.MustCompile(`(?is)\b(?:ALGORITHM|LOCK)\s*=?\s*\w+`)
)

// 子句模式。顺序有意义:先认更具体的写法。
var (
	reOption      = regexp.MustCompile(`(?is)^(?:ALGORITHM|LOCK)\s*=?\s*\w+$`)
	reAddFK       = regexp.MustCompile(`(?is)^ADD\s+(?:CONSTRAINT\s+(?:` + identPat + `\s+)?)?FOREIGN\s+KEY\b`)
	reAddCheck    = regexp.MustCompile(`(?is)^ADD\s+(?:CONSTRAINT\s+(?:` + identPat + `\s+)?)?CHECK\b`)
	reAddPK       = regexp.MustCompile(`(?is)^ADD\s+(?:CONSTRAINT\s+(?:` + identPat + `\s+)?)?PRIMARY\s+KEY\b`)
	reAddUnique   = regexp.MustCompile(`(?is)^ADD\s+(?:CONSTRAINT\s+(?:` + identPat + `\s+)?)?UNIQUE\b`)
	reAddIndex    = regexp.MustCompile(`(?is)^ADD\s+(?:(FULLTEXT|SPATIAL)\s+)?(?:INDEX|KEY)\b`)
	reAddFTS      = regexp.MustCompile(`(?is)^ADD\s+(FULLTEXT|SPATIAL)\b`)
	rePartition   = regexp.MustCompile(`(?is)^(?:PARTITION\s+BY|REMOVE\s+PARTITIONING|(?:ADD|DROP|DISCARD|IMPORT|TRUNCATE|COALESCE|REORGANIZE|EXCHANGE|ANALYZE|CHECK|OPTIMIZE|REBUILD|REPAIR)\s+PARTITION)\b`)
	reAddColumn   = regexp.MustCompile(`(?is)^ADD\s+(?:COLUMN\b\s*)?`)
	reDropPK      = regexp.MustCompile(`(?is)^DROP\s+PRIMARY\s+KEY$`)
	reDropIndex   = regexp.MustCompile(`(?is)^DROP\s+(?:INDEX|KEY)\s+` + identPat + `$`)
	reDropFK      = regexp.MustCompile(`(?is)^DROP\s+(?:FOREIGN\s+KEY|CHECK|CONSTRAINT)\b`)
	reDropColumn  = regexp.MustCompile(`(?is)^DROP\s+(?:COLUMN\s+)?` + identPat + `$`)
	reRenameIdx   = regexp.MustCompile(`(?is)^RENAME\s+(?:INDEX|KEY)\b`)
	reRenameCol   = regexp.MustCompile(`(?is)^RENAME\s+COLUMN\s+` + identPat + `\s+TO\s+` + identPat + `$`)
	reRenameTable = regexp.MustCompile(`(?is)^RENAME\s+(?:TO\s+|AS\s+)?` + qualPat + `$`)
	reAlterCol    = regexp.MustCompile(`(?is)^ALTER\s+(?:COLUMN\s+)?` + identPat + `\s+(?:SET\s+DEFAULT\b|DROP\s+DEFAULT$|SET\s+(?:VISIBLE|INVISIBLE)$)`)
	reAlterIdx    = regexp.MustCompile(`(?is)^ALTER\s+(?:INDEX|CHECK|CONSTRAINT)\b`)
	reModify      = regexp.MustCompile(`(?is)^MODIFY\s+(?:COLUMN\s+)?`)
	reChange      = regexp.MustCompile(`(?is)^CHANGE\s+(?:COLUMN\s+)?` + identPat + `\s+` + identPat + `\s+\S`)
	reConvert     = regexp.MustCompile(`(?is)^CONVERT\s+TO\s+(?:CHARACTER\s+SET|CHARSET)\b`)
	reDefaultCS   = regexp.MustCompile(`(?is)^(?:DEFAULT\s+)?(?:CHARACTER\s+SET|CHARSET|COLLATE)\b`)
	reEngine      = regexp.MustCompile(`(?is)^ENGINE\s*=?\s*(\w+)$`)
	reRebuild     = regexp.MustCompile(`(?is)^(?:FORCE|ROW_FORMAT\s*=?\s*\w+|KEY_BLOCK_SIZE\s*=?\s*\w+)$`)
	reTableMeta   = regexp.MustCompile(`(?is)^(?:AUTO_INCREMENT|COMMENT|STATS_PERSISTENT|STATS_AUTO_RECALC|STATS_SAMPLE_PAGES|AVG_ROW_LENGTH|MAX_ROWS|MIN_ROWS|PACK_KEYS|CHECKSUM|DELAY_KEY_WRITE)\b`)
	reKeysToggle  = regexp.MustCompile(`(?is)^(?:ENABLE|DISABLE)\s+KEYS$`)
	reTablespace  = regexp.MustCompile(`(?is)^(?:DISCARD|IMPORT)\s+TABLESPACE\b`)
	reOrderBy     = regexp.MustCompile(`(?is)^ORDER\s+BY\b`)
	reAutoInc     = regexp.MustCompile(`(?is)\bAUTO_INCREMENT\b`)
	reStoredGen   = regexp.MustCompile(`(?is)\bAS\s*\(.*\)\s*STORED\b`)
	rePositioned  = regexp.MustCompile(`(?is)\b(?:FIRST|AFTER\s+` + identPat + `)\s*$`)
)

func unquote(s string) string {
	if strings.HasPrefix(s, "`") && strings.HasSuffix(s, "`") && len(s) >= 2 {
		return strings.ReplaceAll(s[1:len(s)-1], "``", "`")
	}
	return s
}

// splitQual: 正则里 qualPat 捕获两段;只有一段时它是表名。
func splitQual(a, b string) (schema, table string) {
	if b == "" {
		return "", unquote(a)
	}
	return unquote(a), unquote(b)
}

// ParseDDL looks at one statement. ok=false 表示这根本不是一条针对某张表的 ALTER / CREATE INDEX /
// DROP INDEX(调用方照原路径,不提示)。归类要按实例版本定,调用方随后调 Resolve。
func ParseDDL(sql string) (p Parsed, ok bool) {
	q := strings.TrimSpace(reComment.ReplaceAllString(sql, " "))
	q = strings.TrimSpace(strings.TrimRight(q, "; \t\r\n"))

	var body string
	switch {
	case reCreate.MatchString(q):
		m := reCreate.FindStringSubmatch(q)
		p.Schema, p.Table = splitQual(m[4], m[5])
		kind := ""
		if m[1] != "" {
			kind = strings.ToUpper(m[1]) + " "
		}
		tail := strings.TrimSpace(reAlgoLockWords.ReplaceAllString(m[7], " "))
		body = strings.TrimSpace("ADD " + kind + "INDEX " + m[2] + m[3] + " " + m[6] + " " + tail)
	case reDrop.MatchString(q):
		m := reDrop.FindStringSubmatch(q)
		p.Schema, p.Table = splitQual(m[2], m[3])
		if tail := strings.TrimSpace(reAlgoLockWords.ReplaceAllString(m[4], " ")); tail != "" {
			p.clauses = []clause{{text: tail, kind: clauseUnsupported, why: "DROP INDEX 带了不认识的选项"}}
			return p, true
		}
		body = "DROP INDEX " + m[1]
	default:
		m := reAlter.FindStringSubmatch(q)
		if m == nil {
			return p, false
		}
		p.Schema, p.Table = splitQual(m[1], m[2])
		body = strings.TrimSpace(m[3])
	}
	for _, part := range splitCommas(body) {
		if t := strings.TrimSpace(part); t != "" {
			p.clauses = append(p.clauses, p.classify(t))
		}
	}
	if len(p.clauses) == 0 {
		return p, false
	}
	return p, true
}

// classify 归类一个子句;CHANGE / RENAME COLUMN 顺带记下列改名。
func (p *Parsed) classify(t string) clause {
	c := clause{text: t}
	set := func(k clauseKind, why string) clause { c.kind, c.why = k, why; return c }
	switch {
	case reOption.MatchString(t):
		return set(clauseOption, "")
	case rePartition.MatchString(t):
		return set(clauseUnsupported, "分区操作")
	case reAddFK.MatchString(t):
		return set(clauseUnsupported, "加外键")
	case reAddCheck.MatchString(t):
		return set(clauseOSC, "加 CHECK 约束(要校验存量数据)")
	case reAddPK.MatchString(t):
		return set(clauseOSC, "加主键(重建表)")
	case reAddUnique.MatchString(t):
		return set(clauseOSC, "加唯一索引")
	case reAddFTS.MatchString(t):
		return set(clauseOSC, "加全文 / 空间索引(期间不能写)")
	case reAddIndex.MatchString(t):
		return set(clauseOSC, "加索引")
	case reAddColumn.MatchString(t):
		switch {
		case reAutoInc.MatchString(t):
			return set(clauseOSC, "加自增列(期间不能写)")
		case reStoredGen.MatchString(t):
			return set(clauseOSC, "加存储生成列(COPY)")
		}
		c.kind, c.why = clauseVersioned, "加列(当前版本不是 INSTANT,要重建表)"
		c.instantAt = [3]int{8, 0, 12}
		if rePositioned.MatchString(t) {
			c.instantAt = [3]int{8, 0, 29}
		}
		return c
	case reDropPK.MatchString(t):
		return set(clauseOSC, "删主键(COPY)")
	case reDropIndex.MatchString(t):
		if m := reDropIndex.FindStringSubmatch(t); strings.EqualFold(unquote(m[1]), "PRIMARY") {
			return set(clauseOSC, "删主键(COPY)")
		}
		return set(clauseNative, "")
	case reDropFK.MatchString(t):
		return set(clauseNative, "")
	case reDropColumn.MatchString(t):
		c.kind, c.why, c.instantAt = clauseVersioned, "删列(当前版本不是 INSTANT,要重建表)", [3]int{8, 0, 29}
		return c
	case reRenameIdx.MatchString(t):
		return set(clauseNative, "")
	case reRenameCol.MatchString(t):
		m := reRenameCol.FindStringSubmatch(t)
		p.rename(unquote(m[1]), unquote(m[2]))
		return set(clauseNative, "")
	case reRenameTable.MatchString(t):
		c.tableRen = true
		return set(clauseNative, "")
	case reAlterCol.MatchString(t), reAlterIdx.MatchString(t):
		return set(clauseNative, "")
	case reModify.MatchString(t):
		return set(clauseOSC, "改列定义(MODIFY)")
	case reChange.MatchString(t):
		m := reChange.FindStringSubmatch(t)
		p.rename(unquote(m[1]), unquote(m[2]))
		return set(clauseOSC, "改列定义(CHANGE)")
	case reConvert.MatchString(t):
		return set(clauseOSC, "转字符集(COPY)")
	case reDefaultCS.MatchString(t):
		return set(clauseNative, "") // 只改表的默认字符集,不动已有列
	case reEngine.MatchString(t):
		if e := reEngine.FindStringSubmatch(t)[1]; !strings.EqualFold(e, "InnoDB") {
			return set(clauseUnsupported, "换存储引擎为 "+e)
		}
		return set(clauseOSC, "重建表")
	case reRebuild.MatchString(t):
		return set(clauseOSC, "重建表")
	case reTableMeta.MatchString(t), reKeysToggle.MatchString(t):
		return set(clauseNative, "")
	case reTablespace.MatchString(t):
		return set(clauseUnsupported, "表空间操作")
	case reOrderBy.MatchString(t):
		return set(clauseUnsupported, "ORDER BY 重排")
	}
	return set(clauseUnsupported, "认不出的子句:"+clip(t, 40))
}

func (p *Parsed) rename(from, to string) {
	if strings.EqualFold(from, to) {
		return
	}
	if p.renames == nil {
		p.renames = map[string]string{}
	}
	p.renames[from] = to
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// Resolve 按实例版本给出整条语句的结论,填好 Supported / Native / Unsupported / Why / Change。
func (p *Parsed) Resolve(major, minor, patch int) {
	p.Supported, p.Native, p.Unsupported, p.Why = false, false, "", ""
	atLeast := func(v [3]int) bool {
		got := [3]int{major, minor, patch}
		for i := range got {
			if got[i] != v[i] {
				return got[i] > v[i]
			}
		}
		return true
	}
	needOSC, renameTable := false, false
	var spec []string
	for _, c := range p.clauses {
		switch c.kind {
		case clauseUnsupported:
			if p.Unsupported == "" {
				p.Unsupported = c.why
			}
		case clauseOSC:
			needOSC = true
			if p.Why == "" {
				p.Why = c.why
			}
		case clauseVersioned:
			if !atLeast(c.instantAt) {
				needOSC = true
				if p.Why == "" {
					p.Why = c.why
				}
			}
		}
		if c.tableRen {
			renameTable = true
		}
		if c.kind != clauseOption {
			spec = append(spec, c.text)
		}
	}
	switch {
	case p.Unsupported != "":
		p.Why = ""
	case needOSC && renameTable:
		p.Unsupported, p.Why = "改表名与要重建表的变更写在同一句", ""
	case needOSC:
		p.Supported = true
		p.Change = Change{Action: ActionAlter, Spec: strings.Join(spec, ", "), Renames: p.renames}
	default:
		p.Native = true
	}
}

func splitCommas(s string) []string {
	var out []string
	depth, start := 0, 0
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			if c == '\\' && quote != '`' {
				i++
				continue
			}
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"', '`':
			quote = c
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	if strings.TrimSpace(s[start:]) != "" || len(out) > 0 {
		out = append(out, s[start:])
	}
	return out
}

// TableSize reads the estimated row count and data + index bytes of schema.table, and the
// server version (归类 ALTER 子句要用,见 Parsed.Resolve)。估算值(InnoDB 的 TABLE_ROWS 是采样
// 出来的),够用来判断"是不是大表"。表不存在返回错误。
func TableSize(ctx context.Context, db *sql.DB, schema, table string) (rows, bytes int64, ver [3]int, err error) {
	var r, d, i sql.NullInt64
	var v string
	err = db.QueryRowContext(ctx,
		`SELECT TABLE_ROWS, DATA_LENGTH, INDEX_LENGTH, VERSION() FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?`,
		schema, table).Scan(&r, &d, &i, &v)
	if err == sql.ErrNoRows {
		return 0, 0, ver, apimsg.Errorf("表 %s.%s 不存在", schema, table)
	}
	ver[0], ver[1], ver[2] = parseVersion(v)
	return r.Int64, d.Int64 + i.Int64, ver, err
}
