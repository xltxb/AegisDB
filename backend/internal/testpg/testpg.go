// Package testpg gives tests a throwaway PostgreSQL server.
//
// Each test binary (one per package under `go test`) lazily starts its own
// private PostgreSQL instance in a temp directory on a random 127.0.0.1 port,
// hands every test an isolated database (empty, or cloned from a template built
// once per process), and on exit stops the instance and deletes its directory.
//
// Usage — in the package's test files:
//
//	func TestMain(m *testing.M) { testpg.Main(m) }
//
//	func TestX(t *testing.T) {
//		_, dsn := testpg.MigratedDB(t) // the production pg schema, ready to use
//		...
//	}
//
// Server binaries are looked up in AEGISDB_TEST_PG_BIN, then PATH, then the
// usual Homebrew / Debian / Windows install directories (highest version wins).
// PostgreSQL 13 or newer is required. Setting AEGISDB_TEST_STORE_PG (a libpq
// key=value string without dbname) uses that server instead of starting one;
// testpg then only ever creates and drops databases carrying its own
// per-process prefix (aegis_t_<pid>_ / aegis_tpl_<pid>_).
//
// This is a test-only helper: import it only from _test.go files.
package testpg

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unicode"

	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver "pgx"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"aegisdb/internal/sqlmigrate"
	"aegisdb/migrations"
)

const (
	envStorePG = "AEGISDB_TEST_STORE_PG"
	envPGBin   = "AEGISDB_TEST_PG_BIN"

	minMajor     = 13 // DROP DATABASE … WITH (FORCE)
	superuser    = "aegis_test"
	dirPrefix    = "aegis-testpg-"
	startTries   = 5
	staleDirAge  = 5 * time.Minute
	cmdWaitDelay = 5 * time.Second
)

// Conn is how to reach the test server. DSN always uses sslmode=disable.
type Conn struct {
	Host     string
	Port     int
	User     string
	Password string
}

// DSN renders a libpq key=value connection string; dbname is omitted when empty.
func (c Conn) DSN(dbname string) string {
	parts := []string{
		"host=" + dsnQuote(c.Host),
		"port=" + strconv.Itoa(c.Port),
		"user=" + dsnQuote(c.User),
	}
	if c.Password != "" {
		parts = append(parts, "password="+dsnQuote(c.Password))
	}
	if dbname != "" {
		parts = append(parts, "dbname="+dsnQuote(dbname))
	}
	return strings.Join(append(parts, "sslmode=disable"), " ")
}

func dsnQuote(v string) string {
	if v != "" && !strings.ContainsAny(v, " \t\n'\\") {
		return v
	}
	r := strings.NewReplacer(`\`, `\\`, `'`, `\'`)
	return "'" + r.Replace(v) + "'"
}

// ---------------------------------------------------------------------------
// Public API (backed by the per-process default harness set up by Main).

var (
	defaultMu sync.Mutex
	defaultH  *harness
)

// Main runs the package's tests and then stops the test server, removes its
// directory and drops the templates this process created. Call it as the
// whole body of TestMain.
func Main(m *testing.M) { os.Exit(run(m)) }

func run(m *testing.M) int {
	h := newHarness(os.Getenv(envStorePG), os.TempDir())
	defaultMu.Lock()
	defaultH = h
	defaultMu.Unlock()
	defer h.shutdown()

	// Best effort on Ctrl-C / SIGTERM: tear down before dying.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigs)
	go func() {
		if _, ok := <-sigs; ok {
			h.shutdown()
			os.Exit(130)
		}
	}()

	return m.Run()
}

// Server starts the test server on first use (once per process) and returns
// how to reach it. A failure to start fails the test (it never skips).
func Server(t testing.TB) Conn {
	t.Helper()
	return current(t).mustServer(t)
}

// NewDB creates an empty database that is dropped when the test ends.
func NewDB(t testing.TB) (name string, dsn string) {
	t.Helper()
	return current(t).newDB(t)
}

// FromTemplate clones a template database that is built once per process per
// key: the first caller creates it and runs build(dsnOfTemplate); later callers
// get a copy via CREATE DATABASE … TEMPLATE. build must close every connection
// it opened before returning. key must match [a-z0-9_]+. The clone is dropped
// when the test ends.
func FromTemplate(t testing.TB, key string, build func(dsn string) error) (name string, dsn string) {
	t.Helper()
	return current(t).fromTemplate(t, key, build)
}

// MigratedDB returns a database holding the production PostgreSQL schema
// (every migrations/pg/*.sql applied by sqlmigrate.Run).
func MigratedDB(t testing.TB) (name string, dsn string) {
	t.Helper()
	return FromTemplate(t, "migrated", migrate)
}

func migrate(dsn string) error {
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		return err
	}
	if sqlDB, err := db.DB(); err == nil {
		defer sqlDB.Close()
	}
	return sqlmigrate.Run(db, migrations.PGFS)
}

