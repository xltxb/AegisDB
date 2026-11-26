package testpg

import (
	"database/sql"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestMain(m *testing.M) { Main(m) }

func openDSN(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func dbExists(t *testing.T, c Conn, name string) bool {
	t.Helper()
	admin := openDSN(t, c.DSN("postgres"))
	var n int
	if err := admin.QueryRow("SELECT count(*) FROM pg_database WHERE datname = $1", name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}

func TestNewDBIsUsableAndDropped(t *testing.T) {
	var name string
	t.Run("use", func(t *testing.T) {
		var dsn string
		name, dsn = NewDB(t)
		if !strings.HasPrefix(name, fmt.Sprintf("aegis_t_%d_", os.Getpid())) {
			t.Fatalf("db name %q lacks the per-process prefix", name)
		}
		db := openDSN(t, dsn)
		if _, err := db.Exec("CREATE TABLE t_probe (id int primary key)"); err != nil {
			t.Fatalf("create table: %v", err)
		}
		if _, err := db.Exec("INSERT INTO t_probe VALUES (1)"); err != nil {
			t.Fatalf("insert: %v", err)
		}
	})
	if name == "" {
		t.Fatal("subtest did not create a database")
	}
	if dbExists(t, Server(t), name) {
		t.Fatalf("database %s still exists after the test finished", name)
	}
}

func TestMigratedDBHasSchema(t *testing.T) {
	files, err := filepath.Glob("../../migrations/pg/*.sql")
	if err != nil || len(files) == 0 {
		t.Fatalf("list pg migrations: %v (%d files)", err, len(files))
	}
	_, dsn := MigratedDB(t)
	db := openDSN(t, dsn)
	for _, tbl := range []string{"tbl_user", "schema_migrations"} {
		var reg sql.NullString
		if err := db.QueryRow("SELECT to_regclass($1)::text", tbl).Scan(&reg); err != nil {
			t.Fatal(err)
		}
		if !reg.Valid {
			t.Errorf("table %s missing", tbl)
		}
	}
	var n int
	if err := db.QueryRow("SELECT count(*) FROM schema_migrations").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != len(files) {
		t.Fatalf("schema_migrations has %d rows, want %d", n, len(files))
	}
}

func TestTemplateBuiltOnce(t *testing.T) {
	var builds int32
	build := func(dsn string) error {
		atomic.AddInt32(&builds, 1)
		db, err := sql.Open("pgx", dsn)
		if err != nil {
			return err
		}
		defer db.Close()
		_, err = db.Exec("CREATE TABLE t_item (v text); INSERT INTO t_item VALUES ('seed')")
		return err
	}
	_, dsnA := FromTemplate(t, "built_once", build)
	_, dsnB := FromTemplate(t, "built_once", build)
	if got := atomic.LoadInt32(&builds); got != 1 {
		t.Fatalf("build ran %d times, want 1", got)
	}
	a, b := openDSN(t, dsnA), openDSN(t, dsnB)
	if _, err := a.Exec("INSERT INTO t_item VALUES ('only-a')"); err != nil {
		t.Fatal(err)
	}
	count := func(db *sql.DB) int {
		var n int
		if err := db.QueryRow("SELECT count(*) FROM t_item").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if na, nb := count(a), count(b); na != 2 || nb != 1 {
		t.Fatalf("rows a=%d b=%d, want a=2 b=1 (clones must be independent)", na, nb)
	}
}

func TestVersionFloor(t *testing.T) {
	if _, err := parseMajor("postgres (PostgreSQL) 12.9"); err == nil || !strings.Contains(err.Error(), "13") {
		t.Fatalf("12.9: want an error mentioning 13, got %v", err)
	}
	major, err := parseMajor("postgres (PostgreSQL) 18.6")
	if err != nil || major != 18 {
		t.Fatalf("18.6: got %d, %v", major, err)
	}
	if major, err := parseMajor("postgres (PostgreSQL) 18.6 (Homebrew)"); err != nil || major != 18 {
		t.Fatalf("homebrew 18.6: got %d, %v", major, err)
	}
	if _, err := parseMajor("garbage"); err == nil {
		t.Fatal("garbage: want an error")
	}
}

func TestMissingBinariesMessage(t *testing.T) {
	empty := t.TempDir()
	for name, args := range map[string][3]string{
		"bin env points at empty dir": {empty, "", ""},
		"nothing anywhere":            {"", "", ""},
		"path without postgres":       {"", empty, ""},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := findBinDir(args[0], args[1], nil)
			if err == nil {
				t.Fatal("want an error")
			}
			for _, want := range []string{"brew install postgresql@18", envPGBin, envStorePG} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q lacks %q", err, want)
				}
			}
		})
	}
}

