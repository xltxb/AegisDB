package gateway

// H-2(白盒审计):SET 的作用域识别。
//
// SessionScoped 为真时,判定链会短路成"只要 select 能力",跳过高危字典与严格模式。
// 所以任何**影响面超出本会话**的 SET 都必须被认出来 —— 认漏一条,只读角色就能改
// 整个服务器(关全局只读、关 binlog 掩盖写入)。
//
// 原实现有两个洞,都源于"只看语句开头的第一个赋值":
//   SET @@GLOBAL.read_only = 0     作用域写在变量名上,SET 后紧跟的是 @@
//   SET SESSION a=1, GLOBAL b=2    MySQL 允许混写,逃逸词在第二个赋值上

import "testing"

// 会影响整个服务器/实例/身份的,一条都不能被当成会话级。
func TestSessionScope_EscapesEverythingBeyondTheSession(t *testing.T) {
	beyond := []string{
		// —— 审计报告点名的那一类:作用域写在变量名上 ——
		"SET @@GLOBAL.read_only = 0",
		"SET @@global.sql_log_bin = 0",
		"SET @@GLOBAL.super_read_only = 0",
		"SET @@ GLOBAL . read_only = 0", // 空白不该成为绕过的办法
		"SET @@PERSIST.read_only = 0",
		"SET @@persist_only.read_only = 0",
		// —— 混写:逃逸词不在第一个赋值上 ——
		"SET SESSION sort_buffer_size=1, GLOBAL read_only=0",
		"SET @x = 1, @@GLOBAL.read_only = 0",
		// —— 原本就认得的关键字形式,不能改回归 ——
		"SET GLOBAL read_only = 0",
		"SET PERSIST read_only = 0",
		"SET PERSIST_ONLY read_only = 0",
		"SET PASSWORD FOR 'u'@'%' = 'x'",
		"SET ROLE dba_admin",
		"SET SESSION AUTHORIZATION postgres",
		// —— 注释不能成为绕过的办法 ——
		"/* nothing to see */ SET @@GLOBAL.read_only = 0",
	}
	for _, sql := range beyond {
		if SessionScoped(sql) {
			t.Errorf("被当成了会话级,实际会影响整个服务器/身份: %q", sql)
		}
	}
}

// 真正只影响本连接的必须仍然放宽 —— 否则一个只读分析师连 search_path 都设不了,
// 而设 schema 恰恰是读数据之前必须做的那一步。修洞不能把这件事一起修没了。
func TestSessionScope_StillAllowsRealSessionSettings(t *testing.T) {
	sessionOnly := []string{
		"SET search_path TO dwd",
		"SET NAMES utf8mb4",
		"SET SESSION sort_buffer_size = 1048576",
		"SET LOCAL statement_timeout = '5s'",
		"SET @my_var = 42",
		"SET TRANSACTION ISOLATION LEVEL READ COMMITTED",
		"SET @@SESSION.sort_buffer_size = 1048576",
		"SET @@sort_buffer_size = 1048576",
		"ALTER SESSION SET CURRENT_SCHEMA = app",
	}
	for _, sql := range sessionOnly {
		if !SessionScoped(sql) {
			t.Errorf("这条只影响本连接,不该被收紧: %q", sql)
		}
	}
}

// ALTER SYSTEM 改的是整个实例,和 ALTER SESSION 只差一个词。
func TestSessionScope_AlterSystemIsNotAlterSession(t *testing.T) {
	if SessionScoped("ALTER SYSTEM SET max_connections = 1000") {
		t.Error("ALTER SYSTEM 改的是整个实例,不是会话")
	}
}
