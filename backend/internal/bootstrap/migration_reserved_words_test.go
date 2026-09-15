package bootstrap

// 列名不得是 PostgreSQL 的保留字。
//
// 这条规则是被一次生产故障换来的 —— 而且是同一周的第二次:
//
//   migration 0035_metadata_cache.sql statement 3 failed:
//   Error 1064 (42000): You have an error in your SQL syntax; ...
//   near 'databases     INT          NOT NULL DEFAULT 0, ...' at line 5
//
// 当时的库还是 MySQL,`databases` 是它的保留字。不加引号,数据库读不出这是个列名,
// 只能报一句"你的语法有问题" —— 连"哪个词有问题"都不说,因为报错指向的是**下一个**
// token。换到 PostgreSQL 之后故障形态一模一样,只是错误文本变成
// `ERROR: syntax error at or near "user"`。
//
// 保留字表换成 PostgreSQL 的,是因为两边并不重合,而不重合的那部分正好是最容易踩的:
// `user` / `order` / `limit` / `offset` / `authorization` 在 MySQL 里做列名合法,在
// PostgreSQL 里全是保留字。继续用 MySQL 的表查 PG 的 schema,等于给这几个词开了后门 ——
// 测试照绿,生产第一次 migrate 才炸。
//
// 为什么值得单独一条测试:
//
//   - 它只在**第一次 migrate 的那台真机**上炸,也就是生产。而那时人已经在部署窗口里。
//   - 迁移中途失败是**没有事务**的:0035 的前两条建表成功了、第三条炸了,库停在一个
//     半成品状态上。这次侥幸 —— 前两条是 CREATE TABLE IF NOT EXISTS,重跑无害。
//
// 补救办法当然是加双引号。但那意味着此后每一处引用这一列的地方都得记得加,忘一次就
// 又是一个 syntax error。所以这条测试要的不是"引号写对了",而是**换个名字**。

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"

	"velagateway/migrations"
)

// createTableBodyRe 抓一条 CREATE TABLE 的表名,以及括号里的定义体。
var createTableBodyRe = regexp.MustCompile(`(?is)CREATE TABLE(?:\s+IF NOT EXISTS)?\s+"?(\w+)"?\s*\((.*?)\n\)`)

// addColumnRe 抓 ALTER TABLE ... ADD [COLUMN] 的列名。
//
// COLUMN 在 PG 里可以省略(`ALTER TABLE t ADD c INT` 合法),所以它是可选的。
// 代价是 `ADD CONSTRAINT foo` 这类也会被抓到,由 defKeywords 在下面滤掉。
var addColumnRe = regexp.MustCompile(`(?i)\bADD\s+(?:COLUMN\s+)?(?:IF\s+NOT\s+EXISTS\s+)?("?)(\w+)`)

// alterColumnRe 抓 ALTER COLUMN 的列名,**要求** COLUMN 字面量。
//
// 这里不能像 ADD 那样把 COLUMN 变成可选:`ALTER TABLE` 本身就以 ALTER 开头,一放开
// 每一条 ALTER TABLE 语句都会把表名位置上的 `TABLE` 当成列名报上来 —— 而 TABLE 正好
// 是保留字,于是这条守卫会对着每一个迁移文件尖叫。代价是漏掉省略了 COLUMN 的
// `ALTER t TYPE ...`,而那条路改的是已存在列的类型,不产生新名字 —— 新名字是这条
// 守卫真正要拦的东西,它们都走 ADD 和 RENAME。
var alterColumnRe = regexp.MustCompile(`(?i)\bALTER\s+COLUMN\s+(?:IF\s+EXISTS\s+)?("?)(\w+)`)

// renameColumnRe 抓 RENAME [COLUMN] 的**新**名字 —— 旧名字已经在库里了,查它没有意义。
//
// COLUMN 同样可省略:`ALTER TABLE t RENAME env TO tier_code` 是合法的 PG。要求字面量
// COLUMN 就等于对这种写法视而不见 —— 而改名正是最容易改出一个保留字来的操作。
// `RENAME TO new_table`(改表名)不会误中:那里 TO 后面没有第二个 TO。
var renameColumnRe = regexp.MustCompile(`(?i)\bRENAME\s+(?:COLUMN\s+)?"?\w+"?\s+TO\s+("?)(\w+)`)