func current(t testing.TB) *harness {
	t.Helper()
	defaultMu.Lock()
	h := defaultH
	defaultMu.Unlock()
	if h == nil {
		t.Fatal("testpg: not initialised — the package needs `func TestMain(m *testing.M) { testpg.Main(m) }`")
	}
	return h
}

// ---------------------------------------------------------------------------
// harness: one server (started or overridden) plus the databases made on it.

type harness struct {
	override string // AEGISDB_TEST_STORE_PG value; "" = start an instance
	root     string // where instance directories go

	mu     sync.Mutex
	ready  bool // server() already ran (conn/err final)
	closed bool
	conn   Conn
	err    error
	inst   *instance
	admin  *sql.DB

	createMu sync.Mutex // concurrent CREATE DATABASE from one template collide
	tplMu    sync.Mutex
	tpls     map[string]*tplEntry
	seq      atomic.Int64
}

type tplEntry struct {
	once sync.Once
	err  error
}

func newHarness(override, root string) *harness {
	return &harness{override: override, root: root, tpls: map[string]*tplEntry{}}
}

func (h *harness) dbName() string {
	return fmt.Sprintf("aegis_t_%d_%d", os.Getpid(), h.seq.Add(1))
}

func (h *harness) templateName(key string) string {
	return fmt.Sprintf("aegis_tpl_%d_%s", os.Getpid(), key)
}

func (h *harness) ownsDB(name string) bool {
	pid := strconv.Itoa(os.Getpid())
	return strings.HasPrefix(name, "aegis_t_"+pid+"_") || strings.HasPrefix(name, "aegis_tpl_"+pid+"_")
}

func (h *harness) server() (Conn, *sql.DB, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return Conn{}, nil, errors.New("testpg: server already shut down")
	}
	if !h.ready {
		h.ready = true
		h.err = h.start()
	}
	return h.conn, h.admin, h.err
}

func (h *harness) start() error {
	if h.override != "" {
		c, err := parseOverride(h.override)
		if err != nil {
			return fmt.Errorf("testpg: %s: %w", envStorePG, err)
		}
		admin, err := openAdmin(c)
		if err != nil {
			return fmt.Errorf("testpg: %s: %w", envStorePG, err)
		}
		var num int
		if err := admin.QueryRow("SHOW server_version_num").Scan(&num); err != nil {
			admin.Close()
			return fmt.Errorf("testpg: %s: %w", envStorePG, err)
		}
		if num/10000 < minMajor {
			admin.Close()
			return fmt.Errorf("testpg: %s points at PostgreSQL %d, need >= %d", envStorePG, num/10000, minMajor)
		}
		h.conn, h.admin = c, admin
		return nil
	}

	cleanStaleDirs(h.root)
	bin, err := findBinDir(os.Getenv(envPGBin), os.Getenv("PATH"), defaultGlobs())
	if err != nil {
		return err
	}
	inst, err := startInstance(bin, h.root, 0)
	if err != nil {
		return err
	}
	admin, err := openAdmin(inst.conn)
	if err != nil {
		inst.stop()
		return err
	}
	h.inst, h.conn, h.admin = inst, inst.conn, admin
	return nil
}

