package bootstrap

// 一个文件里的 ALTER,要么**每一条都幂等**,要么只有一条。
//
// 迁移中途失败**没有事务**(见 Migrate:一条一条 Exec,失败就整个函数返回),而失败之后
// 运维能做的只有重跑 —— 那时整个文件会从第一条开始再走一遍。
//
// 于是一个含七条 `ADD COLUMN` 的文件,只要在第五条上炸了(网络、锁等待、列名撞保留字),
// 重跑必然在第一条就报 1060 Duplicate column name:前四条已经加上了。运维看到的是一个
// **完全不同的错误**,而真正的原因在上一次的日志里。库停在半成品状态,而唯一的出路是
// 手工把已经执行过的那几条挑出来删掉。
//
// ADR 0016 §三因此要求:要么写成幂等的形式(MODIFY / CREATE TABLE IF NOT EXISTS /
// CREATE INDEX IF NOT EXISTS 这些重复执行无害的),要么一个文件只放一条 ALTER —— 那样
// 重跑就是从头再来一次,没有"前面几条已经生效"的中间态。
//
// 这条闸**只拦新文件**。历史上那几个(0013 / 0019 / 0020 / 0025 / 0026 / 0029 / 0033)
// 不改:它们在所有已部署的库上都跑过了,改它们不会让任何一台机器变好,只会让"这个文件
// 和当年跑过的那个不是同一个"。它们列在下面的豁免表里,**连同它们各自有几条**,所以
// 往里面加一条也会让这条用例变红。

import (
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"testing"

	"velagateway/migrations"
)

// 历史欠账:文件名 → 它含有的非幂等 ALTER 条数。
//
// 这张表是**现状的快照**,不是某个独立推导出来的期望值 —— 它的作用是把已经欠下的账
// 冻住:新文件一条都不许有,旧文件也不许再长。数字写死正是为了这个,往任何一行里加一条
// 都会让这条用例变红。
//
// 为什么不改这些历史文件:它们在所有已部署的库上都跑过了。改它们不会让任何一台机器变
// 好,只会让「这个文件」和「当年在生产上跑过的那个」不是同一个东西 —— 而迁移的全部价值
// 就建立在这两者相同上。DEPLOY.md 里记着它们中途失败时的手工处理步骤。
var nonIdempotentLegacy = map[string]int{
	"0013_dual_snapshot.sql":             3,
	"0015_approval_script_ref.sql":       2,
	"0019_pipeline_sqlreview.sql":        2,
	"0020_api_client.sql":                7,
	"0025_project.sql":                   3,
	"0026_review_spec_provenance.sql":    2,
	"0029_sensitive_export_approval.sql": 4,
	"0033_exec_window_approval.sql":      5,
}

// nonIdempotentAlterRe 抓重复执行会报错的那几种 ALTER 子句。
//
// MODIFY / CHANGE 不在其中:把一列改成它已经是的样子,MySQL 不报错。
// DROP COLUMN 也不在:它重复执行会报 1091,但 DROP 在迁移里本就罕见,真要用时同样该
// 独占一个文件 —— 所以它也算。
var nonIdempotentAlterRe = regexp.MustCompile(`(?i)\b(ADD\s+COLUMN|ADD\s+INDEX|ADD\s+KEY|ADD\s+CONSTRAINT|ADD\s+UNIQUE|DROP\s+COLUMN|DROP\s+INDEX)\b`)

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
		n := len(nonIdempotentAlterRe.FindAllString(body.String(), -1))
		if legacy, ok := nonIdempotentLegacy[path]; ok {
			if n != legacy {
				offenders = append(offenders,
					path+": 豁免表记着 "+itoa(int64(legacy))+" 条,现在是 "+itoa(int64(n))+
						" 条 —— 历史迁移不该被改动;新增的话请改用幂等写法")
			}
			return nil
		}
		if n > 1 {
			offenders = append(offenders,
				path+": 含 "+itoa(int64(n))+" 条非幂等 ALTER —— 中途失败后重跑会在第一条报 1060,"+
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

// 豁免表只能变短。往里面加名字等于给新的欠账开门。
func TestMigrations_LegacyExemptionsDoNotGrow(t *testing.T) {
	const was = 8
	if len(nonIdempotentLegacy) > was {
		t.Errorf("非幂等迁移的豁免表从 %d 条涨到了 %d 条 —— 这张表只该变短", was, len(nonIdempotentLegacy))
	}
}
