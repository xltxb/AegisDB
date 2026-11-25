package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// 并发名额挂在 Services 实例上:两个实例(测试里的两个用例、同进程的两个网关)互不串味。
func TestBizSlotsArePerInstance(t *testing.T) {
	a, b := &Services{}, &Services{}
	release, busy := a.bizSlots.acquire(1, 1, 1, 1)
	if busy != nil {
		t.Fatalf("第一个名额应拿得到: %s", busy)
	}
	defer release()
	if _, busy := a.bizSlots.acquire(1, 1, 1, 1); busy == nil {
		t.Error("同一实例上名额已满,应当拒绝")
	}
	if rel, busy := b.bizSlots.acquire(1, 1, 1, 1); busy != nil {
		t.Errorf("另一个 Services 实例不该共享并发名额: %s", busy)
	} else {
		rel()
	}
}

// 库端时限先于网关的 context 触发时,各引擎报的是自己的错误原文。它们都得认成"超时已取消",
// 而不是"查询失败"(PRD 5.4:结果明确显示已超时自动取消)。
func TestBizTimedOut(t *testing.T) {
	timedOut := []error{
		context.DeadlineExceeded,
		fmt.Errorf("wrapped: %w", context.DeadlineExceeded),
		errors.New("Error 3024 (HY000): Query execution was interrupted, maximum statement execution time exceeded"), // MySQL
		errors.New("Error 1969 (70100): Query execution was interrupted (max_statement_time exceeded)"),               // MariaDB
		errors.New("pq: canceling statement due to statement timeout"),                                                // PostgreSQL
	}
	for _, err := range timedOut {
		if !bizTimedOut(err) {
			t.Errorf("应认成超时: %v", err)
		}
	}
	for _, err := range []error{errors.New("Error 1146: Table 'x.y' doesn't exist"), context.Canceled} {
		if bizTimedOut(err) {
			t.Errorf("不是超时: %v", err)
		}
	}
}
