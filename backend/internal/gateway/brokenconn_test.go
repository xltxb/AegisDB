package gateway

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	mysqldrv "github.com/go-sql-driver/mysql"

	"velagateway/internal/model"
)

// 2026-10 生产:TiDB 实例在终端实例树里展开报「无法连接目标数据库…: invalid connection」。
// invalid connection 不是连不上,是连接池里的空闲连接被中间设备(HAProxy / 负载均衡 / 防火墙)
// 悄悄断掉了,网关复用时请求已发出、对端回 RST —— 驱动不会自己重试已经发出去的请求。
// 只读的元数据读取(库树、列名、对象、元数据同步、结构对比)遇到这种断开,丢掉这台实例池里
// 的空闲连接、换新连接重试一次。

// cutProxy 是挡在真实 MySQL 前面的 TCP 转发。cut() 之后,此前建立的每条连接在客户端
// 下一次发数据时被 RST(模拟中间设备早已丢掉这条会话);之后的新连接照常转发。
type cutProxy struct {
	ln     net.Listener
	target string
	mu     sync.Mutex
	conns  map[*proxied]bool
}

type proxied struct {
	client, upstream net.Conn
	mu               sync.Mutex
	cut              bool
}

func newCutProxy(t *testing.T, target string) *cutProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &cutProxy{ln: ln, target: target, conns: map[*proxied]bool{}}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			u, err := net.Dial("tcp", target)
			if err != nil {
				c.Close()
				continue
			}
			pc := &proxied{client: c, upstream: u}
			p.mu.Lock()
			p.conns[pc] = true
			p.mu.Unlock()
			go pc.clientToUpstream()
			go func() { _, _ = io.Copy(c, u); c.Close() }()
		}
	}()
	return p
}

func (pc *proxied) clientToUpstream() {
	buf := make([]byte, 32*1024)
	for {
		n, err := pc.client.Read(buf)
		pc.mu.Lock()
		cut := pc.cut
		pc.mu.Unlock()
		if cut { // 中间设备早就不认这条会话了:客户端一发数据就回 RST
			if tc, ok := pc.client.(*net.TCPConn); ok {
				_ = tc.SetLinger(0)
			}
			pc.client.Close()
			pc.upstream.Close()
			return
		}
		if n > 0 {
			if _, werr := pc.upstream.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			pc.upstream.Close()
			return
		}
	}
}

func (p *cutProxy) port() int { return p.ln.Addr().(*net.TCPAddr).Port }

// cut 让此刻已有的每条连接"失效":不发 FIN(驱动复用前的存活检查看不出来),下一次写入时 RST。
func (p *cutProxy) cut() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for pc := range p.conns {
		pc.mu.Lock()
		pc.cut = true
		pc.mu.Unlock()
		delete(p.conns, pc)
	}
}

func TestIsBrokenConn(t *testing.T) {
	for _, c := range []struct {
		err  error
		want bool
	}{
		{mysqldrv.ErrInvalidConn, true},
		{fmt.Errorf("wrap: %w", mysqldrv.ErrInvalidConn), true},
		{io.ErrUnexpectedEOF, true},
		{context.DeadlineExceeded, false},
		{errors.New("Error 1045 (28000): Access denied for user"), false},
		{nil, false},
	} {
		if got := isBrokenConn(c.err); got != c.want {
			t.Errorf("isBrokenConn(%v) = %v,应为 %v", c.err, got, c.want)
		}
	}
}

// 真库:池里的空闲连接全被中间设备断掉后,库树照样能加载出来(换新连接重试一次)。
func TestRealSchema_RetriesWhenPooledConnectionsWereCut(t *testing.T) {
	base, admin := testMySQLConn(t)
	schema := fmt.Sprintf("zz_cut_%d", time.Now().UnixNano()%1_000_000)
	if _, err := admin.Exec("CREATE DATABASE " + schema); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec("DROP DATABASE " + schema)
	if _, err := admin.Exec("CREATE TABLE " + schema + ".t_cut (id INT)"); err != nil {
		t.Fatal(err)
	}

	p := newCutProxy(t, fmt.Sprintf("%s:%d", base.Host, base.Port))
	conn := *base
	conn.Name, conn.Host, conn.Port = "cut", "127.0.0.1", p.port()

	// 先把池热起来:留下 3 条空闲连接(生产上被代理一起断掉的就是它们)
	target := conn
	target.Database = ""
	db, _, err := openConn(&target)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var held []interface{ Close() error }
	for i := 0; i < poolMaxIdle; i++ {
		c, err := db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := c.PingContext(ctx); err != nil {
			t.Fatal(err)
		}
		held = append(held, c)
	}
	for _, c := range held {
		c.Close()
	}
	if _, err := RealSchema(&conn); err != nil {
		t.Fatalf("断开之前应能加载: %v", err)
	}

	p.cut()
	groups, err := RealSchema(&conn)
	if err != nil {
		t.Fatalf("空闲连接被断开后应换新连接重试成功,实际: %v", err)
	}
	found := false
	for _, g := range groups {
		if g.Database == schema {
			found = true
		}
	}
	if !found {
		t.Errorf("重试后的结果里应有库 %s", schema)
	}
}

