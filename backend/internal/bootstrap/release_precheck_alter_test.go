package bootstrap

// 收单检查的第二组:字段是否存在,以及**数据会不会违反即将加上的约束**。
//
// 仍然跑在真的 SQLite 上,而且这三种失败在 SQLite 上都是真会发生的:
//
//	ALTER TABLE t ADD COLUMN c INT NOT NULL   表里有行 → Cannot add a NOT NULL column with default value NULL
//	CREATE UNIQUE INDEX … ON t (c)            有重复值 → UNIQUE constraint failed
//	ALTER TABLE t DROP COLUMN nope            列不存在 → no such column
//
// 也就是说,这组用例挡的不是"我们的判断逻辑自洽",而是"这条语句真的会在目标库上失败,
// 而我们在收单时就说出来了"。
//
// 钉住三件事:
//
//   1. 三种失败各自报出来,**而且分得清是哪一种** —— 字段问题要改脚本,数据问题通常
//      要先清洗数据,两者的下一步动作不同。
//   2. 不该报的时候不报。带默认值的非空列、没有重复值的唯一约束,都必须是干净的 ——
//      一个动不动就报红的检查,很快就没人看了。
//   3. 探不了的时候说"未能核对",不说通过。

import (
	"strings"
	"testing"
)

func issuesOfKindIn(res precheckResult, kind string) []precheckIssue {
	return issuesOfKind(res, kind)
}

// 一、字段已存在 —— 这条加列语句必然失败。
func TestPrecheckAlter_AddingAnExistingColumnIsReported(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	conn := app.realSQLiteConnIn(admin, "dev", "alter-dup-col")
	if r := app.execSQL(admin, conn, `CREATE TABLE t_order (id INTEGER, memo TEXT)`); r.Code != 0 {
		t.Fatalf("建表: %s", r.Msg)
	}

	res := submitForPrecheck(t, app, admin, conn, `ALTER TABLE t_order ADD COLUMN memo TEXT`)

	objs := issuesOfKindIn(res, "object")
	if len(objs) == 0 {
		t.Fatalf("字段已存在没被报出来:%+v", res)
	}
	if !strings.Contains(objs[0].Message, "字段已存在") {
		t.Errorf("话要说清楚,实际 %q", objs[0].Message)
	}
	if !strings.Contains(objs[0].Object, "memo") {
		t.Errorf("要点名是哪个字段,实际 %q", objs[0].Object)
	}
	// 字段一律实时查 —— 结论要如实标注来源,不能含糊成缓存。
	if objs[0].Source != "live" {
		t.Errorf("字段结论的来源应当是 live,实际 %q", objs[0].Source)
	}
}

// 二、字段不存在 —— 删一个没有的列。
func TestPrecheckAlter_DroppingAMissingColumnIsReported(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	conn := app.realSQLiteConnIn(admin, "dev", "alter-missing-col")
	if r := app.execSQL(admin, conn, `CREATE TABLE t_order (id INTEGER)`); r.Code != 0 {
		t.Fatalf("建表: %s", r.Msg)
	}

	res := submitForPrecheck(t, app, admin, conn, `ALTER TABLE t_order DROP COLUMN nope`)

	objs := issuesOfKindIn(res, "object")
	if len(objs) == 0 || !strings.Contains(objs[0].Message, "字段不存在") {
		t.Fatalf("字段不存在没被报出来:%+v", res)
	}
}

