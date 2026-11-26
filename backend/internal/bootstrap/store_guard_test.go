package bootstrap

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"testing"

	"aegisdb/internal/testpg"
)

// closedLocalPort returns a 127.0.0.1 port nothing listens on (listen, then close).
func closedLocalPort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

func pgCfg(env, dsn string) *Config {
	c := &Config{}
	c.Env, c.EnvRaw = normalizeEnv(env), env
	c.Database.Driver = "postgres"
	c.Database.PostgresDSN = dsn
	return c
}

func TestOpenDB_LocalDevConnectFailureHintsAtPostgres(t *testing.T) {
	host, port, _ := net.SplitHostPort(closedLocalPort(t))
	dsn := "host=" + host + " port=" + port + " user=x dbname=x sslmode=disable connect_timeout=2"
	_, err := OpenDB(pgCfg("dev", dsn))
	if err == nil {
		t.Fatal("连不上应报错")
	}
	for _, want := range []string{"PostgreSQL", "SQLite", "AEGISDB_POSTGRES_DSN"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("本地开发的连接失败应提示 %q,got %v", want, err)
		}
	}
	_, err = OpenDB(pgCfg("prod", dsn))
	if err == nil || strings.Contains(err.Error(), "SQLite") {
		t.Errorf("生产环境的错误不带本地开发提示,got %v", err)
	}
}

func TestOpenDB_EmptyDSNFailsNamingTheVariable(t *testing.T) {
	_, err := OpenDB(pgCfg("dev", " "))
	if err == nil || !strings.Contains(err.Error(), "AEGISDB_POSTGRES_DSN") {
		t.Errorf("空 PG 连接串应点名 AEGISDB_POSTGRES_DSN,got %v", err)
	}
	c := &Config{}
	c.Database.Driver = "mysql"
	_, err = OpenDB(c)
	if err == nil || !strings.Contains(err.Error(), "AEGISDB_MYSQL_DSN") {
		t.Errorf("空 MySQL 连接串应点名 AEGISDB_MYSQL_DSN,got %v", err)
	}
}

func TestLoadConfig_DriverAllowlist(t *testing.T) {
	p := writeTempConfig(t, `env: "dev"`)
	t.Setenv("APP_ENV", "")
	t.Setenv("AEGISDB_ENV", "")
	t.Setenv("AEGISDB_DB_DRIVER", "oracle")
	if _, err := LoadConfig(p); err == nil || !strings.Contains(err.Error(), "AEGISDB_DB_DRIVER") {
		t.Errorf("未知 driver 应报错并点名变量,got %v", err)
	}
	t.Setenv("AEGISDB_DB_DRIVER", "SQLite")
	if _, err := LoadConfig(p); err != errSQLiteUnsupported {
		t.Errorf("sqlite 保持专属提示,got %v", err)
	}
	t.Setenv("AEGISDB_DB_DRIVER", "MySQL")
	cfg, err := LoadConfig(p)
	if err != nil || cfg.Database.Driver != "mysql" {
		t.Errorf("mysql 应被接受,got %v %v", cfg, err)
	}
}

func TestRefuseIfUnmigrated(t *testing.T) {
	_, dsn := testpg.NewDB(t)
	cfg := pgCfg("dev", dsn)
	db, err := OpenDB(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := db.DB(); s != nil {
		t.Cleanup(func() { s.Close() })
	}
	if err := RefuseIfUnmigrated(cfg, db); err == nil || !strings.Contains(err.Error(), "server migrate") {
		t.Errorf("本地开发 + 空库应拒绝并提示 server migrate,got %v", err)
	}
	if err := RefuseIfUnmigrated(pgCfg("prod", dsn), db); err != nil {
		t.Errorf("生产行为不变,got %v", err)
	}
	if err := Migrate(cfg, db); err != nil {
		t.Fatal(err)
	}
	if err := RefuseIfUnmigrated(cfg, db); err != nil {
		t.Errorf("迁移后应放行,got %v", err)
	}
}

func TestOpenSQLReview_SQLiteDialectStaysAcceptedAsGeneric(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	token := app.issueClient(admin, "ci", "linwei@aegisdb.io", nil)
	sq := app.openDo(http.MethodPost, "/api/v1/open/sql-review", token, map[string]any{"dialect": "sqlite", "sql": "SELECT 1;"})
	eq(t, sq.Code, 0, "dialect=sqlite 应继续被接受")
	gn := app.openDo(http.MethodPost, "/api/v1/open/sql-review", token, map[string]any{"dialect": "generic", "sql": "SELECT 1;"})
	eq(t, gn.Code, 0, "dialect=generic")
	var a, b struct {
		Result struct {
			Dialect string `json:"dialect"`
		} `json:"result"`
	}
	_ = json.Unmarshal(sq.Data, &a)
	_ = json.Unmarshal(gn.Data, &b)
	if a.Result.Dialect != b.Result.Dialect {
		t.Errorf("sqlite 应按 generic 处理:%q vs %q", a.Result.Dialect, b.Result.Dialect)
	}
}
