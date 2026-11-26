package gateway

import (
	"context"
	"testing"
	"time"
)

// 导出成 SQL 要的是**带类型**的值:NULL 与空串要分得开,数字不加引号,二进制写十六进制。
// 字符串那条路(cellString)把 NULL 读成了 "",在 CSV 里无所谓,在 INSERT 里就把空值改成了空串。
func TestRealQueryEachCells_KeepsNullAndKind(t *testing.T) {
	conn := pgTarget(t)
	for _, q := range []string{
		"CREATE TABLE t (id integer, name text, note text, data bytea, id_card text)",
		"INSERT INTO t VALUES (1, '', NULL, '\\x01ff'::bytea, '310101199001011234')",
	} {
		if _, err := RealRun(context.Background(), conn, q, 5*time.Second); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	prev := SensitiveRulesProvider
	SensitiveRulesProvider = func() []SensitiveRule { return []SensitiveRule{{Table: "*", Column: "id_card", Style: MaskFull}} }
	t.Cleanup(func() { SensitiveRulesProvider = prev })

	var row []Cell
	err := RealQueryEachCells(context.Background(), conn, "SELECT id, name, note, data, id_card FROM t", 5*time.Second,
		func([]string) error { return nil },
		func(r []Cell) error { row = append([]Cell(nil), r...); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(row) != 5 {
		t.Fatalf("应读到 5 格: %+v", row)
	}
	if row[0].Kind != KindNumber || row[0].Text != "1" {
		t.Errorf("id 应是数字 1: %+v", row[0])
	}
	if row[1].Null || row[1].Text != "" {
		t.Errorf("name 是空串,不是 NULL: %+v", row[1])
	}
	if !row[2].Null {
		t.Errorf("note 应是 NULL: %+v", row[2])
	}
	if row[3].Kind != KindBinary || row[3].Text != "\x01\xff" {
		t.Errorf("data 应是二进制: %+v", row[3])
	}
	if row[4].Text != MaskValue("310101199001011234", MaskFull) || row[4].Kind != KindText {
		t.Errorf("敏感列在写出前就该打码: %+v", row[4])
	}
}