func openAdmin(c Conn) (*sql.DB, error) {
	db, err := sql.Open("pgx", c.DSN("postgres"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func (h *harness) mustServer(t testing.TB) Conn {
	t.Helper()
	c, _, err := h.server()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (h *harness) newDB(t testing.TB) (string, string) {
	t.Helper()
	c, admin, err := h.server()
	if err != nil {
		t.Fatal(err)
	}
	name := h.dbName()
	if err := h.create(admin, name, ""); err != nil {
		t.Fatalf("testpg: create database: %v", err)
	}
	t.Cleanup(func() { h.dropOrFail(t, admin, name) })
	return name, c.DSN(name)
}

var keyRe = regexp.MustCompile(`^[a-z0-9_]+$`)

func (h *harness) fromTemplate(t testing.TB, key string, build func(dsn string) error) (string, string) {
	t.Helper()
	tpl := h.templateName(key)
	if !keyRe.MatchString(key) || len(tpl) > 63 {
		t.Fatalf("testpg: template key %q must match [a-z0-9_]+ and keep %q within 63 bytes", key, tpl)
	}
	c, admin, err := h.server()
	if err != nil {
		t.Fatal(err)
	}

	h.tplMu.Lock()
	e := h.tpls[key]
	if e == nil {
		e = &tplEntry{}
		h.tpls[key] = e
	}
	h.tplMu.Unlock()
	e.once.Do(func() {
		e.err = errors.New("template build did not complete") // stays if build panics
		e.err = h.buildTemplate(c, admin, tpl, build)
	})
	if e.err != nil {
		t.Fatalf("testpg: template %q: %v", key, e.err)
	}

	name := h.dbName()
	if err := h.create(admin, name, tpl); err != nil {
		t.Fatalf("testpg: clone template %q: %v", key, err)
	}
	t.Cleanup(func() { h.dropOrFail(t, admin, name) })
	return name, c.DSN(name)
}

func (h *harness) buildTemplate(c Conn, admin *sql.DB, tpl string, build func(string) error) error {
	if err := drop(admin, tpl); err != nil {
		return err
	}
	if err := h.create(admin, tpl, ""); err != nil {
		return err
	}
	if err := build(c.DSN(tpl)); err != nil {
		return err
	}
	// Cloning needs the template to be idle; kick out anything build leaked.
	_, err := admin.Exec("SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()", tpl)
	return err
}

func (h *harness) create(admin *sql.DB, name, template string) error {
	stmt := "CREATE DATABASE " + quoteIdent(name)
	if template != "" {
		stmt += " TEMPLATE " + quoteIdent(template)
	}
	h.createMu.Lock()
	defer h.createMu.Unlock()
	_, err := admin.Exec(stmt)
	return err
}

func (h *harness) dropOrFail(t testing.TB, admin *sql.DB, name string) {
	if err := drop(admin, name); err != nil {
		t.Errorf("testpg: drop database %s: %v", name, err)
	}
}

func drop(admin *sql.DB, name string) error {
	_, err := admin.Exec("DROP DATABASE IF EXISTS " + quoteIdent(name) + " WITH (FORCE)")
	return err
}

func quoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

// shutdown drops this process's leftover databases (only on an override
// server — a started instance is simply discarded), then stops the instance
// and removes its directory. Safe to call more than once.
func (h *harness) shutdown() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	if h.admin != nil {
		if h.inst == nil {
			h.sweepOwnDatabases()
		}
		h.admin.Close()
	}
	if h.inst != nil {
		h.inst.stop()
	}
}

func (h *harness) sweepOwnDatabases() {
	rows, err := h.admin.Query("SELECT datname FROM pg_database")
	if err != nil {
		fmt.Fprintf(os.Stderr, "testpg: list databases for cleanup: %v\n", err)
		return
	}
	var mine []string
	for rows.Next() {
		var n string
		if rows.Scan(&n) == nil && h.ownsDB(n) {
			mine = append(mine, n)
		}
	}
	rows.Close()
	for _, n := range mine {
		if err := drop(h.admin, n); err != nil {
			fmt.Fprintf(os.Stderr, "testpg: drop %s: %v\n", n, err)
		}
	}
}

// ---------------------------------------------------------------------------
// Override parsing (libpq key=value).

func parseOverride(s string) (Conn, error) {
	kv, err := parseKV(s)
	if err != nil {
		return Conn{}, err
	}
	if _, ok := kv["dbname"]; ok {
		return Conn{}, errors.New("must not contain dbname (testpg creates its own databases)")
	}
	c := Conn{Host: "127.0.0.1", Port: 5432, User: "postgres", Password: kv["password"]}
	if v, ok := kv["host"]; ok {
		c.Host = v
	}
	if v, ok := kv["user"]; ok {
		c.User = v
	}
	if v, ok := kv["port"]; ok {
		p, err := strconv.Atoi(v)
		if err != nil {
			return Conn{}, fmt.Errorf("bad port %q", v)
		}
		c.Port = p
	}
	return c, nil
}

// parseKV reads `key=value key2='quoted \' value'` (libpq conninfo syntax).
func parseKV(s string) (map[string]string, error) {
	out := map[string]string{}
	r := []rune(s)
	i := 0
	skipSpace := func() {
		for i < len(r) && unicode.IsSpace(r[i]) {
			i++
		}
	}
	for {
		skipSpace()
		if i >= len(r) {
			return out, nil
		}
		start := i
		for i < len(r) && r[i] != '=' && !unicode.IsSpace(r[i]) {
			i++
		}
		key := string(r[start:i])
		skipSpace()
		if i >= len(r) || r[i] != '=' || key == "" {
			return nil, fmt.Errorf("expected key=value near %q", string(r[start:]))
		}
		i++
		skipSpace()
		var val strings.Builder
		if i < len(r) && r[i] == '\'' {
			i++
			closed := false
			for i < len(r) {
				if r[i] == '\\' && i+1 < len(r) {
					val.WriteRune(r[i+1])
					i += 2
					continue
				}
				if r[i] == '\'' {
					closed = true
					i++
					break
				}
				val.WriteRune(r[i])
				i++
			}
			if !closed {
				return nil, fmt.Errorf("unterminated quote in value of %s", key)
			}
		} else {
			for i < len(r) && !unicode.IsSpace(r[i]) {
				if r[i] == '\\' && i+1 < len(r) {
					i++
				}
				val.WriteRune(r[i])
				i++
			}
		}
		out[key] = val.String()
	}
}

// ---------------------------------------------------------------------------
// Binary lookup and version check.

func exeName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

func hasServerBinaries(dir string) bool {
	for _, n := range []string{"initdb", "pg_ctl", "postgres"} {
		if fi, err := os.Stat(filepath.Join(dir, exeName(n))); err != nil || fi.IsDir() {
			return false
		}
	}
	return true
}

func defaultGlobs() []string {
	return []string{
		"/opt/homebrew/opt/postgresql@*/bin",
		"/usr/local/opt/postgresql@*/bin",
		"/usr/lib/postgresql/*/bin",
		`C:\Program Files\PostgreSQL\*\bin`,
	}
}

var globVersionRe = regexp.MustCompile(`(?i)postgresql(?:@|[/\\])(\d+)`)

const installHint = "install PostgreSQL >= 13 (macOS: `brew install postgresql@18`; Linux/Windows: the official PostgreSQL packages from postgresql.org), " +
	"or set " + envPGBin + " to the directory holding initdb/pg_ctl/postgres, " +
	"or set " + envStorePG + " to a libpq key=value string (no dbname) of a disposable server tests may use"

// findBinDir picks the directory holding initdb, pg_ctl and postgres:
// binEnv (AEGISDB_TEST_PG_BIN) if set, else the first PATH entry, else the
// highest-versioned match of globs.
func findBinDir(binEnv, pathEnv string, globs []string) (string, error) {
	if binEnv != "" {
		if hasServerBinaries(binEnv) {
			return binEnv, nil
		}
		return "", fmt.Errorf("testpg: %s=%q does not contain initdb, pg_ctl and postgres; %s", envPGBin, binEnv, installHint)
	}
	for _, d := range filepath.SplitList(pathEnv) {
		if d != "" && hasServerBinaries(d) {
			return d, nil
		}
	}
	type cand struct {
		dir string
		ver int
	}
	var cands []cand
	for _, g := range globs {
		matches, _ := filepath.Glob(g)
		for _, m := range matches {
			if !hasServerBinaries(m) {
				continue
			}
			v := 0
			if sm := globVersionRe.FindStringSubmatch(m); sm != nil {
				v, _ = strconv.Atoi(sm[1])
			}
			cands = append(cands, cand{m, v})
		}
	}
	if len(cands) == 0 {
		return "", fmt.Errorf("testpg: no PostgreSQL server binaries found; %s", installHint)
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].ver > cands[j].ver })
	return cands[0].dir, nil
}

var versionRe = regexp.MustCompile(`\(PostgreSQL\)\s+(\d+)`)

// parseMajor reads the major version from `postgres --version` output and
// rejects anything older than 13.
func parseMajor(out string) (int, error) {
	m := versionRe.FindStringSubmatch(out)
	if m == nil {
		return 0, fmt.Errorf("testpg: cannot read a PostgreSQL version from %q", strings.TrimSpace(out))
	}
	major, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, err
	}
	if major < minMajor {
		return major, fmt.Errorf("testpg: PostgreSQL %d is too old, need >= %d (DROP DATABASE … WITH (FORCE)); %s", major, minMajor, installHint)
	}
	return major, nil
}

