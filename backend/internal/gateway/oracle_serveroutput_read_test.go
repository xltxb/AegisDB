package gateway

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	goora "github.com/sijms/go-ora/v2"
)

// DBMS_OUTPUT 读回的拼装逻辑(不需要 Oracle):一次往返取一批,Go 这边按分隔符拆回行。
//
// 原先一行一次往返,2000 行就是 2000 次;机器一忙,读回超过 10 秒的时限,已经读到的行也被
// 整个丢掉 —— 用户看到的是"没有输出",而不是"输出很多、截断了"。

// fakeOutput 扮演 Oracle 会话上的 DBMS_OUTPUT 缓冲区,按 collectServerOutput 发来的批量块回答。
type fakeOutput struct {
	lines   []string
	calls   int
	failAt  int // 第几次批量调用返回错误(0 = 不出错)
	disable int
}

func (f *fakeOutput) ExecContext(_ context.Context, q string, args ...any) (sql.Result, error) {
	if strings.Contains(q, "DISABLE") {
		f.disable++
		return nil, nil
	}
	f.calls++
	if f.failAt == f.calls {
		return nil, errors.New("context deadline exceeded")
	}
	max := args[0].(int)
	var buf string
	n, fin := 0, 0
	for {
		if n >= max {
			break
		}
		if len(f.lines) == 0 {
			fin = 1
			break
		}
		buf += f.lines[0] + serverOutputSep
		f.lines = f.lines[1:]
		n++
	}
	*args[1].(goora.Out).Dest.(*goora.Clob) = goora.Clob{String: buf, Valid: true}
	*args[2].(goora.Out).Dest.(*int64) = int64(n)
	*args[3].(goora.Out).Dest.(*int64) = int64(fin)
	return nil, nil
}

func lines(n int, mk func(i int) string) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = mk(i)
	}
	return out
}

func TestCollectServerOutput_Batches(t *testing.T) {
	f := &fakeOutput{lines: []string{"hello", "中文 输出", ""}}
	got, truncated, err := collectServerOutput(context.Background(), f)
	if err != nil || truncated || strings.Join(got, "|") != "hello|中文 输出|" {
		t.Fatalf("三行(含空行):实际 %q truncated=%v err=%v", got, truncated, err)
	}
	if f.calls != 1 || f.disable != 1 {
		t.Errorf("三行应一次往返取完并关掉缓冲区,实际 calls=%d disable=%d", f.calls, f.disable)
	}
}

func TestCollectServerOutput_LongLinesSpanBatches(t *testing.T) {
	long := lines(5, func(i int) string { return strings.Repeat(string(rune('a'+i)), 10000) })
	f := &fakeOutput{lines: append([]string{}, long...)}
	got, truncated, err := collectServerOutput(context.Background(), f)
	if err != nil || truncated || len(got) != 5 {
		t.Fatalf("5 行各 1 万字符:应原样读回,实际 %d 行 truncated=%v err=%v", len(got), truncated, err)
	}
	for i := range long {
		if got[i] != long[i] {
			t.Errorf("第 %d 行被截坏:长度 %d", i+1, len(got[i]))
		}
	}
}

func TestCollectServerOutput_CapAt2000(t *testing.T) {
	f := &fakeOutput{lines: lines(2500, func(i int) string { return "line" })}
	got, truncated, err := collectServerOutput(context.Background(), f)
	if err != nil || !truncated || len(got) != maxServerOutputLines {
		t.Fatalf("2500 行应截断到 %d,实际 %d truncated=%v err=%v", maxServerOutputLines, len(got), truncated, err)
	}
	if f.calls > 5 {
		t.Errorf("2000 行应分几批读完(每批 %d 行),实际 %d 次往返", serverOutputBatch, f.calls)
	}
}

// 读到一半出错(超时):已经读到的行留着,标记截断 —— 不再整个丢掉。
func TestCollectServerOutput_KeepsWhatItReadOnError(t *testing.T) {
	f := &fakeOutput{lines: lines(1200, func(i int) string { return "x" }), failAt: 2}
	got, truncated, err := collectServerOutput(context.Background(), f)
	if err == nil {
		t.Fatal("应报告出错")
	}
	if len(got) == 0 || !truncated {
		t.Fatalf("出错前读到的行应保留并标记截断,实际 %d 行 truncated=%v", len(got), truncated)
	}
}
