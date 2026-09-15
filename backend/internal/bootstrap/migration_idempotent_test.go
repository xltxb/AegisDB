package bootstrap

// 一个文件里的 ALTER,要么**每一条都幂等**,要么只有一条。
//
// 迁移中途失败**没有事务**(见 Migrate:一条一条 Exec,失败就整个函数返回),而失败之后
// 运维能做的只有重跑 —— 那时整个文件会从第一条开始再走一遍。
//
// 于是一个含七条 `ADD COLUMN` 的文件,只要在第五条上炸了(网络、锁等待、列名撞保留字),
// 重跑必然在第一条就报 42701 column ... already exists:前四条已经加上了。运维看到的是一个
// **完全不同的错误**,而真正的原因在上一次的日志里。库停在半成品状态,而唯一的出路是
// 手工把已经执行过的那几条挑出来删掉。
//
// ADR 0016 §三因此要求:要么写成幂等的形式(ALTER COLUMN / ADD COLUMN IF NOT EXISTS /
// CREATE TABLE IF NOT EXISTS / CREATE INDEX IF NOT EXISTS 这些重复执行无害的),要么
// 一个文件只放一条 ALTER —— 那样重跑就是从头再来一次,没有"前面几条已经生效"的中间态。
//
// 迁移历史已经被压成一份 PostgreSQL baseline(0001_init.sql),那份文件整体幂等:
// 全部是 CREATE TABLE / CREATE INDEX IF NOT EXISTS,重跑只出 NOTICE。所以这里不再有
// 豁免名单 —— 没有对象可豁免。这条闸从此只对**新写的**迁移生效。

import (
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"testing"

	"velagateway/migrations"
)

// 这条闸数的是「重跑会炸的 ALTER 子句」。
//
// 做法是先切句、再只看 ALTER TABLE 那些句子,而不是对整个文件做关键字匹配。
// 从前是后者,于是不得不维护一张 standaloneObjects 表去滤掉 DROP TABLE /
// DROP VIEW 这类独立语句 —— 而那张表按**关键字**匹配,列名一旦撞上表里的词就
// 被静默滤掉:`ALTER TABLE t ADD type TEXT` 数成 0。type / schema / rule /
// policy 都是再普通不过的列名,database 更是这个项目自己用过的
// (tbl_schema_object.database,迁移 0040 才改名)。漏抓是静默的,比误报危险。
//
// 切句之后那张表就不需要了:独立语句不以 ALTER TABLE 开头,天然落在计数之外。
var (
	// 语句必须以 ALTER TABLE 开头才进入计数。
	//
	// 与隔壁 migration_tables_test.go 的 alterTableRe 不是一回事:那个抓的是
	// ALTER TABLE **后面的表名**(查拼写用),不锚定行首;这个只问「这句是不是
	// 一条 ALTER TABLE」。
	alterStmtHeadRe = regexp.MustCompile(`(?is)^\s*ALTER\s+TABLE\b`)

	// 列属性修改:ALTER [COLUMN] x SET/DROP DEFAULT|NOT NULL。它改的是已有列的
	// 属性,重跑无害,但句子里带着 DROP —— 先整段摘掉,免得被下面数进去。
	columnAttrRe = regexp.MustCompile(`(?is)\bALTER\s+(?:COLUMN\s+)?\w+\s+(?:SET|DROP)\s+(?:DEFAULT|NOT\s+NULL)`)

	// 剩下的 ADD/DROP 子句。第 1 组是紧跟的那个词,第 2 组是可选的 IF (NOT) EXISTS。
	//
	// COLUMN 是**可省的**:`ADD a TEXT` / `DROP a` 都是合法 PG,而那恰恰是最容易
	// 随手写出来的形式。隔壁 migration_reserved_words_test.go 的 addColumnRe 同理。
	alterClauseRe = regexp.MustCompile(`(?i)\b(?:ADD|DROP)\s+(\w+)(\s+IF\s+(?:NOT\s+)?EXISTS\b)?`)

	// 字符串字面量。里面的 ADD/DROP 是数据不是语句。
	// 简化处理:不认 '' 这种转义写法 —— 迁移文件里没有,真出现了会多摘一点,
	// 方向是保守的(少数不是多数)。
	sqlStringRe = regexp.MustCompile(`'[^']*'`)
)

// countNonIdempotentAlters 数出 body 里不带 IF (NOT) EXISTS 的 ADD/DROP 子句。
//
// PostgreSQL 给了幂等写法:ADD COLUMN IF NOT EXISTS / DROP COLUMN IF EXISTS。
// 不带它的 ADD/DROP 重跑会报 42701 / 42703,所以都算。ADD CONSTRAINT 没有幂等
// 写法,照样算 —— 它就该独占一个迁移文件。
func countNonIdempotentAlters(body string) int {
	body = sqlStringRe.ReplaceAllString(stripSQLComments(body), "''")

	n := 0
	for _, stmt := range strings.Split(body, ";") {
		if !alterStmtHeadRe.MatchString(stmt) {
			continue // CREATE TABLE、DROP TABLE、INSERT … 都不是 ALTER 的子句
		}
		stmt = columnAttrRe.ReplaceAllString(stmt, " ")
		for _, m := range alterClauseRe.FindAllStringSubmatch(stmt, -1) {
			if strings.EqualFold(m[1], "IF") {
				continue // `ADD IF NOT EXISTS c …`:\w+ 把 IF 吃掉了,后缀组落空,但它是幂等写法
			}
			if m[2] == "" { // 没有 IF (NOT) EXISTS —— 重跑就会报错
				n++
			}
		}
	}
	return n
}

