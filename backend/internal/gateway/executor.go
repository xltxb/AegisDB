package gateway

import (
	"fmt"
	"math/rand"
	"strings"
	"time"

	"velagateway/internal/model"
)

// ExecResult is the outcome of a proxied command execution.
type ExecResult struct {
	Output string
	Rows   int
	Ms     int
	Err    error // non-nil when the target DB rejected the statement
}

// Executor proxies a command to the target instance on behalf of the user.
//
// NOTE: target instances (prod-order-cluster, etc.) are not reachable in a dev
// environment, so execution is simulated — returning realistic row/latency
// figures, mirroring the prototype. Wire a real database/sql driver per engine
// here for production.
type Executor struct{}

func NewExecutor() *Executor { return &Executor{} }

// Run executes sql against conn. When the connection is configured for real
// execution (credentials present, supported engine) it opens the target DB and
// runs the statement; otherwise it returns a simulated result (demo instances
// are unreachable in dev).
func (x *Executor) Run(conn *model.Connection, sql string) ExecResult {
	if RealExecSupported(conn) {
		res, err := RealRun(conn, sql)
		if err != nil {
			return ExecResult{Output: "· 数据库执行失败: " + err.Error(), Err: err}
		}
		return res
	}
	ms := rand.Intn(40) + 4
	if IsRead(sql) {
		rows := rand.Intn(9000) + 200
		return ExecResult{Output: fmt.Sprintf("+ %s rows · %dms", thousands(rows), ms), Rows: rows, Ms: ms}
	}
	affected := rand.Intn(5)
	return ExecResult{Output: fmt.Sprintf("执行成功 · %d 行受影响 · %dms", affected, ms), Rows: affected, Ms: ms}
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
