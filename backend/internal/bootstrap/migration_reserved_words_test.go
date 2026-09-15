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

// addModColumnRe 抓 ALTER TABLE ... ADD/ALTER COLUMN 的列名。
var addModColumnRe = regexp.MustCompile(`(?i)\b(?:ADD|ALTER)\s+COLUMN\s+(?:IF\s+NOT\s+EXISTS\s+)?("?)(\w+)`)

// renameColumnRe 抓 RENAME COLUMN 的**新**名字 —— 旧名字已经在库里了,查它没有意义。
var renameColumnRe = regexp.MustCompile(`(?i)\bRENAME\s+COLUMN\s+"?\w+"?\s+TO\s+("?)(\w+)`)

// defKeywords:定义体里以这些词开头的行不是列,是索引/约束。
var defKeywords = map[string]bool{
	"CONSTRAINT": true, "PRIMARY": true, "UNIQUE": true, "KEY": true, "INDEX": true,
	"FOREIGN": true, "CHECK": true, "EXCLUDE": true, "LIKE": true,
}

// firstIdentRe 取一行的第一个标识符,并告诉我们它有没有被双引号括起来。
var firstIdentRe = regexp.MustCompile(`^("?)(\w+)`)

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
		sql := stripSQLComments(string(b))

		for _, m := range createTableBodyRe.FindAllStringSubmatch(sql, -1) {
			table, body := m[1], m[2]
			checkIdent(t, path, table, table, false)
			for _, line := range strings.Split(body, "\n") {
				im := firstIdentRe.FindStringSubmatch(strings.TrimSpace(line))
				if im == nil {
					continue
				}
				quoted, ident := im[1] == `"`, im[2]
				if !quoted && defKeywords[strings.ToUpper(ident)] {
					continue
				}
				seen++
				checkIdent(t, path, table, ident, quoted)
			}
		}
		for _, re := range []*regexp.Regexp{addModColumnRe, renameColumnRe} {
			for _, m := range re.FindAllStringSubmatch(sql, -1) {
				seen++
				checkIdent(t, path, "ALTER TABLE", m[2], m[1] == `"`)
			}
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

// checkIdent 只在标识符**没有**双引号且是保留字时报错。
//
// 带双引号在 PostgreSQL 上是合法的,所以不算错;但也不鼓励 —— 理由见文件顶部。
// 另外双引号还会把名字变成大小写敏感的,`"User"` 和 user 从此是两个东西。
func checkIdent(t *testing.T, path, table, ident string, quoted bool) {
	if quoted || !pgReserved[strings.ToUpper(ident)] {
		return
	}
	t.Errorf("%s 的 %s 里,标识符 %q 是 PostgreSQL 的保留字。\n"+
		"    不加双引号时 PG 报 `syntax error at or near \"%s\"`,而且往往指向下一个 token;\n"+
		"    这类错误只会在第一次 migrate 的那台真机上出现,也就是生产。\n"+
		"    请换个名字(如 %s_count / %s_name),不要靠双引号绕过去:\n"+
		"    此后每一处引用都得记得加引号,忘一次就又是一个 syntax error,\n"+
		"    而且加了引号的名字还是大小写敏感的。",
		path, table, ident, strings.ToLower(ident), strings.ToLower(ident), strings.ToLower(ident))
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
