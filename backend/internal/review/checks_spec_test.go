package review

// 规范派生规则的回归。
//
// 每个用例都写成一对:**该报的报**、**不该报的不报**。第二半是这里真正的价值 ——
// 一条只测"能命中"的规则,可以靠 `return ["violation"]` 通过测试;而一条老是误报的
// 拦截规则,换来的是所有人学会无视整个审查,比没有这条规则更糟。

import (
	"strings"
	"testing"
)

// fireOne 跑一条内置规则,返回它在这段 SQL 上报出的消息。
func fireOne(t *testing.T, code, dialect, sql string) []string {
	t.Helper()
	var b *Builtin
	for i := range Builtins {
		if Builtins[i].Code == code {
			b = &Builtins[i]
			break
		}
	}
	if b == nil {
		t.Fatalf("规则 %s 不在 Builtins 里", code)
	}
	r := Rule{
		Code: b.Code, Name: b.Name, Dialect: b.Dialect, Category: b.Category,
		Level: b.Level, Kind: "builtin", Params: b.Params, Enabled: true,
	}
	res := Check(dialect, sql, []Rule{r})
	out := make([]string, 0, len(res.Findings))
	for _, f := range res.Findings {
		out = append(out, f.Message)
	}
	return out
}

// hits 断言规则命中,且消息里带上了具体对象(而不是一句放之四海的空话)。
func hits(t *testing.T, code, dialect, sql, wantSubstr string) {
	t.Helper()
	msgs := fireOne(t, code, dialect, sql)
	if len(msgs) == 0 {
		t.Fatalf("%s 应命中但没有报告\nSQL: %s", code, sql)
	}
	if wantSubstr != "" {
		for _, m := range msgs {
			if strings.Contains(m, wantSubstr) {
				return
			}
		}
		t.Errorf("%s 的消息里应提到 %q,实际: %v", code, wantSubstr, msgs)
	}
}

// quiet 断言规则不报 —— 合规写法必须安静。
func quiet(t *testing.T, code, dialect, sql string) {
	t.Helper()
	if msgs := fireOne(t, code, dialect, sql); len(msgs) > 0 {
		t.Errorf("%s 不应命中却报了 %v\nSQL: %s", code, msgs, sql)
	}
}

// ---------------------------------------------------------------- 出处

// 规则库是按规范重写的,所以"出处"必须是真的:分级只能取规范用的三个词,
// 写了分级就得说出自哪一节 —— 一条自称【強制】却指不出处的规则,是在借规范的
// 名义立平台自己的规矩。
func TestEveryRuleCitesItsStandardHonestly(t *testing.T) {
	cited := 0
	for _, b := range Builtins {
		switch b.Spec {
		case SpecNone:
			if b.SpecRef != "" {
				t.Errorf("%s 没有分级却写了出处 %q", b.Code, b.SpecRef)
			}
			continue
		case SpecCritical, SpecMandatory, SpecRecommended:
			cited++
		default:
			t.Errorf("%s 的分级 %q 不在规范的三级词汇里", b.Code, b.Spec)
			continue
		}
		if strings.TrimSpace(b.SpecRef) == "" {
			t.Errorf("%s 标了分级 %q 却没写出自规范哪一节", b.Code, b.Spec)
		}
		// 出处要指到能翻到的位置:老规范用 §章节,新版 DWS 规范用 RULE 编号。
		if !strings.Contains(b.SpecRef, "§") && !strings.Contains(b.SpecRef, "RULE") &&
			!strings.Contains(b.SpecRef, "·") && !strings.Contains(b.SpecRef, "限制") {
			t.Errorf("%s 的出处 %q 没有指到具体条目", b.Code, b.SpecRef)
		}
	}
	if cited == 0 {
		t.Fatal("规则库里一条规范派生的规则都没有")
	}
}

