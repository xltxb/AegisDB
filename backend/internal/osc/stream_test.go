package osc

import (
	"context"
	"strings"
	"testing"
	"time"
)

// 订阅层要真 MySQL:binlog 里到底发来什么,只有真服务器说了算。
//
// 这一层只做两件事,因此只测两件事:
//   - 目标表上的每一次写,都变成一条事件交出来(增 / 改 / 删各一次)
//   - **只交目标表的**。别的表的事件混进来,就会被重放写进影子表 —— 那是把无关
//     的数据搬进一张即将顶替生产表的表里。

func TestStream_DeliversEveryChangeOnTheWatchedTable(t *testing.T) {
	db := openMySQL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	watched := makeTable(t, db, "CREATE TABLE `%s` (id BIGINT PRIMARY KEY, memo VARCHAR(64))")
	other := makeTable(t, db, "CREATE TABLE `%s` (id BIGINT PRIMARY KEY, memo VARCHAR(64))")

	// 从"现在"开始订阅,这样只收到下面这几条写入。
	pos, err := CurrentPosition(ctx, db)
	if err != nil {
		t.Fatalf("读当前位点失败: %v", err)
	}

	got := make(chan RowEvent, 32)
	streamErr := make(chan error, 1)
	go func() {
		streamErr <- Stream(ctx, StreamConfig{
			DSN: mysqlDSN(), ServerID: 4411,
			Schema: "osc_test", Table: watched, Position: pos,
		}, func(ev RowEvent) error {
			select {
			case got <- ev:
			default:
			}
			return nil
		})
	}()

	// 目标表:增、改、删各一次。
	mustExec(t, db, "INSERT INTO `"+watched+"` (id, memo) VALUES (1, '甲')")
	mustExec(t, db, "UPDATE `"+watched+"` SET memo = '乙' WHERE id = 1")
	mustExec(t, db, "DELETE FROM `"+watched+"` WHERE id = 1")
	// 无关的表:一条都不该冒出来。
	mustExec(t, db, "INSERT INTO `"+other+"` (id, memo) VALUES (99, '不该出现')")

	var kinds []RowEventKind
	deadline := time.After(20 * time.Second)
	for len(kinds) < 3 {
		select {
		case ev := <-got:
			if ev.Table != watched {
				t.Fatalf("收到了别的表的事件: %s —— 那会被重放写进影子表", ev.Table)
			}
			kinds = append(kinds, ev.Kind)
		case err := <-streamErr:
			t.Fatalf("订阅提前结束: %v", err)
		case <-deadline:
			t.Fatalf("20 秒内只收到 %d 条事件(%v),想要 3 条", len(kinds), kinds)
		}
	}

	want := []RowEventKind{RowInsert, RowUpdate, RowDelete}
	for i := range want {
		if kinds[i] != want[i] {
			t.Errorf("第 %d 条事件是 %s,想要 %s", i+1, kinds[i], want[i])
		}
	}

	// 再等一小会儿,确认无关表的那条确实没来。
	select {
	case ev := <-got:
		t.Errorf("收到了多余的事件: %+v", ev)
	case <-time.After(1500 * time.Millisecond):
	}
}

// 事件里带的镜像要能直接喂给 replayStatement —— 两层接得上,才谈得上重放。
func TestStream_EventsCarryWhatReplayNeeds(t *testing.T) {
	db := openMySQL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	watched := makeTable(t, db, "CREATE TABLE `%s` (id BIGINT PRIMARY KEY, memo VARCHAR(64))")
	pos, err := CurrentPosition(ctx, db)
	if err != nil {
		t.Fatalf("读位点失败: %v", err)
	}

	got := make(chan RowEvent, 8)
	go func() {
		_ = Stream(ctx, StreamConfig{
			DSN: mysqlDSN(), ServerID: 4412,
			Schema: "osc_test", Table: watched, Position: pos,
		}, func(ev RowEvent) error {
			select {
			case got <- ev:
			default:
			}
			return nil
		})
	}()

	mustExec(t, db, "INSERT INTO `"+watched+"` (id, memo) VALUES (42, '值')")

	select {
	case ev := <-got:
		if len(ev.Columns) != 2 || ev.Columns[0] != "id" {
			t.Errorf("列名没带上或顺序不对: %v", ev.Columns)
		}
		if len(ev.After) != 2 {
			t.Fatalf("改后镜像有 %d 列,想要 2", len(ev.After))
		}
		if len(ev.KeyColumns) == 0 {
			t.Error("没带主键列 —— DELETE 的重放需要它来定位行")
		}
		// 接上纯函数层:这条事件该翻成一条 REPLACE。
		q, args := replayStatement(RowEvent{
			Kind: ev.Kind, Schema: "osc_test", Table: "_x_gho",
			Columns: ev.Columns, After: ev.After, Before: ev.Before, KeyColumns: ev.KeyColumns,
		})
		if q == "" {
			t.Error("这条事件翻不出 SQL —— 订阅层与重放层没接上")
		}
		if len(args) != 2 {
			t.Errorf("参数有 %d 个,想要 2", len(args))
		}
	case <-time.After(20 * time.Second):
		t.Fatal("20 秒内没收到事件")
	}
}

// binlog 里的库名/表名大小写随实例的 lower_case_table_names 而变:macOS 默认 2
// (存原样、比较不分大小写)、Windows 1(存小写)、Linux 常见 0(敏感)。
//
// 用大小写敏感的比较去过滤,在前两种实例上会一条事件都收不到 —— 而订阅本身不报错,
// 看起来只是"没有人在写"。这个 bug 在 Linux 上开发时永远不会现形,所以钉一条。
func TestStream_MatchesTheTableRegardlessOfCase(t *testing.T) {
	db := openMySQL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	watched := makeTable(t, db, "CREATE TABLE `%s` (id BIGINT PRIMARY KEY, memo VARCHAR(64))")
	pos, err := CurrentPosition(ctx, db)
	if err != nil {
		t.Fatalf("读位点失败: %v", err)
	}

	got := make(chan RowEvent, 8)
	go func() {
		// 故意用**全大写**的表名去订阅:它指的是同一张表,过滤必须认得出。
		_ = Stream(ctx, StreamConfig{
			DSN: mysqlDSN(), ServerID: 4413,
			Schema: "OSC_TEST", Table: strings.ToUpper(watched), Position: pos,
		}, func(ev RowEvent) error {
			select {
			case got <- ev:
			default:
			}
			return nil
		})
	}()

	mustExec(t, db, "INSERT INTO `"+watched+"` (id, memo) VALUES (1, '甲')")

	select {
	case ev := <-got:
		if ev.Kind != RowInsert {
			t.Errorf("事件种类 = %s,想要 insert", ev.Kind)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("用大写表名订阅时收不到事件 —— 表名比对必须大小写不敏感")
	}
}
