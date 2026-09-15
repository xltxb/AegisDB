package testsupport

import "testing"

// 两个库互不可见:各自建一行,都只看得到自己那一行。
func TestNewDB_SchemasAreIsolated(t *testing.T) {
	a := NewDB(t)
	b := NewDB(t)

	if err := a.Exec(`INSERT INTO tbl_role (code, name, layer) VALUES ('only_in_a', 'A', 'l')`).Error; err != nil {
		t.Fatalf("insert into a: %v", err)
	}

	var inB int64
	if err := b.Raw(`SELECT count(*) FROM tbl_role WHERE code = 'only_in_a'`).Scan(&inB).Error; err != nil {
		t.Fatalf("count in b: %v", err)
	}
	if inB != 0 {
		t.Fatalf("b 看到了 a 的行:%d,schema 隔离失效", inB)
	}
}

// baseline 真的跑过了:36 张表都在。
func TestNewDB_RunsBaseline(t *testing.T) {
	db := NewDB(t)
	var n int64
	err := db.Raw(`SELECT count(*) FROM information_schema.tables
	               WHERE table_schema = current_schema() AND table_name LIKE 'tbl_%'`).Scan(&n).Error
	if err != nil {
		t.Fatalf("count tables: %v", err)
	}
	if n != 36 {
		t.Fatalf("表数 = %d, want 36", n)
	}
}
