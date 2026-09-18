package osc

import (
	"context"
	"fmt"
	"testing"
)

// 分块拷贝把存量行搬进影子表。它与 binlog 重放是**同时**在跑的,两者会交错,
// 收敛靠的是 ADR 0011 里那条不变式:
//
//     拷贝用 INSERT IGNORE —— 永不覆盖
//     重放用 REPLACE INTO  —— 永远覆盖
//
// 这条不变式是整件事的正确性所在:一行若已被重放写成新值,拷贝读到的旧值绝不能
// 把它盖回去。下面最后一条用例单独钉住它。

// 拷完之后,两表行数一致、内容一致。
func TestCopy_MovesEveryRow(t *testing.T) {
	db := openMySQL(t)
	ctx := context.Background()
	name := makeTable(t, db, "CREATE TABLE `%s` (id BIGINT PRIMARY KEY, memo VARCHAR(64))")

	const total = 250
	for i := 1; i <= total; i++ {
		if _, err := db.ExecContext(ctx,
			"INSERT INTO `"+name+"` (id, memo) VALUES (?, ?)", i, fmt.Sprintf("row-%d", i)); err != nil {
			t.Fatalf("灌数据失败: %v", err)
		}
	}

	sh, err := CreateShadow(ctx, db, "osc_test", name, "ADD INDEX idx_memo (memo)")
	if err != nil {
		t.Fatalf("建影子表失败: %v", err)
	}
	t.Cleanup(func() { _, _ = db.ExecContext(context.Background(), "DROP TABLE IF EXISTS `"+sh+"`") })

	// 分块大小故意小于总行数,逼出多轮 —— 一轮就搬完的话,分块逻辑等于没测。
	res, err := CopyAll(ctx, db, "osc_test", name, sh, CopyOptions{ChunkSize: 60})
	if err != nil {
		t.Fatalf("拷贝失败: %v", err)
	}
	if res.Copied != total {
		t.Errorf("拷了 %d 行,原表有 %d 行", res.Copied, total)
	}

	var n int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM `"+sh+"`").Scan(&n); err != nil {
		t.Fatalf("数影子表行数失败: %v", err)
	}
	if n != total {
		t.Errorf("影子表 %d 行,原表 %d 行", n, total)
	}

	// 行数一致还不够 —— 内容也要一致。用一条对比查询找出任何不匹配的行。
	var mismatch int
	err = db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT COUNT(*) FROM `+"`%s`"+` o
		  LEFT JOIN `+"`%s`"+` s ON s.id = o.id
		 WHERE s.id IS NULL OR s.memo <> o.memo`, name, sh)).Scan(&mismatch)
	if err != nil {
		t.Fatalf("对比失败: %v", err)
	}
	if mismatch != 0 {
		t.Errorf("有 %d 行内容与原表对不上", mismatch)
	}
}

// 这条是整件事的正确性所在。
//
// 场景:某一行已经被 binlog 重放写成了新值(REPLACE INTO),而分块拷贝随后才走到
// 这个主键 —— 它读到的是原表里那份可能已经过时的值。拷贝**必须让步**,否则会把
// 一次已经发生的更新悄悄回滚掉,而且没有任何报错。
func TestCopy_NeverOverwritesWhatReplayAlreadyWrote(t *testing.T) {
	db := openMySQL(t)
	ctx := context.Background()
	name := makeTable(t, db, "CREATE TABLE `%s` (id BIGINT PRIMARY KEY, memo VARCHAR(64))")

	for i := 1; i <= 10; i++ {
		if _, err := db.ExecContext(ctx,
			"INSERT INTO `"+name+"` (id, memo) VALUES (?, ?)", i, "旧值"); err != nil {
			t.Fatalf("灌数据失败: %v", err)
		}
	}
	sh, err := CreateShadow(ctx, db, "osc_test", name, "ADD INDEX idx_memo (memo)")
	if err != nil {
		t.Fatalf("建影子表失败: %v", err)
	}
	t.Cleanup(func() { _, _ = db.ExecContext(context.Background(), "DROP TABLE IF EXISTS `"+sh+"`") })

	// 模拟重放已经把 id=5 写成了新值(重放用的就是 REPLACE INTO)。
	if _, err := db.ExecContext(ctx,
		"REPLACE INTO `"+sh+"` (id, memo) VALUES (5, '重放写的新值')"); err != nil {
		t.Fatalf("模拟重放失败: %v", err)
	}

	if _, err := CopyAll(ctx, db, "osc_test", name, sh, CopyOptions{ChunkSize: 100}); err != nil {
		t.Fatalf("拷贝失败: %v", err)
	}

	var memo string
	if err := db.QueryRowContext(ctx, "SELECT memo FROM `"+sh+"` WHERE id = 5").Scan(&memo); err != nil {
		t.Fatalf("读 id=5 失败: %v", err)
	}
	if memo != "重放写的新值" {
		t.Errorf("id=5 的值被拷贝盖回成了 %q —— 拷贝必须 INSERT IGNORE,永不覆盖重放写下的值", memo)
	}

	// 其余各行照常搬过来。
	var n int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM `"+sh+"`").Scan(&n); err != nil {
		t.Fatalf("数行数失败: %v", err)
	}
	if n != 10 {
		t.Errorf("影子表 %d 行,应为 10", n)
	}
}
