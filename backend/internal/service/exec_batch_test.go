package service

// 一批语句:判定看整批,下发时逐条。
//
// 为什么必须在网关这一侧拆开,而不是把整段交给驱动:MySQL 的 DSN 上
// AllowMultiStatements 是**故意**关掉的(见 gateway.engineDriver),关掉它正是为了让
// "一次判定管住的就是这些语句"没法靠 DSN 绕过。不拆的话,同一段批量在 SQLite /
// PostgreSQL 上能跑、在 MySQL 上直接语法错 —— 同一个网关、同一段 SQL,结果取决于
// 对面是什么引擎。
//
// 这组用例钉三件事,每一件都对应一种会真正伤到人的错法:
//   - 三条都要跑到(少跑一条 = 变更只应用了一半,而界面报成功);
//   - 中间失败要**停下来**并说清停在第几条(接着往下跑 = 在一个半坏的库上继续改);
//   - 单条语句原样走老路(它要带回结果集,批量不带)。

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"aegisdb/internal/gateway"
	"aegisdb/internal/model"
)

// 用临时文件而不是 :memory: —— sqlite 的每次连接都会新开一个内存库,
// 那样第二条语句根本看不见第一条建的表,用例会测了个寂寞。
func batchConn(t *testing.T) *model.Connection {
	t.Helper()
	return &model.Connection{Engine: "SQLite", Database: filepath.Join(t.TempDir(), "batch.db")}
}

func batchSvc() *Services { return &Services{Executor: &gateway.Executor{}} }

func tableCount(t *testing.T, s *Services, conn *model.Connection) string {
	t.Helper()
	res := s.Executor.Run(context.Background(), conn,
		`select count(*) from sqlite_master where type='table' and name like 't_b%'`, 5*time.Second)
	if res.Err != nil || len(res.Data) == 0 {
		t.Fatalf("查表数失败: %v", res.Err)
	}
	return res.Data[0][0]
}

func TestExecBatch_RunsEveryStatement(t *testing.T) {
	s, conn := batchSvc(), batchConn(t)
	res := s.execCommand(context.Background(), conn,
		"create table t_b1 (id integer);\ncreate table t_b2 (id integer);\ncreate table t_b3 (id integer);",
		10*time.Second)

	if res.Err != nil {
		t.Fatalf("整批应当成功: %v · %s", res.Err, res.Output)
	}
	if got := tableCount(t, s, conn); got != "3" {
		t.Errorf("三条都该跑到,实际建了 %s 张表", got)
	}
	if !strings.Contains(res.Output, "共 3 条语句") {
		t.Errorf("输出要说清跑了几条:%q", res.Output)
	}
	if res.OutputRef == nil || res.OutputRef.Code != model.OutBatchDone {
		t.Errorf("批量完成要有自己的标识:%+v", res.OutputRef)
	}
}

// 中间那条失败:必须停下,并说清停在第几条 —— 接着往下跑,等于在一个已经半坏的库上
// 继续改;而报成功则是最糟的:没人会去查。
func TestExecBatch_StopsAtTheFirstFailure(t *testing.T) {
	s, conn := batchSvc(), batchConn(t)
	res := s.execCommand(context.Background(), conn,
		"create table t_b1 (id integer);\nselect * from t_no_such_table;\ncreate table t_b3 (id integer);",
		10*time.Second)

	if res.Err == nil {
		t.Fatal("中间一条失败,整批应当报失败")
	}
	if !strings.Contains(res.Output, "已执行 1/3 条后失败") || !strings.Contains(res.Output, "第 2 条") {
		t.Errorf("要说清停在第几条:%q", res.Output)
	}
	if res.OutputRef == nil || res.OutputRef.Code != model.OutBatchFailed {
		t.Fatalf("批量失败要有自己的标识:%+v", res.OutputRef)
	}
	if a := res.OutputRef.Args; a["done"] != "1" || a["total"] != "3" || a["pos"] != "2" {
		t.Errorf("进度参数不对:%+v", a)
	}
	// 失败那一条自己的结局(execFailed,带驱动原文)要作为子项带上,界面按读者语言渲染;
	// 只给它的中文规范串,英文界面上就会冒出"数据库执行失败"。
	if p := res.OutputRef.Parts; len(p) != 1 || p[0].Code != model.OutExecFailed || p[0].Args["err"] == "" {
		t.Errorf("应嵌入失败那条语句的文案码(execFailed):%+v", p)
	}
	// 第三条**不能**跑 —— 只该剩下第一条建的那张表。
	if got := tableCount(t, s, conn); got != "1" {
		t.Errorf("失败之后不该继续往下跑,实际有 %s 张表", got)
	}
}

// 单条语句原样走老路:它要带回列和行,批量不带(一次回执里塞不下 N 个结果集)。
func TestExecBatch_SingleStatementKeepsItsResultSet(t *testing.T) {
	s, conn := batchSvc(), batchConn(t)
	res := s.execCommand(context.Background(), conn, "select 7 as answer", 5*time.Second)

	if res.Err != nil {
		t.Fatalf("单条查询失败: %v", res.Err)
	}
	if len(res.Columns) != 1 || res.Columns[0] != "answer" || len(res.Data) != 1 || res.Data[0][0] != "7" {
		t.Errorf("单条语句应当照常带回结果集:cols=%v data=%v", res.Columns, res.Data)
	}
	if res.OutputRef != nil && res.OutputRef.Code == model.OutBatchDone {
		t.Error("单条语句不该被报成批量")
	}
}

