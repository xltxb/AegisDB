package gateway

// 七个引擎的语法自查:MySQL / PolarDB / TiDB / PostgreSQL / DWS / Oracle / MongoDB。
//
// 网关不解析完整 SQL,也不该解析。它只需要在三件事上不出错:
//
//	1. 这条语句**会不会返回结果集** —— 判错就走 Exec,页面上什么都不显示。
//	2. 这条语句**需要什么权限** —— 判错就是无谓地拦人,或者放过不该放的。
//	3. 这条语句**会不会改数据** —— 判错就是安全洞。
//
// EXPLAIN PERFORMANCE 那次是第 1 类:一个引擎特有的关键字没被认出来,有效动词成了
// 不存在的词,于是既不按读处理(没结果)、又按写判定(还可能被拦)。这份自查是把同一
// 类问题在七个引擎上一次性扫完。
//
// 它不声称覆盖"所有语法" —— 那是做不到也不必做的。它覆盖的是**日常会用到、且判错
// 会让人当场卡住**的那些。

import (
	"testing"

	"velagateway/internal/model"
)

// 会返回结果集的语句:必须按读处理(否则执行侧不取结果集),且只需 select 权限。
func TestAudit_RowReturningStatementsAreReads(t *testing.T) {
	cases := []struct{ engine, sql string }{
		// PostgreSQL / DWS —— TABLE 和 VALUES 都是独立的查询语句
		{"PG/DWS", `TABLE t_orders`},
		{"PG/DWS", `VALUES (1),(2)`},
		{"PG/DWS", `WITH a AS (SELECT 1) TABLE a`},
		{"PG/DWS", `FETCH ALL FROM mycur`},
		{"PG/DWS", `FETCH FORWARD 100 FROM mycur`},
		// MySQL 8.0.19+ / TiDB / PolarDB 也支持 TABLE 与 VALUES
		{"MySQL8", `TABLE t_orders`},
		{"MySQL8", `VALUES ROW(1),ROW(2)`},
		{"MySQL", `HELP 'SELECT'`},
		// 已有的
		{"MySQL", `SHOW TABLES`},
		{"MySQL", `DESC t_orders`},
		{"Oracle", `SELECT * FROM dual`},
	}
	for _, c := range cases {
		if !IsRead(c.sql) {
			t.Errorf("[%s] IsRead(%q) = false —— 它会返回结果集,判成写就会走 Exec,页面上什么都不显示", c.engine, c.sql)
		}
		if got := MapVerbToCapability(ParseVerb(c.sql)); got != "select" {
			t.Errorf("[%s] %q 的能力维度 = %q, want select —— 只是查询,不该按写来拦", c.engine, c.sql, got)
		}
	}
}

// 会话级设置只影响当前连接,不读也不写数据。把它们按写(Oracle 的 ALTER SESSION
// 甚至按 DDL)来判,等于让只读用户连 search_path 都设不了 —— 而设 schema 恰恰是
// 读数据之前必须做的事。
func TestAudit_SessionScopedSettingsAreNotWrites(t *testing.T) {
	sessionScoped := []struct{ engine, sql string }{
		{"PG/DWS", `SET search_path TO dwd, public`},
		{"PG/DWS", `SET LOCAL work_mem = '64MB'`},
		{"PG/DWS", `SET SESSION statement_timeout = 60000`},
		{"PG/DWS", `SET TIME ZONE 'Asia/Shanghai'`},
		{"MySQL", `SET NAMES utf8mb4`},
		{"MySQL", `SET CHARACTER SET utf8mb4`},
		{"MySQL", `SET @my_var = 1`},
		{"MySQL", `SET SESSION sql_mode = 'STRICT_TRANS_TABLES'`},
		{"all", `SET TRANSACTION ISOLATION LEVEL READ COMMITTED`},
		{"Oracle", `ALTER SESSION SET CURRENT_SCHEMA = G04`},
		{"Oracle", `ALTER SESSION SET NLS_DATE_FORMAT = 'YYYY-MM-DD'`},
	}
	for _, c := range sessionScoped {
		if !SessionScoped(c.sql) {
			t.Errorf("[%s] SessionScoped(%q) = false —— 它只影响当前会话", c.engine, c.sql)
		}
	}
}

