package bootstrap

// 建表必须显式声明 COLLATE。
//
// 这条规则是被一次生产故障换来的:
//
//   Error 1267 (HY000): Illegal mix of collations
//   (utf8mb4_unicode_ci,IMPLICIT) and (utf8mb4_0900_ai_ci,IMPLICIT) for operation '='
//
// 成因是 0001_init.sql 建表时只写了 `DEFAULT CHARSET=utf8mb4`。库本身建成了
// utf8mb4_unicode_ci,但 MySQL 在这里**不继承库的排序规则** —— 只给字符集不给排序
// 规则时,用的是该字符集的默认排序规则,MySQL 8 上是 utf8mb4_0900_ai_ci。于是 0001
// 那批表与后来显式写了 COLLATE 的表分属两种排序规则,同一个键跨表一 JOIN 就是 1267。
//
// 为什么值得单独一条测试(理由和隔壁 TestMigrations_AlterTargetsExistingTables 同源):
//
//   - 它**不报任何错**。建表成功、插入成功、查询也成功 —— 直到某天有人写下第一条
//     跨这两批表的 JOIN。
//   - 开发环境**永远碰不到**。那边是 SQLite,根本没有排序规则这回事,所以整套测试
//     跑绿也说明不了什么。
//   - 炸出来的时候还很安静。这次是 CountProdInterceptions,它的错误在 handler 里被
//     `_` 吞掉,界面上只是"拦截次数 0" —— 没有人会把一个 0 当成故障。
//
// 所以只能在**写下 SQL 的那一刻**挡住。

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"

	"velagateway/migrations"
)

// createTableOptsRe 抓一条 CREATE TABLE 的表名,以及它结尾的表选项
// (`) ENGINE=InnoDB DEFAULT CHARSET=… COLLATE=…;` 那一段)。
var createTableOptsRe = regexp.MustCompile(`(?is)CREATE TABLE(?:\s+IF NOT EXISTS)?\s+` + "`?" + `(\w+)` + "`?" + `\s*\(.*?\n\)([^;]*);`)

// 0001_init.sql 是这条规则的**成因**,不是它的例外。
//
// 它早已在每一套环境上执行过,改这个文件不会让任何已存在的表变一个字节 —— 真正修它
// 的是 0034_collation_align.sql(把承载环境/分层代码的那几列对齐)。名单留在这里,
// 是为了让豁免只有一条、且写着为什么。**新文件不得进入这个名单。**
var collationExempt = map[string]bool{"0001_init.sql": true}

// stripSQLComments 去掉 `--` 注释行:说明文字里出现 CREATE TABLE / COLLATE 都不算数。
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

func TestMigrationsDeclareCollation(t *testing.T) {
	seen := 0
	err := fs.WalkDir(migrations.FS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".sql") {
			return err
		}
		if collationExempt[path] {
			return nil
		}
		b, rerr := fs.ReadFile(migrations.FS, path)
		if rerr != nil {
			return rerr
		}
		for _, m := range createTableOptsRe.FindAllStringSubmatch(stripSQLComments(string(b)), -1) {
			seen++
			table, opts := m[1], m[2]
			if !strings.Contains(strings.ToUpper(opts), "COLLATE") {
				t.Errorf("%s 的 %s 没有显式声明 COLLATE。\n"+
					"    只写 CHARSET 时 MySQL 用的是字符集的默认排序规则(8.0 上是 utf8mb4_0900_ai_ci),\n"+
					"    而本库其余的表是 utf8mb4_unicode_ci —— 两者一 JOIN 就是 ERROR 1267,\n"+
					"    而且只会在生产上炸(开发环境是 SQLite,没有排序规则这回事)。\n"+
					"    请写成 `DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`。当前表选项:%s",
					path, table, strings.TrimSpace(opts))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk migrations: %v", err)
	}
	// 正则一旦失配就会静静地检查 0 张表,然后这条测试永远绿着 —— 那比没有更糟。
	if seen == 0 {
		t.Fatal("一张建表语句都没解析到,这条守卫等于没生效")
	}
}

// 豁免名单只该有 0001 这一条。多一条就说明有人把新表塞了进去,而那正是这条规则要挡的。
func TestCollationExemptionStaysAtOne(t *testing.T) {
	if len(collationExempt) != 1 || !collationExempt["0001_init.sql"] {
		t.Errorf("豁免名单被动过了:%v —— 新迁移必须显式写 COLLATE,不能进名单", collationExempt)
	}
}

// 0034 必须真的把那三列对齐 —— 它是这次生产故障的修复本身。
//
// 钉住列名而不只是钉住文件存在:这三列各有来历(tier_code 是 0016 从 env 改名来的),
// 少改一列就还剩一半会炸,而剩下的那一半同样不会有任何报错。
func TestCollationMigrationCoversTheJoinedColumns(t *testing.T) {
	b, err := fs.ReadFile(migrations.FS, "0034_collation_align.sql")
	if err != nil {
		t.Fatalf("read 0034: %v", err)
	}
	sql := stripSQLComments(string(b))
	for _, want := range []string{
		"tbl_connection",      // 报错的那一半:env = tbl_environment.code
		"tbl_role_capability", // 同一个键(能力矩阵按分层存)
		"tbl_risk_command",    // 同一个键(高危字典按分层存)
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("0034 没有覆盖 %s", want)
		}
	}
	if n := strings.Count(strings.ToUpper(sql), "COLLATE UTF8MB4_UNICODE_CI"); n < 3 {
		t.Errorf("三列都要显式对齐到 utf8mb4_unicode_ci,实际只有 %d 处", n)
	}
}
