package oscroute

import "testing"

// 这一层回答一个问题:这条语句是不是"一次纯粹的索引变更",如果是,交给 OSC 的
// alter 子句长什么样。
//
// 认错的代价有限 —— 顶多是走了或没走 OSC,而 OSC 自己的 Preflight 会对着真实例
// 再拒一次。所以这里用受限的模式匹配,不引入 SQL parser。但**认得太宽**是有代价的:
// 把一条改列语句交给 OSC,那次变更会在 Preflight 那里失败,而人看到的是一张失败的
// 发布单,不是"这条语句不该走这条路"。

func TestParseIndexDDL_AlterAddIndex(t *testing.T) {
	got, ok := ParseIndexDDL("ALTER TABLE t_order ADD INDEX idx_memo (memo)")
	if !ok {
		t.Fatal("没认出最常见的那一种写法")
	}
	if got.Table != "t_order" {
		t.Errorf("表名认成了 %q,期望 t_order", got.Table)
	}
	if got.Alter != "ADD INDEX idx_memo (memo)" {
		t.Errorf("alter 子句是 %q,期望 ADD INDEX idx_memo (memo)", got.Alter)
	}
}

func TestParseIndexDDL_AlterDropIndex(t *testing.T) {
	got, ok := ParseIndexDDL("ALTER TABLE `t_order` DROP KEY idx_memo")
	if !ok {
		t.Fatal("没认出 DROP KEY")
	}
	if got.Table != "t_order" {
		t.Errorf("反引号没有被去掉:表名是 %q", got.Table)
	}
	if got.Alter != "DROP KEY idx_memo" {
		t.Errorf("alter 子句是 %q", got.Alter)
	}
}

func TestParseIndexDDL_TranslatesCreateIndex(t *testing.T) {
	// CREATE INDEX 是很常见的写法。不翻译的话它永远走不到 OSC —— 而"静默地
	// 走不到"比"明确不支持"更糟:没有人会发现。
	got, ok := ParseIndexDDL("CREATE INDEX idx_memo ON t_order (memo)")
	if !ok {
		t.Fatal("没认出 CREATE INDEX")
	}
	if got.Table != "t_order" {
		t.Errorf("表名认成了 %q", got.Table)
	}
	if got.Alter != "ADD INDEX idx_memo (memo)" {
		t.Errorf("翻译成了 %q,期望 ADD INDEX idx_memo (memo)", got.Alter)
	}
}

func TestParseIndexDDL_TranslatesCreateUniqueIndex(t *testing.T) {
	got, ok := ParseIndexDDL("CREATE UNIQUE INDEX uk_no ON t_order (no)")
	if !ok {
		t.Fatal("没认出 CREATE UNIQUE INDEX")
	}
	if got.Alter != "ADD UNIQUE INDEX uk_no (no)" {
		t.Errorf("翻译成了 %q,期望 ADD UNIQUE INDEX uk_no (no)", got.Alter)
	}
}

func TestParseIndexDDL_TranslatesDropIndexOn(t *testing.T) {
	got, ok := ParseIndexDDL("DROP INDEX idx_memo ON t_order")
	if !ok {
		t.Fatal("没认出 DROP INDEX ... ON")
	}
	if got.Table != "t_order" || got.Alter != "DROP INDEX idx_memo" {
		t.Errorf("认成了 %+v", got)
	}
}

func TestParseIndexDDL_StripsSchemaPrefix(t *testing.T) {
	// OSC 的 StartRequest 分开收 schema 和 table,schema 来自发布单的目标库。
	// 表名里带着库名前缀会拼出 `app`.`app.t_order` 这样的东西。
	got, ok := ParseIndexDDL("ALTER TABLE app.t_order ADD INDEX idx_memo (memo)")
	if !ok {
		t.Fatal("没认出带库名前缀的写法")
	}
	if got.Table != "t_order" {
		t.Errorf("表名是 %q,库名前缀没有被剥掉", got.Table)
	}
}

func TestParseIndexDDL_RejectsMixedClauses(t *testing.T) {
	// 混合子句超出 OSC 的能力范围(ADR 0011 刻意收窄到只做索引)。认下来的话,
	// 这次变更会在 Preflight 那里失败,而人看到的是一张失败的发布单。
	if _, ok := ParseIndexDDL("ALTER TABLE t_order ADD COLUMN c INT, ADD INDEX i (c)"); ok {
		t.Error("认下了一条混合子句的 ALTER —— 它不是纯粹的索引变更")
	}
}

func TestParseIndexDDL_RejectsNonIndexDDL(t *testing.T) {
	for _, q := range []string{
		"ALTER TABLE t_order MODIFY COLUMN memo VARCHAR(128)",
		"ALTER TABLE t_order ADD COLUMN memo VARCHAR(64)",
		"ALTER TABLE t_order ENGINE=InnoDB",
		"UPDATE t_order SET memo = 'x'",
		"CREATE TABLE t (id INT)",
		"DROP TABLE t_order",
		"",
	} {
		if _, ok := ParseIndexDDL(q); ok {
			t.Errorf("认下了一条不是索引变更的语句: %q", q)
		}
	}
}

func TestParseIndexDDL_RejectsAddPrimaryKey(t *testing.T) {
	// 加主键不是加二级索引:它会重建整张表的聚簇索引,而 OSC 的影子表方案对它
	// 有另一套语义要考虑(空值、去重)。不在这次的范围里。
	if _, ok := ParseIndexDDL("ALTER TABLE t_order ADD PRIMARY KEY (id)"); ok {
		t.Error("认下了 ADD PRIMARY KEY")
	}
}
