package gateway

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"
)

// DESC 的列重复(现场 uat-g13/g16/g23,COLORGAME_UAT 下 desc T_BINGO_GIFT_SETUP 每列两遍):
// 不带 schema 时只按 TABLE_NAME 过滤,同名表在两个 schema 各有一份,两份列按 COLUMN_ID
// 交错排出来。这组用例不连库,只看翻译出来的 SQL 有没有把属主钉死。

func TestOracleSQLPlus_DescribeUnqualifiedIsPinnedToCurrentSchema(t *testing.T) {
	got, ok := OracleSQLPlus("desc T_BINGO_GIFT_SETUP;")
	if !ok {
		t.Fatal("DESC 应当被翻译")
	}
	up := strings.ToUpper(got)
	if !strings.Contains(up, "OWNER = SYS_CONTEXT('USERENV','CURRENT_SCHEMA')") {
		t.Errorf("不带 schema 时应先按当前 schema 解析(终端标签切过 schema),实际: %q", got)
	}
	if !strings.Contains(up, "WHERE ROWNUM = 1") {
		t.Errorf("名字应只解析成一个属主,实际: %q", got)
	}
	if !IsRead(got) {
		t.Errorf("翻译结果必须是读: %q", got)
	}
}

func TestOracleSQLPlus_DescribeQualifiedSkipsPublicAndFallback(t *testing.T) {
	got, _ := OracleSQLPlus("DESC colorgame_uat.t_bingo_gift_setup")
	if !strings.Contains(got, "OWNER = 'COLORGAME_UAT'") {
		t.Errorf("带 schema 时只查那个 schema,实际: %q", got)
	}
	if strings.Contains(got, "'PUBLIC'") || strings.Contains(got, "CURRENT_SCHEMA") || strings.Contains(got, "COUNT(DISTINCT") {
		t.Errorf("带 schema 时不该再按公共同义词 / 当前 schema / 唯一属主回落,实际: %q", got)
	}
}

// 现场(非 23ai 的 Oracle)报 ORA-00904: "PRI":ORDER BY 直接挂在 UNION ALL 上,老版本在内联视图里
// 认不出复合查询的别名。23ai 不报,实库用例测不出来,所以这里钉住写法:排序必须在包了一层的视图外面。
func TestOracleSQLPlus_DescribeDoesNotOrderTheUnionDirectly(t *testing.T) {
	for _, in := range []string{"desc t_bingo_gift_setup", "desc colorgame_uat.t_bingo_gift_setup"} {
		got, _ := OracleSQLPlus(in)
		if !strings.Contains(got, ") ORDER BY PRI) WHERE ROWNUM = 1") {
			t.Errorf("%s: 排序应作用在包了一层的视图上,实际: %q", in, got)
		}
		if strings.Contains(got, "= 1 ORDER BY") || strings.Contains(got, "IS NULL ORDER BY") {
			t.Errorf("%s: ORDER BY 不能直接挂在 UNION ALL 的最后一个分支后面,实际: %q", in, got)
		}
	}
}

// 真实 Oracle:同名表在两个 schema、表有同名公共/私有同义词、列有注释 —— desc 都只出一份,
// 而且出的是**该出的那一份**(两张同名表故意差一列,好分辨解析到了谁)。
func TestOracleDescribe_RealOracle_NoDuplicateColumns(t *testing.T) {
	conn := testOracle(t)
	drv, dsn, _ := engineDriver(conn)
	db, err := sql.Open(drv, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	cleanup := func() {
		for _, s := range []string{
			"DROP PUBLIC SYNONYM VDESC_GIFT",
			"DROP USER VDESC_A CASCADE", "DROP USER VDESC_B CASCADE", "DROP USER VDESC_C CASCADE",
		} {
			_, _ = db.Exec(s)
		}
	}
	cleanup()
	defer cleanup()
	for _, s := range []string{
		"CREATE USER VDESC_A IDENTIFIED BY x QUOTA UNLIMITED ON USERS",
		"CREATE USER VDESC_B IDENTIFIED BY x QUOTA UNLIMITED ON USERS",
		"CREATE USER VDESC_C IDENTIFIED BY x QUOTA UNLIMITED ON USERS",
		"CREATE TABLE VDESC_A.VDESC_GIFT (GMTYPE VARCHAR2(10) NOT NULL, GIFT_ID NUMBER(10) NOT NULL, PRICE NUMBER(10,2))",
		"CREATE TABLE VDESC_B.VDESC_GIFT (GMTYPE VARCHAR2(10) NOT NULL, GIFT_ID NUMBER(10) NOT NULL, PRICE NUMBER(10,2), B_ONLY DATE)",
		"COMMENT ON COLUMN VDESC_A.VDESC_GIFT.GMTYPE IS 'game type'",
		"COMMENT ON COLUMN VDESC_B.VDESC_GIFT.GMTYPE IS 'game type'",
		"CREATE PUBLIC SYNONYM VDESC_GIFT FOR VDESC_B.VDESC_GIFT",
		"CREATE SYNONYM VDESC_C.VDESC_GIFT FOR VDESC_A.VDESC_GIFT",
		"CREATE TABLE VDESC_B.VDESC_ONLY_B (Y NUMBER)",
	} {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}

	const a = "GMTYPE,GIFT_ID,PRICE"
	const b = "GMTYPE,GIFT_ID,PRICE,B_ONLY"
	for _, c := range []struct{ schema, stmt, want string }{
		{"VDESC_A", "desc vdesc_gift;", a},           // 当前 schema 的表,不混进 B 的同名表
		{"VDESC_B", "desc vdesc_gift", b},            // 当前 schema 的表,不被公共同义词带走
		{"VDESC_C", "DESCRIBE vdesc_gift", a},        // 当前 schema 的私有同义词 → A
		{"", "desc vdesc_gift", b},                   // 登录用户自己没有 → 公共同义词 → B
		{"VDESC_A", "desc vdesc_b.vdesc_gift", b},    // 带 schema 只查那个 schema
		{"VDESC_C", "desc vdesc_c.vdesc_gift", a},    // 带 schema 也认那个 schema 的私有同义词
		{"VDESC_A", "desc vdesc_only_b", "Y"},        // 只有一个属主有:沿用从前能看到列的行为
		{"VDESC_A", "desc vdesc_a.vdesc_only_b", ""}, // 带 schema 不回落
	} {
		cc := *conn
		cc.TargetSchema = c.schema
		res, err := RealRun(context.Background(), &cc, c.stmt, 30*time.Second)
		if err != nil {
			t.Fatalf("[%s] %s: %v", c.schema, c.stmt, err)
		}
		var names []string
		for _, row := range res.Data {
			names = append(names, row[0])
		}
		if got := strings.Join(names, ","); got != c.want {
			t.Errorf("[%s] %s: 列应为 %q,实际 %q", c.schema, c.stmt, c.want, got)
		}
	}
}
