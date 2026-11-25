package gateway

import "testing"

// The verb that governs a WITH statement is its effective action: the main
// clause's verb for a read, the mutation wherever one hides (a CTE body's
// DELETE really deletes — ER9). Keying on the literal WITH filed every CTE
// into the default write capability, refusing read-only CTE exports.
func TestParseVerb_CTE(t *testing.T) {
	cases := map[string]string{
		"WITH t AS (SELECT id FROM users) SELECT * FROM t":                        "SELECT",
		"WITH RECURSIVE cnt(x) AS (VALUES(1)) SELECT x FROM cnt":                  "SELECT",
		"with monthly as (\n select 1\n)\nselect * from monthly":                  "SELECT",
		"WITH d AS (DELETE FROM orders RETURNING id) SELECT count(*) FROM d":      "DELETE",
		"WITH t AS (SELECT 1) UPDATE orders SET x=1":                              "UPDATE",
		"WITH t AS (SELECT 1) INSERT INTO orders SELECT * FROM t":                 "INSERT",
		"WITH t AS (SELECT 'delete me' AS note FROM users) SELECT note FROM t":    "SELECT", // quoted text must not trigger
		"WITH t AS (":                                                             "WITH",   // unparseable stays WITH (write-tier default)
	}
	for sql, want := range cases {
		if got := ParseVerb(sql); got != want {
			t.Errorf("ParseVerb(%q) = %q, want %q", sql, got, want)
		}
	}
	if !IsRead("WITH t AS (SELECT id FROM users) SELECT * FROM t") {
		t.Error("read-only CTE must classify as read")
	}
	if IsRead("WITH d AS (DELETE FROM orders RETURNING id) SELECT count(*) FROM d") {
		t.Error("mutating CTE must NOT classify as read")
	}
}

// 与写操作同名的函数 REPLACE()、INSERT()、TRUNCATE() 不是写操作(2026-10 生产:生成分区 DDL 的只读
// WITH RECURSIVE 查询用了 REPLACE(...),整条被当成写执行,查询结果一行都没显示)。
func TestParseVerb_CTEWithSameNameFunctionsIsARead(t *testing.T) {
	reads := []string{
		"WITH p AS (SELECT REPLACE(partition_description, '''', '') AS d FROM information_schema.partitions) SELECT d FROM p",
		"WITH RECURSIVE g AS (SELECT 1 AS i UNION ALL SELECT i + 1 FROM g WHERE i < 3) SELECT INSERT('abcdef', 2, 3, 'XYZ'), TRUNCATE(1.2345, 2) FROM g",
		"WITH t AS (SELECT replace (name, 'a', 'b') AS n FROM users) SELECT n FROM t", // 函数名与括号之间有空格
	}
	for _, sql := range reads {
		if got := ParseVerb(sql); got != "SELECT" {
			t.Errorf("ParseVerb(%q) = %q, want SELECT", sql, got)
		}
		if !IsRead(sql) {
			t.Errorf("只读 CTE 用了同名函数,仍应是读: %q", sql)
		}
	}
	// 真的写操作照样认得出来
	writes := map[string]string{
		"WITH t AS (SELECT 1 AS id) INSERT INTO orders SELECT id FROM t":                    "INSERT",
		"WITH t AS (SELECT REPLACE(code, 'a', 'b') AS c FROM src) DELETE FROM orders WHERE 1=0": "DELETE",
		"WITH t AS (SELECT 1) REPLACE INTO orders SELECT * FROM t":                          "REPLACE",
	}
	for sql, want := range writes {
		if got := ParseVerb(sql); got != want {
			t.Errorf("ParseVerb(%q) = %q, want %q", sql, got, want)
		}
		if IsRead(sql) {
			t.Errorf("写操作不该被当成读: %q", sql)
		}
	}
}