// ---------------------------------------------------------------------------
// Instance lifecycle.

type instance struct {
	bin      string
	dir      string // data directory == instance directory
	conn     Conn
	attempts int       // pg_ctl start attempts it took
	watchdog *exec.Cmd // stops the instance if this process dies uncleanly; nil on Windows
}

func command(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.WaitDelay = cmdWaitDelay // never hang on a pipe a daemon inherited
	return cmd
}

// startInstance initdb's a fresh cluster under root and starts it on
// 127.0.0.1 (TCP only). firstPort, if non-zero, is the first port tried;
// a failed start retries on a fresh free port, up to startTries attempts.
func startInstance(bin, root string, firstPort int) (*instance, error) {
	out, err := command(filepath.Join(bin, exeName("postgres")), "--version").Output()
	if err != nil {
		return nil, fmt.Errorf("testpg: %s --version: %w", filepath.Join(bin, "postgres"), err)
	}
	if _, err := parseMajor(string(out)); err != nil {
		return nil, err
	}

	dir, err := os.MkdirTemp(root, fmt.Sprintf("%s%d-*", dirPrefix, os.Getpid()))
	if err != nil {
		return nil, fmt.Errorf("testpg: %w", err)
	}
	inst := &instance{bin: bin, dir: dir}
	ok := false
	defer func() {
		if !ok {
			os.RemoveAll(dir)
		}
	}()

	initOut, err := command(filepath.Join(bin, exeName("initdb")),
		"-D", dir, "-U", superuser, "--auth=trust", "-E", "UTF8", "--no-locale", "--no-sync").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("testpg: initdb: %w\n%s", err, initOut)
	}
	conf := "\n# testpg\n" +
		"listen_addresses = '127.0.0.1'\n" +
		"unix_socket_directories = ''\n" +
		"fsync = off\n" +
		"synchronous_commit = off\n" +
		"full_page_writes = off\n" +
		"max_connections = 400\n"
	f, err := os.OpenFile(filepath.Join(dir, "postgresql.conf"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return nil, fmt.Errorf("testpg: %w", err)
	}
	_, werr := f.WriteString(conf)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return nil, fmt.Errorf("testpg: write postgresql.conf: %w", werr)
	}

	logFile := filepath.Join(dir, "testpg.log")
	var lastErr error
	for attempt := 1; attempt <= startTries; attempt++ {
		port := firstPort
		if attempt > 1 || port == 0 {
			if port, err = freePort(); err != nil {
				return nil, fmt.Errorf("testpg: pick a port: %w", err)
			}
		}
		inst.attempts = attempt
		startOut, err := command(inst.pgCtl(), "start", "-w", "-t", "60", "-D", dir, "-l", logFile,
			"-o", fmt.Sprintf("-p %d", port)).CombinedOutput()
		if err == nil {
			inst.conn = Conn{Host: "127.0.0.1", Port: port, User: superuser}
			inst.watchdog = startWatchdog(inst.pgCtl(), dir)
			ok = true
			return inst, nil
		}
		_ = command(inst.pgCtl(), "stop", "-D", dir, "-m", "immediate", "-w").Run()
		lastErr = fmt.Errorf("pg_ctl start on port %d: %w\n%s\n%s", port, err, bytes.TrimSpace(startOut), tail(logFile, 20))
	}
	return nil, fmt.Errorf("testpg: could not start PostgreSQL after %d attempts: %v", startTries, lastErr)
}

