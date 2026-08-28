package gateway

import (
	"fmt"
	"math/rand"
	"strings"
	"time"

	"velagateway/internal/model"
)

// ExecResult is the outcome of a proxied command execution. For a real read,
// Columns/Data carry the actual result set (Data capped at maxResultRows for the
// terminal); for a simulated read they stay empty and the client synthesises a
// preview from Rows.
type ExecResult struct {
	Output    string
	Rows      int
	Ms        int
	Columns   []string   // result-set column names (real reads only)
	Data      [][]string // result rows as strings, capped (real reads only)
	Truncated bool       // true when the result set exceeded maxResultRows
	// MaskedColumns:被脱敏的列名。数据本身已经是打码后的了 —— 这个字段只是让
	// 界面能标一句"该列已脱敏",而不是让前端去做脱敏。
	MaskedColumns []string
	Err       error      // non-nil when the target DB rejected the statement
}

// Executor proxies a command to the target instance on behalf of the user.
//
// NOTE: target instances (prod-order-cluster, etc.) are not reachable in a dev
// environment, so execution is simulated — returning realistic row/latency
// figures, mirroring the prototype. Wire a real database/sql driver per engine
// here for production.
type Executor struct{}

func NewExecutor() *Executor { return &Executor{} }

// Run executes sql against conn within the given execution timeout. When the
// connection is configured for real execution (credentials present, supported
// engine) it opens the target DB and runs the statement; otherwise it returns a
// simulated result (demo instances are unreachable in dev; timeout is ignored).
func (x *Executor) Run(conn *model.Connection, sql string, timeout time.Duration) ExecResult {
	// Ms is MEASURED here, around the call itself, for every path.
	//
	// A real run never set it, so the console reported every query as "0ms"; the
	// simulated one reported rand.Intn(40)+4 — an invented latency printed in the
	// same place, in the same format, as a measurement. A number nobody measured
	// is worse than no number, because it is indistinguishable from one that was.
	//
	// It times the execution, not the request: judgement, approval routing and
	// the audit write are the gateway's own overhead and do not belong in a figure
	// an operator reads as "how long the database took".
	started := time.Now()
	elapsed := func() int { return int(time.Since(started).Milliseconds()) }

	if RealExecSupported(conn) {
		res, err := RealRun(conn, sql, timeout)
		if err != nil {
			return ExecResult{Output: "· 数据库执行失败: " + err.Error(), Err: err, Ms: elapsed()}
		}
		res.Ms = elapsed()
		return res
	}
	// Simulated: the ROW COUNT is still synthetic (there is no database to ask),
	// but the duration is the real time this took. The output text no longer
	// carries it — the console renders Ms uniformly for both paths.
	if IsRead(sql) {
		rows := rand.Intn(9000) + 200
		return ExecResult{Output: fmt.Sprintf("+ %s rows", thousands(rows)), Rows: rows, Ms: elapsed()}
	}
	affected := rand.Intn(5)
	return ExecResult{Output: fmt.Sprintf("执行成功 · %d 行受影响", affected), Rows: affected, Ms: elapsed()}
}

// Test simulates "test connection & attach to gateway".
func (x *Executor) Test(conn *model.Connection) (bool, string) {
	time.Sleep(120 * time.Millisecond)
	return true, fmt.Sprintf("%s %s · 已接入网关", conn.Engine, conn.Host)
}

func thousands(n int) string {
	s := fmt.Sprintf("%d", n)
	var out []string
	for len(s) > 3 {
		out = append([]string{s[len(s)-3:]}, out...)
		s = s[:len(s)-3]
	}
	out = append([]string{s}, out...)
	return strings.Join(out, ",")
}
