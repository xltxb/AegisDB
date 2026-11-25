package sqlutil

// 对象引用提取。
//
// 这组用例的重点不是"认得多全",而是**认错的方向**。提取结果会变成发布单上的一句
// "表 X 不存在",而那句话会让人去建表、或者以为发布环境配错了。所以:
//
//   认不出 → 必须说认不出(Recognised=false),调用方据此不下结论;
//   认出来 → 名字必须是语句里真写的那个,不能是别名、CTE 名或函数名。
//
// 反过来漏认一条语句只是少一次检查,没有人会因此做错事。

import "testing"

func names(rs []Ref) []string {
	out := []string{}
	for _, r := range rs {
		if r.Schema != "" {
			out = append(out, r.Schema+"."+r.Name)
			continue
		}
		out = append(out, r.Name)
	}
	return out
}

func eq(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestObjectRefs_RequiresTheObjectItTouches(t *testing.T) {
	cases := []struct {
		sql  string
		want string
	}{
		{`INSERT INTO t_order (id) VALUES (1)`, "t_order"},
		{`insert into app.t_order values (1)`, "app.t_order"},
		{"INSERT INTO `t order` VALUES (1)", "t order"},
		{`INSERT IGNORE INTO t_order VALUES (1)`, "t_order"},
		{`UPDATE t_order SET n = 1 WHERE id = 2`, "t_order"},
		{`DELETE FROM t_order WHERE id = 2`, "t_order"},
		{`TRUNCATE TABLE t_order`, "t_order"},
		{`TRUNCATE t_order`, "t_order"},
		{`ALTER TABLE t_order ADD COLUMN n INT`, "t_order"},
		{`CREATE INDEX idx_n ON t_order (n)`, "t_order"},
		{`CREATE UNIQUE INDEX idx_n ON app.t_order (n)`, "app.t_order"},
		{`SELECT * FROM t_order WHERE id = 1`, "t_order"},
		{`SELECT * FROM "T_Order"`, "T_Order"},
		{`DROP TABLE t_order`, "t_order"},
	}
	for _, c := range cases {
		got := ObjectRefs(c.sql)
		if !got.Recognised {
			t.Errorf("%s:没认出来", c.sql)
			continue
		}
		eq(t, names(got.Requires), c.want)
	}
}

// 三段限定名(库.schema.表)取后两段 —— 最后一段是对象,倒数第二段是它的 schema。
func TestObjectRefs_QualifiedNameKeepsSchemaAndObject(t *testing.T) {
	got := ObjectRefs(`SELECT * FROM mydb.public.t_order`)
	eq(t, names(got.Requires), "public.t_order")
}

// 建表不要求它已经存在 —— 它正是来建它的。
// 这一条是"建表 + 灌数据"脚本不报一堆假警报的前提。
func TestObjectRefs_CreateDeclaresRatherThanRequires(t *testing.T) {
	got := ObjectRefs(`CREATE TABLE IF NOT EXISTS t_new (id INT)`)
	if !got.Recognised {
		t.Fatal("没认出来")
	}
	eq(t, names(got.Creates), "t_new")
	eq(t, names(got.Requires))
}

// CREATE TABLE … AS SELECT:建一张,同时要求源表存在。
func TestObjectRefs_CreateAsSelectAlsoRequiresTheSource(t *testing.T) {
	got := ObjectRefs(`CREATE TABLE t_copy AS SELECT * FROM t_order`)
	eq(t, names(got.Creates), "t_copy")
	eq(t, names(got.Requires), "t_order")
}

// DROP ... IF EXISTS 不要求对象存在,那正是这个写法的意思。
// 把它判成 Requires,每一份幂等脚本的第一行都会报"对象不存在"。
func TestObjectRefs_DropIfExistsDoesNotRequire(t *testing.T) {
	got := ObjectRefs(`DROP TABLE IF EXISTS t_old`)
	if !got.Recognised {
		t.Fatal("没认出来")
	}
	eq(t, names(got.Requires))
	eq(t, names(got.Drops), "t_old")
}

func TestObjectRefs_JoinCollectsEveryTableOnce(t *testing.T) {
	got := ObjectRefs(`SELECT a.id FROM t_order a JOIN t_item b ON b.oid = a.id JOIN t_item c ON c.oid = a.id`)
	eq(t, names(got.Requires), "t_order", "t_item")
}

// 拿不准的形状一律不认。每一条都是"猜一下就会报出一个不存在的名字"的地方。
func TestObjectRefs_UnsureShapesAreNotGuessed(t *testing.T) {
	cases := []string{
		`SELECT * FROM (SELECT 1) x`,                          // 子查询:x 是别名不是表
		`WITH c AS (SELECT 1) SELECT * FROM c`,                // CTE:c 不是表
		`SELECT * FROM generate_series(1, 10) g`,              // 表值函数
		`SELECT * FROM unnest(ARRAY[1,2]) u`,                  // 同上
		`ALTER SESSION SET current_schema = app`,              // 不是表操作
		`DROP INDEX idx_n`,                                    // 索引名不在这里查
		`BEGIN`,                                               // 块
		`GRANT SELECT ON t_order TO alice`,                    // 权限语句,没在支持之列
	}
	for _, sql := range cases {
		if got := ObjectRefs(sql); got.Recognised {
			t.Errorf("%s:不该认出来,却报了 %v / %v", sql, names(got.Requires), names(got.Creates))
		}
	}
}

// 字面量里的 SQL 不算引用。'DROP TABLE x' 是一个字符串,不是一条语句。
func TestObjectRefs_StringLiteralsAreNotStatements(t *testing.T) {
	got := ObjectRefs(`INSERT INTO t_log (msg) VALUES ('SELECT * FROM t_secret')`)
	if !got.Recognised {
		t.Fatal("没认出来")
	}
	eq(t, names(got.Requires), "t_log")
}
