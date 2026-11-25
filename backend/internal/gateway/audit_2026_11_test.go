package gateway

import (
	"strings"
	"testing"

	"velagateway/internal/model"
)

// 2026-11 白盒审计 V-05:SQLite 的 ATTACH / VACUUM INTO 碰的是网关主机上的文件。
func TestV05_SQLiteFileOpsDenied(t *testing.T) {
	e := NewRiskEngine(&fakeStore{})
	for _, s := range []string{
		"ATTACH 'x' AS y", "attach database 'vela-gateway.db' as g", "DETACH g", "VACUUM INTO '/tmp/p.html'",
		"/* c */ ATTACH 'x' AS y",
	} {
		v := e.EvaluateFor([]int64{1}, "SQLite", "dev", s)
		if v.Action != ActionDeny || v.Ref == nil || v.Ref.Code != model.RuleSQLiteFileOp {
			t.Errorf("%q 应被拒,实际 %+v", s, v)
		}
	}
	// 原有行为:日常语句不受影响,字面量里的词是数据,别的引擎上不适用这条规则。
	for _, s := range []string{"SELECT 'ATTACH' FROM t", "VACUUM", "PRAGMA table_info(t)", "SELECT 1", "INSERT INTO attach VALUES (1)"} {
		if v := e.EvaluateFor([]int64{1}, "SQLite", "dev", s); v.Ref != nil && v.Ref.Code == model.RuleSQLiteFileOp {
			t.Errorf("%q 不该命中,实际 %+v", s, v)
		}
	}
	if v := e.EvaluateFor([]int64{1}, "MySQL", "dev", "ATTACH 'x' AS y"); v.Ref != nil && v.Ref.Code == model.RuleSQLiteFileOp {
		t.Errorf("MySQL 上不适用,实际 %+v", v)
	}
}

// 2026-11 白盒审计 V-07:go-ora 的 BuildUrl 不转义 server,带 / ? # 的地址能往连接串里塞选项。
func TestV07_OracleHostCannotInjectOptions(t *testing.T) {
	bad := &model.Connection{Engine: "Oracle", Host: "evil/x?TRACE FILE=/tmp/t#", Port: 1521, Database: "svc", Username: "u"}
	if drv, dsn, _ := engineDriver(bad); drv != "" || strings.Contains(dsn, "TRACE") {
		t.Fatalf("存量的非法地址不能拼进连接串:%q %q", drv, dsn)
	}
	good := &model.Connection{Engine: "Oracle", Host: "db.prod.vela.io", Port: 1521, Database: "svc", Username: "u"}
	if drv, dsn, ok := engineDriver(good); drv != "oracle" || !ok || !strings.Contains(dsn, "db.prod.vela.io:1521") {
		t.Fatalf("正常地址照旧:%q %q %v", drv, dsn, ok)
	}
	for _, h := range []string{"db.x.io", "10.0.0.1", "::1", "[::1]", "my_host-1"} {
		if !ValidHost(h) {
			t.Errorf("%q 是合法地址", h)
		}
	}
	for _, h := range []string{"", "a/b", "a?b", "a#b", "a b", "a@b", "a)b", "a=b"} {
		if ValidHost(h) {
			t.Errorf("%q 不是合法地址", h)
		}
	}
}
