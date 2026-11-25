package schemadiff

import (
	"reflect"
	"testing"
)

func col(name, typ string, ord int) Column {
	return Column{Name: name, DataType: typ, Nullable: true, Ordinal: ord}
}

func snap(engine string, tables ...Table) *Snapshot {
	return &Snapshot{Engine: engine, Database: "orders", Tables: tables}
}

func orders() Table {
	return Table{
		Name: "t_order", Kind: "table", Comment: "订单",
		Columns: []Column{
			{Name: "id", DataType: "bigint(20)", Nullable: false, Ordinal: 1},
			{Name: "status", DataType: "tinyint(1)", Nullable: false, HasDefault: true, Default: "0", Ordinal: 2},
			{Name: "amount", DataType: "decimal(10,2)", Nullable: true, Ordinal: 3, Comment: "金额"},
			{Name: "created_at", DataType: "datetime", Nullable: false, HasDefault: true, Default: "CURRENT_TIMESTAMP", Ordinal: 4},
		},
		PrimaryKey: []string{"id"},
		Indexes: []Index{
			{Name: "PRIMARY", Columns: []string{"id"}, Unique: true},
			{Name: "idx_status", Columns: []string{"status"}},
			{Name: "uk_amount", Columns: []string{"amount", "id"}, Unique: true},
		},
	}
}

func TestDiff_IdenticalSchemasHaveNoDifferences(t *testing.T) {
	r := DiffSchemas(snap("MySQL 8.0", orders()), snap("MySQL 8.0", orders()), Options{})
	if r.Summary != (Summary{Left: 1, Right: 1, Same: 1}) || len(r.Changed) != 0 {
		t.Fatalf("两边一样应无差异,实际 %+v", r)
	}
}

func TestDiff_TablesOnlyOnOneSide(t *testing.T) {
	a, b := orders(), orders()
	b.Name = "t_order_bak"
	r := DiffSchemas(snap("MySQL 8.0", a, Table{Name: "t_user", Kind: "table"}), snap("MySQL 8.0", b, Table{Name: "t_user", Kind: "table"}), Options{})
	if !reflect.DeepEqual(r.OnlyLeft, []string{"t_order"}) || !reflect.DeepEqual(r.OnlyRight, []string{"t_order_bak"}) ||
		!reflect.DeepEqual(r.Same, []string{"t_user"}) {
		t.Fatalf("只在一边的表归类错: %+v", r)
	}
	if r.Summary != (Summary{Left: 2, Right: 2, Same: 1, OnlyLeft: 1, OnlyRight: 1}) {
		t.Errorf("汇总不对: %+v", r.Summary)
	}
}

// MySQL 表名不区分大小写(Windows / lower_case_table_names=1 的实例目录里是小写,另一台可能不是)。
func TestDiff_MySQLTableNamesAreCaseInsensitive(t *testing.T) {
	a, b := orders(), orders()
	b.Name = "T_ORDER"
	if r := DiffSchemas(snap("MySQL 8.0", a), snap("MySQL 8.0", b), Options{}); r.Summary.Same != 1 {
		t.Errorf("MySQL 表名大小写不同应视为同一张,实际 %+v", r)
	}
	if r := DiffSchemas(snap("PostgreSQL 16", a), snap("PostgreSQL 16", b), Options{}); r.Summary.Same != 0 {
		t.Errorf("PG 目录里是真名,大小写不同就是两张表,实际 %+v", r)
	}
}

func changedTable(t *testing.T, r *Result) TableDiff {
	t.Helper()
	if len(r.Changed) != 1 {
		t.Fatalf("应有一张结构不同的表,实际 %+v", r)
	}
	return r.Changed[0]
}