// countNonIdempotentAlters 自己也要有测试。这条闸今天守着**零个**活的对象 ——
// baseline 里一条 ALTER 都没有(整份是 CREATE TABLE / CREATE INDEX IF NOT EXISTS),
// 所以「对着迁移目录跑一遍」永远是绿的,它证明不了这条闸还认得出缺陷。表驱动用例
// 是它此刻唯一的牙齿。
func TestCountNonIdempotentAlters(t *testing.T) {
	for _, c := range []struct {
		name string
		sql  string
		want int
	}{
		{"ADD COLUMN 带关键字", "ALTER TABLE t ADD COLUMN x TEXT;", 1},
		{"ADD 省略 COLUMN", "ALTER TABLE t ADD x TEXT;", 1},
		{"DROP 省略 COLUMN", "ALTER TABLE t DROP x;", 1},
		{"ADD COLUMN IF NOT EXISTS", "ALTER TABLE t ADD COLUMN IF NOT EXISTS x TEXT;", 0},
		{"DROP COLUMN IF EXISTS", "ALTER TABLE t DROP COLUMN IF EXISTS x;", 0},
		{"ADD CONSTRAINT 没有幂等写法", "ALTER TABLE t ADD CONSTRAINT c CHECK (n > 0);", 1},
		{"三条省略 COLUMN 的 ADD", "ALTER TABLE t ADD a TEXT;\nALTER TABLE t ADD b TEXT;\nALTER TABLE t ADD c TEXT;", 3},
		{"DROP TABLE 不是 ALTER 子句", "DROP TABLE IF EXISTS t;\nDROP TABLE u;", 0},
		{"CREATE TABLE 不该被抓到", "CREATE TABLE IF NOT EXISTS t (id BIGSERIAL PRIMARY KEY);", 0},

		// 列名撞上「独立语句的宾语」那批词。按关键字滤会把这些全放过去,而
		// type / schema / rule / policy 都是再普通不过的列名 —— database 更是
		// 这个项目自己用过的(tbl_schema_object.database,迁移 0040 才改名)。
		{"列名叫 type", "ALTER TABLE t ADD type TEXT;", 1},
		{"列名叫 schema", "ALTER TABLE t ADD schema TEXT;", 1},
		{"列名叫 rule", "ALTER TABLE t DROP rule;", 1},
		{"列名叫 database 且带幂等写法", "ALTER TABLE t ADD COLUMN IF NOT EXISTS database TEXT;", 0},

		// ALTER COLUMN … DROP DEFAULT / DROP NOT NULL 改的是已有列的属性,
		// 重跑无害,不该被记成非幂等。
		{"ALTER COLUMN DROP DEFAULT", "ALTER TABLE t ALTER COLUMN x DROP DEFAULT;", 0},
		{"ALTER COLUMN DROP NOT NULL", "ALTER TABLE t ALTER COLUMN x DROP NOT NULL;", 0},

		// 注释和字符串字面量里的 ADD/DROP 不是语句。
		{"注释里的 ADD COLUMN", "-- ALTER TABLE t ADD COLUMN x TEXT;\nCREATE TABLE IF NOT EXISTS u (id BIGINT);", 0},
		{"字符串字面量里的 ADD COLUMN", "INSERT INTO t (note) VALUES ('ALTER TABLE t ADD COLUMN x TEXT');", 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := countNonIdempotentAlters(c.sql); got != c.want {
				t.Errorf("countNonIdempotentAlters(%q) = %d, 期望 %d", c.sql, got, c.want)
			}
		})
	}
}

func TestMigrations_AltersAreIdempotentOrAlone(t *testing.T) {
	var offenders []string
	err := fs.WalkDir(migrations.FS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".sql") {
			return err
		}
		b, rerr := fs.ReadFile(migrations.FS, path)
		if rerr != nil {
			return rerr
		}
		// 注释行不算:它们常常在讲这个文件为什么这么写。
		var body strings.Builder
		for _, line := range strings.Split(string(b), "\n") {
			if t := strings.TrimSpace(line); !strings.HasPrefix(t, "--") {
				body.WriteString(line)
				body.WriteString("\n")
			}
		}
		n := countNonIdempotentAlters(body.String())
		if n > 1 {
			offenders = append(offenders,
				path+": 含 "+itoa(int64(n))+" 条非幂等 ALTER —— 中途失败后重跑会在第一条报 42701,"+
					"而库已经停在半成品状态。要么写成幂等的形式,要么一个文件只放一条")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk migrations: %v", err)
	}
	sort.Strings(offenders)
	if len(offenders) > 0 {
		t.Errorf("迁移里的非幂等 ALTER:\n%s", strings.Join(offenders, "\n"))
	}
}
