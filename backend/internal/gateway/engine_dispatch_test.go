package gateway

import (
	"strings"
	"testing"

	"velagateway/internal/model"
)

// 「这台实例说的是哪种协议」只有一张表 —— engineFamily。
//
// realdb.go 那张表的注释写着「只在一处决定」,理由也写着:标签里可以同时出现两个产品词,
// 谁的分支写在前面谁就赢。但对象浏览、库表加载各自又抄了一遍子串级联,于是同一台实例
// 在不同的页面上被当成不同的数据库:
//
//   · PolarDB for PostgreSQL 在对象浏览里按 MySQL 查(polardb 那一支写在 postgre 前面)
//   · PolarDB(MySQL 版)在库表加载里**根本没有分支**,直接报「暂不支持库表加载」
//
// 后一条尤其难查:连接是通的、终端能跑 SQL,只有左边那棵树是空的。

func TestSchemaIntrospect_FollowsTheEngineFamilyTable(t *testing.T) {
	for _, c := range []struct {
		engine string
		want   string // 查询里必须出现的那个标志串
		why    string
	}{
		{"PolarDB", "information_schema.tables", "裸标签的 PolarDB 默认是 MySQL 兼容版(engineFamily 就是这么判的)"},
		{"polardb", "information_schema.tables", "同上"},
		{"PolarDB for MySQL", "information_schema.tables", "PolarDB 的 MySQL 版说 MySQL 协议"},
		{"polardb-mysql 8.0", "information_schema.tables", "同上"},
		{"PolarDB for PostgreSQL", "pg_catalog", "PolarDB 的 PG 版说 PG 协议"},
		{"MySQL 8.0", "information_schema.tables", ""},
		{"TiDB 7.1", "information_schema.tables", ""},
		{"MariaDB 10", "information_schema.tables", ""},
		{"PostgreSQL 15", "pg_catalog", ""},
		{"DWS 8.1", "pg_catalog", ""},
		{"GaussDB", "pg_catalog", ""},
		{"Oracle 19c", "all_tables", ""},
		{"SQLite", "sqlite_master", ""},
	} {
		q, _ := schemaIntrospectQuery(&model.Connection{Engine: c.engine})
		if q == "" {
			t.Errorf("engine=%q:没有库表加载查询 —— 界面上那棵树会是空的(%s)", c.engine, c.why)
			continue
		}
		if !strings.Contains(q, c.want) {
			t.Errorf("engine=%q:按错的协议查了(期望查询含 %q)%s\n%s", c.engine, c.want, c.why, q)
		}
	}
}

// 认不出的引擎仍然如实报「不支持」—— 猜一个协议去连比报错更糟。
func TestSchemaIntrospect_UnknownEngineHasNoQuery(t *testing.T) {
	for _, e := range []string{"ClickHouse", "Redis", "MongoDB", ""} {
		if q, _ := schemaIntrospectQuery(&model.Connection{Engine: e}); q != "" {
			t.Errorf("engine=%q 不该有查询:%s", e, q)
		}
	}
}