func TestDiff_ColumnDifferences(t *testing.T) {
	a, b := orders(), orders()
	b.Columns[2].DataType = "decimal(12,2)"                         // amount 类型
	b.Columns[2].Nullable = false                                   // amount 可空
	b.Columns[1].Default = "1"                                      // status 默认值
	b.Columns = append(b.Columns, col("remark", "varchar(255)", 5)) // 只在右边
	a.Columns = append(a.Columns, col("legacy", "int(11)", 6))      // 只在左边
	td := changedTable(t, DiffSchemas(snap("MySQL 8.0", a), snap("MySQL 8.0", b), Options{}))

	got := map[string]ColumnDiff{}
	for _, c := range td.Columns {
		got[c.Column] = c
	}
	if got["remark"].Status != OnlyRight || got["legacy"].Status != OnlyLeft {
		t.Errorf("只在一边的列: %+v", td.Columns)
	}
	amount := got["amount"]
	if amount.Status != Changed || len(amount.Changes) != 2 ||
		amount.Changes[0] != (Change{Attr: "type", Left: "decimal(10,2)", Right: "decimal(12,2)"}) ||
		amount.Changes[1] != (Change{Attr: "nullable", Left: "NULL", Right: "NOT NULL"}) {
		t.Errorf("amount 应有类型、可空两处差异且保留原文: %+v", amount)
	}
	if st := got["status"]; st.Status != Changed || st.Changes[0] != (Change{Attr: "default", Left: "0", Right: "1"}) {
		t.Errorf("status 默认值差异: %+v", st)
	}
	if _, ok := got["id"]; ok {
		t.Error("没变的列不该出现在差异里")
	}
}

// 每条归一化规则:写法不同、意思相同 → 不算差异;意思不同 → 仍是差异。
func TestDiff_Normalization(t *testing.T) {
	cases := []struct {
		name        string
		engine      string
		left, right Column
		same        bool
	}{
		{"int 显示宽度", "MySQL 8.0", col("n", "int(11)", 1), col("n", "int", 1), true},
		{"bigint unsigned 显示宽度", "MySQL 8.0", col("n", "bigint(20) unsigned", 1), col("n", "bigint unsigned", 1), true},
		{"tinyint(1) 是布尔约定,保留", "MySQL 8.0", col("n", "tinyint(1)", 1), col("n", "tinyint", 1), false},
		{"类型大小写与空格", "MySQL 8.0", col("n", "VARCHAR(64)", 1), col("n", "varchar( 64 )", 1), true},
		{"varchar 长度不同", "MySQL 8.0", col("n", "varchar(64)", 1), col("n", "varchar(128)", 1), false},
		{"Oracle NUMBER(*)", "Oracle 19c", col("N", "NUMBER(*)", 1), col("N", "NUMBER", 1), true},
		{"Oracle NUMBER 精度不同", "Oracle 19c", col("N", "NUMBER(10)", 1), col("N", "NUMBER(12)", 1), false},
		{"默认值引号", "MySQL 8.0",
			Column{Name: "s", DataType: "int", HasDefault: true, Default: "'0'", Ordinal: 1},
			Column{Name: "s", DataType: "int", HasDefault: true, Default: "0", Ordinal: 1}, true},
		{"CURRENT_TIMESTAMP 写法", "MySQL 8.0",
			Column{Name: "c", DataType: "datetime", HasDefault: true, Default: "CURRENT_TIMESTAMP", Ordinal: 1},
			Column{Name: "c", DataType: "datetime", HasDefault: true, Default: "current_timestamp()", Ordinal: 1}, true},
		{"PG now() 与类型转换", "PostgreSQL 16",
			Column{Name: "c", DataType: "timestamp", HasDefault: true, Default: "now()", Ordinal: 1},
			Column{Name: "c", DataType: "timestamp", HasDefault: true, Default: "CURRENT_TIMESTAMP", Ordinal: 1}, true},
		{"PG 字符串默认值带 ::类型", "PostgreSQL 16",
			Column{Name: "c", DataType: "varchar(8)", HasDefault: true, Default: "'new'::character varying", Ordinal: 1},
			Column{Name: "c", DataType: "varchar(8)", HasDefault: true, Default: "'new'", Ordinal: 1}, true},
		{"可空列:默认 NULL 等于没有默认值", "MySQL 8.0",
			Column{Name: "c", DataType: "int", Nullable: true, HasDefault: true, Default: "NULL", Ordinal: 1},
			Column{Name: "c", DataType: "int", Nullable: true, Ordinal: 1}, true},
		{"Oracle 默认值带尾部空白", "Oracle 19c",
			Column{Name: "C", DataType: "NUMBER", HasDefault: true, Default: "0 \n", Ordinal: 1},
			Column{Name: "C", DataType: "NUMBER", HasDefault: true, Default: "0", Ordinal: 1}, true},
		{"默认值真的不同", "MySQL 8.0",
			Column{Name: "c", DataType: "int", HasDefault: true, Default: "0", Ordinal: 1},
			Column{Name: "c", DataType: "int", HasDefault: true, Default: "1", Ordinal: 1}, false},
		{"有默认值 vs 没有", "MySQL 8.0",
			Column{Name: "c", DataType: "int", HasDefault: true, Default: "0", Ordinal: 1},
			Column{Name: "c", DataType: "int", Ordinal: 1}, false},
	}
	for _, c := range cases {
		a := Table{Name: "t", Kind: "table", Columns: []Column{c.left}}
		b := Table{Name: "t", Kind: "table", Columns: []Column{c.right}}
		r := DiffSchemas(snap(c.engine, a), snap(c.engine, b), Options{})
		if same := r.Summary.Same == 1; same != c.same {
			t.Errorf("%s:应视为相同=%v,实际 %+v", c.name, c.same, r.Changed)
		}
	}
}