// defKeywords:定义体里以这些词开头的行不是列,是索引/约束。
//
// LIKE **不在**这里,尽管 `CREATE TABLE ... (LIKE src INCLUDING ALL)` 是合法子句:
// LIKE 同时也是保留字,把它无条件跳过就等于给一个叫 like 的列开了后门。它由
// lineIsLikeClause 单独判断 —— 看 LIKE 后面跟的是表名还是类型名。
var defKeywords = map[string]bool{
	"CONSTRAINT": true, "PRIMARY": true, "UNIQUE": true, "KEY": true, "INDEX": true,
	"FOREIGN": true, "CHECK": true, "EXCLUDE": true,
}

// firstIdentRe 取一行的第一个标识符,并告诉我们它有没有被双引号括起来。
var firstIdentRe = regexp.MustCompile(`^("?)(\w+)`)

// likeOperandRe 取 `LIKE xxx` 里 LIKE 后面那个词。
var likeOperandRe = regexp.MustCompile(`(?i)^LIKE\s+"?(\w+)`)

// pgTypeNames 是 PG 的内建类型名。只用来回答一个问题:LIKE 后面跟的是类型还是表名。
var pgTypeNames = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`
BIGINT BIGSERIAL BIT BOOL BOOLEAN BOX BYTEA CHAR CHARACTER CIDR CIRCLE DATE
DECIMAL DOUBLE FLOAT FLOAT4 FLOAT8 INET INT INT2 INT4 INT8 INTEGER INTERVAL
JSON JSONB LINE LSEG MACADDR MONEY NUMERIC PATH POINT POLYGON REAL SERIAL
SERIAL2 SERIAL4 SERIAL8 SMALLINT SMALLSERIAL TEXT TIME TIMESTAMP TIMESTAMPTZ
TIMETZ TSQUERY TSVECTOR UUID VARBIT VARCHAR XML
`) {
		pgTypeNames[w] = true
	}
}

// lineIsLikeClause 区分建表体里的两种 `like ...` 开头:
//
//	LIKE tbl_source INCLUDING ALL   —— 子句,跳过
//	like VARCHAR(32) NOT NULL       —— 一个叫 like 的列,必须查
//
// 判据是 LIKE 后面那个词:类型名 → 是列定义;别的 → 是子句。
func lineIsLikeClause(line string) bool {
	m := likeOperandRe.FindStringSubmatch(line)
	if m == nil {
		return false // 光一个 LIKE,后面什么都没有 —— 交给保留字检查
	}
	return !pgTypeNames[strings.ToUpper(m[1])]
}

// stripSQLComments 去掉 `--` 注释行:说明文字里出现 CREATE TABLE / 保留字都不算数。
func stripSQLComments(s string) string {
	lines := strings.Split(s, "\n")
	kept := lines[:0]
	for _, l := range lines {
		if !strings.HasPrefix(strings.TrimSpace(l), "--") {
			kept = append(kept, l)
		}
	}
	return strings.Join(kept, "\n")
}

// reservedHit 是一处违规:哪张表里的哪个标识符。
type reservedHit struct{ table, ident string }

// scanReserved 在一份 SQL 里找出所有没加引号的保留字标识符,并报告它一共查了多少个
// 标识符 —— 后者用来证明正则没有静静地失配。
//
// 单独拆出来是为了能用内联 SQL 直接测这条守卫本身(见
// TestReservedWordGuard_CatchesWhatItMustCatch):守卫自己也会坏,而一条坏掉的守卫
// 是绿的。
func scanReserved(sqlText string) (hits []reservedHit, seen int) {
	sql := stripSQLComments(sqlText)

	for _, m := range createTableBodyRe.FindAllStringSubmatch(sql, -1) {
		table, body := m[1], m[2]
		if isReserved(table, false) {
			hits = append(hits, reservedHit{table, table})
		}
		for _, line := range strings.Split(body, "\n") {
			line = strings.TrimSpace(line)
			im := firstIdentRe.FindStringSubmatch(line)
			if im == nil {
				continue
			}
			quoted, ident := im[1] == `"`, im[2]
			if !quoted {
				if strings.EqualFold(ident, "LIKE") {
					if lineIsLikeClause(line) {
						continue
					}
				} else if defKeywords[strings.ToUpper(ident)] {
					continue
				}
			}
			seen++
			if isReserved(ident, quoted) {
				hits = append(hits, reservedHit{table, ident})
			}
		}
	}
	for _, re := range []*regexp.Regexp{addColumnRe, alterColumnRe, renameColumnRe} {
		for _, m := range re.FindAllStringSubmatch(sql, -1) {
			quoted, ident := m[1] == `"`, m[2]
			// `ADD CONSTRAINT` / `ADD PRIMARY KEY` 这些不是列名。
			if !quoted && defKeywords[strings.ToUpper(ident)] {
				continue
			}
			seen++
			if isReserved(ident, quoted) {
				hits = append(hits, reservedHit{"ALTER TABLE", ident})
			}
		}
	}
	return hits, seen
}

