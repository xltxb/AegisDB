package review

// 新版 DWS 规范(RULE 1..62)派生规则的回归。
//
// 与 checks_spec_test.go 同一写法:每条都测**该报的报**和**不该报的不报**。
// 后者尤其重要 —— DWS 这批规则里有一半会落在每一条建表语句上,一个误报的规则
// 会让整页审查结果变成噪音,人就再也不看它了。

import "testing"

// ---------------------------------------------------------------- 命名

func TestDWSObjectNameCharsetAndPrefix(t *testing.T) {
	hits(t, "dws.object.name.charset", DialectDWS,
		"CREATE TABLE g_ODS_Big (a INT) DISTRIBUTE BY HASH(a);", "g_ODS_Big")
	hits(t, "dws.object.name.charset", DialectDWS,
		"CREATE TABLE 1_bad (a INT) DISTRIBUTE BY HASH(a);", "")
	quiet(t, "dws.object.name.charset", DialectDWS,
		"CREATE TABLE g_ods_big_order_di (a INT) DISTRIBUTE BY HASH(a);")

	hits(t, "dws.object.name.reserved.prefix", DialectDWS,
		"CREATE TABLE pg_my_table (a INT) DISTRIBUTE BY HASH(a);", "pg")
	quiet(t, "dws.object.name.reserved.prefix", DialectDWS,
		"CREATE TABLE g_ods_big_order_di (a INT) DISTRIBUTE BY HASH(a);")
}

// 63 是**字节**上限,不是字符 —— 截断按字节发生,一个汉字占三个。
func TestDWSNameLengthCountsBytesNotCharacters(t *testing.T) {
	long := "CREATE TABLE g_ods_big_this_is_a_very_long_table_name_that_goes_past_sixty_three (a INT) DISTRIBUTE BY HASH(a);"
	hits(t, "dws.object.name.max.length", DialectDWS, long, "63")
	quiet(t, "dws.object.name.max.length", DialectDWS,
		"CREATE TABLE g_ods_big_order_di (a INT) DISTRIBUTE BY HASH(a);")
}

func TestDWSTempTableDateSuffix(t *testing.T) {
	hits(t, "dws.temp.table.date.suffix", DialectDWS, "CREATE TEMP TABLE t_calc (a INT);", "t_calc")
	quiet(t, "dws.temp.table.date.suffix", DialectDWS, "CREATE TEMP TABLE t_calc_20251202 (a INT);")
	// 普通表不在这条的适用范围。
	quiet(t, "dws.temp.table.date.suffix", DialectDWS, "CREATE TABLE g_ods_big_order_di (a INT) DISTRIBUTE BY HASH(a);")
}

// ---------------------------------------------------------------- 建库

func TestDWSDatabaseCreationRules(t *testing.T) {
	hits(t, "dws.database.charset.utf8", DialectDWS, "CREATE DATABASE app_dw;", "UTF8")
	quiet(t, "dws.database.charset.utf8", DialectDWS,
		"CREATE DATABASE app_dw WITH ENCODING = 'UTF8' DBCOMPATIBILITY = 'MySQL';")

	hits(t, "dws.database.dbcompatibility", DialectDWS,
		"CREATE DATABASE app_dw WITH ENCODING = 'UTF8' DBCOMPATIBILITY = 'ORA';", "ORA")
	quiet(t, "dws.database.dbcompatibility", DialectDWS,
		"CREATE DATABASE app_dw WITH ENCODING = 'UTF8' DBCOMPATIBILITY = 'MySQL';")

	hits(t, "dws.forbid.tablespace", DialectDWS, "CREATE TABLESPACE ts_a LOCATION '/x';", "表空间")
	quiet(t, "dws.forbid.tablespace", DialectDWS, "CREATE TABLE g_a (a INT) DISTRIBUTE BY HASH(a);")
}

// ---------------------------------------------------------------- 高危不支持

func TestDWSUnsupportedFeatures(t *testing.T) {
	hits(t, "dws.forbid.trigger", DialectDWS, "CREATE TRIGGER tr_a BEFORE INSERT ON g_a FOR EACH ROW EXECUTE PROCEDURE f();", "")
	quiet(t, "dws.forbid.trigger", DialectDWS, "CREATE TABLE g_a (a INT) DISTRIBUTE BY HASH(a);")

	hits(t, "dws.forbid.unlogged.table", DialectDWS, "CREATE UNLOGGED TABLE g_a (a INT);", "")
	quiet(t, "dws.forbid.unlogged.table", DialectDWS, "CREATE TABLE g_a (a INT) DISTRIBUTE BY HASH(a);")

	hits(t, "dws.forbid.udf", DialectDWS, "CREATE FUNCTION f() RETURNS INT AS 'x' LANGUAGE C;", "")
	// plpgsql 的函数是允许的,规范只禁 C/Java 外部函数。
	quiet(t, "dws.forbid.udf", DialectDWS, "CREATE FUNCTION f() RETURNS INT AS $$ BEGIN RETURN 1; END $$ LANGUAGE plpgsql;")
}

