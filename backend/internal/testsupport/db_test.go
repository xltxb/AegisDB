package testsupport

import (
	"fmt"
	"os"
	"testing"
)

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
//
// 计数只数**本进程**建的 schema。
//
// 数 `t\_%` 会把整个 vela_test 里所有 schema 都算进来,而 `go test ./...` 默认让多个
// package 的测试二进制并行跑,同一个库上随时有别人在建和删 —— internal/bootstrap 那个
// 包一趟就有几百次 NewDB。那样这条用例会随机变红,而且它报出来的话
// (「有 schema 没被清理」)和真实泄漏一模一样,会把人直接带进一场排查清理逻辑的冤枉路。
//
// NewDB 的名字是 t_<pid>_<seq>,所以按自己的 pid 收窄就与别的测试二进制完全隔开了。
// LIKE 里的 `_` 是通配符,要转义成 `\_` 才是字面量下划线。
//
// 前提:本包内不并发调 NewDB(这三条用例都没有 t.Parallel)。谁要给这个包加并行,
// 按 pid 收窄就挡不住自己人了 —— 那时得让 NewDB 交出它的 schema 名来精确断言。
func TestNewDB_DropsItsSchemaOnCleanup(t *testing.T) {
	probe := NewDB(t) // 这个 handle 要活过下面的子测试,用来数账
	mine := fmt.Sprintf(`t\_%d\_%%`, os.Getpid())
	count := func() int64 {
		var n int64
		if err := probe.Raw(
			`SELECT count(*) FROM information_schema.schemata WHERE schema_name LIKE ?`, mine,
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
	// 这里守的是「迁移真的被应用了」,不是「恰好 N 张表」。写死一个数字的话,
	// 每加一张表都要回来改它,而那次改动里没有人再想一遍这条断言想说什么。
	//
	// 下限取得足够低,低到任何一次「迁移根本没跑」都会掉到它下面(那时是 0 张)。
	if n < 30 {
		t.Fatalf("只建出 %d 张 tbl_* 表 —— 迁移多半没被应用", n)
	}

	// 再点名几张分属不同迁移文件的表:光看数量,少了哪一张是看不出来的。
	for _, want := range []string{"tbl_user", "tbl_connection", "tbl_osc_job"} {
		var k int64
		if err := db.Raw(`SELECT count(*) FROM information_schema.tables
		                  WHERE table_schema = current_schema() AND table_name = ?`, want).
			Scan(&k).Error; err != nil {
			t.Fatalf("查表 %s: %v", want, err)
		}
		if k != 1 {
			t.Errorf("表 %s 不存在 —— 它所在的那个迁移文件没被应用", want)
		}
	}
}
