package sqlparse

import (
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func one(t *testing.T, dialect, sql string) Statement {
	t.Helper()
	r := Parse(dialect, sql)
	if r.Status != StatusOK || len(r.Statements) != 1 {
		t.Fatalf("%s %q: status=%s n=%d err=%s", dialect, sql, r.Status, len(r.Statements), r.Err)
	}
	return r.Statements[0]
}

func TestStatementFields(t *testing.T) {
	mysqlFamily := []string{DialectMySQL, DialectTiDB}
	all := []string{DialectMySQL, DialectTiDB, DialectOracle}
	cases := []struct {
		dialects []string
		sql      string
		kind     Kind
		verb     string
		table    string
		where    bool
		readOnly bool
		planOnly bool
	}{
		{all, "SELECT a FROM t WHERE id=1", KindSelect, "SELECT", "t", true, true, false},
		{all, "SELECT a FROM t", KindSelect, "SELECT", "t", false, true, false},
		{all, "SELECT a FROM t FOR UPDATE", KindSelect, "SELECT", "t", false, false, false},
		{all, "INSERT INTO t (a) VALUES (1)", KindInsert, "INSERT", "t", false, false, false},
		{all, "UPDATE t SET a=1 WHERE id=1", KindUpdate, "UPDATE", "t", true, false, false},
		{all, "UPDATE t SET a=1", KindUpdate, "UPDATE", "t", false, false, false},
		{all, "DELETE FROM t WHERE id=1", KindDelete, "DELETE", "t", true, false, false},
		{all, "DELETE FROM t", KindDelete, "DELETE", "t", false, false, false},
		{all, "DROP TABLE t", KindDDL, "DROP", "t", false, false, false},
		{all, "TRUNCATE TABLE t", KindDDL, "TRUNCATE", "t", false, false, false},
		{all, "CREATE TABLE t (id INT)", KindDDL, "CREATE", "t", false, false, false},
		{all, "ALTER TABLE t ADD c INT", KindDDL, "ALTER", "t", false, false, false},
		{all, "GRANT SELECT ON t TO u", KindDCL, "GRANT", "", false, false, false},
		{all, "COMMIT", KindTxn, "COMMIT", "", false, false, false},

		// 字面量和注释里的词不算数:这正是文本扫描容易错的地方。
		{all, "SELECT 'DELETE FROM x' FROM t", KindSelect, "SELECT", "t", false, true, false},
		{all, "SELECT REPLACE(a,'x','y') FROM t", KindSelect, "SELECT", "t", false, true, false},
		{all, "UPDATE t SET a='WHERE x'", KindUpdate, "UPDATE", "t", false, false, false},
		{all, "DELETE FROM t /* WHERE id=1 */", KindDelete, "DELETE", "t", false, false, false},

		{mysqlFamily, "(SELECT a FROM t) UNION (SELECT a FROM t)", KindSelect, "SELECT", "t", false, true, false},
		{mysqlFamily, "WITH c AS (SELECT 1) SELECT * FROM c", KindSelect, "SELECT", "c", false, true, false},
		{mysqlFamily, "REPLACE INTO t (a) VALUES (1)", KindInsert, "REPLACE", "t", false, false, false},
		{mysqlFamily, "SHOW CREATE TABLE t", KindRead, "SHOW", "t", false, true, false},
		{mysqlFamily, "SET NAMES utf8mb4", KindSession, "SET", "", false, false, false},
		{mysqlFamily, "RENAME TABLE t TO u", KindDDL, "RENAME", "t", false, false, false},
		{mysqlFamily, "SELECT a FROM t INTO OUTFILE '/tmp/p'", KindSelect, "SELECT", "t", false, false, false},
		{mysqlFamily, "SELECT a FROM t WHERE id IN (SELECT id FROM u FOR UPDATE)", KindSelect, "SELECT", "t", true, false, false},
		{mysqlFamily, "EXPLAIN DELETE FROM t", KindDelete, "DELETE", "t", false, true, true},
		{mysqlFamily, "EXPLAIN SELECT a FROM t", KindSelect, "SELECT", "t", false, true, true},
		{mysqlFamily, "EXPLAIN ANALYZE SELECT a FROM t", KindSelect, "SELECT", "t", false, true, false},

		{[]string{DialectOracle}, "SELECT a FROM t UNION SELECT a FROM t", KindSelect, "SELECT", "t", false, true, false},
		{[]string{DialectOracle}, "MERGE INTO t USING s ON (t.id=s.id) WHEN MATCHED THEN UPDATE SET t.a=s.a", KindMerge, "MERGE", "t", false, false, false},
		{[]string{DialectOracle}, "BEGIN UPDATE t SET a=1; COMMIT; END;", KindPLSQL, "BEGIN", "", false, false, false},
		{[]string{DialectOracle}, "CREATE OR REPLACE PACKAGE p AS PROCEDURE q(a IN NUMBER); END p;", KindDDL, "CREATE", "", false, false, false},
		{[]string{DialectOracle}, "EXPLAIN PLAN FOR DELETE FROM t", KindDelete, "DELETE", "t", false, true, true},
	}
	for _, c := range cases {
		for _, d := range c.dialects {
			st := one(t, d, c.sql)
			if st.Kind != c.kind || st.Verb != c.verb || st.HasWhere != c.where || st.ReadOnly != c.readOnly || st.PlanOnly != c.planOnly {
				t.Errorf("%s %q: 实际 %+v", d, c.sql, st)
			}
			if c.table != "" && (len(st.Tables) == 0 || !strings.EqualFold(st.Tables[0].Name, c.table)) {
				t.Errorf("%s %q: 表名应为 %s,实际 %+v", d, c.sql, c.table, st.Tables)
			}
		}
	}
}

func TestTablesKeepSchemaAndDedupe(t *testing.T) {
	const q = "SELECT 1 FROM db.a JOIN b ON 1=1 WHERE EXISTS (SELECT 1 FROM db.a)"
	lower := []Table{{Schema: "db", Name: "a"}, {Name: "b"}}
	for _, d := range []string{DialectMySQL, DialectTiDB} {
		if st := one(t, d, q); !reflect.DeepEqual(st.Tables, lower) {
			t.Errorf("%s: 实际 %+v", d, st.Tables)
		}
	}
	upper := []Table{{Schema: "DB", Name: "A"}, {Name: "B"}}
	if st := one(t, DialectOracle, q); !reflect.DeepEqual(st.Tables, upper) {
		t.Errorf("oracle: 实际 %+v", st.Tables)
	}
}

// 只有 TiDB 认的语法:引擎标签填错时,这类语句在 MySQL 方言上解析失败,调用方退回现有逻辑。
func TestDialectsAreNotInterchangeable(t *testing.T) {
	const autoRandom = "CREATE TABLE t (id BIGINT AUTO_RANDOM PRIMARY KEY) SHARD_ROW_ID_BITS=4"
	if r := Parse(DialectTiDB, autoRandom); r.Status != StatusOK {
		t.Errorf("TiDB 应认 AUTO_RANDOM: %s", r.Err)
	}
	if r := Parse(DialectMySQL, autoRandom); r.Status != StatusError {
		t.Errorf("MySQL 方言应不认 AUTO_RANDOM,实际 %s", r.Status)
	}
	const trigger = "CREATE TRIGGER tr BEFORE INSERT ON t FOR EACH ROW SET NEW.a=1"
	if r := Parse(DialectMySQL, trigger); r.Status != StatusOK {
		t.Errorf("MySQL 应认触发器: %s", r.Err)
	}
}

func TestMultipleAndEmpty(t *testing.T) {
	for _, d := range []string{DialectMySQL, DialectTiDB, DialectOracle} {
		if r := Parse(d, "SELECT 1 FROM t; DROP TABLE t"); r.Status != StatusOK || len(r.Statements) != 2 {
			t.Errorf("%s 两条语句: status=%s n=%d err=%s", d, r.Status, len(r.Statements), r.Err)
		}
		if r := Parse(d, "   "); r.Status != StatusOK || len(r.Statements) != 0 {
			t.Errorf("%s 空白: status=%s n=%d err=%s", d, r.Status, len(r.Statements), r.Err)
		}
		if r := Parse(d, "/* 只有注释 */"); r.Status != StatusOK || len(r.Statements) != 0 {
			t.Errorf("%s 注释: status=%s n=%d err=%s", d, r.Status, len(r.Statements), r.Err)
		}
	}
}

func TestSyntaxErrorAndUnsupported(t *testing.T) {
	for _, d := range []string{DialectMySQL, DialectTiDB, DialectOracle} {
		if r := Parse(d, "SELEC 1 FORM"); r.Status != StatusError || r.Err == "" {
			t.Errorf("%s: status=%s err=%q", d, r.Status, r.Err)
		}
	}
	for _, d := range []string{DialectDWS, "generic", "", "postgres"} {
		if r := Parse(d, "SELECT 1"); r.Status != StatusUnsupported {
			t.Errorf("%q: status=%s", d, r.Status)
		}
	}
}

func TestWritesFile(t *testing.T) {
	for _, d := range []string{DialectMySQL, DialectTiDB} {
		if st := one(t, d, "SELECT a FROM t INTO OUTFILE '/tmp/p'"); !st.WritesFile || st.ReadOnly {
			t.Errorf("%s: %+v", d, st)
		}
		if st := one(t, d, "SELECT a FROM t"); st.WritesFile {
			t.Errorf("%s: %+v", d, st)
		}
		// 可执行注释里的 INTO OUTFILE 服务器会执行,解析器也要读到。
		if st := one(t, d, "SELECT a FROM t /*!50000 INTO OUTFILE '/tmp/p' */"); !st.WritesFile {
			t.Errorf("%s 可执行注释: %+v", d, st)
		}
	}
	if st := one(t, DialectMySQL, "SELECT a FROM t INTO DUMPFILE '/tmp/p'"); !st.WritesFile {
		t.Errorf("dumpfile: %+v", st)
	}
}

// 默认 SQL mode 下双引号是字符串,不是标识符。服务器开了 ANSI_QUOTES 时读法不同,所以这种语句的
// 解析结果只能当旁证。
func TestDoubleQuotesAreStrings(t *testing.T) {
	for _, d := range []string{DialectMySQL, DialectTiDB} {
		if st := one(t, d, `SELECT "a" FROM t`); st.Verb != "SELECT" {
			t.Errorf("%s: %+v", d, st)
		}
		if r := Parse(d, `UPDATE t SET "a"=1`); r.Status != StatusError {
			t.Errorf("%s: 双引号列名默认应解析失败,实际 %s", d, r.Status)
		}
	}
	// Oracle 的双引号一直是标识符。
	if st := one(t, DialectOracle, `SELECT "a" FROM "T"`); st.Tables[0].Name != "T" {
		t.Errorf("oracle: %+v", st)
	}
}

// 恶意输入不能拖垮进程。Go 的栈溢出是致命错误,recover 接不住,所以嵌套要靠解析器自己的上限。
func TestHostileInput(t *testing.T) {
	big := strings.Repeat("(", MaxStatementBytes-16)
	hostile := []string{
		"SELECT " + big,
		"SELECT " + strings.Repeat("(", 200000) + "1" + strings.Repeat(")", 200000),
		"SELECT " + strings.Repeat("NOT ", 200000) + "1",
		"SELECT " + strings.Repeat("- ", 400000) + "1",
		"SELECT " + strings.Repeat("CASE WHEN 1 THEN ", 50000) + "1",
		"SELECT 1 FROM " + strings.Repeat("(SELECT 1 FROM ", 50000) + "t",
		strings.Repeat("BEGIN ", 150000),
		"'",
		"/*",
		"\x00",
		"SELECT '" + strings.Repeat("x", MaxStatementBytes-16),
	}
	for _, d := range []string{DialectMySQL, DialectTiDB, DialectOracle} {
		for i, s := range hostile {
			start := time.Now()
			_ = Parse(d, s)
			if el := time.Since(start); el > parseTimeout+time.Second {
				t.Errorf("%s 恶意输入 %d 用时 %s", d, i, el)
			}
		}
		if r := Parse(d, strings.Repeat("x", MaxStatementBytes+1)); r.Status != StatusUnsupported {
			t.Errorf("%s 超长: %s", d, r.Status)
		}
		if r := Parse(d, "SELECT '\xff'"); r.Status != StatusUnsupported {
			t.Errorf("%s 非法 UTF-8: %s", d, r.Status)
		}
	}
}

func TestParserPanicBecomesError(t *testing.T) {
	r := runParser(func(string) ([]Statement, error) { panic("boom") }, "SELECT 1")
	if r.Status != StatusError || !strings.Contains(r.Err, "boom") {
		t.Fatalf("%+v", r)
	}
}

// Oracle 解析器在深层 CASE 上要跑 11 秒。调用方在请求路径上,不能等它。
func TestSlowParseTimesOut(t *testing.T) {
	start := time.Now()
	r := Parse(DialectOracle, "SELECT "+strings.Repeat("CASE WHEN 1 THEN ", 50000)+"1")
	if r.Status != StatusError || r.Err != "解析超时" {
		t.Fatalf("%+v", r)
	}
	if el := time.Since(start); el > parseTimeout+time.Second {
		t.Fatalf("用时 %s", el)
	}
}

func TestBusyDoesNotQueue(t *testing.T) {
	var held int
	for held < maxInFlight {
		select {
		case inFlight <- struct{}{}:
			held++
			continue
		default:
		}
		time.Sleep(10 * time.Millisecond) // 等前面超时的解析让出名额
	}
	defer func() {
		for ; held > 0; held-- {
			<-inFlight
		}
	}()
	if r := Parse(DialectMySQL, "SELECT 1"); r.Status != StatusUnsupported {
		t.Fatalf("%+v", r)
	}
}

func TestConcurrent(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				for _, d := range []string{DialectMySQL, DialectTiDB, DialectOracle} {
					if st := Parse(d, "UPDATE t SET a=1 WHERE id=1").Statements; len(st) != 1 || st[0].Verb != "UPDATE" {
						t.Errorf("%s: %+v", d, st)
					}
				}
			}
		}()
	}
	wg.Wait()
}

func BenchmarkScript1MB(b *testing.B) {
	stmt := "UPDATE orders SET status=1, note='x' WHERE id=42"
	n := (1 << 20) / len(stmt)
	for _, d := range []string{DialectMySQL, DialectTiDB, DialectOracle} {
		b.Run(d, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				for j := 0; j < n; j++ {
					Parse(d, stmt)
				}
			}
		})
	}
}

func BenchmarkParse(b *testing.B) {
	short := "UPDATE orders SET status=1 WHERE id=42"
	long := "CREATE TABLE t (" + strings.Repeat("c1 VARCHAR(10) NOT NULL DEFAULT 'x' COMMENT 'y',\n", 200) + "id INT PRIMARY KEY)"
	for _, d := range []string{DialectMySQL, DialectTiDB, DialectOracle} {
		if d == DialectOracle {
			long = "CREATE TABLE t (" + strings.Repeat("c1 VARCHAR2(10) DEFAULT 'x' NOT NULL,\n", 200) + "id INT PRIMARY KEY)"
		}
		sql := long
		b.Run(d+"/short", func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				Parse(d, short)
			}
		})
		b.Run(d+"/ddl200cols", func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				Parse(d, sql)
			}
		})
	}
}
