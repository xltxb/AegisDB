package osc

import (
	"context"
	"testing"
	"time"
)

// 心跳是延迟的量尺:主库上有一行时间戳在不停往前走,从库上读到的那一行落后多少,
// 就是从库落后多少。
//
// 为什么不用 Seconds_Behind_Source:它在从库空闲时报 0、在一个大事务重放到一半时
// 也报 0(那个事务还没开始应用),而恰恰是大事务期间最需要限流。心跳量的是
// **数据什么时候到的从库**,与从库忙不忙无关。

func TestHeartbeat_KeepsOneRowMovingForward(t *testing.T) {
	db := openMySQL(t)
	table := makeTable(t, db, "CREATE TABLE `%s` (id BIGINT PRIMARY KEY)")

	hb, err := startHeartbeat(context.Background(), db, heartbeatConfig{
		Schema: "osc_test", Table: table, Interval: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("起心跳失败: %v", err)
	}
	defer hb.Stop()

	first, err := readHeartbeat(context.Background(), db, "osc_test", hb.Table())
	if err != nil {
		t.Fatalf("读心跳失败: %v", err)
	}
	time.Sleep(120 * time.Millisecond)
	second, err := readHeartbeat(context.Background(), db, "osc_test", hb.Table())
	if err != nil {
		t.Fatalf("再读心跳失败: %v", err)
	}

	if !second.After(first) {
		t.Errorf("心跳没有往前走:先读到 %v,后读到 %v —— 停住的心跳会让延迟越算越大,"+
			"把拷贝永远按在限流里", first, second)
	}
	// 一行,不是一张越写越长的表。跑几小时的迁移要是每 500ms 追加一行,
	// 结束时这张表比被迁移的表还热闹。
	var n int
	if err := db.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM `osc_test`.`"+hb.Table()+"`").Scan(&n); err != nil {
		t.Fatalf("数心跳表行数失败: %v", err)
	}
	if n != 1 {
		t.Errorf("心跳表里有 %d 行,期望恒为 1", n)
	}
}

func TestHeartbeat_TimestampIsWrittenByTheGatewayNotTheServer(t *testing.T) {
	// 心跳值必须是**网关进程的时钟**写下的,不是 MySQL 的 NOW()。
	//
	// 用 NOW() 的话,比较的是"MySQL 的时钟"与"网关的时钟"两个钟 —— 两台机器差几秒
	// 是常事,而那几秒会被整个算成从库延迟:要么凭空限流,要么把真延迟抵消掉。
	db := openMySQL(t)
	table := makeTable(t, db, "CREATE TABLE `%s` (id BIGINT PRIMARY KEY)")

	fake := time.Date(2001, 2, 3, 4, 5, 6, 0, time.Local)
	hb, err := startHeartbeat(context.Background(), db, heartbeatConfig{
		Schema: "osc_test", Table: table, Interval: 20 * time.Millisecond,
		Now: func() time.Time { return fake },
	})
	if err != nil {
		t.Fatalf("起心跳失败: %v", err)
	}
	defer hb.Stop()

	got, err := readHeartbeat(context.Background(), db, "osc_test", hb.Table())
	if err != nil {
		t.Fatalf("读心跳失败: %v", err)
	}
	if !got.Equal(fake) {
		t.Errorf("心跳里的时间是 %v,期望网关给的 %v —— 值来自服务器的话,"+
			"算出来的延迟里掺着两台机器的时钟差", got, fake)
	}
}

func TestHeartbeat_StopDropsTheTable(t *testing.T) {
	// 心跳表是迁移期间的临时物件。留在库里没有任何用处,而 Preflight 的残留检查
	// 只认 _gho / _del —— 它不会替这张表说话,所以它必须自己走干净。
	db := openMySQL(t)
	table := makeTable(t, db, "CREATE TABLE `%s` (id BIGINT PRIMARY KEY)")

	hb, err := startHeartbeat(context.Background(), db, heartbeatConfig{
		Schema: "osc_test", Table: table, Interval: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("起心跳失败: %v", err)
	}
	name := hb.Table()
	hb.Stop()

	exists, err := tableExists(context.Background(), db, "osc_test", name)
	if err != nil {
		t.Fatalf("查表是否存在失败: %v", err)
	}
	if exists {
		t.Errorf("心跳表 %s 在停止之后还留在库里", name)
	}
}

func TestHeartbeat_RefusesToReuseALeftoverTable(t *testing.T) {
	// 同一张表上不可能同时有两次迁移在跑。心跳表已存在意味着要么上一次没走干净、
	// 要么另一个进程正在推进它 —— 前者要人看一眼,后者更不能碰:两个心跳互相覆盖,
	// 两边算出的延迟都是错的。
	db := openMySQL(t)
	table := makeTable(t, db, "CREATE TABLE `%s` (id BIGINT PRIMARY KEY)")

	hb, err := startHeartbeat(context.Background(), db, heartbeatConfig{
		Schema: "osc_test", Table: table, Interval: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("起第一个心跳失败: %v", err)
	}
	defer hb.Stop()

	if _, err := startHeartbeat(context.Background(), db, heartbeatConfig{
		Schema: "osc_test", Table: table, Interval: 20 * time.Millisecond,
	}); err == nil {
		t.Error("第二个心跳起成功了,它该被残留的那张表挡下来")
	}
}
