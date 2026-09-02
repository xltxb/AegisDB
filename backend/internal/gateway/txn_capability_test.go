package gateway

// 事务控制不再要 write 权限。
//
// COMMIT / ROLLBACK / SAVEPOINT 自己不改任何数据,只是决定前面那些改动作不作数 ——
// 而那些改动各自已经按自己的动词判过了。要求 write 才能 COMMIT,挡住的是"把一组
// SELECT 包进事务里"这种正当用法,挡不住任何东西。
//
// 但这是一次**放宽**,所以边界必须钉死:下面后半段那两条比前半段重要得多。

import "testing"

func TestTxnControl_DoesNotRequireWrite(t *testing.T) {
	for _, v := range []string{"COMMIT", "ROLLBACK", "SAVEPOINT", "commit", "Rollback"} {
		if got := MapVerbToCapability(v); got != "select" {
			t.Errorf("%s 不该要 write —— 它自己不改任何数据,得到 %q", v, got)
		}
	}
}

// ——— 放宽的边界 ———

// BEGIN 绝不能跟着放宽:在 Oracle 里它是匿名 PL/SQL 块的开头,块里可以 DELETE。
// 判成 select 就是把整块代码放行了。
func TestTxnControl_BeginStaysAWrite(t *testing.T) {
	if got := MapVerbToCapability("BEGIN"); got == "select" {
		t.Fatal("BEGIN 被放宽成了 select —— Oracle 的匿名 PL/SQL 块以它开头,块里可以改数据")
	}
}

// START 也不行:MySQL 里还有 START REPLICA / START SLAVE,那是复制管理。
// 光看动词分不出它是哪一件事,分不出就不放。
func TestTxnControl_StartStaysAWrite(t *testing.T) {
	if got := MapVerbToCapability("START"); got == "select" {
		t.Fatal("START 被放宽成了 select —— 它也可能是 START REPLICA,复制管理不该按读放行")
	}
}

// 放宽只动了这三个词,写/DDL/授权一个都没动。
func TestTxnControl_NothingElseMoved(t *testing.T) {
	unchanged := map[string]string{
		"INSERT": "write", "UPDATE": "write", "DELETE": "write", "MERGE": "write",
		"DROP": "ddl", "ALTER": "ddl", "TRUNCATE": "ddl", "CREATE": "ddl", "RENAME": "ddl",
		"GRANT": "grant", "REVOKE": "grant",
		"CALL": "write", // 存储过程能干任何事,按写处理
	}
	for verb, want := range unchanged {
		if got := MapVerbToCapability(verb); got != want {
			t.Errorf("%s 的判定被改动了:%q,应当仍是 %q", verb, got, want)
		}
	}
}