// warmPool 让 conn 对应的连接池留下 poolMaxIdle 条空闲连接(生产上被代理一起断掉的就是它们)。
func warmPool(t *testing.T, conn *model.Connection) {
	t.Helper()
	db, _, err := openConn(conn)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var held []*sql.Conn
	for i := 0; i < poolMaxIdle; i++ {
		c, err := db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := c.PingContext(ctx); err != nil {
			t.Fatal(err)
		}
		held = append(held, c)
	}
	for _, c := range held {
		c.Close()
	}
}

// 2026-10-27 UAT-TIDB 数据查询偶发「无法确认实例 … 上的库列表,暂不能查询:invalid connection」:
// 查询前读实例库名(RealDatabaseNames)是只读元数据读取,却没走断连重试。
func TestRealDatabaseNames_RetriesWhenPooledConnectionsWereCut(t *testing.T) {
	base, _ := testMySQLConn(t)
	p := newCutProxy(t, fmt.Sprintf("%s:%d", base.Host, base.Port))
	conn := *base
	conn.Name, conn.Host, conn.Port = "cut-dbs", "127.0.0.1", p.port()
	target := conn
	target.Database = "" // RealDatabaseNames 用不绑库的池
	warmPool(t, &target)

	p.cut()
	names, err := RealDatabaseNames(&conn)
	if err != nil {
		t.Fatalf("空闲连接被断开后应换新连接重试成功,实际: %v", err)
	}
	if !slices.Contains(names, "mysql") {
		t.Errorf("结果里应有 mysql 库: %v", names)
	}
}

// 同一批死连接也会落到查询本身:用户的 SQL 发出之前(取线程号、设时限、开事务)断开的,
// 换新连接再来一次;用户的 SQL 一条都还没发。
func TestRealBizQuery_RetriesWhenBrokenBeforeQuerySent(t *testing.T) {
	base, _ := testMySQLConn(t)
	p := newCutProxy(t, fmt.Sprintf("%s:%d", base.Host, base.Port))
	conn := *base
	conn.Name, conn.Host, conn.Port = "cut-q", "127.0.0.1", p.port()
	warmPool(t, &conn)

	p.cut()
	res, err := RealBizQuery(context.Background(), &conn, "SELECT 1 AS one", 10, 10*time.Second)
	if err != nil {
		t.Fatalf("准备阶段断开应换新连接重试成功,实际: %v", err)
	}
	if len(res.Data) != 1 || res.Data[0][0] != "1" {
		t.Errorf("结果不对: %+v", res)
	}
}

// 库树加载失败的提示要说对原因:中途断开(已自动重试仍失败)、超时、连不上是三件事,
// 只有最后一种该让人去查网络 / 账号密码(2026-10 生产:invalid connection 被说成"检查账号密码")。
func TestSchemaLoadError_NamesTheRightCause(t *testing.T) {
	for _, c := range []struct {
		err          error
		want, notHas string
	}{
		{mysqldrv.ErrInvalidConn, "中途断开", "账号密码"},
		{fmt.Errorf("x: %w", context.DeadlineExceeded), "超时", "账号密码"},
		{errors.New("dial tcp 10.0.0.1:4000: connect: connection refused"), "账号密码", "中途断开"},
		{errors.New("Error 1045 (28000): Access denied for user 'u'"), "账号密码", "超时"},
	} {
		msg := SchemaLoadError(c.err)
		if !strings.Contains(msg, c.want) || strings.Contains(msg, c.notHas) {
			t.Errorf("SchemaLoadError(%v) = %q:应含 %q、不含 %q", c.err, msg, c.want, c.notHas)
		}
		if !strings.Contains(msg, c.err.Error()) {
			t.Errorf("提示里应带原始错误,便于排查:%q", msg)
		}
	}
}
