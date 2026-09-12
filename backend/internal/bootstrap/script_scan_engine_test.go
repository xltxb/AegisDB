package bootstrap

// 脚本扫描也要按**目标实例的引擎**读字符串。
//
// 反斜杠在 MySQL 的字符串里是转义(于是 `'x\' WHERE id=1 --'` 整段都是字面量,那条
// UPDATE 没有 WHERE),在 PostgreSQL 的标准字符串里不是(于是 WHERE 是真的子句)。
// 判定层已经按引擎分开读了,而脚本扫描这条路调的是不带引擎的 NoWhere ——
// 它对认不出的引擎取两种读法里更严的那个。
//
// 结果:一份**合法的 PostgreSQL 脚本**被报成无 WHERE 的整表更新,在严格分层上升成
// high、强制走审批。扫描器手里明明就攥着目标连接,引擎就在上面。
//
// 这是「宁可多拦」在错误位置上的代价:多拦在判定层是安全余量,多拦在报告层是假警报,
// 而假警报多了,真的那条就没人看了。

import "testing"

const pgEscapedUpdate = `UPDATE t SET note='x\' WHERE id=1 --'`

func TestScriptScan_ReadsStringsWithTheTargetEngine(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")

	// PostgreSQL 实例:标准字符串,反斜杠不转义 —— 这条语句带着 WHERE。
	pg := app.connIDByEngine(token, "PostgreSQL")
	got := app.scanScript(token, pg, pgEscapedUpdate)
	if got.High != 0 {
		t.Errorf("PostgreSQL 上这条带 WHERE 的更新被报成了高危:high=%d mid=%d safe=%d",
			got.High, got.Mid, got.Safe)
	}

	// MySQL 实例:同一串字节在那里真的是整表更新,该报就得报。
	my := app.connIDByEngine(token, "MySQL")
	mysqlGot := app.scanScript(token, my, pgEscapedUpdate)
	if mysqlGot.High == 0 {
		t.Errorf("MySQL 上这条整表更新没被报出来:high=%d mid=%d safe=%d",
			mysqlGot.High, mysqlGot.Mid, mysqlGot.Safe)
	}
}
