package review

// 时间类型字段不强制 NOT NULL(TiDB / MySQL / Oracle / DWS 规范)。
//
// 每个引擎都测三件事:时间列可空不报、非时间列可空照报、同一张表里混写时只报
// 非时间列。带精度、带时区、INTERVAL 这些多词写法单独覆盖 —— 类型名是从列定义里
// 切出来的,写法一变就可能切错。

import (
	"strings"
	"testing"
)

// onlyCols 断言规则只报了 want 里的列,一个时间列都没混进去。
func onlyCols(t *testing.T, code, dialect, sql string, want []string, notWant []string) {
	t.Helper()
	msgs := fireOne(t, code, dialect, sql)
	joined := strings.Join(msgs, "\n")
	if len(msgs) != len(want) {
		t.Fatalf("%s 应报 %d 条(%v),实际 %d 条: %v\nSQL: %s", code, len(want), want, len(msgs), msgs, sql)
	}
	for _, w := range want {
		if !strings.Contains(joined, "列 "+w+" ") {
			t.Errorf("%s 应报列 %s,实际: %v", code, w, msgs)
		}
	}
	for _, n := range notWant {
		if strings.Contains(joined, "列 "+n+" ") {
			t.Errorf("%s 不应报时间列 %s,实际: %v", code, n, msgs)
		}
	}
}

func TestNotNullExemptsTemporalTiDB(t *testing.T) {
	const code = "tidb.require.col.not.null"
	quiet(t, code, DialectTiDB, "CREATE TABLE t_a (id BIGINT PRIMARY KEY, created_at DATETIME COMMENT 'c') COMMENT 'x';")
	quiet(t, code, DialectTiDB, "CREATE TABLE t_a (id BIGINT PRIMARY KEY, ts TIMESTAMP(6) NULL DEFAULT NULL, d DATE, tm TIME(3), y YEAR, dt DATETIME (3)) COMMENT 'x';")
	quiet(t, code, DialectTiDB, "ALTER TABLE t_a ADD COLUMN updated_at TIMESTAMP(6) NULL;")
	hits(t, code, DialectTiDB, "CREATE TABLE t_a (id BIGINT PRIMARY KEY, name VARCHAR(64)) COMMENT 'x';", "name")
	hits(t, code, DialectTiDB, "ALTER TABLE t_a ADD COLUMN amount DECIMAL(10,2);", "amount")
	onlyCols(t, code, DialectTiDB,
		"CREATE TABLE t_a (id BIGINT PRIMARY KEY, name VARCHAR(64), created_at DATETIME(3), memo TEXT, `ts` TIMESTAMP) COMMENT 'x';",
		[]string{"name", "memo"}, []string{"created_at", "ts"})
}

func TestNotNullExemptsTemporalMySQL(t *testing.T) {
	const code = "ddl.require.col.not.null"
	quiet(t, code, DialectMySQL, "CREATE TABLE t_a (id BIGINT PRIMARY KEY, `created_at` DATETIME(6) DEFAULT NULL, d DATE, tm TIME, y YEAR(4), ts TIMESTAMP NULL);")
	quiet(t, code, DialectMySQL, "ALTER TABLE t_a ADD COLUMN deleted_at DATETIME(3) NULL COMMENT 'd', ADD d2 DATE;")
	hits(t, code, DialectMySQL, "CREATE TABLE t_a (id BIGINT PRIMARY KEY, status TINYINT);", "status")
	hits(t, code, DialectMySQL, "ALTER TABLE t_a ADD COLUMN remark VARCHAR(255);", "remark")
	onlyCols(t, code, DialectMySQL,
		"ALTER TABLE t_a ADD COLUMN deleted_at DATETIME(3), ADD COLUMN remark VARCHAR(255);",
		[]string{"remark"}, []string{"deleted_at"})
}

