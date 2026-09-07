package service

import (
	"testing"

	"velagateway/internal/model"
)

// Oracle 选中的 schema 要带到执行层,而实例的服务名不能被改掉。
//
// 从前这里直接 return:点了 schema 只改界面标签,会话里从没切过,SELECT 仍然解析到
// 登录用户自己的 schema。
func TestApplyTargetDatabase_OracleCarriesSchemaAndKeepsService(t *testing.T) {
	conn := &model.Connection{Engine: "oracle", Database: "ORCLPDB1"}
	applyTargetDatabase(conn, "SCOTT")

	if conn.Database != "ORCLPDB1" {
		t.Errorf("Oracle 的 Database 是服务名,不能被 schema 覆盖,实际 %q", conn.Database)
	}
	if conn.TargetSchema != "SCOTT" {
		t.Errorf("选中的 schema 应带到执行层,实际 %q", conn.TargetSchema)
	}
}

// 别的引擎照旧:换库就是换 Database。
func TestApplyTargetDatabase_OtherEnginesStillSwitchDatabase(t *testing.T) {
	conn := &model.Connection{Engine: "mysql", Database: "old"}
	applyTargetDatabase(conn, "neu")
	if conn.Database != "neu" {
		t.Errorf("MySQL 应当换库,实际 %q", conn.Database)
	}
	if conn.TargetSchema != "" {
		t.Errorf("非 Oracle 不该带 TargetSchema,实际 %q", conn.TargetSchema)
	}
}

// 空值不动任何东西。
func TestApplyTargetDatabase_EmptyIsANoop(t *testing.T) {
	conn := &model.Connection{Engine: "oracle", Database: "ORCLPDB1"}
	applyTargetDatabase(conn, "  ")
	if conn.Database != "ORCLPDB1" || conn.TargetSchema != "" {
		t.Errorf("空目标不该改动任何字段: db=%q schema=%q", conn.Database, conn.TargetSchema)
	}
}