// 末尾多余的分号、空语句不该被算成一条 —— 否则"共 N 条"会比人看到的多。
func TestExecBatch_IgnoresEmptyTrailingStatements(t *testing.T) {
	s, conn := batchSvc(), batchConn(t)
	res := s.execCommand(context.Background(), conn,
		"create table t_b1 (id integer);\ncreate table t_b2 (id integer);\n;\n", 10*time.Second)

	if res.Err != nil {
		t.Fatalf("整批应当成功: %v · %s", res.Err, res.Output)
	}
	if !strings.Contains(res.Output, "共 2 条语句") {
		t.Errorf("空语句不该被算进去:%q", res.Output)
	}
}

// 批量里 UPDATE / DELETE / INSERT 影响了 0 行的,要单独点出来是第几条。
//
// 成因是一次真实排查:一批 8 条 UPDATE 报"7 行受影响",人以为网关算错了,其实是其中一条的
// where 条件在那套库里没匹配上 —— 而只有一个总数,看不出是哪一条。建表、查询、COMMIT
// 本来就不影响行,不算。
func TestExecBatch_PointsOutWritesThatAffectedNoRows(t *testing.T) {
	s, conn := batchSvc(), batchConn(t)
	setup := s.execCommand(context.Background(), conn,
		"create table t_b1 (id integer, v integer);\ninsert into t_b1 values (1, 0);\ninsert into t_b1 values (2, 0);", 10*time.Second)
	if setup.Err != nil {
		t.Fatal(setup.Err)
	}
	res := s.execCommand(context.Background(), conn, strings.Join([]string{
		"update t_b1 set v = 1 where id = 1;",
		"update t_b1 set v = 1 where id = 99; -- 不存在",
		"select count(*) from t_b1;",
		"delete from t_b1 where id = 98;",
		"create table t_b2 (id integer);",
		"update t_b1 set v = 2 where id = 2;",
	}, "\n"), 10*time.Second)
	if res.Err != nil {
		t.Fatalf("整批应当成功: %v", res.Err)
	}
	if res.OutputRef == nil || res.OutputRef.Code != model.OutBatchDoneZero {
		t.Fatalf("有写语句影响 0 行时要换成带提示的标识:%+v", res.OutputRef)
	}
	if a := res.OutputRef.Args; a["n"] != "6" || a["rows"] != "2" || a["zero"] != "2, 4" || a["z"] != "2" {
		t.Errorf("参数不对:%+v", a)
	}
	if want := "批量执行完成 · 共 6 条语句 · 2 行受影响 · ⚠ 其中 2 条影响 0 行:第 2, 4 条"; res.Output != want {
		t.Errorf("输出\n实际 %q\n应为 %q", res.Output, want)
	}

	// 全都命中:还是原来那句话
	ok := s.execCommand(context.Background(), conn, "update t_b1 set v = 3 where id = 1;\nupdate t_b1 set v = 3 where id = 2;", 10*time.Second)
	if ok.OutputRef == nil || ok.OutputRef.Code != model.OutBatchDone {
		t.Errorf("都命中时不该带提示:%+v %q", ok.OutputRef, ok.Output)
	}
}

// 2026-10 生产:TiDB 建库脚本里的 `/*T![auto_rand] AUTO_RANDOM(5) */` 在建好的表上丢了 —— 多条
// 语句逐条下发的是剥掉注释的文本。在真实 MySQL 上用 `/*!99999 … */` 验证"可执行注释原样
// 送达":标记还在,MySQL 认为版本不够、跳过注释体;标记被剥掉,注释体被执行,引擎不存在报错。
func TestExecBatch_ExecutableCommentMarkersReachTheServer(t *testing.T) {
	v := os.Getenv("AEGISDB_TEST_MYSQL") // host:port/user/password
	if v == "" {
		t.Skip("未设置 AEGISDB_TEST_MYSQL")
	}
	p := strings.SplitN(v, "/", 3)
	hp := strings.SplitN(p[0], ":", 2)
	port, _ := strconv.Atoi(hp[1])
	admin, err := sql.Open("mysql", fmt.Sprintf("%s:%s@tcp(%s)/", p[1], p[2], p[0]))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("zz_execc_%d", time.Now().UnixNano()%1_000_000)
	if _, err := admin.Exec("CREATE DATABASE " + schema); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec("DROP DATABASE " + schema)
	conn := &model.Connection{Name: "execc", Engine: "MySQL 8.0", Host: hp[0], Port: port, Username: p[1], Password: p[2], Database: schema}

	script := "/* 建表 */\nCREATE TABLE zz_a (id INT) /*!99999 ENGINE=NoSuchEngine */;\n" +
		"CREATE TABLE zz_b (id BIGINT NOT NULL /*T![auto_rand] AUTO_RANDOM(5) */ PRIMARY KEY /*T![clustered_index] CLUSTERED */);"
	s := batchSvc()
	if res := s.execCommand(context.Background(), conn, script, 30*time.Second); res.Err != nil {
		t.Fatalf("可执行注释应原样送达(MySQL 跳过 /*!99999 */ 与 /*T! */),实际失败: %s", res.Output)
	}
	var n int
	if err := admin.QueryRow("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = ?", schema).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("应建出 2 张表,实际 %d", n)
	}
}