func TestDiff_ColumnOrder(t *testing.T) {
	a, b := orders(), orders()
	// 右边把 amount 挪到最前;PG 删过列后 ordinal 有空洞 —— 比的是相对顺序,不是 ordinal 数值
	b.Columns = []Column{b.Columns[2], b.Columns[0], b.Columns[1], b.Columns[3]}
	for i := range b.Columns {
		b.Columns[i].Ordinal = (i + 1) * 3
	}
	td := changedTable(t, DiffSchemas(snap("MySQL 8.0", a), snap("MySQL 8.0", b), Options{}))
	if len(td.Columns) == 0 || td.Columns[0].Changes[0].Attr != "position" {
		t.Fatalf("列顺序变了应报 position: %+v", td.Columns)
	}
	if r := DiffSchemas(snap("MySQL 8.0", a), snap("MySQL 8.0", b), Options{IgnoreColumnOrder: true}); r.Summary.Same != 1 {
		t.Errorf("勾了忽略列顺序应视为相同: %+v", r.Changed)
	}
	// 只是 ordinal 数值不同、顺序一样:不算差异
	c := orders()
	for i := range c.Columns {
		c.Columns[i].Ordinal = (i + 1) * 10
	}
	if r := DiffSchemas(snap("MySQL 8.0", orders()), snap("MySQL 8.0", c), Options{}); r.Summary.Same != 1 {
		t.Errorf("ordinal 数值不同但顺序一样不该报: %+v", r.Changed)
	}
}

func TestDiff_Comments(t *testing.T) {
	a, b := orders(), orders()
	b.Comment = "订单表"
	b.Columns[2].Comment = "订单金额"
	td := changedTable(t, DiffSchemas(snap("MySQL 8.0", a), snap("MySQL 8.0", b), Options{}))
	if td.Comment == nil || *td.Comment != (Pair{Left: "订单", Right: "订单表"}) || len(td.Columns) != 1 {
		t.Errorf("表注释与列注释差异: %+v", td)
	}
	if r := DiffSchemas(snap("MySQL 8.0", a), snap("MySQL 8.0", b), Options{IgnoreComments: true}); r.Summary.Same != 1 {
		t.Errorf("勾了忽略注释应视为相同: %+v", r.Changed)
	}
}

func TestDiff_PrimaryKeyAndKind(t *testing.T) {
	a, b := orders(), orders()
	b.PrimaryKey = []string{"id", "status"}
	b.Kind = "view"
	td := changedTable(t, DiffSchemas(snap("MySQL 8.0", a), snap("MySQL 8.0", b), Options{}))
	if td.PrimaryKey == nil || *td.PrimaryKey != (Pair{Left: "(id)", Right: "(id, status)"}) {
		t.Errorf("主键差异: %+v", td.PrimaryKey)
	}
	if td.Kind == nil || *td.Kind != (Pair{Left: "table", Right: "view"}) {
		t.Errorf("表 / 视图差异: %+v", td.Kind)
	}
}

