package service

import (
	"strings"
	"testing"
	"time"

	"aegisdb/internal/model"
)

// L-API3(2026-10 安全审计):SQL 格式导出是 UTF-8,却不声明连接字符集。导入端连接若是 GBK 这类
// 多字节编码,字面量里的转义可能被吞进前一个字符 —— 数据错乱,甚至后半截被当成语句。
func TestSQLExportPreamble_DeclaresCharset(t *testing.T) {
	s := &Services{}
	for engine, want := range map[string]string{
		"MySQL 8.0":     "SET NAMES utf8mb4;",
		"TiDB":          "SET NAMES utf8mb4;",
		"PostgreSQL 15": "SET client_encoding = 'UTF8';",
	} {
		got := s.sqlExportPreamble(&model.Connection{Engine: engine}, &model.ExportJob{}, "SELECT 1", true, time.Now())
		if !strings.Contains(got, want) {
			t.Errorf("%s:前言应声明字符集 %q,实际:\n%s", engine, want, got)
		}
	}
}
