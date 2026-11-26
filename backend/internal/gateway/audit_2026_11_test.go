package gateway

import (
	"strings"
	"testing"

	"aegisdb/internal/model"
)

// 2026-11 白盒审计 V-07:go-ora 的 BuildUrl 不转义 server,带 / ? # 的地址能往连接串里塞选项。
func TestV07_OracleHostCannotInjectOptions(t *testing.T) {
	bad := &model.Connection{Engine: "Oracle", Host: "evil/x?TRACE FILE=/tmp/t#", Port: 1521, Database: "svc", Username: "u"}
	if drv, dsn, _ := engineDriver(bad); drv != "" || strings.Contains(dsn, "TRACE") {
		t.Fatalf("存量的非法地址不能拼进连接串:%q %q", drv, dsn)
	}
	good := &model.Connection{Engine: "Oracle", Host: "db.prod.aegisdb.io", Port: 1521, Database: "svc", Username: "u"}
	if drv, dsn, ok := engineDriver(good); drv != "oracle" || !ok || !strings.Contains(dsn, "db.prod.aegisdb.io:1521") {
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
