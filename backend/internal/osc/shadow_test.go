package osc

import (
	"context"
	"database/sql"
	"sort"
	"testing"
)

// 影子表的契约只有两条,而它们是后面所有事情的地基:
//
//   - **列集合与原表一致**。拷贝是按列名搬的;影子表多一列或少一列,拷贝要么写不进去,
//     要么把数据写进错的列。
//   - **索引集合 = 原表 + 这次要加的那个**。这正是这次变更的全部内容 —— 多出来或漏掉
//     一个索引,意味着 cut-over 之后的表不是人要的那张表。
//
// 断言一律对着 information_schema 查真实结果,不看我们自己拼的那条 SQL —— 否则测的
// 是"我们打算做什么",不是"库里现在是什么"。

func columnsOf(t *testing.T, db *sql.DB, schema, table string) []string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `
		SELECT COLUMN_NAME FROM information_schema.COLUMNS
		 WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? ORDER BY ORDINAL_POSITION`, schema, table)
	if err != nil {
		t.Fatalf("读列失败: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatalf("读列行失败: %v", err)
		}
		out = append(out, c)
	}
	return out
}

func indexesOf(t *testing.T, db *sql.DB, schema, table string) []string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `
		SELECT DISTINCT INDEX_NAME FROM information_schema.STATISTICS
		 WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?`, schema, table)
	if err != nil {
		t.Fatalf("读索引失败: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("读索引行失败: %v", err)
		}
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestShadow_HasTheSameColumnsAndTheNewIndex(t *testing.T) {
	db := openMySQL(t)
	ctx := context.Background()
	name := makeTable(t, db, "CREATE TABLE `%s` (id BIGINT PRIMARY KEY, memo VARCHAR(64), amount INT)")

	sh, err := CreateShadow(ctx, db, "osc_test", name, "ADD INDEX idx_memo (memo)")
	if err != nil {
		t.Fatalf("建影子表失败: %v", err)
	}
	t.Cleanup(func() { _, _ = db.ExecContext(context.Background(), "DROP TABLE IF EXISTS `"+sh+"`") })

	// gh-ost 的命名约定,残留检查(LeftoverGhost)认的也是这个后缀。
	if want := "_" + name + "_gho"; sh != want {
		t.Errorf("影子表名 = %q,想要 %q", sh, want)
	}

	origCols := columnsOf(t, db, "osc_test", name)
	shadowCols := columnsOf(t, db, "osc_test", sh)
	if !sameStrings(origCols, shadowCols) {
		t.Errorf("列集合必须一致 —— 拷贝是按列名搬的\n原表:   %v\n影子表: %v", origCols, shadowCols)
	}

	// 索引集合 = 原表 + 这次要加的那个。期望值是手写出来的,不从被测代码推导。
	shadowIdx := indexesOf(t, db, "osc_test", sh)
	want := []string{"PRIMARY", "idx_memo"}
	sort.Strings(want)
	if !sameStrings(shadowIdx, want) {
		t.Errorf("影子表索引 = %v,想要 %v", shadowIdx, want)
	}

	// 原表一根汗毛都不能动 —— 这一步只是准备,还没到 cut-over。
	if got := indexesOf(t, db, "osc_test", name); !sameStrings(got, []string{"PRIMARY"}) {
		t.Errorf("原表索引被改了: %v —— 这一步不该碰原表", got)
	}
}

// 残留的影子表可能是上一次迁移跑到一半留下的,里面有别人还没看过的数据。
// 悄悄覆盖它 = 悄悄毁掉那些数据,所以这里必须停下来让人决定。
func TestShadow_RefusesToOverwriteALeftover(t *testing.T) {
	db := openMySQL(t)
	ctx := context.Background()
	name := makeTable(t, db, "CREATE TABLE `%s` (id BIGINT PRIMARY KEY, memo VARCHAR(64))")

	sh, err := CreateShadow(ctx, db, "osc_test", name, "ADD INDEX idx_memo (memo)")
	if err != nil {
		t.Fatalf("第一次建影子表就失败了: %v", err)
	}
	t.Cleanup(func() { _, _ = db.ExecContext(context.Background(), "DROP TABLE IF EXISTS `"+sh+"`") })

	// 往残留里放一行,用来证明它没有被悄悄重建。
	if _, err := db.ExecContext(ctx, "INSERT INTO `"+sh+"` (id, memo) VALUES (1, '上次留下的')"); err != nil {
		t.Fatalf("写残留数据失败: %v", err)
	}

	if _, err := CreateShadow(ctx, db, "osc_test", name, "ADD INDEX idx_memo (memo)"); err == nil {
		t.Fatal("影子表已存在时必须拒绝,不能覆盖")
	}

	var n int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM `"+sh+"`").Scan(&n); err != nil {
		t.Fatalf("读残留失败: %v", err)
	}
	if n != 1 {
		t.Errorf("残留里的数据被动过了(行数=%d,应为 1)", n)
	}
}

// ALTER 写错时不能留下一张半成品影子表 —— 下一次重试会撞上自己刚留下的残留,
// 而人看到的是"影子表已存在",完全指不到真正的原因。
func TestShadow_CleansUpWhenTheAlterIsRejected(t *testing.T) {
	db := openMySQL(t)
	ctx := context.Background()
	name := makeTable(t, db, "CREATE TABLE `%s` (id BIGINT PRIMARY KEY, memo VARCHAR(64))")

	// 对着一个不存在的列加索引。
	if _, err := CreateShadow(ctx, db, "osc_test", name, "ADD INDEX idx_nope (no_such_column)"); err == nil {
		t.Fatal("ALTER 指向不存在的列,本该失败")
	}

	exists, err := tableExists(ctx, db, "osc_test", ShadowName(name))
	if err != nil {
		t.Fatalf("查影子表失败: %v", err)
	}
	if exists {
		_, _ = db.ExecContext(ctx, "DROP TABLE IF EXISTS `"+ShadowName(name)+"`")
		t.Error("ALTER 失败后影子表应当被收走,否则重试时会撞上自己留下的残留")
	}
}
