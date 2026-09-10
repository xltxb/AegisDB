package gateway

// PostgreSQL / DWS 表结构重建的渲染测试。
//
// 和 Oracle 那边同样的理由:仓库里没有 PostgreSQL 也没有 DWS 可以跑集成测试,而取数
// 与渲染是分开的,所以这里把"同样一组目录事实,输出长什么样"钉住。
//
// 重点在 DWS 的两件事上 —— 分布键和行存/列存。它们在原生 PostgreSQL 上不存在,却是
// DWS 上看一张表时最先要看的东西:分布键选错了,表结构再"对"也没有意义。

import (
	"strings"
	"testing"
)

func pgFixture() *pgTable {
	return &pgTable{
		Schema:     "public",
		Name:       "t_order",
		Tablespace: "tbs_app",
		RelOptions: "orientation=column, compression=low",
		Comment:    "订单主表 · 别写 'DROP'",
		Distribute: `HASH("id")`,
		PartKeyDef: "RANGE (created_at)",
		PartCount:  12,
		Columns: []pgColumn{
			{AttNum: 1, Name: "id", Type: "bigint", NotNull: true},
			{AttNum: 2, Name: "amount", Type: "numeric(10,2)", NotNull: true, Default: "0", Comment: "金额,单位元"},
			{AttNum: 3, Name: "title", Type: "character varying(100)"},
			{AttNum: 4, Name: "created_at", Type: "timestamp without time zone", Default: "now()"},
		},
		Constraints: []pgConstraint{
			{Name: "pk_order", Type: "p", Def: "PRIMARY KEY (id)"},
			{Name: "fk_order_cust", Type: "f", Def: "FOREIGN KEY (cust_id) REFERENCES public.t_customer(id) ON DELETE CASCADE"},
			{Name: "ck_amount", Type: "c", Def: "CHECK ((amount >= (0)::numeric))"},
		},
		Indexes: []pgIndex{
			{Name: "idx_order_created", Def: "CREATE INDEX idx_order_created ON public.t_order USING btree (created_at DESC)"},
		},
	}
}

func TestRenderPGTable_Complete(t *testing.T) {
	out := renderPGTable(pgFixture())

	must := []struct{ what, frag string }{
		{"表空间", "-- 表空间:tbs_app"},
		{"行存/列存与压缩", "存储:orientation=column, compression=low"},
		{"分布键", `分布:HASH("id")`},
		{"分区", "分区:RANGE (created_at) · 12 个分区"},
		{"建表语句带 schema", `CREATE TABLE "public"."t_order" (`},
		{"类型原样取自 format_type", `"amount" numeric(10,2) DEFAULT 0 NOT NULL`},
		{"可空列不写 NOT NULL", `"title" character varying(100),`},
		{"WITH 子句", "WITH (orientation=column, compression=low)"},
		{"TABLESPACE 子句", `TABLESPACE "tbs_app"`},
		{"DISTRIBUTE BY 子句", `DISTRIBUTE BY HASH("id");`},
		{"约束用 PostgreSQL 自己的渲染", `ADD CONSTRAINT "fk_order_cust" FOREIGN KEY (cust_id) REFERENCES public.t_customer(id) ON DELETE CASCADE;`},
		{"索引", "CREATE INDEX idx_order_created ON public.t_order USING btree (created_at DESC);"},
		{"表注释里的单引号被转义", `COMMENT ON TABLE "public"."t_order" IS '订单主表 · 别写 ''DROP''';`},
		{"列注释", `COMMENT ON COLUMN "public"."t_order"."amount" IS '金额,单位元';`},
	}
	for _, m := range must {
		if !strings.Contains(out, m.frag) {
			t.Errorf("%s:输出里找不到\n  %s\n--- 实际输出 ---\n%s", m.what, m.frag, out)
		}
	}
}

