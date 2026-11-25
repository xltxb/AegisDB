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
	err := RealQueryEach(context.Background(), conn, slow, 50*time.Millisecond,
		func([]string) error { return nil }, func([]string) error { return nil })
	if err == nil {
		t.Fatal("expected the deadline to abort the query")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("timeout must be recognisable via errors.Is, got %v", err)
	}
}

// 2026-10-26 讨论 13:Web 命令行读到一半撞上执行超时,交出已读到的行并说明是被超时截断的,
// 而不是整条报错、一行不返回。只对 Web 命令行(WithResultPaging);别的调用方、以及一行都没读到时,照旧报错。
func TestRealRun_TerminalTimeoutReturnsRowsReadSoFar(t *testing.T) {
	conn := &model.Connection{Engine: "sqlite", Database: filepath.Join(t.TempDir(), "t.db")}
	// 每一行都要算一阵子:几百毫秒里只读得出一部分
	slow := "WITH RECURSIVE cnt(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM cnt WHERE x < 100000) " +
		"SELECT x, (WITH RECURSIVE c(y) AS (VALUES(1) UNION ALL SELECT y+1 FROM c WHERE y < 20000 + (cnt.x % 2)) SELECT count(*) FROM c) FROM cnt"
	ctx := WithResultPaging(context.Background(), 1, 200)
	res, err := RealRun(ctx, conn, slow, 300*time.Millisecond)
	if err != nil {
		t.Fatalf("读到一部分时应交出已读的行,实际报错: %v", err)
	}
	if res.TotalRows == 0 || res.TotalRows >= 100000 {
		t.Fatalf("应只读到一部分,实际 %d 行", res.TotalRows)
	}
	if !res.Truncated || res.NoticeRef == nil || res.NoticeRef.Code != "termReadTimeout" {
		t.Errorf("应标成截断并说明超时: truncated=%v ref=%+v notice=%q", res.Truncated, res.NoticeRef, res.Notice)
	}
	// 回归:不带分页(审批执行、导出等)照旧报错
	if _, err := RealRun(context.Background(), conn, slow, 300*time.Millisecond); err == nil {
		t.Error("非 Web 命令行调用方超时仍应报错")
	}
}
