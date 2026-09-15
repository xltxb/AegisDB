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

// schema 用完就得还回去。
//
// 泄漏在测试里是隐形的:每个漏掉的 schema 还攥着一个没关的连接池,攒够了就撞上 PG 的
// max_connections,之后所有测试都报 "too many clients already" —— 那句报错跟真正的
// 病因毫无关系,而真正的病因在几百个测试之前。
func TestNewDB_DropsItsSchemaOnCleanup(t *testing.T) {
	probe := NewDB(t) // 这个 handle 要活过下面的子测试,用来数账
	count := func() int64 {
		var n int64
		if err := probe.Raw(
			`SELECT count(*) FROM information_schema.schemata WHERE schema_name LIKE 't\_%'`,
		).Scan(&n).Error; err != nil {
			t.Fatalf("count schemas: %v", err)
		}
		return n
	}

	before := count()
	t.Run("inner", func(t *testing.T) {
		NewDB(t)
		NewDB(t)
		if got := count(); got != before+2 {
			t.Fatalf("子测试里 schema 数 = %d, want %d —— NewDB 没建出独占 schema", got, before+2)
		}
	})
	if got := count(); got != before {
		t.Fatalf("子测试结束后 schema 数 = %d, want %d —— 有 schema 没被清理", got, before)
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