func TestFindBinDirPrefersHighestGlobVersion(t *testing.T) {
	root := t.TempDir()
	for _, v := range []string{"14", "16", "9"} {
		bin := filepath.Join(root, "postgresql@"+v, "bin")
		if err := os.MkdirAll(bin, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, exe := range []string{"initdb", "pg_ctl", "postgres"} {
			if err := os.WriteFile(filepath.Join(bin, exeName(exe)), nil, 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	got, err := findBinDir("", "", []string{filepath.Join(root, "postgresql@*", "bin")})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, "postgresql@16", "bin"); got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestStaleDirsAreRemoved(t *testing.T) {
	root := t.TempDir()
	dead := filepath.Join(root, "aegis-testpg-999999-x")
	alive := filepath.Join(root, "aegis-testpg-999998-y")                      // creator gone, postmaster alive
	live := filepath.Join(root, fmt.Sprintf("aegis-testpg-%d-z", os.Getpid())) // creator alive, no pid file yet
	other := filepath.Join(root, "not-ours-999999")
	for _, d := range []string{dead, alive, live, other} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writePid := func(dir string, pid int) {
		if err := os.WriteFile(filepath.Join(dir, "postmaster.pid"), []byte(fmt.Sprintf("%d\n/some/data\n", pid)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writePid(dead, 999999)
	writePid(alive, os.Getpid())
	writePid(other, 999999)

	cleanStaleDirs(root)

	if _, err := os.Stat(dead); !os.IsNotExist(err) {
		t.Errorf("dir with dead postmaster pid survived: %v", err)
	}
	if _, err := os.Stat(alive); err != nil {
		t.Errorf("dir with live pid was removed: %v", err)
	}
	if _, err := os.Stat(live); err != nil {
		t.Errorf("dir of a live creator was removed: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("dir without our prefix was removed: %v", err)
	}
}

func TestStartRetriesOnBusyPort(t *testing.T) {
	bin, err := findBinDir(os.Getenv(envPGBin), os.Getenv("PATH"), defaultGlobs())
	if err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	busy := l.Addr().(*net.TCPAddr).Port

	inst, err := startInstance(bin, t.TempDir(), busy)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer inst.stop()
	if inst.conn.Port == busy {
		t.Fatalf("instance claims busy port %d", busy)
	}
	if inst.attempts < 2 {
		t.Fatalf("attempts = %d, want >= 2 (first candidate was busy)", inst.attempts)
	}
	db := openDSN(t, inst.conn.DSN("postgres"))
	if err := db.Ping(); err != nil {
		t.Fatalf("ping: %v", err)
	}
}

func TestOverrideLeavesForeignDatabases(t *testing.T) {
	bin, err := findBinDir(os.Getenv(envPGBin), os.Getenv("PATH"), defaultGlobs())
	if err != nil {
		t.Fatal(err)
	}
	inst, err := startInstance(bin, t.TempDir(), 0)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer inst.stop()

	admin := openDSN(t, inst.conn.DSN("postgres"))
	foreignOtherPid := fmt.Sprintf("aegis_t_%d_1", os.Getpid()+1)
	for _, name := range []string{"someone_else", foreignOtherPid} {
		if _, err := admin.Exec("CREATE DATABASE " + name); err != nil {
			t.Fatal(err)
		}
	}

	h := newHarness(inst.conn.DSN(""), t.TempDir())
	var mine, leaked string
	t.Run("as override", func(t *testing.T) {
		mine, _ = h.newDB(t)
		h.fromTemplate(t, "ovr", func(string) error { return nil })
	})
	// A database of ours that a test never dropped (e.g. a crashed cleanup):
	// shutdown must sweep it, but nothing outside the prefix.
	leaked = h.dbName()
	if _, err := admin.Exec("CREATE DATABASE " + leaked); err != nil {
		t.Fatal(err)
	}
	if h.inst != nil {
		t.Fatal("override mode must not start an instance")
	}
	h.shutdown()

	existing := map[string]bool{}
	rows, err := admin.Query("SELECT datname FROM pg_database")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		existing[n] = true
	}
	rows.Close()
	for _, name := range []string{"someone_else", foreignOtherPid} {
		if !existing[name] {
			t.Errorf("foreign database %s was dropped", name)
		}
	}
	for _, name := range []string{mine, leaked, h.templateName("ovr")} {
		if existing[name] {
			t.Errorf("own database %s survived shutdown", name)
		}
	}
}

func TestParseOverride(t *testing.T) {
	c, err := parseOverride(`host=10.0.0.5 port=6543 user=bob password='p w\'d' sslmode=disable`)
	if err != nil {
		t.Fatal(err)
	}
	if c != (Conn{Host: "10.0.0.5", Port: 6543, User: "bob", Password: "p w'd"}) {
		t.Fatalf("got %+v", c)
	}
	if _, err := parseOverride("host=x dbname=y"); err == nil {
		t.Fatal("dbname in override must be rejected")
	}
	// DSN round-trips the quoted password.
	back, err := parseOverride(c.DSN(""))
	if err != nil || back != c {
		t.Fatalf("round trip: %+v %v", back, err)
	}
}