func TestNotNullExemptsTemporalOracle(t *testing.T) {
	const code = "ddl.require.col.not.null"
	quiet(t, code, DialectOracle, `CREATE TABLE t_a (
		id NUMBER(19) PRIMARY KEY,
		d DATE,
		ts TIMESTAMP,
		ts6 TIMESTAMP(6),
		tz TIMESTAMP(6) WITH TIME ZONE,
		ltz TIMESTAMP WITH LOCAL TIME ZONE,
		ds INTERVAL DAY(2) TO SECOND(6),
		ym INTERVAL YEAR TO MONTH
	);`)
	quiet(t, code, DialectOracle, "ALTER TABLE t_a ADD (expire_at TIMESTAMP(3) WITH LOCAL TIME ZONE, born DATE);")
	hits(t, code, DialectOracle, "CREATE TABLE t_a (id NUMBER(19) PRIMARY KEY, name VARCHAR2(64));", "name")
	hits(t, code, DialectOracle, "ALTER TABLE t_a ADD (qty NUMBER(10));", "qty")
	onlyCols(t, code, DialectOracle,
		`CREATE TABLE t_a (id NUMBER(19) PRIMARY KEY, name VARCHAR2(64), CREATED_AT TIMESTAMP(6) WITH TIME ZONE, d DATE);`,
		[]string{"name"}, []string{"CREATED_AT", "d"})
	// 工具导出的 Oracle DDL 列名都带双引号:引号里的内容在审查前被抹成空格,
	// 类型名得跳过整个引号块再取,否则时间列会被当成"类型未知"照报。
	quiet(t, code, DialectOracle,
		`CREATE TABLE "APP"."T_A" ("ID" NUMBER(19) PRIMARY KEY, "CREATED_AT" TIMESTAMP (6) WITH LOCAL TIME ZONE, "BORN" DATE, "MY COL" INTERVAL DAY (2) TO SECOND (6));`)
	if msgs := fireOne(t, code, DialectOracle,
		`CREATE TABLE "T_A" ("ID" NUMBER(19) PRIMARY KEY, "NAME" VARCHAR2(64), "CREATED_AT" DATE);`); len(msgs) != 1 {
		t.Errorf("引号列名:只应报 NAME 一条,实际 %v", msgs)
	}
}

func TestNotNullExemptsTemporalDWS(t *testing.T) {
	const code = "dws.require.col.not.null"
	quiet(t, code, DialectDWS, `CREATE TABLE g_ods_a (
		id BIGINT NOT NULL,
		d date,
		ts timestamp(6),
		tz timestamp with time zone,
		tz2 timestamptz,
		ntz timestamp(3) without time zone,
		tm time,
		ttz time with time zone,
		ttz2 timetz,
		iv interval,
		ivd interval day to second(3),
		sdt smalldatetime
	) DISTRIBUTE BY HASH(id);`)
	quiet(t, code, DialectDWS, "ALTER TABLE g_ods_a ADD COLUMN etl_time timestamp with time zone;")
	quiet(t, code, DialectDWS, `CREATE TABLE "g_ods_a" ("id" BIGINT NOT NULL, "etl_time" timestamp(6) with time zone) DISTRIBUTE BY HASH("id");`)
	hits(t, code, DialectDWS, "CREATE TABLE g_ods_a (id BIGINT NOT NULL, name varchar(64)) DISTRIBUTE BY HASH(id);", "name")
	hits(t, code, DialectDWS, "ALTER TABLE g_ods_a ADD COLUMN amt numeric(18,2);", "amt")
	onlyCols(t, code, DialectDWS,
		"CREATE TABLE g_ods_a (id BIGINT NOT NULL, memo text, etl_time timestamptz, biz_date date) DISTRIBUTE BY HASH(id);",
		[]string{"memo"}, []string{"etl_time", "biz_date"})
}

// 类型名切分本身:列名长得像时间类型、或数组类型,都不能被当成时间列。
func TestIsTemporalTypeBoundaries(t *testing.T) {
	for typ, want := range map[string]bool{
		"DATETIME(3)": true, "TIMESTAMP(6)": true, "timestamptz": true, "INTERVAL": true,
		"date": true, "YEAR(4)": true, "smalldatetime": true,
		"timestamp[]": false, "VARCHAR(64)": false, "datetime2x": false, "": false, "BIGINT": false,
	} {
		if got := isTemporalType(typ); got != want {
			t.Errorf("isTemporalType(%q) = %v, want %v", typ, got, want)
		}
	}
	// 列名叫 date / time,类型却是字符串 —— 照样要报。
	hits(t, "tidb.require.col.not.null", DialectTiDB,
		"CREATE TABLE t_a (id BIGINT PRIMARY KEY, `date` VARCHAR(10)) COMMENT 'x';", "date")
}
