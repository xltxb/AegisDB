package gateway

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"velagateway/internal/model"
)

// The MySQL DSN must not carry a socket ReadTimeout: it is a per-read deadline
// shared by every caller of the pooled DSN, and the 60s it used to carry killed
// any operation whose server goes quiet for a minute (a big export's sort
// phase, an async statement advertised as 30–60min+) with a cryptic
// "unexpected EOF" — despite the caller's own context allowing far more.
// Per-operation budgets live in the contexts every call path already passes.
func TestEngineDriver_MySQLHasNoSocketReadTimeout(t *testing.T) {
	conn := &model.Connection{Engine: "mysql", Host: "db.internal", Port: 3306, Username: "u", Password: "p", Database: "app"}
	_, dsn, ok := engineDriver(conn)
	if !ok {
		t.Fatal("mysql driver not resolved")
	}
	if strings.Contains(dsn, "readTimeout") {
		t.Errorf("mysql DSN must not set a socket readTimeout, got %q", dsn)
	}
}

// When RealQueryEach's own deadline fires mid-stream, the driver can surface
// the killed connection as a transport error instead of the context error. The
// caller must still be able to errors.Is the timeout, so the export layer can
// report "导出执行超时" rather than parroting "unexpected EOF".
func TestRealQueryEach_TimeoutIsRecognisable(t *testing.T) {
	conn := &model.Connection{Engine: "sqlite", Database: filepath.Join(t.TempDir(), "t.db")}
	// A recursive CTE that grinds long enough for a 50ms deadline to fire first.
	slow := "WITH RECURSIVE cnt(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM cnt WHERE x < 5000000) SELECT count(*) FROM cnt"
	err := RealQueryEach(conn, slow, 50*time.Millisecond,
		func([]string) error { return nil }, func([]string) error { return nil })
	if err == nil {
		t.Fatal("expected the deadline to abort the query")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("timeout must be recognisable via errors.Is, got %v", err)
	}
}
