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
	"path/filepath"
	"strings"
	"testing"
	"time"

	"velagateway/internal/gateway"
	"velagateway/internal/model"
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