// 种子的默认强度必须照着规范:高危/强制拦发布,建议只提示。个别查得出但查不准的
// 条目可以压低一档,但压低这件事必须是**明写在条目上的例外**,而不是随手为之 ——
// 所以这里把例外逐条列出来,新增的偏离会当场失败。
func TestSeededLevelsFollowTheStandard(t *testing.T) {
	// 刻意低于规范强度的条目,以及原因(详见 builtin.go 上各自的注释)。
	deliberate := map[string]string{
		"dws.require.col.not.null": `"明确不存在 NULL 值"是业务判断,语句里读不出来`,
		"dws.replication.confirm":  "行数与表的角色都不在语句里,只能要求确认",
		"dws.partition.ttl":        "有没有 drop partition 的调度不在建表语句里",
	}
	for _, b := range Builtins {
		if b.Spec == SpecNone {
			continue
		}
		want := LevelForSpec(b.Spec)
		if b.Level == want {
			continue
		}
		if _, ok := deliberate[b.Code]; !ok {
			t.Errorf("%s 是【%s】,种子强度应为 %s,实际 %s —— 若确为有意压低,请在 builtin.go 上写明原因并登记到本用例",
				b.Code, b.Spec, want, b.Level)
		}
	}
	for code := range deliberate {
		found := false
		for _, b := range Builtins {
			if b.Code == code {
				found = true
			}
		}
		if !found {
			t.Errorf("例外名单里的 %s 已不在规则库中,请一并删掉", code)
		}
	}
}

// ---------------------------------------------------------------- MySQL

func TestAlterAlgorithmIsRequiredButRenameIsExempt(t *testing.T) {
	hits(t, "mysql.alter.require.algorithm", DialectMySQL,
		"ALTER TABLE t_tests ADD COLUMN c1 VARCHAR(20);", "t_tests")
	quiet(t, "mysql.alter.require.algorithm", DialectMySQL,
		"ALTER TABLE t_tests ADD COLUMN c1 VARCHAR(20), ALGORITHM=INSTANT;")
	quiet(t, "mysql.alter.require.algorithm", DialectMySQL,
		"ALTER TABLE t_tests DROP PRIMARY KEY, ADD PRIMARY KEY (id), ALGORITHM=INPLACE, LOCK=NONE;")
	// RENAME 不接受 ALGORITHM,要求它只会制造无法满足的报错。
	quiet(t, "mysql.alter.require.algorithm", DialectMySQL, "ALTER TABLE t_a RENAME TO t_b;")
}

// 三份规范都禁止在生产环境 DROP COLUMN。它和开头就是 DROP 的语句不是一个形状,
// 不单独认就会整条溜过去;而 DROP CONSTRAINT/INDEX 去掉的是规则不是数据,不能一起拦。
func TestDropColumnIsCaughtButDroppingAConstraintIsNot(t *testing.T) {
	hits(t, "ddl.forbid.drop", DialectMySQL, "ALTER TABLE t_users DROP COLUMN nickname;", "nickname")
	hits(t, "ddl.forbid.drop", DialectMySQL, "DROP TABLE t_users;", "t_users")
	quiet(t, "ddl.forbid.drop", DialectMySQL, "ALTER TABLE t_users DROP INDEX idx_us_un;")
	quiet(t, "ddl.forbid.drop", DialectMySQL, "ALTER TABLE t_users DROP CONSTRAINT fk_user_org;")
	quiet(t, "ddl.forbid.drop", DialectMySQL, "ALTER TABLE t_users ADD COLUMN nickname VARCHAR(45);")
}

func TestIndexNameLengthCap(t *testing.T) {
	hits(t, "naming.index.max.length", DialectMySQL,
		"CREATE INDEX idx_this_index_name_is_definitely_far_too_long ON t_a(b);", "32")
	quiet(t, "naming.index.max.length", DialectMySQL, "CREATE INDEX idx_us_un ON t_users(username);")
}

// ---------------------------------------------------------------- TiDB