// 三、数据违反约束:表里有行,加一个无默认值的非空列。
//
// 这条在 SQLite 上真的会失败,在 MySQL/PG 上同样 —— 它是这类检查里最常见的一种。
func TestPrecheckAlter_NotNullColumnOnANonEmptyTableIsReported(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	conn := app.realSQLiteConnIn(admin, "dev", "alter-notnull")
	if r := app.execSQL(admin, conn, `CREATE TABLE t_order (id INTEGER)`); r.Code != 0 {
		t.Fatalf("建表: %s", r.Msg)
	}
	if r := app.execSQL(admin, conn, `INSERT INTO t_order (id) VALUES (1)`); r.Code != 0 {
		t.Fatalf("灌数据: %s", r.Msg)
	}

	res := submitForPrecheck(t, app, admin, conn, `ALTER TABLE t_order ADD COLUMN n INT NOT NULL`)

	cons := issuesOfKindIn(res, "constraint")
	if len(cons) == 0 {
		t.Fatalf("数据违反约束没被报出来:%+v", res)
	}
	if !strings.Contains(cons[0].Message, "已有数据") {
		t.Errorf("要说清是数据的问题,实际 %q", cons[0].Message)
	}
	// 与字段问题分开:一个要改脚本,一个要先清洗数据。
	if cons[0].Kind != "constraint" {
		t.Errorf("种类应当是 constraint,实际 %q", cons[0].Kind)
	}
}

// 带了默认值就不会失败 —— 不该报。
// 上一条证明它会报,这一条证明它不乱报。
func TestPrecheckAlter_NotNullWithDefaultRaisesNothing(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	conn := app.realSQLiteConnIn(admin, "dev", "alter-notnull-default")
	if r := app.execSQL(admin, conn, `CREATE TABLE t_order (id INTEGER)`); r.Code != 0 {
		t.Fatalf("建表: %s", r.Msg)
	}
	if r := app.execSQL(admin, conn, `INSERT INTO t_order (id) VALUES (1)`); r.Code != 0 {
		t.Fatalf("灌数据: %s", r.Msg)
	}

	res := submitForPrecheck(t, app, admin, conn, `ALTER TABLE t_order ADD COLUMN n INT NOT NULL DEFAULT 0`)

	if n := len(issuesOfKindIn(res, "constraint")); n != 0 {
		t.Errorf("带默认值不会失败,不该报:%+v", res.Issues)
	}
	if res.Errors != 0 {
		t.Errorf("不该有 error:%+v", res.Issues)
	}
}

// 空表上加非空列是合法的 —— 探测要看数据,不是看语句形状。
func TestPrecheckAlter_NotNullOnAnEmptyTableRaisesNothing(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	conn := app.realSQLiteConnIn(admin, "dev", "alter-notnull-empty")
	if r := app.execSQL(admin, conn, `CREATE TABLE t_empty (id INTEGER)`); r.Code != 0 {
		t.Fatalf("建表: %s", r.Msg)
	}

	res := submitForPrecheck(t, app, admin, conn, `ALTER TABLE t_empty ADD COLUMN n INT NOT NULL`)

	if n := len(issuesOfKindIn(res, "constraint")); n != 0 {
		t.Errorf("空表上不会失败,不该报:%+v", res.Issues)
	}
}

// 四、唯一约束撞上重复值。
func TestPrecheckAlter_UniqueOnDuplicateDataIsReported(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	conn := app.realSQLiteConnIn(admin, "dev", "alter-unique")
	if r := app.execSQL(admin, conn, `CREATE TABLE t_user (id INTEGER, email TEXT)`); r.Code != 0 {
		t.Fatalf("建表: %s", r.Msg)
	}
	for _, v := range []string{`(1, 'a@x.io')`, `(2, 'a@x.io')`} {
		if r := app.execSQL(admin, conn, `INSERT INTO t_user (id, email) VALUES `+v); r.Code != 0 {
			t.Fatalf("灌数据: %s", r.Msg)
		}
	}

	res := submitForPrecheck(t, app, admin, conn, `ALTER TABLE t_user ADD UNIQUE (email)`)

	cons := issuesOfKindIn(res, "constraint")
	if len(cons) == 0 {
		t.Fatalf("重复值没被报出来:%+v", res)
	}
	if !strings.Contains(cons[0].Message, "重复值") {
		t.Errorf("要说清是重复值,实际 %q", cons[0].Message)
	}
}

