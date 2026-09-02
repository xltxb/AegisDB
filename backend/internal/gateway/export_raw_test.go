package gateway

// 导出这一路的脱敏开关。
//
// 默认必须打码 —— 一份带身份证号的 CSV 落到磁盘、发进聊天工具,比在终端上看一眼跑得
// 远得多,这正是脱敏在导出这一路最要紧的原因。原值只有一条路可以拿到,而那条路的
// 名字必须刺眼(RealQueryEachRaw),否则读代码的人看不出这行调用是不是把明文写了出去。

import (
	"strings"
	"testing"
	"time"
)

func withIDCardRule(t *testing.T) {
	t.Helper()
	prev := SensitiveRulesProvider
	SensitiveRulesProvider = func() []SensitiveRule {
		return []SensitiveRule{{Table: "*", Column: "id_card", Style: "mask"}}
	}
	t.Cleanup(func() { SensitiveRulesProvider = prev })
}



// 流式导出默认打码。
func TestExport_StreamsMaskedByDefault(t *testing.T) {
	withIDCardRule(t)
	conn := sqliteConn(t)
	const plain = "310101199001011234"
	if _, err := RealRun(conn, "CREATE TABLE t_person (id INTEGER PRIMARY KEY, id_card TEXT)", 5*time.Second); err != nil {
		t.Fatalf("建表: %v", err)
	}
	if _, err := RealRun(conn, "INSERT INTO t_person (id, id_card) VALUES (1, '"+plain+"')", 5*time.Second); err != nil {
		t.Fatalf("插入: %v", err)
	}

	var got []string
	err := RealQueryEach(conn, "SELECT id, id_card FROM t_person", 5*time.Second,
		func([]string) error { return nil },
		func(row []string) error { got = append(got, strings.Join(row, ",")); return nil })
	if err != nil {
		t.Fatalf("导出流: %v", err)
	}
	joined := strings.Join(got, "\n")
	if strings.Contains(joined, plain) {
		t.Fatalf("导出流里出现了身份证明文 —— 默认必须打码,实际: %q", joined)
	}
	if len(got) != 1 {
		t.Fatalf("应当有一行,实际 %d 行", len(got))
	}
}

// 只有 Raw 那条路给原值 —— 它是整套脱敏机制唯一的旁路。
func TestExport_RawIsTheOnlyWayToPlaintext(t *testing.T) {
	withIDCardRule(t)
	conn := sqliteConn(t)
	const plain = "310101199001011234"
	if _, err := RealRun(conn, "CREATE TABLE t_person (id INTEGER PRIMARY KEY, id_card TEXT)", 5*time.Second); err != nil {
		t.Fatalf("建表: %v", err)
	}
	if _, err := RealRun(conn, "INSERT INTO t_person (id, id_card) VALUES (1, '"+plain+"')", 5*time.Second); err != nil {
		t.Fatalf("插入: %v", err)
	}

	var got []string
	err := RealQueryEachRaw(conn, "SELECT id, id_card FROM t_person", 5*time.Second,
		func([]string) error { return nil },
		func(row []string) error { got = append(got, strings.Join(row, ",")); return nil })
	if err != nil {
		t.Fatalf("导出流: %v", err)
	}
	if !strings.Contains(strings.Join(got, "\n"), plain) {
		t.Fatalf("已批准的敏感字段导出应当拿到原值,实际: %q", got)
	}
}

// SELECT * 同样打码 —— 它恰恰是最安全的形态(驱动回传的就是真实列名),不能因为
// 语句里没写出列名就漏过去。
func TestExport_StarSelectIsMaskedToo(t *testing.T) {
	withIDCardRule(t)
	conn := sqliteConn(t)
	const plain = "310101199001011234"
	if _, err := RealRun(conn, "CREATE TABLE t_person (id INTEGER PRIMARY KEY, id_card TEXT)", 5*time.Second); err != nil {
		t.Fatalf("建表: %v", err)
	}
	if _, err := RealRun(conn, "INSERT INTO t_person (id, id_card) VALUES (1, '"+plain+"')", 5*time.Second); err != nil {
		t.Fatalf("插入: %v", err)
	}

	var got []string
	err := RealQueryEach(conn, "SELECT * FROM t_person", 5*time.Second,
		func([]string) error { return nil },
		func(row []string) error { got = append(got, strings.Join(row, ",")); return nil })
	if err != nil {
		t.Fatalf("导出流: %v", err)
	}
	if strings.Contains(strings.Join(got, "\n"), plain) {
		t.Fatalf("SELECT * 也必须打码,实际: %q", got)
	}
}