// 但"看起来像会话设置"的这几条会改到会话之外,必须仍按写/更严来判。
// 这条是上一条的对照面:放宽只能放宽到会话边界为止。
func TestAudit_ServerWideAndPrivilegeSettingsStayGated(t *testing.T) {
	notSessionScoped := []struct{ engine, sql string }{
		{"MySQL", `SET GLOBAL read_only = 0`},                    // 全服务器
		{"MySQL", `SET PERSIST max_connections = 1000`},          // 写进配置文件
		{"MySQL", `SET PERSIST_ONLY max_connections = 1000`},     // 同上
		{"MySQL", `SET PASSWORD FOR 'app'@'%' = 'x'`},            // 改口令
		{"PG/DWS", `SET ROLE dba_admin`},                         // 提权
		{"PG/DWS", `SET SESSION AUTHORIZATION postgres`},         // 换身份
		{"Oracle", `ALTER SYSTEM SET processes = 500`},           // 全实例
		{"PG/DWS", `ALTER SYSTEM SET shared_buffers = '8GB'`},    // 全实例
	}
	for _, c := range notSessionScoped {
		if SessionScoped(c.sql) {
			t.Errorf("[%s] SessionScoped(%q) = true —— 它的影响超出了当前会话,不能按会话设置放宽", c.engine, c.sql)
		}
	}
}

// 会话设置在**判定链上**也要真的走得通。这条比上面两条更要紧:字典里有 "ALTER"
// (种子里生产是 high),而 Oracle 切 schema 就是 `ALTER SESSION SET CURRENT_SCHEMA`
// —— 若字典照样命中,只读用户在生产上什么都干不了。
func TestAudit_SessionSettingPassesTheFullJudgementChain(t *testing.T) {
	// 一个只读角色:select 放行、write 与 ddl 拒绝。这样"走了哪道闸"才看得出来 ——
	// 若 store 一律放行,写操作也会通过,断言就什么都证明不了。
	store := &fakeStore{
		cmds: []model.RiskCommand{
			{Command: "ALTER", TierCode: "PROD", Level: model.RiskHigh},
			{Command: "DROP", TierCode: "PROD", Level: model.RiskHigh},
		},
		caps: map[string]string{
			"select|prod": model.LevelAllow,
			"write|prod":  model.LevelDeny,
			"ddl|prod":    model.LevelDeny,
		},
	}
	store.strict = true // 该分层的严格模式也开着
	e := NewRiskEngine(store)

	if v := e.EvaluateFor([]int64{1}, "oracle", "PROD", `ALTER SESSION SET CURRENT_SCHEMA = G04`); v.Action != ActionAllow {
		t.Errorf("ALTER SESSION 应放行,实际 %s / %s —— 它只改这条连接,而切 schema 是读数据的前置步骤", v.Action, v.Rule)
	}
	if v := e.EvaluateFor([]int64{1}, "dws", "PROD", `SET search_path TO dwd`); v.Action != ActionAllow {
		t.Errorf("SET search_path 应放行,实际 %s / %s", v.Action, v.Rule)
	}
	// 对照面:真正的 ALTER TABLE 仍然被字典拦住。
	if v := e.EvaluateFor([]int64{1}, "oracle", "PROD", `ALTER TABLE t ADD (c NUMBER)`); v.Action == ActionAllow {
		t.Error("ALTER TABLE 在生产不该放行 —— 会话设置的放宽不能顺带把它带过去")
	}
	// 超出会话边界的也不能沾光。
	for _, sql := range []string{`SET GLOBAL read_only = 0`, `ALTER SYSTEM SET processes = 500`, `SET ROLE dba_admin`} {
		if v := e.EvaluateFor([]int64{1}, "mysql", "PROD", sql); v.Action == ActionAllow {
			t.Errorf("%q 影响超出当前会话,不该按会话设置放行", sql)
		}
	}
	// 只读被明确拒绝的角色,连会话设置也不该放行 —— 放宽的是走哪道闸,不是不走闸。
	denied := &fakeStore{caps: map[string]string{"select|prod": model.LevelDeny}}
	e2 := NewRiskEngine(denied)
	if v := e2.EvaluateFor([]int64{1}, "dws", "PROD", `SET search_path TO dwd`); v.Action != ActionDeny {
		t.Errorf("select 被拒的角色不该能设置会话,实际 %s", v.Action)
	}
}

