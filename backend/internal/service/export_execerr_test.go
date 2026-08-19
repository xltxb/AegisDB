package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

// A streaming export that dies mid-run used to surface the raw driver error —
// for a dropped connection that is a bare "unexpected EOF", which tells the
// operator nothing. The translation must (a) recognise our own deadline and
// report it as a timeout with the configured budget, (b) turn a mid-stream
// disconnect into an actionable message carrying how far the export got, and
// (c) leave every other error untouched.
func TestExportExecErr_Translation(t *testing.T) {
	wrapped := fmt.Errorf("%w (unexpected EOF)", context.DeadlineExceeded)
	if got := exportExecErr(wrapped, 1234, 30*time.Minute).Error(); !strings.Contains(got, "导出执行超时(30分钟)") || !strings.Contains(got, "1234 行") {
		t.Errorf("deadline not translated: %q", got)
	}

	eof := fmt.Errorf("driver: %w", io.ErrUnexpectedEOF)
	got := exportExecErr(eof, 98765, time.Minute).Error()
	if !strings.Contains(got, "断开连接") || !strings.Contains(got, "98765 行") || !strings.Contains(got, "net_write_timeout") {
		t.Errorf("mid-stream disconnect not translated: %q", got)
	}

	plain := errors.New("Table 'app.users' doesn't exist")
	if got := exportExecErr(plain, 0, time.Minute); got != plain {
		t.Errorf("ordinary errors must pass through, got %v", got)
	}
}
