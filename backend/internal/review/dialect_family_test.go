package review

import "testing"

// 规范审查挑哪一套规则,要和网关判定挑哪一种协议出自同一张表。
//
// 这里原本也抄了一遍子串级联,而且抄漏了 postgre 那一支:于是
//
//   · PolarDB for PostgreSQL → 含 polardb → 套上了 **MySQL 规范**
//   · 普通 PostgreSQL        → 一支都不匹配 → Generic(通用规则)
//
// 前一条是实实在在的错报:MySQL 规范里那些「VARCHAR 长度」「表必须有主键自增」之类
// 的条目,拿去审一份 PG 脚本,报出来的问题没有一条是真的。而人对审查结果的信任是
// 一次性的 —— 报错了一次没道理的,下一次真的那条也不会有人看。
//
// DWS / GaussDB 仍然有自己的规范库(docs 里那份 DWS 规范),所以它们从 PG 家族里单拎
// 出来;剩下的 PG 走 Generic —— 我们没有为它写过规范,套别人的不如不套。
func TestDialectFor_FollowsTheEngineFamily(t *testing.T) {
	for _, c := range []struct {
		engine string
		want   string
		why    string
	}{
		{"PolarDB for PostgreSQL", DialectGeneric, "PG 版不能套 MySQL 规范"},
		{"polardb postgres 14", DialectGeneric, "同上"},
		{"PolarDB", DialectMySQL, "裸标签的 PolarDB 是 MySQL 兼容版"},
		{"PolarDB for MySQL", DialectMySQL, ""},
		{"MySQL 8.0", DialectMySQL, ""},
		{"MariaDB 10", DialectMySQL, ""},
		{"TiDB 7.1", DialectTiDB, "TiDB 有自己那份规范"},
		{"Oracle 19c", DialectOracle, ""},
		{"DWS 8.1", DialectDWS, "DWS 有自己那份规范"},
		{"GaussDB", DialectDWS, ""},
		{"PostgreSQL 15", DialectGeneric, "没为通用 PG 写过规范,套别人的不如不套"},
		{"ClickHouse", DialectGeneric, ""},
		{"", DialectGeneric, ""},
	} {
		if got := DialectFor(c.engine); got != c.want {
			t.Errorf("DialectFor(%q) = %q,期望 %q %s", c.engine, got, c.want, c.why)
		}
	}
}
