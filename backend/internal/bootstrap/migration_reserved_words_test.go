package bootstrap

// 列名不得是 MySQL 的保留字。
//
// 这条规则也是被一次生产故障换来的 —— 而且是同一周的第二次:
//
//   migration 0035_metadata_cache.sql statement 3 failed:
//   Error 1064 (42000): You have an error in your SQL syntax; ...
//   near 'databases     INT          NOT NULL DEFAULT 0, ...' at line 5
//
// `databases` 是 MySQL 8 的保留字。不加反引号,MySQL 读不出这是个列名,只能报一句
// "你的语法有问题" —— 连"哪个词有问题"都不说,因为报错指向的是**下一个** token。
//
// 为什么值得单独一条测试(和隔壁 TestMigrationsDeclareCollation 同源):
//
//   - 开发环境**永远碰不到**。那边是 SQLite,它对保留字宽容得多(`databases` 在
//     SQLite 上就是个合法列名),所以整套测试跑绿也说明不了什么。
//   - 它只在**第一次 migrate 的那台真机**上炸,也就是生产。而那时人已经在部署窗口里。
//   - 迁移中途失败是**没有事务**的:0035 的前两条建表成功了、第三条炸了,库停在一个
//     半成品状态上。这次侥幸 —— 前两条是 CREATE TABLE IF NOT EXISTS,重跑无害。
//
// 补救办法当然是加反引号。但那意味着此后每一处引用这一列的地方都得记得加,忘一次就
// 又是 1064。所以这条测试要的不是"引号写对了",而是**换个名字**。

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"

	"velagateway/migrations"
)

// createTableBodyRe 抓一条 CREATE TABLE 的表名,以及括号里的定义体。
var createTableBodyRe = regexp.MustCompile(`(?is)CREATE TABLE(?:\s+IF NOT EXISTS)?\s+` + "`?" + `(\w+)` + "`?" + `\s*\((.*?)\n\)`)

// addModColumnRe 抓 ALTER TABLE ... ADD/MODIFY COLUMN 的列名。
var addModColumnRe = regexp.MustCompile(`(?i)\b(?:ADD|MODIFY)\s+COLUMN\s+` + "(`?)" + `(\w+)`)

// changeColumnRe 抓 CHANGE COLUMN 的**新**名字 —— 旧名字已经在库里了,查它没有意义。
var changeColumnRe = regexp.MustCompile(`(?i)\bCHANGE\s+COLUMN\s+` + "`?" + `\w+` + "`?" + `\s+` + "(`?)" + `(\w+)`)

// defKeywords:定义体里以这些词开头的行不是列,是索引/约束。
var defKeywords = map[string]bool{
	"CONSTRAINT": true, "PRIMARY": true, "UNIQUE": true, "KEY": true, "INDEX": true,
	"FULLTEXT": true, "SPATIAL": true, "FOREIGN": true, "CHECK": true,
}

