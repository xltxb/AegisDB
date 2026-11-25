package gateway

import "testing"

// 库树要列出**所有**库,包括一张表都没有的。
//
// MySQL 系原先只从 information_schema.tables 聚合库名:新建的空库(或账号对库里的表没有
// 任何权限的库)不出现在树里,终端 `use` 拿这份列表校验,于是报"不在当前实例的库列表中" ——
// 而 SHOW DATABASES 明明列着它。库名要和 SHOW DATABASES 同源:information_schema.schemata。
func TestRealSchema_MySQLListsEmptyDatabases(t *testing.T) {
	conn := tcMySQL(t)
	rawExec(t, conn, "DROP DATABASE IF EXISTS tc_empty_db", "CREATE DATABASE tc_empty_db")
	t.Cleanup(func() { rawExec(t, conn, "DROP DATABASE IF EXISTS tc_empty_db") })

	groups, err := RealSchema(conn)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range groups {
		if g.Database == "tc_empty_db" {
			if len(g.Tables) != 0 {
				t.Errorf("空库不该带表: %v", g.Tables)
			}
			return
		}
	}
	t.Fatal("空库 tc_empty_db 没有出现在库树里")
}
