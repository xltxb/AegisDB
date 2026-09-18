package osc

import (
	"fmt"
	"strings"
)

// RowEventKind 是一条行事件的种类。
type RowEventKind string

const (
	RowInsert RowEventKind = "insert"
	RowUpdate RowEventKind = "update"
	RowDelete RowEventKind = "delete"
)

// RowEvent 是本包自己的行事件表示,**刻意不直接用 binlog 库的类型**。
//
// 多一层转换,换来的是:这张不变式表(下面的 replayStatement)完全不依赖那个库。
// 将来换库、或者它改了 API,要改的是订阅那一处,不是整套重放语义 —— 而重放语义
// 正是这个特性里最不该被一次依赖升级碰到的东西。
type RowEvent struct {
	Kind   RowEventKind
	Schema string // 影子表所在的库
	Table  string // 影子表名 —— 重放的目的地始终是影子表,不是原表

	Columns []string // 列名,顺序与 Before/After 一致

	// Before 是改前镜像(UPDATE / DELETE 有),After 是改后镜像(INSERT / UPDATE 有)。
	// binlog_row_image 必须是 FULL,否则这两份镜像都只带被改的列 —— 而 REPLACE INTO
	// 会把没带的列写成默认值,悄悄毁掉数据。Preflight 拦的就是这一条。
	Before []any
	After  []any

	// KeyColumns 是用来定位行的键(主键或唯一非空键)。DELETE 靠它。
	KeyColumns []string
}

// replayStatement 把一条行事件翻成一条 SQL。
//
// 这是 ADR 0011 那条不变式的另一半:
//
//	拷贝 INSERT IGNORE —— 永不覆盖
//	重放 REPLACE INTO  —— 永远覆盖
//
// binlog 给的永远是「这一行现在的样子」,所以它可以无条件盖过拷贝写下的旧值。
// 反过来写(重放用 INSERT IGNORE)的后果是更新永远写不进去,数据停在旧值,而且
// 不会有任何报错。
//
// 认不出的事件返回空,让调用方跳过 —— 猜着发 SQL 比不发更危险。
func replayStatement(ev RowEvent) (string, []any) {
	switch ev.Kind {
	case RowInsert, RowUpdate:
		// UPDATE 用**改后**的镜像。用改前的等于把这次更新原样丢掉。
		row := ev.After
		if len(row) == 0 || len(ev.Columns) == 0 {
			return "", nil
		}
		return fmt.Sprintf("REPLACE INTO %s (%s) VALUES (%s)",
			quoteName(ev.Schema, ev.Table), quoteCols(ev.Columns), placeholders(len(row))), row

	case RowDelete:
		// 按键删,不按整行匹配。整行匹配会在拷贝尚未搬到这一行时删不掉任何东西,
		// 而那一行随后会被拷贝写进来 —— 一条已经删掉的记录就这样复活了。
		if len(ev.KeyColumns) == 0 || len(ev.Before) == 0 {
			return "", nil
		}
		where := make([]string, 0, len(ev.KeyColumns))
		args := make([]any, 0, len(ev.KeyColumns))
		for _, k := range ev.KeyColumns {
			i := indexOf(ev.Columns, k)
			if i < 0 || i >= len(ev.Before) {
				return "", nil // 键不在镜像里,宁可不发
			}
			where = append(where, quoteIdent(k)+" = ?")
			args = append(args, ev.Before[i])
		}
		return fmt.Sprintf("DELETE FROM %s WHERE %s",
			quoteName(ev.Schema, ev.Table), strings.Join(where, " AND ")), args
	}
	return "", nil
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

func indexOf(ss []string, s string) int {
	for i, v := range ss {
		if v == s {
			return i
		}
	}
	return -1
}