// firstIdentRe 取一行的第一个标识符,并告诉我们它有没有被反引号括起来。
var firstIdentRe = regexp.MustCompile(`^` + "(`?)" + `(\w+)`)

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
				quoted, ident := im[1] == "`", im[2]
				if !quoted && defKeywords[strings.ToUpper(ident)] {
					continue
				}
				seen++
				checkIdent(t, path, table, ident, quoted)
			}
		}
		for _, re := range []*regexp.Regexp{addModColumnRe, changeColumnRe} {
			for _, m := range re.FindAllStringSubmatch(sql, -1) {
				seen++
				checkIdent(t, path, "ALTER TABLE", m[2], m[1] == "`")
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

// checkIdent 只在标识符**没有**反引号且是保留字时报错。
//
// 带反引号在 MySQL 上是合法的,所以不算错;但也不鼓励 —— 理由见文件顶部。
func checkIdent(t *testing.T, path, table, ident string, quoted bool) {
	if quoted || !mysqlReserved[strings.ToUpper(ident)] {
		return
	}
	t.Errorf("%s 的 %s 里,标识符 %q 是 MySQL 的保留字。\n"+
		"    不加反引号时 MySQL 报 ERROR 1064,而且指向下一个 token,不告诉你是哪个词;\n"+
		"    开发环境是 SQLite —— 它接受这个名字,所以只会在生产第一次 migrate 时炸。\n"+
		"    请换个名字(如 %s_count / %s_name),不要靠反引号绕过去:\n"+
		"    此后每一处引用都得记得加引号,忘一次就又是 1064。",
		path, table, ident, strings.ToLower(ident), strings.ToLower(ident))
}

// mysqlReserved 是 MySQL 8.0 的保留字表(官方 Keywords and Reserved Words 中标 (R) 的)。
//
// 非保留字**不在**这里:它们做标识符是合法的,把它们也拦掉会逼着人给 status、comment
// 这类再普通不过的列名改名 —— 一条规则如果开始拦正常的写法,它很快就会被绕过去。
//
// 类型名(INT、VARCHAR、BIGINT ...)在表里是刻意的:上面只在**列名的位置**查这张表,
// 所以一个真叫 `int` 的列会被拦下来,而 `x INT` 里的 INT 不会。
var mysqlReserved = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`
ACCESSIBLE ADD ALL ALTER ANALYZE AND ARRAY AS ASC ASENSITIVE BEFORE BETWEEN
BIGINT BINARY BLOB BOTH BY CALL CASCADE CASE CHANGE CHAR CHARACTER CHECK
COLLATE COLUMN CONDITION CONSTRAINT CONTINUE CONVERT CREATE CROSS CUBE
CUME_DIST CURRENT_DATE CURRENT_TIME CURRENT_TIMESTAMP CURRENT_USER CURSOR
DATABASE DATABASES DAY_HOUR DAY_MICROSECOND DAY_MINUTE DAY_SECOND DEC DECIMAL
DECLARE DEFAULT DELAYED DELETE DENSE_RANK DESC DESCRIBE DETERMINISTIC DISTINCT
DISTINCTROW DIV DOUBLE DROP DUAL EACH ELSE ELSEIF EMPTY ENCLOSED ESCAPED
EXCEPT EXISTS EXIT EXPLAIN FALSE FETCH FIRST_VALUE FLOAT FLOAT4 FLOAT8 FOR
FORCE FOREIGN FROM FULLTEXT FUNCTION GENERATED GET GRANT GROUP GROUPING GROUPS
HAVING HIGH_PRIORITY HOUR_MICROSECOND HOUR_MINUTE HOUR_SECOND IF IGNORE IN
INDEX INFILE INNER INOUT INSENSITIVE INSERT INT INT1 INT2 INT3 INT4 INT8
INTEGER INTERVAL INTO IO_AFTER_GTIDS IO_BEFORE_GTIDS IS ITERATE JOIN
JSON_TABLE KEY KEYS KILL LAG LAST_VALUE LATERAL LEAD LEADING LEAVE LEFT LIKE
LIMIT LINEAR LINES LOAD LOCALTIME LOCALTIMESTAMP LOCK LONG LONGBLOB LONGTEXT
LOOP LOW_PRIORITY MASTER_BIND MASTER_SSL_VERIFY_SERVER_CERT MATCH MAXVALUE
MEDIUMBLOB MEDIUMINT MEDIUMTEXT MEMBER MIDDLEINT MINUTE_MICROSECOND
MINUTE_SECOND MOD MODIFIES NATURAL NOT NO_WRITE_TO_BINLOG NTH_VALUE NTILE NULL
NUMERIC OF ON OPTIMIZE OPTIMIZER_COSTS OPTION OPTIONALLY OR ORDER OUT OUTER
OUTFILE OVER PARTITION PERCENT_RANK PRECISION PRIMARY PROCEDURE PURGE RANGE
RANK READ READS READ_WRITE REAL RECURSIVE REFERENCES REGEXP RELEASE RENAME
REPEAT REPLACE REQUIRE RESIGNAL RESTRICT RETURN REVOKE RIGHT RLIKE ROW ROWS
ROW_NUMBER SCHEMA SCHEMAS SECOND_MICROSECOND SELECT SENSITIVE SEPARATOR SET
SHOW SIGNAL SMALLINT SPATIAL SPECIFIC SQL SQLEXCEPTION SQLSTATE SQLWARNING
SQL_BIG_RESULT SQL_CALC_FOUND_ROWS SQL_SMALL_RESULT SSL STARTING STORED
STRAIGHT_JOIN SYSTEM TABLE TERMINATED THEN TINYBLOB TINYINT TINYTEXT TO
TRAILING TRIGGER TRUE UNDO UNION UNIQUE UNLOCK UNSIGNED UPDATE USAGE USE USING
UTC_DATE UTC_TIME UTC_TIMESTAMP VALUES VARBINARY VARCHAR VARCHARACTER VARYING
VIRTUAL WHEN WHERE WHILE WINDOW WITH WRITE XOR YEAR_MONTH ZEROFILL
`) {
		mysqlReserved[w] = true
	}
}
