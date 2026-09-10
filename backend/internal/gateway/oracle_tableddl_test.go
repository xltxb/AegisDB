package gateway

// Oracle 表结构重建的渲染测试。
//
// 这个仓库里没有真实 Oracle 可以跑集成测试(唯一的真库是 SQLite),所以取数与渲染
// 是分开写的,而这里钉住的是渲染 —— "同样一组字典事实,输出到底长什么样"。
// 它挡住的是最容易悄悄回退的一类:精度丢了、DESC 丢了、外键的 ON DELETE 丢了、
// 失效的约束看起来仍然有效。这些错误不会让任何东西报错,只会让人看到一份读起来
// 很像、实际不对的表结构。

import (
	"database/sql"
	"strings"
	"testing"
)

func n64(v int64) sql.NullInt64 { return sql.NullInt64{Int64: v, Valid: true} }

// 类型还原:旧实现只给 CHAR/RAW 补长度,NUMBER(10,2) 会显示成光秃秃的 NUMBER。
func TestOracleColumnType(t *testing.T) {
	cases := []struct {
		name string
		col  oraColumn
		want string
	}{
		{"浮动精度的 NUMBER 不补括号", oraColumn{Type: "NUMBER"}, "NUMBER"},
		{"整数 NUMBER 只写精度", oraColumn{Type: "NUMBER", Prec: n64(10), Scale: n64(0)}, "NUMBER(10)"},
		{"带标度的 NUMBER", oraColumn{Type: "NUMBER", Prec: n64(10), Scale: n64(2)}, "NUMBER(10,2)"},
		{"VARCHAR2 按字节", oraColumn{Type: "VARCHAR2", Length: n64(50), CharLen: n64(50), CharUsed: "B"}, "VARCHAR2(50)"},
		{"VARCHAR2 按字符要标出来", oraColumn{Type: "VARCHAR2", Length: n64(200), CharLen: n64(50), CharUsed: "C"}, "VARCHAR2(50 CHAR)"},
		{"RAW 用字节长度", oraColumn{Type: "RAW", Length: n64(16)}, "RAW(16)"},
		{"DATE 不带括号", oraColumn{Type: "DATE", Length: n64(7)}, "DATE"},
		{"TIMESTAMP 的精度字典里已经带着", oraColumn{Type: "TIMESTAMP(6)", Length: n64(11)}, "TIMESTAMP(6)"},
		{"CLOB 不带长度", oraColumn{Type: "CLOB", Length: n64(4000)}, "CLOB"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := oraColumnType(c.col); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// NOT NULL 在 Oracle 里是一条系统生成的检查约束。它已经写在列上了,再列一遍会把
// 真正的业务约束淹掉 —— 但复合条件里出现的 IS NOT NULL 不能跟着一起被吃掉。
func TestOraIsNotNullCheck(t *testing.T) {
	yes := []string{`"ID" IS NOT NULL`, `("NAME" IS NOT NULL)`, `  "X" is not null  `}
	no := []string{`"A" IS NOT NULL AND "B" > 0`, `"STATUS" IN ('A','B')`, `"A" IS NULL`, ``}
	for _, s := range yes {
		if !oraIsNotNullCheck(s) {
			t.Errorf("%q 应被认作 NOT NULL 的自动约束", s)
		}
	}
	for _, s := range no {
		if oraIsNotNullCheck(s) {
			t.Errorf("%q 不该被当成 NOT NULL 的自动约束吃掉", s)
		}
	}
}

// 一张"什么都有"的表,验完整输出。
func fixtureTable() *oraTable {
	return &oraTable{
		Owner:       "APP",
		Name:        "T_ORDER",
		Tablespace:  "TBS_APP",
		Partitioned: true,
		PartType:    "RANGE",
		PartKeys:    []string{"CREATED_AT"},
		PartCount:   12,
		Comment:     "订单主表 · 别写 'DROP'",
		Columns: []oraColumn{
			{Name: "ID", Type: "NUMBER", Prec: n64(12), Scale: n64(0), Nullable: "N"},
			{Name: "AMOUNT", Type: "NUMBER", Prec: n64(10), Scale: n64(2), Nullable: "N", Default: "0"},
			{Name: "TITLE", Type: "VARCHAR2", Length: n64(400), CharLen: n64(100), CharUsed: "C", Nullable: "Y"},
			{Name: "CREATED_AT", Type: "DATE", Nullable: "N", Default: "SYSDATE"},
		},
		Constraints: []oraConstraint{
			{Name: "PK_ORDER", Type: "P", Cols: []string{"ID"}, Status: "ENABLED"},
			{Name: "UK_ORDER_TITLE", Type: "U", Cols: []string{"TITLE"}, Status: "DISABLED"},
			{Name: "FK_ORDER_CUST", Type: "R", Cols: []string{"CUST_ID"},
				RefOwner: "APP", RefTable: "T_CUSTOMER", RefCols: []string{"ID"},
				DeleteRule: "CASCADE", Status: "ENABLED"},
			{Name: "CK_AMOUNT", Type: "C", Condition: `"AMOUNT" >= 0`, Status: "ENABLED"},
		},
		Indexes: []oraIndex{
			{Name: "IDX_ORDER_CREATED", Owner: "APP", Type: "NORMAL", Tablespace: "TBS_IDX",
				Cols: []string{`"CREATED_AT" DESC`}, Status: "VALID"},
			{Name: "IDX_ORDER_UPPER_TITLE", Owner: "APP", Type: "FUNCTION-BASED NORMAL",
				Cols: []string{`UPPER("TITLE")`}, Status: "VALID"},
			{Name: "UK_ORDER_TITLE", Owner: "APP", Unique: true, Type: "NORMAL",
				Cols: []string{`"TITLE"`}, Status: "UNUSABLE"},
		},
		ColComments: []oraColComment{
			{Column: "AMOUNT", Comment: "金额,单位元"},
			{Column: "TITLE", Comment: "标题"},
		},
	}
}

func TestRenderOracleTable_Complete(t *testing.T) {
	out := renderOracleTable(fixtureTable(), "")

	must := []struct {
		what string
		frag string
	}{
		{"表空间", `-- 表空间:TBS_APP`},
		{"分区方式与分区键", `分区:RANGE("CREATED_AT") · 12 个分区`},
		{"建表语句带属主", `CREATE TABLE "APP"."T_ORDER" (`},
		{"数值精度与标度", `"AMOUNT" NUMBER(10,2) DEFAULT 0 NOT NULL`},
		{"按字符的长度", `"TITLE" VARCHAR2(100 CHAR)`},
		{"表级 TABLESPACE 子句", `) TABLESPACE "TBS_APP";`},
		{"主键", `ADD CONSTRAINT "PK_ORDER" PRIMARY KEY ("ID");`},
		{"外键与删除动作", `FOREIGN KEY ("CUST_ID") REFERENCES "APP"."T_CUSTOMER" ("ID") ON DELETE CASCADE;`},
		{"检查约束", `ADD CONSTRAINT "CK_AMOUNT" CHECK ("AMOUNT" >= 0);`},
		{"失效的约束要标出来", `ADD CONSTRAINT "UK_ORDER_TITLE" UNIQUE ("TITLE");  -- 状态:DISABLED`},
		{"倒序索引", `CREATE INDEX "APP"."IDX_ORDER_CREATED" ON "APP"."T_ORDER" ("CREATED_AT" DESC) TABLESPACE "TBS_IDX";`},
		{"函数索引写表达式", `("APP"."IDX_ORDER_UPPER_TITLE" ON "APP"."T_ORDER" (UPPER("TITLE"))`[1:]},
		{"唯一索引", `CREATE UNIQUE INDEX "APP"."UK_ORDER_TITLE"`},
		{"不可用的索引要标出来", `-- 状态:UNUSABLE`},
		{"表注释里的单引号被转义", `COMMENT ON TABLE "APP"."T_ORDER" IS '订单主表 · 别写 ''DROP''';`},
		{"列注释", `COMMENT ON COLUMN "APP"."T_ORDER"."AMOUNT" IS '金额,单位元';`},
		{"分段标题", "-- 约束"},
	}
	for _, m := range must {
		if !strings.Contains(out, m.frag) {
			t.Errorf("%s:输出里找不到\n  %s\n--- 实际输出 ---\n%s", m.what, m.frag, out)
		}
	}

	// 没有说明原因的头部:权威路径不该出现"重建"字样。
	if strings.Contains(out, "重建") {
		t.Error("reason 为空时不该出现重建说明")
	}
}

// 重建路径必须自报家门,并把读不到的段落说出来 —— 缺口要看得见,不能是一段静悄悄
// 的空白。
func TestRenderOracleTable_ReasonAndNotes(t *testing.T) {
	tab := fixtureTable()
	tab.Notes = []string{"约束未列出:读取 ALL_CONSTRAINTS 失败(ORA-00942: 表或视图不存在)"}
	out := renderOracleTable(tab, "ORA-31603: 对象未找到")

	for _, frag := range []string{
		"-- DBMS_METADATA.GET_DDL 不可用(ORA-31603: 对象未找到),以下由 ALL_* 数据字典重建。",
		"-- 约束未列出:读取 ALL_CONSTRAINTS 失败(ORA-00942: 表或视图不存在)",
	} {
		if !strings.Contains(out, frag) {
			t.Errorf("输出里找不到:\n  %s\n--- 实际输出 ---\n%s", frag, out)
		}
	}
	if strings.Index(out, "CREATE TABLE") < strings.Index(out, "GET_DDL 不可用") {
		t.Error("说明应当在建表语句之前")
	}
}

// 只有列、其余全部读不到时,仍然要给出一份能看的建表语句。
func TestRenderOracleTable_ColumnsOnly(t *testing.T) {
	tab := &oraTable{
		Owner:   "APP",
		Name:    "T_MIN",
		Columns: []oraColumn{{Name: "ID", Type: "NUMBER", Prec: n64(9), Scale: n64(0), Nullable: "N"}},
	}
	out := renderOracleTable(tab, "ORA-01031: 权限不足")
	if !strings.Contains(out, `CREATE TABLE "APP"."T_MIN" (`) || !strings.Contains(out, `"ID" NUMBER(9) NOT NULL`) {
		t.Fatalf("最小输出不成立:\n%s", out)
	}
	// 空的段落标题不该出现 —— 一个只有标题、下面什么都没有的"索引"段会让人以为
	// 这张表真的没有索引,而事实是没读到。
	for _, s := range []string{"-- 约束", "-- 索引", "-- 注释"} {
		if strings.Contains(out, s) {
			t.Errorf("没有内容时不该出现段落标题 %q:\n%s", s, out)
		}
	}
}

// 转义:注释和标识符都直接落进 SQL 文本,这里是唯一一处会被内容影响的地方。
func TestOracleQuotingEscapes(t *testing.T) {
	if got := sqlLit("it's"); got != `'it''s'` {
		t.Errorf("字符串字面量转义错误:%s", got)
	}
	if got := sqlIdent(`WEIRD"NAME`); got != `"WEIRD""NAME"` {
		t.Errorf("标识符转义错误:%s", got)
	}
}