// 原生 PostgreSQL:没有分布键、没有 reloptions,那几行就不该出现 —— 一个空的
// "分布:" 比不写更糟。
func TestRenderPGTable_PlainPostgres(t *testing.T) {
	tab := &pgTable{
		Schema:  "public",
		Name:    "t_min",
		Columns: []pgColumn{{AttNum: 1, Name: "id", Type: "integer", NotNull: true}},
	}
	out := renderPGTable(tab)
	for _, s := range []string{"DISTRIBUTE BY", "WITH (", "TABLESPACE", "分布:", "存储:", "-- 约束", "-- 索引", "-- 注释"} {
		if strings.Contains(out, s) {
			t.Errorf("没有内容时不该出现 %q:\n%s", s, out)
		}
	}
	if !strings.Contains(out, `CREATE TABLE "public"."t_min" (`) || !strings.Contains(out, `"id" integer NOT NULL`) {
		t.Fatalf("最小输出不成立:\n%s", out)
	}
}

// UNLOGGED / 临时表要写在 CREATE 上,不然重放出来的是另一种表。
func TestRenderPGTable_Persistence(t *testing.T) {
	tab := &pgTable{Schema: "s", Name: "t", Unlogged: true,
		Columns: []pgColumn{{AttNum: 1, Name: "id", Type: "integer"}}}
	if !strings.Contains(renderPGTable(tab), `CREATE UNLOGGED TABLE "s"."t"`) {
		t.Error("UNLOGGED 没有出现在建表语句上")
	}
	tab.Unlogged, tab.Temporary = false, true
	if !strings.Contains(renderPGTable(tab), `CREATE TEMPORARY TABLE "s"."t"`) {
		t.Error("临时表没有出现在建表语句上")
	}
}

// 主键/唯一约束自带的同名索引不重复列一遍 —— 否则索引看起来比实际多一倍。
// 这一条挡的是"取数"那半边的过滤逻辑退化,所以直接构造 pgTable 验渲染的入参约定:
// 约束里有 pk_order,索引段里就不该再出现同名的那条。
func TestRenderPGTable_ConstraintBackedIndexNotRepeated(t *testing.T) {
	tab := pgFixture()
	out := renderPGTable(tab)
	if strings.Count(out, "pk_order") != 1 {
		t.Errorf("pk_order 应当只出现一次(在约束段):\n%s", out)
	}
}

// DISTRIBUTE BY 的定位类型字母 → 子句。认不出来的原样带出去:猜一个错的分布方式
// 比承认不认识更糟。
func TestPGDistributeClause(t *testing.T) {
	cases := []struct {
		locator string
		cols    []string
		want    string
	}{
		{"H", []string{"id"}, `HASH("id")`},
		{"H", []string{"a", "b"}, `HASH("a", "b")`},
		{"R", nil, "REPLICATION"},
		{"N", nil, "ROUNDROBIN"},
		{"M", []string{"id"}, `MODULO("id")`},
		{"L", []string{"region"}, `LIST("region")`},
		{"G", []string{"day"}, `RANGE("day")`},
		{"H", nil, ""}, // 哈希却没有分布列:宁可不写
		{"", nil, ""},
		{"Z", nil, "Z"}, // 不认识的原样带出
	}
	for _, c := range cases {
		if got := pgDistributeClause(c.locator, c.cols); got != c.want {
			t.Errorf("locator=%q cols=%v: got %q, want %q", c.locator, c.cols, got, c.want)
		}
	}
}

// 读不到的段落降级成一行说明,出现在最上面。缺口要看得见。
func TestRenderPGTable_Notes(t *testing.T) {
	tab := pgFixture()
	tab.Notes = []string{"约束未列出:读取 pg_constraint 失败(permission denied)"}
	out := renderPGTable(tab)
	if !strings.HasPrefix(out, "-- 约束未列出:读取 pg_constraint 失败(permission denied)") {
		t.Errorf("说明应当在最前面:\n%s", out)
	}
}
