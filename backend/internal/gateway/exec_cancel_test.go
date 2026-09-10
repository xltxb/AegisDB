package gateway

// 操作员取消一条正在执行的语句。
//
// 终端里按 Ctrl+C 走到这里:上下文被取消,驱动收到 KILL QUERY / cancel request。
// 这组用例钉的是**取消之后那句话怎么说**,而它是这个功能里最容易说错的地方:
//
//   - 取消不是"数据库报错"。混在一起,操作员会以为语句写错了,去改 SQL。
//   - 取消更**不等于没执行**。驱动的取消是发出去的一个请求,语句可能已经跑完、
//     也可能跑了一半。说成"已取消,未执行"是这个功能最坏的一种错:它会让人放心地
//     以为库里什么都没发生,然后按原样再跑一遍。
//
// 至于"按下 Ctrl+C 多快能停",那是各驱动自己的事:MySQL 另开连接发 KILL QUERY、
// PostgreSQL 发 CancelRequest、Oracle 发 OOB break,都很快;而这里用的纯 Go SQLite
// 驱动在一次长 step 里不响应中断,要等那一步跑完。所以这组用例用**预先取消**的上下文,
// 测的是语义,不是驱动的响应速度。

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"velagateway/internal/model"
)

func cancelledCtx() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func TestExecCancelled_SaysCancelledNotFailed(t *testing.T) {
	x := &Executor{}
	conn := &model.Connection{Engine: "SQLite", Database: "file::memory:"}
	res := x.Run(cancelledCtx(), conn, "SELECT 1", 5*time.Second)

	if res.Err == nil || !errors.Is(res.Err, context.Canceled) {
		t.Fatalf("取消应当以 context.Canceled 呈现,实际 err = %v", res.Err)
	}
	if res.OutputRef == nil || res.OutputRef.Code != model.OutExecCancelled {
		t.Fatalf("取消要有自己的标识,实际 %+v", res.OutputRef)
	}
	// 提示类文案以 · 开头 —— 终端据此用黄色显示、且不追加耗时。
	if !strings.HasPrefix(strings.TrimSpace(res.Output), "·") {
		t.Errorf("取消是一条提示,应当以 · 开头:%q", res.Output)
	}
	// 这一条是重点:话不能说成"没执行"。
	if !strings.Contains(res.Output, "可能已执行") {
		t.Errorf("取消不等于没执行,这句话必须说出来:%q", res.Output)
	}
	if strings.Contains(res.Output, "执行失败") {
		t.Errorf("取消不是数据库报错,不该说成执行失败:%q", res.Output)
	}
}

// 真正的数据库错误仍然走"执行失败"那条,带驱动原文 —— 两者不能混。
func TestExecFailed_StillReportsTheDriverError(t *testing.T) {
	x := &Executor{}
	conn := &model.Connection{Engine: "SQLite", Database: "file::memory:"}
	res := x.Run(context.Background(), conn, "SELECT * FROM no_such_table_here", 5*time.Second)

	if res.Err == nil {
		t.Fatal("一条查不存在的表的语句应当报错")
	}
	if res.OutputRef == nil || res.OutputRef.Code != model.OutExecFailed {
		t.Fatalf("数据库报错应当是 execFailed,实际 %+v", res.OutputRef)
	}
	// 驱动原文不翻译:它是目标库说的话。
	if !strings.Contains(res.OutputRef.Args["err"], "no_such_table_here") {
		t.Errorf("驱动原文要原样带出:%+v", res.OutputRef.Args)
	}
}

// 超时与取消是两件事:一个是等太久了,一个是人说停。混起来的话,一条超时的语句会被
// 报成"操作员取消",而当时根本没人按过任何键。
func TestExecTimeout_IsNotReportedAsCancelled(t *testing.T) {
	x := &Executor{}
	conn := &model.Connection{Engine: "SQLite", Database: "file::memory:"}
	// 用一个**已经过期**的 deadline,而不是极短的 timeout:后者靠定时器置位 ctx.Err(),
	// 1 纳秒的预算未必在语句开始前就已经触发,那样的用例是碰运气的。
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	res := x.Run(expired, conn, "SELECT 1", 5*time.Second)

	if res.Err == nil || !errors.Is(res.Err, context.DeadlineExceeded) {
		t.Fatalf("超时应当以 DeadlineExceeded 呈现,实际 err = %v", res.Err)
	}
	if res.OutputRef != nil && res.OutputRef.Code == model.OutExecCancelled {
		t.Error("超时被报成了操作员取消")
	}
}