func TestDiff_Indexes(t *testing.T) {
	a, b := orders(), orders()
	b.Indexes = []Index{
		{Name: "PRIMARY", Columns: []string{"id"}, Unique: true},
		{Name: "idx_status", Columns: []string{"status", "created_at"}},         // 列组成变了
		{Name: "uk_amount_id", Columns: []string{"amount", "id"}, Unique: true}, // 只是改了名
		{Name: "idx_created", Columns: []string{"created_at"}},                  // 只在右边
	}
	td := changedTable(t, DiffSchemas(snap("MySQL 8.0", a), snap("MySQL 8.0", b), Options{}))
	got := map[string]IndexDiff{}
	for _, d := range td.Indexes {
		got[d.Index] = d
	}
	if d := got["idx_status"]; d.Status != Changed || d.Left != "(status)" || d.Right != "(status, created_at)" {
		t.Errorf("索引列组成变了: %+v", d)
	}
	if got["uk_amount"].Status != OnlyLeft || got["uk_amount_id"].Status != OnlyRight || got["idx_created"].Status != OnlyRight {
		t.Errorf("按名字匹配时,改名 = 左边少一个、右边多一个: %+v", td.Indexes)
	}
	if _, ok := got["PRIMARY"]; ok {
		t.Error("主键索引单独比(PrimaryKey),不该出现在索引差异里")
	}

	// 忽略索引名:按列组成匹配,改名不再算差异
	td = changedTable(t, DiffSchemas(snap("MySQL 8.0", a), snap("MySQL 8.0", b), Options{IgnoreIndexNames: true}))
	for _, d := range td.Indexes {
		if d.Index == "uk_amount" || d.Index == "uk_amount_id" {
			t.Errorf("忽略索引名后,只改了名的唯一索引不该报: %+v", td.Indexes)
		}
	}
}

// Oracle 系统命名的索引 / 约束(SYS_C…)两边不可能同名,不勾忽略也按列组成匹配。
func TestDiff_OracleSystemNamedIndexesMatchByColumns(t *testing.T) {
	a := Table{Name: "T", Kind: "table", Columns: []Column{col("ID", "NUMBER", 1), col("CODE", "VARCHAR2(20)", 2)},
		Indexes: []Index{{Name: "SYS_C008123", Columns: []string{"CODE"}, Unique: true, SystemNamed: true}}}
	b := a
	b.Indexes = []Index{{Name: "SYS_C009981", Columns: []string{"CODE"}, Unique: true, SystemNamed: true}}
	if r := DiffSchemas(snap("Oracle 19c", a), snap("Oracle 19c", b), Options{}); r.Summary.Same != 1 {
		t.Errorf("系统命名的索引列组成相同应视为相同: %+v", r.Changed)
	}
}

func TestDiff_ResultsAreSortedAndStable(t *testing.T) {
	l := snap("MySQL 8.0", Table{Name: "b"}, Table{Name: "a"}, Table{Name: "c"})
	r := snap("MySQL 8.0", Table{Name: "c"}, Table{Name: "d"})
	res := DiffSchemas(l, r, Options{})
	if !reflect.DeepEqual(res.OnlyLeft, []string{"a", "b"}) || !reflect.DeepEqual(res.OnlyRight, []string{"d"}) {
		t.Errorf("表名应排序: %+v", res)
	}
}

// ---- 评审修复(2026-09-29):以下每条都是一个曾经被抹掉的真差异或报出来的假差异 ----