// ---------------------------------------------------------------- 表结构

// RULE 11 要求三样齐全,缺哪样就报哪样 —— 笼统一句"不合规"没法让人知道少写了什么。
func TestDWSHstoreOptNamesWhatIsMissing(t *testing.T) {
	msgs := fireOne(t, "dws.require.hstore.opt", DialectDWS,
		"CREATE TABLE dfm.t1 (carno BIGINT) DISTRIBUTE BY HASH(carno);")
	if len(msgs) == 0 {
		t.Fatal("缺少列存三件套时应报")
	}
	for _, want := range []string{"orientation=column", "enable_hstore_opt=true", "colversion=3.0"} {
		found := false
		for _, m := range msgs {
			if contains(m, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("消息里应点出缺少 %s,实际: %v", want, msgs)
		}
	}
	quiet(t, "dws.require.hstore.opt", DialectDWS,
		"CREATE TABLE dfm.t1 (carno BIGINT, name TEXT) WITH (orientation=column,enable_hstore_opt=true,colversion=3.0) DISTRIBUTE BY HASH(carno);")
	// colversion 写成 2.0 也是不合规,且要说清当前值。
	hits(t, "dws.require.hstore.opt", DialectDWS,
		"CREATE TABLE dfm.t1 (carno BIGINT) WITH (orientation=column,enable_hstore_opt=true,colversion=2.0) DISTRIBUTE BY HASH(carno);", "2.0")
	// CREATE TABLE … AS SELECT 继承来源表的存储形态,不该被要求写这些。
	quiet(t, "dws.require.hstore.opt", DialectDWS, "CREATE TABLE dfm.t2 AS SELECT * FROM dfm.t1;")
}

func TestDWSDistributeKeyMustBeSubsetOfAKey(t *testing.T) {
	hits(t, "dws.distribute.key.subset", DialectDWS,
		"CREATE TABLE g_a (id BIGINT, code VARCHAR(32), PRIMARY KEY (id)) DISTRIBUTE BY HASH(code);", "code")
	quiet(t, "dws.distribute.key.subset", DialectDWS,
		"CREATE TABLE g_a (id BIGINT, code VARCHAR(32), PRIMARY KEY (id)) DISTRIBUTE BY HASH(id);")
	// 表没声明任何键时,这条规范无从谈起 —— "建表必须有主键"是另一条规则的事。
	quiet(t, "dws.distribute.key.subset", DialectDWS,
		"CREATE TABLE g_a (id BIGINT, code VARCHAR(32)) DISTRIBUTE BY HASH(code);")
	// 复制表没有分布键。
	quiet(t, "dws.distribute.key.subset", DialectDWS,
		"CREATE TABLE g_a (id BIGINT, PRIMARY KEY (id)) DISTRIBUTE BY REPLICATION;")
}

func TestDWSUUIDDefaultOnDistributionColumn(t *testing.T) {
	hits(t, "dws.distribute.key.no.uuid.default", DialectDWS,
		"CREATE TABLE g_a (id VARCHAR(36) DEFAULT sys_guid(), PRIMARY KEY (id)) DISTRIBUTE BY HASH(id);", "sys_guid")
	// 非分布列上用 UUID 默认值不在这条禁令里。
	quiet(t, "dws.distribute.key.no.uuid.default", DialectDWS,
		"CREATE TABLE g_a (id BIGINT, uid VARCHAR(36) DEFAULT sys_guid()) DISTRIBUTE BY HASH(id);")
}

func TestDWSPrimaryKeyColumnCeiling(t *testing.T) {
	hits(t, "dws.pk.max.columns", DialectDWS,
		"CREATE TABLE g_a (a INT,b INT,c INT,d INT,e INT,f INT, PRIMARY KEY (a,b,c,d,e,f)) DISTRIBUTE BY HASH(a);", "5")
	quiet(t, "dws.pk.max.columns", DialectDWS,
		"CREATE TABLE g_a (a INT,b INT, PRIMARY KEY (a,b)) DISTRIBUTE BY HASH(a);")
}

// ---------------------------------------------------------------- 分区

func TestDWSPartitionMustBeSingleColumnRange(t *testing.T) {
	hits(t, "dws.partition.single.range", DialectDWS,
		"CREATE TABLE g_a (a INT, b INT) DISTRIBUTE BY HASH(a) PARTITION BY LIST (b);", "LIST")
	hits(t, "dws.partition.single.range", DialectDWS,
		"CREATE TABLE g_a (a INT, b INT) DISTRIBUTE BY HASH(a) PARTITION BY RANGE (a, b);", "2")
	quiet(t, "dws.partition.single.range", DialectDWS,
		"CREATE TABLE g_a (a INT, biz_date DATE) DISTRIBUTE BY HASH(a) PARTITION BY RANGE (biz_date);")
}

func TestDWSPartitionTTLIsAReminderNotAVerdict(t *testing.T) {
	hits(t, "dws.partition.ttl", DialectDWS,
		"CREATE TABLE g_a (a INT, biz_date DATE) DISTRIBUTE BY HASH(a) PARTITION BY RANGE (biz_date);", "ttl")
	quiet(t, "dws.partition.ttl", DialectDWS,
		"CREATE TABLE g_a (a INT, biz_date DATE) WITH (ttl='30 days') DISTRIBUTE BY HASH(a) PARTITION BY RANGE (biz_date);")
	// 非分区表不该被问 ttl。
	quiet(t, "dws.partition.ttl", DialectDWS, "CREATE TABLE g_a (a INT) DISTRIBUTE BY HASH(a);")
}

// ---------------------------------------------------------------- 索引

func TestDWSIndexMethodAllowlist(t *testing.T) {
	hits(t, "dws.index.btree.only", DialectDWS, "CREATE INDEX idx_a ON g_a USING psort (a);", "PSORT")
	quiet(t, "dws.index.btree.only", DialectDWS, "CREATE INDEX idx_a ON g_a USING btree (a);")
	// 不写 USING 就是默认的 btree,不该报。
	quiet(t, "dws.index.btree.only", DialectDWS, "CREATE INDEX idx_a ON g_a (a);")
}

// ---------------------------------------------------------------- SQL 写法

func TestDWSNonPushdownWritings(t *testing.T) {
	hits(t, "dws.forbid.returning", DialectDWS, "INSERT INTO s.t_a (a) VALUES (1) RETURNING a;", "")
	quiet(t, "dws.forbid.returning", DialectDWS, "INSERT INTO s.t_a (a) VALUES (1);")

	hits(t, "dws.forbid.distinct.on", DialectDWS, "SELECT DISTINCT ON (a) a, b FROM s.t_a;", "")
	quiet(t, "dws.forbid.distinct.on", DialectDWS, "SELECT DISTINCT a FROM s.t_a;")

	hits(t, "dws.forbid.orderby.in.aggregate", DialectDWS, "SELECT string_agg(a ORDER BY b) FROM s.t_a;", "")
	// 语句自己的 ORDER BY 不是聚集函数里的。
	quiet(t, "dws.forbid.orderby.in.aggregate", DialectDWS, "SELECT sum(a) FROM s.t_a ORDER BY 1;")

	hits(t, "dws.forbid.query.dop", DialectDWS, "SET query_dop = 8;", "query_dop")
	quiet(t, "dws.forbid.query.dop", DialectDWS, "SELECT a FROM s.t_a;")
}

// RULE 47 劝人少用 EXISTS/IN,但 RULE 43 恰恰要求把 NOT IN 改写成 NOT EXISTS ——
// 两条规则不能把人夹在中间,所以 NOT EXISTS 必须放行。
func TestPreferJoinDoesNotFightTheNotExistsRule(t *testing.T) {
	hits(t, "dws.prefer.join.over.exists", DialectDWS,
		"SELECT a FROM s.t_a WHERE EXISTS (SELECT 1 FROM s.t_b WHERE id = t_a.id);", "JOIN")
	quiet(t, "dws.prefer.join.over.exists", DialectDWS,
		"SELECT a FROM s.t_a WHERE NOT EXISTS (SELECT 1 FROM s.t_b WHERE id = t_a.id);")
}

// ---------------------------------------------------------------- 类型

func TestDWSPrecisionAndVarcharLength(t *testing.T) {
	hits(t, "dws.require.precision", DialectDWS, "CREATE TABLE g_a (amt NUMERIC) DISTRIBUTE BY HASH(amt);", "amt")
	quiet(t, "dws.require.precision", DialectDWS, "CREATE TABLE g_a (amt NUMERIC(18,2)) DISTRIBUTE BY HASH(amt);")
	// 新版规范把 VARCHAR 长度挪到了 RULE 29,精度这条只管 NUMERIC/DECIMAL。
	quiet(t, "dws.require.precision", DialectDWS, "CREATE TABLE g_a (name VARCHAR) DISTRIBUTE BY HASH(name);")

	hits(t, "dws.varchar.length", DialectDWS, "CREATE TABLE g_a (name VARCHAR) DISTRIBUTE BY HASH(name);", "name")
	hits(t, "dws.varchar.length", DialectDWS, "CREATE TABLE g_a (name VARCHAR(9000)) DISTRIBUTE BY HASH(name);", "6000")
	quiet(t, "dws.varchar.length", DialectDWS, "CREATE TABLE g_a (name VARCHAR(64)) DISTRIBUTE BY HASH(name);")
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOfSub(s, sub) >= 0)
}

func indexOfSub(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
