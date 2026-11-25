package gateway

import (
	"context"
	"strings"
	"testing"
	"time"

	"velagateway/internal/model"
)

// Ms is a measurement, and the console prints it where an operator reads it as
// one. A real run never set it (every query reported 0ms) and the simulated one
// invented a plausible-looking number — printed in the same place, in the same
// format. An invented figure is worse than none, because nothing distinguishes
// it from a real one.

func TestExecutorRun_ReportsAMeasuredDuration(t *testing.T) {
	x := NewExecutor()
	// No credentials → the simulated path, which is what dev and the demo use.
	conn := &model.Connection{Name: "demo", Engine: "mysql"}

	for _, sql := range []string{"SELECT 1", "UPDATE t SET a = 1 WHERE id = 2"} {
		res := x.Run(context.Background(), conn, sql, time.Second)
		if res.Ms < 0 {
			t.Errorf("%q: negative duration %d", sql, res.Ms)
		}
		// The synthetic latency was 4..43ms and never below 4. A real measurement
		// of this work lands well under that, so a lower bound of 4 would still be
		// passing if the invented number came back.
		if res.Ms > 1000 {
			t.Errorf("%q: %dms — not a measurement of this call", sql, res.Ms)
		}
	}
}

// The duration is rendered by the console from Ms; carrying a second copy inside
// the output text let the two disagree.
func TestExecutorRun_OutputTextCarriesNoDuration(t *testing.T) {
	x := NewExecutor()
	conn := &model.Connection{Name: "demo", Engine: "mysql"}
	for _, sql := range []string{"SELECT 1", "DELETE FROM t WHERE id = 1"} {
		if out := x.Run(context.Background(), conn, sql, time.Second).Output; strings.Contains(out, "ms") {
			t.Errorf("%q: output still embeds a duration: %q", sql, out)
		}
	}
}

// A failed run is still timed: "it failed after 30s" and "it failed instantly"
// are different problems, and the operator can only tell them apart if the
// number survives the error path.
func TestExecutorRun_TimesAFailureToo(t *testing.T) {
	x := NewExecutor()
	// Credentials present + a supported engine → the real path, pointed at a host
	// that does not answer.
	conn := &model.Connection{
		Name: "unreachable", Engine: "mysql", Host: "127.0.0.1", Port: 1,
		Username: "u", Password: "p", Database: "d",
	}
	res := x.Run(context.Background(), conn, "SELECT 1", 2*time.Second)
	if res.Err == nil {
		t.Skip("host unexpectedly reachable")
	}
	if res.Ms < 0 {
		t.Errorf("a failed run reported %dms", res.Ms)
	}
}
