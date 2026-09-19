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
	// I1:剥掉的前缀不能凭空消失 —— Decide 要靠它判断这条语句实际指向的库和发布单
	// 目标库是不是同一个,剥了不留痕就没法做这个判断了。
	if got.Schema != "app" {
		t.Errorf("Schema 是 %q,期望 app —— 剥下来的前缀丢了", got.Schema)
	}
}

func TestParseIndexDDL_SchemaIsEmptyWhenThereIsNoPrefix(t *testing.T) {
	got, ok := ParseIndexDDL("ALTER TABLE t_order ADD INDEX idx_memo (memo)")
	if !ok {
		t.Fatal("没认出最常见的那一种写法")
	}
	if got.Schema != "" {
		t.Errorf("没有前缀的语句,Schema 却是 %q", got.Schema)
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

func TestParseIndexDDL_RejectsTwoIndexClausesInOneAlter(t *testing.T) {
	// 两条各自合法的索引子句并在一条 ALTER 里。OSC 的 StartRequest 一次只收一个
	// alter 子句,整串交过去,第二条会被当成第一条的一部分。
	//
	// **这条用例是 isSingleIndexClause 的括号计数唯一的守卫。**(该函数原名
	// insideParens,后来因为要一并处理字符串字面量而改名。)隔壁那条混合子句的
	// 用例(ADD COLUMN c INT, ADD INDEX i (c))在正则阶段就失配了,走不到这个函数——
	// 去掉括号计数那段,它照样绿。
	if _, ok := ParseIndexDDL("ALTER TABLE t_order ADD INDEX i1 (a), ADD INDEX i2 (b)"); ok {
		t.Error("认下了一条带两个索引子句的 ALTER")
	}
}

func TestParseIndexDDL_AcceptsACompositeIndex(t *testing.T) {
	// 括号**里**的逗号是列的分隔,不是子句的分隔。分不开这两者的话,最常见的
	// 复合索引就再也走不到 OSC 了 —— 而那种静默的退化没有人会发现。
	got, ok := ParseIndexDDL("ALTER TABLE t_order ADD INDEX idx_ab (a, b)")
	if !ok {
		t.Fatal("没认出复合索引 —— 括号里的逗号被当成了子句分隔")
	}
	if got.Alter != "ADD INDEX idx_ab (a, b)" {
		t.Errorf("alter 子句是 %q,期望 ADD INDEX idx_ab (a, b)", got.Alter)
	}
}

func TestParseIndexDDL_RefusesWhenAClauseCarriesAStringLiteral(t *testing.T) {
	// 字符串字面量里的括号会骗过括号计数:'see (spec' 里那个裸括号让深度永久偏移,
	// 后面真正分隔子句的顶层逗号就被当成"在括号里",于是整条混合 ALTER 被认下来。
	//
	// 不去写引号状态机(那是这个包明确不要的复杂度),而是**看见引号就不认** ——
	// 带 COMMENT 的索引变更从此照常直发,那是安全的那一边。
	if _, ok := ParseIndexDDL(
		"ALTER TABLE t_order ADD INDEX idx_memo (memo) COMMENT 'see (spec', ADD COLUMN evil INT"); ok {
		t.Error("字符串字面量里的括号骗过了子句计数,混合 ALTER 被当成纯索引变更")
	}
}

func TestParseIndexDDL_StillAcceptsBacktickedColumns(t *testing.T) {
	// 反引号是标识符,不是字符串字面量,而用反引号包列名是最常见的写法 ——
	// 把它和单引号一起拒掉,等于把大多数真实的 DDL 挡在外面。
	got, ok := ParseIndexDDL("ALTER TABLE `t_order` ADD INDEX `idx_memo` (`memo`)")
	if !ok {
		t.Fatal("拒掉了用反引号包标识符的写法")
	}
	if got.Table != "t_order" {
		t.Errorf("表名认成了 %q", got.Table)
	}
}

func TestParseIndexDDL_KeepsADottedIndexNameIntact(t *testing.T) {
	// 剥库名前缀是给**表名**准备的。同一把剪刀用在索引名上,交给 OSC 的名字就和
	// 库里真实的索引名对不上 —— DROP 找错对象、CREATE 建出一个改了名的索引,
	// 而且一声不响。
	got, ok := ParseIndexDDL("DROP INDEX `idx.v2` ON t_order")
	if !ok {
		t.Fatal("没认出 DROP INDEX")
	}
	if got.Alter != "DROP INDEX idx.v2" {
		t.Errorf("alter 子句是 %q,索引名被截断了", got.Alter)
	}
}