func TestMigrationsAvoidReservedWords(t *testing.T) {
	seen := 0
	err := fs.WalkDir(migrations.FS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".sql") {
			return err
		}
		b, rerr := fs.ReadFile(migrations.FS, path)
		if rerr != nil {
			return rerr
		}
		hits, n := scanReserved(string(b))
		seen += n
		for _, h := range hits {
			reportReserved(t, path, h)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk migrations: %v", err)
	}
	// 正则一旦失配就会静静地检查 0 个列名,然后这条测试永远绿着 —— 那比没有更糟。
	if seen < 50 {
		t.Fatalf("只解析到 %d 个标识符,这条守卫基本没生效", seen)
	}
}

// 守卫自己也会坏,而一条坏掉的守卫是绿的 —— 所以这里用内联 SQL 直接喂它那几个它必须
// 拦下、以及必须放过的形状。
func TestReservedWordGuard_CatchesWhatItMustCatch(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want string // 期望被拦下的标识符;"" 表示这条必须放过
	}{
		{
			// MySQL 字典里没有 user,换成 PG 字典的全部意义就在这儿。
			name: "column named user",
			sql:  "CREATE TABLE IF NOT EXISTS t (\n  id BIGINT,\n  user VARCHAR(32)\n);",
			want: "user",
		},
		{
			// LIKE 同时是保留字和建表子句。无条件跳过就是给它开后门。
			name: "column named like",
			sql:  "CREATE TABLE IF NOT EXISTS t (\n  id BIGINT,\n  like VARCHAR(32)\n);",
			want: "like",
		},
		{
			// 无参数类型:`like text,` 长得和 LIKE 子句几乎一模一样。
			name: "column named like with a parenless type",
			sql:  "CREATE TABLE IF NOT EXISTS t (\n  id BIGINT,\n  like text\n);",
			want: "like",
		},
		{
			// 真的 LIKE 子句必须放过,否则这条规则开始拦正常写法。
			name: "a real LIKE clause is not a column",
			sql:  "CREATE TABLE IF NOT EXISTS t (\n  LIKE tbl_source INCLUDING ALL,\n  id BIGINT\n);",
			want: "",
		},
		{
			// PG 允许省略 COLUMN。要求字面量 COLUMN 就等于对这种写法视而不见。
			name: "RENAME without the COLUMN keyword",
			sql:  "ALTER TABLE tbl_x RENAME env TO offset;",
			want: "offset",
		},
		{
			name: "RENAME COLUMN with the keyword still works",
			sql:  "ALTER TABLE tbl_x RENAME COLUMN env TO authorization;",
			want: "authorization",
		},
		{
			// 改表名不是改列名,TO 后面那个词不该被当成列。
			name: "RENAME TO (table rename) is not a column",
			sql:  "ALTER TABLE tbl_x RENAME TO tbl_y;",
			want: "",
		},
		{
			// ADD 也可以省略 COLUMN。
			name: "ADD without the COLUMN keyword",
			sql:  "ALTER TABLE tbl_x ADD limit INT;",
			want: "limit",
		},
		{
			// ADD CONSTRAINT 不是列名,不能误报。
			name: "ADD CONSTRAINT is not a column",
			sql:  "ALTER TABLE tbl_x ADD CONSTRAINT ck_x CHECK (n > 0);",
			want: "",
		},
		{
			// `ALTER TABLE` 的 TABLE 是保留字。把 ALTER 的 COLUMN 放成可选,
			// 每一条 ALTER TABLE 都会在这里误报 —— 这条用例就是钉住它。
			name: "ALTER TABLE itself is never a column",
			sql:  "ALTER TABLE tbl_x ALTER COLUMN n TYPE BIGINT;",
			want: "",
		},
		{
			// 加了引号在 PG 上合法,这条守卫只管没加引号的。
			name: "a quoted reserved word is allowed",
			sql:  "CREATE TABLE IF NOT EXISTS t (\n  id BIGINT,\n  \"user\" VARCHAR(32)\n);",
			want: "",
		},
		{
			// 注释里写什么都不算数。
			name: "reserved words in comments do not count",
			sql:  "-- the user column was renamed\nCREATE TABLE IF NOT EXISTS t (\n  id BIGINT\n);",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hits, _ := scanReserved(tc.sql)
			var got []string
			for _, h := range hits {
				got = append(got, strings.ToLower(h.ident))
			}
			if tc.want == "" {
				if len(got) != 0 {
					t.Fatalf("不该拦下任何东西,却拦下了 %v", got)
				}
				return
			}
			for _, g := range got {
				if g == tc.want {
					return
				}
			}
			t.Fatalf("应该拦下 %q,实际拦下 %v", tc.want, got)
		})
	}
}

