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

// alterClauseRe 抓 ALTER 的 ADD/DROP 子句,并记下它后面有没有跟 IF (NOT) EXISTS。
//
// PostgreSQL 给了幂等的写法:ADD COLUMN IF NOT EXISTS / DROP COLUMN IF EXISTS /
// CREATE INDEX IF NOT EXISTS —— 用它们就不会被这条正则抓到。不带 IF (NOT) EXISTS 的
// ADD/DROP 重跑就会报 42701 / 42703,所以它们都算。
var alterClauseRe = regexp.MustCompile(`(?i)\b(?:ADD|DROP)\s+(?:COLUMN|INDEX|KEY|CONSTRAINT|UNIQUE)\b(\s+IF\s+(?:NOT\s+)?EXISTS\b)?`)

// countNonIdempotentAlters 数出 body 里不带 IF (NOT) EXISTS 的 ADD/DROP 子句。
// Go 的 regexp 没有负向先行断言,所以把后缀一起匹配下来,再按捕获组筛掉幂等的那些。
func countNonIdempotentAlters(body string) int {
	n := 0
	for _, m := range alterClauseRe.FindAllStringSubmatch(body, -1) {
		if m[1] == "" { // 没有 IF (NOT) EXISTS —— 重跑就会报错
			n++
		}
	}
	return n
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