func (i *instance) pgCtl() string { return filepath.Join(i.bin, exeName("pg_ctl")) }

// stop shuts the instance down hard and deletes its directory.
func (i *instance) stop() {
	if i.watchdog != nil {
		_ = i.watchdog.Process.Kill()
		_ = i.watchdog.Wait()
	}
	if out, err := command(i.pgCtl(), "stop", "-D", i.dir, "-m", "immediate", "-w").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "testpg: pg_ctl stop: %v\n%s\n", err, out)
	}
	if err := os.RemoveAll(i.dir); err != nil {
		fmt.Fprintf(os.Stderr, "testpg: remove %s: %v\n", i.dir, err)
	}
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func tail(path string, n int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// cleanStaleDirs removes instance directories left by crashed test processes:
// only aegis-testpg-* directories whose creating process is gone and whose
// postmaster.pid names a dead process (or, lacking a pid file, that are older
// than staleDirAge). A dead postmaster means there is no server to stop.
func cleanStaleDirs(root string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || !strings.HasPrefix(name, dirPrefix) {
			continue
		}
		if creator := leadingInt(strings.TrimPrefix(name, dirPrefix)); creator > 0 && processAlive(creator) {
			continue
		}
		dir := filepath.Join(root, name)
		raw, err := os.ReadFile(filepath.Join(dir, "postmaster.pid"))
		switch {
		case err == nil:
			if pid := leadingInt(strings.TrimSpace(string(raw))); pid > 0 && processAlive(pid) {
				continue
			}
		case os.IsNotExist(err):
			info, ierr := e.Info()
			if ierr != nil || time.Since(info.ModTime()) < staleDirAge {
				continue
			}
		default:
			continue
		}
		_ = os.RemoveAll(dir)
	}
}

func leadingInt(s string) int {
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	n, _ := strconv.Atoi(s[:end])
	return n
}
