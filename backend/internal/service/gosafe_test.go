package service

import (
	"testing"
	"time"
)

// L-OSC4(2026-10 安全审计):后台 goroutine 里的 panic 会带走整个网关进程。goSafe 收住它。
func TestGoSafe_RecoversPanic(t *testing.T) {
	done := make(chan struct{})
	goSafe("test", func() {
		defer close(done)
		panic("boom")
	})
	select {
	case <-done: // 走到这里而进程没退出,说明 panic 被收住了
	case <-time.After(5 * time.Second):
		t.Fatal("goroutine 没有结束")
	}
}