func TestTiDBRejectsTimestampButNotADefaultOfCurrentTimestamp(t *testing.T) {
	hits(t, "tidb.forbid.timestamp", DialectTiDB,
		"CREATE TABLE t_a (id BIGINT PRIMARY KEY, create_time TIMESTAMP NOT NULL);", "create_time")
	// 类型是 DATETIME、默认值写 CURRENT_TIMESTAMP —— 这正是规范推荐的写法,不能报。
	quiet(t, "tidb.forbid.timestamp", DialectTiDB,
		"CREATE TABLE t_a (id BIGINT PRIMARY KEY, create_time DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP);")
}

func TestTiDBTablePrefix(t *testing.T) {
	hits(t, "tidb.table.prefix", DialectTiDB, "CREATE TABLE user_profile (id BIGINT PRIMARY KEY);", "user_profile")
	quiet(t, "tidb.table.prefix", DialectTiDB, "CREATE TABLE t_user_profile (id BIGINT PRIMARY KEY);")
}

// ---------------------------------------------------------------- DWS 建表

func TestDWSDistributeKeyCountAndType(t *testing.T) {
	hits(t, "dws.distribute.key.max", DialectDWS,
		"CREATE TABLE s.t_a (a INT, b INT, c INT, d INT) DISTRIBUTE BY HASH(a,b,c,d);", "4")
	quiet(t, "dws.distribute.key.max", DialectDWS,
		"CREATE TABLE s.t_a (a INT, b INT) DISTRIBUTE BY HASH(a,b);")

	hits(t, "dws.distribute.key.type", DialectDWS,
		"CREATE TABLE s.t_a (a FLOAT, b INT) DISTRIBUTE BY HASH(a);", "a")
	quiet(t, "dws.distribute.key.type", DialectDWS,
		"CREATE TABLE s.t_a (a BIGINT, b INT) DISTRIBUTE BY HASH(a);")
	// 分布键引用了本语句里看不到的列时不猜 —— 交给数据库自己报。
	quiet(t, "dws.distribute.key.type", DialectDWS,
		"CREATE TABLE s.t_a (b INT) DISTRIBUTE BY HASH(zzz);")
}


func TestDWSPrecisionIsRequiredOnlyWhereTheTypeTakesOne(t *testing.T) {
	hits(t, "dws.require.precision", DialectDWS,
		"CREATE TABLE s.t_a (amt NUMERIC);", "amt")
	quiet(t, "dws.require.precision", DialectDWS,
		"CREATE TABLE s.t_a (name VARCHAR(64), amt NUMERIC(18,2), n BIGINT, flag BOOLEAN);")
}


func TestDWSViewRules(t *testing.T) {
	hits(t, "dws.view.forbid.orderby", DialectDWS,
		"CREATE VIEW s.v_a AS SELECT a FROM s.t_a ORDER BY a;", "ORDER BY")
	quiet(t, "dws.view.forbid.orderby", DialectDWS, "CREATE VIEW s.v_a AS SELECT a FROM s.t_a;")
	// 语句本身的 ORDER BY 不是视图定义,不能顺手也报了。
	quiet(t, "dws.view.forbid.orderby", DialectDWS, "SELECT a FROM s.t_a ORDER BY a;")
}

// ---------------------------------------------------------------- DWS SQL 写法

func TestDWSSQLWritingRules(t *testing.T) {
	hits(t, "dws.forbid.not.in.subquery", DialectDWS,
		"SELECT a FROM s.t_a WHERE id NOT IN (SELECT id FROM s.t_b);", "NOT EXISTS")
	// NOT IN 接常量列表是合法写法,规范禁的是子查询。
	quiet(t, "dws.forbid.not.in.subquery", DialectDWS, "SELECT a FROM s.t_a WHERE id NOT IN (1,2,3);")


	hits(t, "dws.forbid.with.recursive", DialectDWS,
		"WITH RECURSIVE r AS (SELECT 1) SELECT * FROM r;", "")
	quiet(t, "dws.forbid.with.recursive", DialectDWS, "WITH r AS (SELECT 1 AS n) SELECT n FROM r;")

	hits(t, "dws.delete.use.truncate", DialectDWS, "DELETE FROM s.t_a;", "TRUNCATE")
	quiet(t, "dws.delete.use.truncate", DialectDWS, "DELETE FROM s.t_a WHERE id = 1;")
}

