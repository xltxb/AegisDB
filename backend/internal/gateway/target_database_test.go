package gateway

import "testing"

// 每请求的"目标数据库"能不能替换连接上配置的 Database,取决于该引擎的
// Database 字段到底是什么。
//
// 这不是风格问题,是一次真实故障:Oracle 的 Database 字段存的是 SERVICE NAME,
// 而对象树里的第二层是 OWNER(all_tables 查出来的 G04 这类)。展开属主节点时
// 前端把属主当"数据库"回传,服务层照单把它写进 conn.Database,go-ora 于是拿
// 属主去要服务 —— 监听器日志里就出现 SERVICE_NAME=G04 与 TNS-12514,连接直接
// 建不起来。
func TestTargetDatabaseSwitchable(t *testing.T) {
	notSwitchable := []string{"Oracle", "Oracle 19c", "ORACLE 11g"}
	for _, e := range notSwitchable {
		if TargetDatabaseSwitchable(e) {
			t.Errorf("%s 的 Database 是服务名,不能被每请求的目标库替换", e)
		}
	}
	switchable := []string{"MySQL 8.0", "TiDB 7", "PostgreSQL 15", "DWS", "SQLite", "PolarDB"}
	for _, e := range switchable {
		if !TargetDatabaseSwitchable(e) {
			t.Errorf("%s 的 Database 是可切换的命名空间,应当允许替换", e)
		}
	}
}
