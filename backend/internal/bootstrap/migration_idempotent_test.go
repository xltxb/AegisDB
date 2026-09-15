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

// alterClauseRe 抓 ALTER 的 ADD/DROP 子句:第 1 组是紧跟 ADD/DROP 的那个词,第 2 组是
// 它后面可选的 IF (NOT) EXISTS。
//
// PostgreSQL 给了幂等的写法:ADD COLUMN IF NOT EXISTS / DROP COLUMN IF EXISTS /
// CREATE INDEX IF NOT EXISTS —— 用它们就不会被算进来。不带 IF (NOT) EXISTS 的
// ADD/DROP 重跑就会报 42701 / 42703,所以它们都算。
//
// **COLUMN 是可省略的**,和隔壁 migration_reserved_words_test.go 的 addColumnRe 同理:
// `ALTER TABLE t ADD a TEXT` / `ALTER TABLE t DROP a` 都是合法的 PG。从前这条正则要求
// 字面量 COLUMN,于是省了它的写法一条都抓不到 —— 那恰恰是最容易随手写出来的形式,
// 而这条闸会对着它绿着。所以这里把关键字整个做成可选的,代价是 ADD/DROP 后面跟什么
// 都会先被抓下来,由 standaloneObjects 在下面滤掉。
var alterClauseRe = regexp.MustCompile(`(?i)\b(?:ADD|DROP)\s+(\w+)(\s+IF\s+(?:NOT\s+)?EXISTS\b)?`)

// standaloneObjects:跟在 DROP/ADD 后面的这些词开的是**独立语句**(DROP TABLE、
// DROP VIEW、DROP TYPE …),不是 ALTER TABLE 的子句。放开 COLUMN 之后它们会被上面
// 那条正则一并抓到,在这里滤掉 —— 否则一句无害的 `DROP TABLE IF EXISTS` 也会被记成
// 一条非幂等 ALTER。
//
// INDEX / CONSTRAINT / KEY / UNIQUE **不在**这张表里:它们既可能是 ALTER 的子句,
// 独立的 `DROP INDEX x` 重跑一样会报错,两种身份都该被这条闸管。
var standaloneObjects = map[string]bool{
	"TABLE": true, "VIEW": true, "MATERIALIZED": true, "SCHEMA": true, "DATABASE": true,
	"TYPE": true, "SEQUENCE": true, "TRIGGER": true, "FUNCTION": true, "PROCEDURE": true,
	"EXTENSION": true, "POLICY": true, "RULE": true, "OWNED": true,
}

// countNonIdempotentAlters 数出 body 里不带 IF (NOT) EXISTS 的 ADD/DROP 子句。
// Go 的 regexp 没有负向先行断言,所以把后缀一起匹配下来,再按捕获组筛掉幂等的那些。
func countNonIdempotentAlters(body string) int {
	n := 0
	for _, m := range alterClauseRe.FindAllStringSubmatch(body, -1) {
		word := strings.ToUpper(m[1])
		if standaloneObjects[word] {
			continue // DROP TABLE / DROP TYPE … —— 不是 ALTER 的子句
		}
		if word == "IF" {
			continue // `ADD IF NOT EXISTS c …`:\w+ 把 IF 吃掉了,后缀组落空,但它是幂等写法
		}
		if m[2] == "" { // 没有 IF (NOT) EXISTS —— 重跑就会报错
			n++
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