// 子查询里的 ORDER BY 要报,语句自己最外层的 ORDER BY 不能报 —— 后者是正当写法,
// 一起拦会让这条规则在任何带排序的查询上都误报。
func TestSubqueryOrderByDoesNotCatchTheOuterOne(t *testing.T) {
	hits(t, "dws.subquery.forbid.orderby", DialectDWS,
		"SELECT a FROM (SELECT a FROM s.t_a ORDER BY a) x;", "子查询")
	quiet(t, "dws.subquery.forbid.orderby", DialectDWS, "SELECT a FROM s.t_a ORDER BY a;")
}

func TestVolatileFunctionOnlyCountsInsideASubquery(t *testing.T) {
	hits(t, "dws.forbid.volatile.in.subquery", DialectDWS,
		"SELECT a FROM s.t_a WHERE id = (SELECT nextval('s.seq_a'));", "nextval")
	// 顶层调用 nextval 是插入数据的正常写法,规范禁的是子查询里的。
	quiet(t, "dws.forbid.volatile.in.subquery", DialectDWS,
		"INSERT INTO s.t_a (id, a) VALUES (nextval('s.seq_a'), 1);")
}

func TestSchemaQualificationIgnoresDerivedTables(t *testing.T) {
	hits(t, "dws.require.schema.qualified", DialectDWS, "SELECT a FROM t_a;", "t_a")
	quiet(t, "dws.require.schema.qualified", DialectDWS, "SELECT a FROM dwd.t_a;")
	// 派生表不是对象引用,要求它带 schema 是无理的。
	quiet(t, "dws.require.schema.qualified", DialectDWS, "SELECT a FROM (SELECT a FROM dwd.t_a) x;")
}

func TestSQLTagRequiredOnDMLOnly(t *testing.T) {
	hits(t, "dws.require.sql.tag", DialectDWS, "SELECT a FROM s.t_a;", "注释")
	quiet(t, "dws.require.sql.tag", DialectDWS, "/* crm_order_sync_step1 */ SELECT a FROM s.t_a;")
	// DDL 不在这条规范的适用范围里。
	quiet(t, "dws.require.sql.tag", DialectDWS, "CREATE TABLE s.t_a (a INT) DISTRIBUTE BY HASH(a);")
}

func TestJoinTableCeiling(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("/* j */ SELECT 1 FROM s.t0")
	for i := 1; i <= 20; i++ {
		sb.WriteString(" JOIN s.t")
		sb.WriteString(string(rune('a' + i%26)))
		sb.WriteString(" ON 1=1")
	}
	sb.WriteString(";")
	hits(t, "dws.max.join.tables", DialectDWS, sb.String(), "8")
	quiet(t, "dws.max.join.tables", DialectDWS, "SELECT 1 FROM s.t_a JOIN s.t_b ON 1=1;")
}

// ---------------------------------------------------------------- 安全 / 命名

func TestSensitiveColumnsAreRefused(t *testing.T) {
	hits(t, "security.forbid.sensitive.column", DialectMySQL,
		"CREATE TABLE t_a (id BIGINT, id_card VARCHAR(32));", "id_card")
	hits(t, "security.forbid.sensitive.column", DialectMySQL,
		"ALTER TABLE t_a ADD COLUMN bank_card_no VARCHAR(32);", "bank_card_no")
	quiet(t, "security.forbid.sensitive.column", DialectMySQL,
		"CREATE TABLE t_a (id BIGINT, card_type TINYINT, user_name VARCHAR(45));")
}


