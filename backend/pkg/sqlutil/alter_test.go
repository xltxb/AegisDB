package sqlutil

// ALTER TABLE 的拆解。
//
// 和 refs_test.go 同一个重点:**认错的方向**。这里的产出会变成发布单上一句
// "字段 c 已存在"或"字段 c 不存在",而那两句话都会让人去改脚本。所以:
//
//   认出来 → 列名必须是语句里真写的那个,三个信号(NOT NULL / 有无默认值 / 新旧名)不能错;
//   认不出 → 整条不认,调用方不下任何结论。
//
// 只要一个子句没认出来就整条不认:一条 ALTER 里的子句一起成败,报一半结论会让人
// 以为另一半没问题。

import "testing"

func TestAlterOps_AddColumnCarriesItsTwoSignals(t *testing.T) {
	cases := []struct {
		sql        string
		col        string
		notNull    bool
		hasDefault bool
	}{
		{`ALTER TABLE t_order ADD COLUMN memo VARCHAR(64)`, "memo", false, false},
		{`ALTER TABLE t_order ADD memo VARCHAR(64)`, "memo", false, false},
		{`ALTER TABLE t_order ADD COLUMN n INT NOT NULL`, "n", true, false},
		{`ALTER TABLE t_order ADD COLUMN n INT NOT NULL DEFAULT 0`, "n", true, true},
		{"ALTER TABLE `t order` ADD COLUMN `my col` INT", "my col", false, false},
		{`ALTER TABLE t_order ADD (n NUMBER NOT NULL)`, "n", true, false}, // Oracle
	}
	for _, c := range cases {
		tb, ops, ok := AlterOps(c.sql)
		if !ok || len(ops) != 1 {
			t.Errorf("%s:没认出来 (ok=%v ops=%d)", c.sql, ok, len(ops))
			continue
		}
		if tb.Name == "" {
			t.Errorf("%s:表名没取到", c.sql)
		}
		op := ops[0]
		if op.Kind != AlterAddColumn || op.Column != c.col {
			t.Errorf("%s:得到 %s/%q", c.sql, op.Kind, op.Column)
		}
		if op.NotNull != c.notNull || op.HasDefault != c.hasDefault {
			t.Errorf("%s:notNull=%v hasDefault=%v,期望 %v/%v", c.sql, op.NotNull, op.HasDefault, c.notNull, c.hasDefault)
		}
	}
}

func TestAlterOps_DropModifyChange(t *testing.T) {
	cases := []struct {
		sql  string
		kind AlterKind
		col  string
		nw   string
	}{
		{`ALTER TABLE t DROP COLUMN memo`, AlterDropColumn, "memo", ""},
		{`ALTER TABLE t DROP memo`, AlterDropColumn, "memo", ""},
		{`ALTER TABLE t MODIFY COLUMN n INT NOT NULL`, AlterModifyColumn, "n", ""},
		{`ALTER TABLE t MODIFY (n NUMBER NOT NULL)`, AlterModifyColumn, "n", ""},
		{`ALTER TABLE t CHANGE COLUMN old_n new_n INT`, AlterChangeColumn, "old_n", "new_n"},
		{`ALTER TABLE t ALTER COLUMN n SET NOT NULL`, AlterSetNotNull, "n", ""},
		{`ALTER TABLE t ALTER n SET NOT NULL`, AlterSetNotNull, "n", ""},
	}
	for _, c := range cases {
		_, ops, ok := AlterOps(c.sql)
		if !ok || len(ops) != 1 {
			t.Errorf("%s:没认出来", c.sql)
			continue
		}
		if ops[0].Kind != c.kind || ops[0].Column != c.col || ops[0].NewColumn != c.nw {
			t.Errorf("%s:得到 %+v", c.sql, ops[0])
		}
	}
}

// 唯一约束要取出它涉及的列 —— 探重复值时要按这些列分组。
func TestAlterOps_UniqueConstraintCollectsItsColumns(t *testing.T) {
	for _, sql := range []string{
		`ALTER TABLE t ADD UNIQUE (a, b)`,
		`ALTER TABLE t ADD UNIQUE KEY uk_ab (a, b)`,
		`ALTER TABLE t ADD CONSTRAINT uk_ab UNIQUE (a, b)`,
	} {
		_, ops, ok := AlterOps(sql)
		if !ok || len(ops) != 1 {
			t.Errorf("%s:没认出来", sql)
			continue
		}
		if ops[0].Kind != AlterAddUnique || len(ops[0].Columns) != 2 ||
			ops[0].Columns[0] != "a" || ops[0].Columns[1] != "b" {
			t.Errorf("%s:得到 %+v", sql, ops[0])
		}
	}
}

// 顶层逗号才分子句;括号里的逗号是列表。
func TestAlterOps_MultipleClauses(t *testing.T) {
	_, ops, ok := AlterOps(`ALTER TABLE t ADD COLUMN a INT, ADD COLUMN b INT NOT NULL, DROP COLUMN c`)
	if !ok || len(ops) != 3 {
		t.Fatalf("应当是 3 条,得到 ok=%v ops=%+v", ok, ops)
	}
	if ops[1].Column != "b" || !ops[1].NotNull || ops[2].Kind != AlterDropColumn {
		t.Errorf("子句错位:%+v", ops)
	}
}

// 拿不准的一律不认。每一条都是"猜一下就会报出一句会让人改脚本的话"的地方。
func TestAlterOps_UnsureShapesAreNotGuessed(t *testing.T) {
	cases := []string{
		`ALTER TABLE t ADD CONSTRAINT fk FOREIGN KEY (a) REFERENCES p(id)`, // 外键:探测代价另说
		`ALTER TABLE t ADD INDEX idx_a (a)`,                               // 普通索引不会因数据失败
		`ALTER TABLE t ADD PRIMARY KEY (a)`,
		`ALTER TABLE t ADD COLUMN IF NOT EXISTS a INT`, // 幂等写法,已存在也不报错
		`ALTER TABLE t DROP COLUMN IF EXISTS a`,        // 同上
		`ALTER TABLE t ALTER COLUMN a TYPE bigint`,     // 改类型宽窄:一条便宜的探测答不了
		`ALTER TABLE t ALTER COLUMN a DROP NOT NULL`,   // 放松约束,不会因数据失败
		`ALTER TABLE t ADD (a NUMBER, b NUMBER)`,       // Oracle 多列括号写法
		`ALTER TABLE t DROP PARTITION p1`,
		`ALTER TABLE t RENAME TO t2`,
		`ALTER TABLE t ADD COLUMN a INT, ADD INDEX idx (a)`, // 有一个子句不认 → 整条不认
	}
	for _, sql := range cases {
		if _, ops, ok := AlterOps(sql); ok {
			t.Errorf("%s:不该认出来,却给了 %+v", sql, ops)
		}
	}
}

// 不是 ALTER TABLE 的语句一概不认。
func TestAlterOps_OnlyAlterTable(t *testing.T) {
	for _, sql := range []string{
		`CREATE TABLE t (id INT)`,
		`ALTER SESSION SET current_schema = app`,
		`ALTER INDEX idx REBUILD`,
		`UPDATE t SET a = 1`,
	} {
		if _, _, ok := AlterOps(sql); ok {
			t.Errorf("%s:不该认出来", sql)
		}
	}
}