// PolarDB 有 MySQL 版和 PostgreSQL 版两条产品线,名字里都带 polardb。
// 认错版本就是选错驱动 —— 连都连不上。
func TestAudit_PolarDBPostgresIsNotRoutedToTheMySQLDriver(t *testing.T) {
	cases := []struct{ engine, wantFamily string }{
		{"PolarDB", familyMySQL},
		{"PolarDB-X", familyMySQL},
		{"PolarDB MySQL", familyMySQL},
		{"PolarDB PostgreSQL", familyPostgres},
		{"polardb-postgresql", familyPostgres},
		{"MySQL 8.0", familyMySQL},
		{"TiDB 5.7", familyMySQL},
		{"DWS", familyPostgres},
		{"GaussDB", familyPostgres},
		{"Oracle 19c", familyOracle},
		{"MongoDB 6", familyMongo},
	}
	for _, c := range cases {
		if got := engineFamily(c.engine); got != c.wantFamily {
			t.Errorf("engineFamily(%q) = %q, want %q", c.engine, got, c.wantFamily)
		}
	}
}

// MongoDB:只读操作不能落进 write。getMore 尤其要命 —— 它是翻页取下一批,
// 判成写就意味着一个只读用户翻不动自己的查询结果。
func TestAudit_MongoReadOperationsAreReads(t *testing.T) {
	d := DialectFor("mongodb")
	reads := []string{
		"find", "findOne", "aggregate", "count", "countDocuments", "distinct",
		"listIndexes", "listCollections", "explain",
		// 本次自查补上的
		"getMore", "listDatabases", "dbStats", "collStats", "currentOp", "watch",
	}
	for _, m := range reads {
		if got := d.Capability(m); got != "select" {
			t.Errorf("mongo %s 的能力维度 = %q, want select", m, got)
		}
	}
	// 对照面:会改数据或改结构的,一条都不能松。
	writes := map[string]string{
		"insertOne": "write", "updateMany": "write", "deleteOne": "write",
		"bulkWrite": "write", "findAndModify": "write",
		"mapReduce": "write", // 可以把结果写进另一个集合
		"drop":      "ddl", "createIndex": "ddl", "dropIndex": "ddl",
		"createUser": "grant", "grantRolesToUser": "grant",
	}
	for m, want := range writes {
		if got := d.Capability(m); got != want {
			t.Errorf("mongo %s 的能力维度 = %q, want %q", m, got, want)
		}
	}
}

// 会改数据的语句一条都不能被当成读 —— 这是自查的另一半,比上面几条重要得多。
func TestAudit_MutatingStatementsAreNeverReads(t *testing.T) {
	mutating := []struct{ engine, sql string }{
		{"PG/DWS", `WITH d AS (DELETE FROM t RETURNING *) SELECT * FROM d`},
		{"PG/DWS", `WITH u AS (UPDATE t SET x=1 RETURNING *) TABLE u`},
		{"PG/DWS", `WITH i AS (INSERT INTO t VALUES (1) RETURNING *) VALUES (1)`},
		{"MySQL", `REPLACE INTO t VALUES (1)`},
		{"Oracle", `MERGE INTO t USING s ON (1=1) WHEN MATCHED THEN UPDATE SET x=1`},
		{"PG/DWS", `COPY t FROM '/tmp/x.csv'`},
		{"MySQL", `LOAD DATA INFILE '/tmp/x' INTO TABLE t`},
		{"all", `CALL my_proc()`},
		{"Oracle", `BEGIN my_proc(); END;`},
		{"PG/DWS", `TRUNCATE TABLE t`},
		{"DWS", `EXPLAIN PERFORMANCE DELETE FROM t`},
		{"PG/DWS", `EXPLAIN ANALYSE DELETE FROM t`},
	}
	for _, c := range mutating {
		if IsRead(c.sql) {
			t.Errorf("[%s] IsRead(%q) = true —— 它会改数据", c.engine, c.sql)
		}
	}
}