func TestDiff_ReviewFixes_Normalization(t *testing.T) {
	cases := []struct {
		name        string
		engine      string
		left, right Column
		same        bool
	}{
		// 类型里的字面量大小写有意义:整体转小写会抹掉 enum 取值的差异
		{"enum 取值大小写不同是真差异", "MySQL 8.0", col("e", "enum('A','B')", 1), col("e", "enum('a','b')", 1), false},
		{"类型关键字大小写仍不算差异", "MySQL 8.0", col("e", "ENUM('A','B')", 1), col("e", "enum('A','B')", 1), true},
		// zerofill 时显示宽度决定补几个 0,是真差异
		{"zerofill 的宽度不同", "MySQL 8.0", col("n", "int(5) unsigned zerofill", 1), col("n", "int(8) unsigned zerofill", 1), false},
		// Oracle 的几个时间函数不是同一个东西
		{"SYSDATE 与 SYSTIMESTAMP 不同", "Oracle 19c",
			Column{Name: "C", DataType: "DATE", HasDefault: true, Default: "SYSDATE", Ordinal: 1},
			Column{Name: "C", DataType: "DATE", HasDefault: true, Default: "SYSTIMESTAMP", Ordinal: 1}, false},
		{"Oracle 默认值关键字大小写不算差异", "Oracle 19c",
			Column{Name: "C", DataType: "DATE", HasDefault: true, Default: "sysdate", Ordinal: 1},
			Column{Name: "C", DataType: "DATE", HasDefault: true, Default: "SYSDATE", Ordinal: 1}, true},
		// MySQL 的时间默认值带精度时,精度是真差异
		{"CURRENT_TIMESTAMP(3) 与 CURRENT_TIMESTAMP", "MySQL 8.0",
			Column{Name: "c", DataType: "datetime(3)", HasDefault: true, Default: "CURRENT_TIMESTAMP(3)", Ordinal: 1},
			Column{Name: "c", DataType: "datetime(3)", HasDefault: true, Default: "CURRENT_TIMESTAMP", Ordinal: 1}, false},
		{"CURRENT_TIMESTAMP(3) 与 now(3)", "MySQL 8.0",
			Column{Name: "c", DataType: "datetime(3)", HasDefault: true, Default: "CURRENT_TIMESTAMP(3)", Ordinal: 1},
			Column{Name: "c", DataType: "datetime(3)", HasDefault: true, Default: "now(3)", Ordinal: 1}, true},
		// PG:localtimestamp 不带时区,与 now() 不同
		{"PG localtimestamp 与 now() 不同", "PostgreSQL 16",
			Column{Name: "c", DataType: "timestamp", HasDefault: true, Default: "LOCALTIMESTAMP", Ordinal: 1},
			Column{Name: "c", DataType: "timestamp", HasDefault: true, Default: "now()", Ordinal: 1}, false},
		// PG:可空列上 NULL::character varying 就是没有默认值
		{"PG NULL::类型 等于无默认值", "PostgreSQL 16",
			Column{Name: "c", DataType: "varchar(8)", Nullable: true, HasDefault: true, Default: "NULL::character varying", Ordinal: 1},
			Column{Name: "c", DataType: "varchar(8)", Nullable: true, Ordinal: 1}, true},
		// 字符串默认值的大小写是真差异
		{"字符串默认值大小写不同", "MySQL 8.0",
			Column{Name: "c", DataType: "varchar(8)", HasDefault: true, Default: "'New'", Ordinal: 1},
			Column{Name: "c", DataType: "varchar(8)", HasDefault: true, Default: "'new'", Ordinal: 1}, false},
	}
	for _, c := range cases {
		a := Table{Name: "t", Kind: "table", Columns: []Column{c.left}}
		b := Table{Name: "t", Kind: "table", Columns: []Column{c.right}}
		r := DiffSchemas(snap(c.engine, a), snap(c.engine, b), Options{})
		if same := r.Summary.Same == 1; same != c.same {
			t.Errorf("%s:应视为相同=%v,实际 %+v", c.name, c.same, r.Changed)
		}
	}
}

// 与主键同列的唯一索引,只要不是支撑主键的那个,就要照常比 —— 只在一边有时要报出来。
func TestDiff_UniqueIndexOnPrimaryKeyColumnsIsCompared(t *testing.T) {
	a := orders()
	b := orders()
	for i := range a.Indexes {
		a.Indexes[i].Primary = a.Indexes[i].Name == "PRIMARY"
		b.Indexes[i].Primary = b.Indexes[i].Name == "PRIMARY"
	}
	b.Indexes = append(b.Indexes, Index{Name: "uk_id_dup", Columns: []string{"id"}, Unique: true})
	td := changedTable(t, DiffSchemas(snap("MySQL 8.0", a), snap("MySQL 8.0", b), Options{}))
	if len(td.Indexes) != 1 || td.Indexes[0].Index != "uk_id_dup" || td.Indexes[0].Status != OnlyRight {
		t.Errorf("与主键同列的多余唯一索引应报出来: %+v", td.Indexes)
	}
}

// 结构不同的表带上两边完整定义,页面据此做左右对照。一致的表不带(省流量)。
func TestDiff_ChangedTablesCarryFullDefinitions(t *testing.T) {
	a, b := orders(), orders()
	b.Columns[2].DataType = "decimal(12,2)"
	td := changedTable(t, DiffSchemas(snap("MySQL 8.0", a, Table{Name: "t_user"}), snap("MySQL 8.0", b, Table{Name: "t_user"}), Options{}))
	if td.LeftDef == nil || td.RightDef == nil || len(td.LeftDef.Columns) != 4 || td.RightDef.Columns[2].DataType != "decimal(12,2)" {
		t.Errorf("应带两边完整定义: %+v %+v", td.LeftDef, td.RightDef)
	}
}