// 没有重复值时不报 —— 同样是"证明它不乱报"。
func TestPrecheckAlter_UniqueOnCleanDataRaisesNothing(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	conn := app.realSQLiteConnIn(admin, "dev", "alter-unique-clean")
	if r := app.execSQL(admin, conn, `CREATE TABLE t_user (id INTEGER, email TEXT)`); r.Code != 0 {
		t.Fatalf("建表: %s", r.Msg)
	}
	for _, v := range []string{`(1, 'a@x.io')`, `(2, 'b@x.io')`} {
		if r := app.execSQL(admin, conn, `INSERT INTO t_user (id, email) VALUES `+v); r.Code != 0 {
			t.Fatalf("灌数据: %s", r.Msg)
		}
	}

	res := submitForPrecheck(t, app, admin, conn, `ALTER TABLE t_user ADD UNIQUE (email)`)

	if n := len(issuesOfKindIn(res, "constraint")); n != 0 {
		t.Errorf("没有重复值,不该报:%+v", res.Issues)
	}
}

// 五、同一批里前面刚建的表,不去查它的字段 —— 它此刻还没有列。
func TestPrecheckAlter_TableCreatedInTheSameBatchIsNotColumnChecked(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	conn := app.realSQLiteConnIn(admin, "dev", "alter-batch")

	res := submitForPrecheck(t, app, admin, conn,
		"CREATE TABLE t_new (id INTEGER);\nALTER TABLE t_new ADD COLUMN memo TEXT;")

	for _, i := range issuesOfKindIn(res, "object") {
		t.Errorf("刚建的表不该报字段问题:%+v", i)
	}
	for _, i := range issuesOfKindIn(res, "constraint") {
		t.Errorf("刚建的表不该报约束问题:%+v", i)
	}
}

// 前一条 ALTER 加的列,后一条再加同名列 —— 那是真的会失败,要报。
// 它同时证明上一条不是靠"字段检查整个没跑"过的。
func TestPrecheckAlter_ColumnAddedEarlierInTheSameBatchIsSeen(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	conn := app.realSQLiteConnIn(admin, "dev", "alter-batch-col")
	if r := app.execSQL(admin, conn, `CREATE TABLE t_order (id INTEGER)`); r.Code != 0 {
		t.Fatalf("建表: %s", r.Msg)
	}

	res := submitForPrecheck(t, app, admin, conn,
		"ALTER TABLE t_order ADD COLUMN memo TEXT;\nALTER TABLE t_order ADD COLUMN memo TEXT;")

	objs := issuesOfKindIn(res, "object")
	if len(objs) == 0 || !strings.Contains(objs[0].Message, "字段已存在") {
		t.Fatalf("同一批里重复加同名列没被报出来:%+v", res)
	}
	if objs[0].Stmt != 2 {
		t.Errorf("报的应当是第 2 条,实际第 %d 条", objs[0].Stmt)
	}
}

// 六、认不出形状的 ALTER 不下结论 —— 而且要说"没查",不能沉默。
func TestPrecheckAlter_UnrecognisedShapeSaysNothingWasChecked(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	conn := app.realSQLiteConnIn(admin, "dev", "alter-unknown")
	if r := app.execSQL(admin, conn, `CREATE TABLE t_order (id INTEGER)`); r.Code != 0 {
		t.Fatalf("建表: %s", r.Msg)
	}

	// 外键形状不在支持之列(见 pkg/sqlutil/alter.go 顶部)。
	res := submitForPrecheck(t, app, admin, conn,
		`ALTER TABLE t_order ADD CONSTRAINT fk FOREIGN KEY (id) REFERENCES t_other(id)`)

	if n := len(issuesOfKindIn(res, "object")) + len(issuesOfKindIn(res, "constraint")); n != 0 {
		t.Errorf("认不出的形状不该下结论:%+v", res.Issues)
	}
	if len(issuesOfKind(res, "skipped")) == 0 {
		t.Errorf("没查就要说没查:%+v", res)
	}
}