// isReserved 报告一个标识符是不是**没加引号**的 PostgreSQL 保留字。
//
// 带双引号在 PostgreSQL 上是合法的,所以不算错;但也不鼓励 —— 理由见文件顶部。
// 另外双引号还会把名字变成大小写敏感的,`"User"` 和 user 从此是两个东西。
func isReserved(ident string, quoted bool) bool {
	return !quoted && pgReserved[strings.ToUpper(ident)]
}

func reportReserved(t *testing.T, path string, h reservedHit) {
	t.Helper()
	low := strings.ToLower(h.ident)
	t.Errorf("%s 的 %s 里,标识符 %q 是 PostgreSQL 的保留字。\n"+
		"    不加双引号时 PG 报 `syntax error at or near \"%s\"`,而且往往指向下一个 token;\n"+
		"    这类错误只会在第一次 migrate 的那台真机上出现,也就是生产。\n"+
		"    请换个名字(如 %s_count / %s_name),不要靠双引号绕过去:\n"+
		"    此后每一处引用都得记得加引号,忘一次就又是一个 syntax error,\n"+
		"    而且加了引号的名字还是大小写敏感的。",
		path, h.table, h.ident, low, low, low)
}

// pgReserved 是 PostgreSQL 的保留字表:官方 SQL Key Words 附录里标 reserved
// (以及 reserved, can be function or type name)的那些,包含 SQL:2016 的保留字。
//
// 非保留字(non-reserved)**不在**这里:它们做标识符是合法的,把它们也拦掉会逼着人给
// status、comment、name、value 这类再普通不过的列名改名 —— 一条规则如果开始拦正常的
// 写法,它很快就会被绕过去。
//
// 类型名(如 CHAR / VARCHAR / DECIMAL,它们在 PG 里属于"可作函数或类型名的保留字")
// 留在表里是刻意的:上面只在**列名的位置**查这张表,所以一个真叫 `char` 的列会被拦
// 下来,而 `x VARCHAR(32)` 里的 VARCHAR 不会。
var pgReserved = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`
ALL ANALYSE ANALYZE AND ANY ARRAY AS ASC ASYMMETRIC AUTHORIZATION BETWEEN
BIGINT BINARY BIT BOOLEAN BOTH CASE CAST CHAR CHARACTER CHECK COALESCE COLLATE
COLLATION COLUMN CONCURRENTLY CONSTRAINT CREATE CROSS CURRENT_CATALOG
CURRENT_DATE CURRENT_ROLE CURRENT_SCHEMA CURRENT_TIME CURRENT_TIMESTAMP
CURRENT_USER DEC DECIMAL DEFAULT DEFERRABLE DESC DISTINCT DO ELSE END EXCEPT
EXISTS EXTRACT FALSE FETCH FLOAT FOR FOREIGN FREEZE FROM FULL GRANT GREATEST
GROUP GROUPING HAVING ILIKE IN INITIALLY INNER INOUT INT INTEGER INTERSECT
INTERVAL INTO IS ISNULL JOIN LATERAL LEADING LEAST LEFT LIKE LIMIT LOCALTIME
LOCALTIMESTAMP NATIONAL NATURAL NCHAR NONE NORMALIZE NOT NOTNULL NULL NULLIF
NUMERIC OFFSET ON ONLY OR ORDER OUT OUTER OVERLAPS OVERLAY PLACING POSITION
PRECISION PRIMARY REAL REFERENCES RETURNING RIGHT ROW SELECT SESSION_USER
SETOF SIMILAR SMALLINT SOME SUBSTRING SYMMETRIC SYSTEM_USER TABLE TABLESAMPLE
THEN TIME TIMESTAMP TO TRAILING TREAT TRIM TRUE UNION UNIQUE USER USING
VALUES VARCHAR VARIADIC VERBOSE WHEN WHERE WINDOW WITH XMLATTRIBUTES
XMLCONCAT XMLELEMENT XMLEXISTS XMLFOREST XMLNAMESPACES XMLPARSE XMLPI XMLROOT
XMLSERIALIZE XMLTABLE
`) {
		pgReserved[w] = true
	}
}