// 没有默认值时不再写死"(无)"(英文界面会露出中文),留空由前端按语言显示。
func TestDiff_NoDefaultIsEmptyNotChinese(t *testing.T) {
	a := Table{Name: "t", Columns: []Column{{Name: "c", DataType: "int", HasDefault: true, Default: "0", Ordinal: 1}}}
	b := Table{Name: "t", Columns: []Column{{Name: "c", DataType: "int", Ordinal: 1}}}
	td := changedTable(t, DiffSchemas(snap("MySQL 8.0", a), snap("MySQL 8.0", b), Options{}))
	if ch := td.Columns[0].Changes[0]; ch.Attr != "default" || ch.Left != "0" || ch.Right != "" {
		t.Errorf("没有默认值应为空串: %+v", ch)
	}
}

// ---- 第二轮评审修复 ----

func TestDiff_Round2_Normalization(t *testing.T) {
	cases := []struct {
		name        string
		engine      string
		left, right Column
		same        bool
	}{
		// MySQL 与 MariaDB 互比:on update 的时间函数写法不同
		{"on update 写法(MySQL vs MariaDB)", "MySQL 8.0",
			col("c", "timestamp on update CURRENT_TIMESTAMP", 1), col("c", "timestamp on update current_timestamp()", 1), true},
		{"on update 精度不同是真差异", "MySQL 8.0",
			col("c", "datetime(3) on update CURRENT_TIMESTAMP(3)", 1), col("c", "datetime(3) on update CURRENT_TIMESTAMP", 1), false},
		// Oracle 默认值:引号外不分大小写,引号里区分
		{"Oracle 函数写法大小写", "Oracle 19c",
			Column{Name: "C", DataType: "TIMESTAMP(6)", HasDefault: true, Default: "SYSTIMESTAMP(6)", Ordinal: 1},
			Column{Name: "C", DataType: "TIMESTAMP(6)", HasDefault: true, Default: "systimestamp(6)", Ordinal: 1}, true},
		{"Oracle 字符串默认值大小写是真差异", "Oracle 19c",
			Column{Name: "C", DataType: "VARCHAR2(8)", HasDefault: true, Default: "'New'", Ordinal: 1},
			Column{Name: "C", DataType: "VARCHAR2(8)", HasDefault: true, Default: "'new'", Ordinal: 1}, false},
		{"Oracle 表达式里引号外大小写", "Oracle 19c",
			Column{Name: "C", DataType: "VARCHAR2(8)", HasDefault: true, Default: "TO_CHAR(SYSDATE,'YYYY')", Ordinal: 1},
			Column{Name: "C", DataType: "VARCHAR2(8)", HasDefault: true, Default: "to_char(sysdate,'YYYY')", Ordinal: 1}, true},
		// MySQL 的字符串默认值在目录里不带引号,仍区分大小写
		{"MySQL 不带引号的字符串默认值区分大小写", "MySQL 8.0",
			Column{Name: "c", DataType: "varchar(8)", HasDefault: true, Default: "New", Ordinal: 1},
			Column{Name: "c", DataType: "varchar(8)", HasDefault: true, Default: "new", Ordinal: 1}, false},
	}
	for _, c := range cases {
		a := Table{Name: "t", Kind: "table", Columns: []Column{c.left}}
		b := Table{Name: "t", Kind: "table", Columns: []Column{c.right}}
		r := DiffSchemas(snap(c.engine, a), snap(c.engine, b), Options{})
		if same := r.Summary.Same == 1; same != c.same {
			t.Errorf("%s:应视为相同=%v,实际 %+v", c.name, c.same, r.Changed)
		}
	}
}

// MySQL 表达式索引:引号里的字面量大小写有意义,不能随列名一起转小写。
func TestDiff_MySQLExpressionIndexKeepsQuotedCase(t *testing.T) {
	mk := func(expr string) Table {
		return Table{Name: "t", Kind: "table", Columns: []Column{col("doc", "json", 1)},
			Indexes: []Index{{Name: "idx_a", Columns: []string{expr}}}}
	}
	r := DiffSchemas(snap("MySQL 8.0", mk("json_extract(`doc`,'$.A')")), snap("MySQL 8.0", mk("json_extract(`doc`,'$.a')")), Options{})
	if r.Summary.Changed != 1 {
		t.Errorf("表达式里 '$.A' 与 '$.a' 是不同的索引: %+v", r)
	}
	r = DiffSchemas(snap("MySQL 8.0", mk("JSON_EXTRACT(`doc`,'$.A')")), snap("MySQL 8.0", mk("json_extract(`doc`,'$.A')")), Options{})
	if r.Summary.Same != 1 {
		t.Errorf("函数名大小写不算差异: %+v", r.Changed)
	}
}
