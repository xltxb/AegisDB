package osc

import (
	"reflect"
	"testing"
)

// 一条行事件该变成什么 SQL。这是 ADR 0011 那条不变式的另一半:
//
//	拷贝 INSERT IGNORE —— 永不覆盖
//	重放 REPLACE INTO  —— 永远覆盖
//
// 写反的后果不对称,而且都不会报错:
//   - 重放用了 INSERT IGNORE → 更新永远写不进去,数据停在旧值
//   - DELETE 漏掉           → 已删的行在新表里复活
//
// 所以这张表逐格钉住。期望的 SQL 是手写出来的字面量,不从被测代码推导。

func TestReplay_InsertAndUpdateBothOverwrite(t *testing.T) {
	cols := []string{"id", "memo"}

	for _, tc := range []struct {
		name string
		kind RowEventKind
	}{
		{"INSERT 写进影子表", RowInsert},
		{"UPDATE 覆盖影子表里那一行", RowUpdate},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q, args := replayStatement(RowEvent{
				Kind: tc.kind, Schema: "osc_test", Table: "_t_gho",
				Columns: cols, After: []any{int64(7), "新值"},
			})
			// REPLACE 而不是 INSERT:binlog 给的永远是「这一行现在的样子」,
			// 它可以无条件盖过拷贝写下的旧值。
			want := "REPLACE INTO `osc_test`.`_t_gho` (`id`, `memo`) VALUES (?, ?)"
			if q != want {
				t.Errorf("SQL = %q\n想要   = %q", q, want)
			}
			if !reflect.DeepEqual(args, []any{int64(7), "新值"}) {
				t.Errorf("参数 = %v,想要 [7 新值]", args)
			}
		})
	}
}

func TestReplay_DeleteRemovesByKey(t *testing.T) {
	q, args := replayStatement(RowEvent{
		Kind: RowDelete, Schema: "osc_test", Table: "_t_gho",
		Columns: []string{"id", "memo"}, Before: []any{int64(7), "旧值"},
		KeyColumns: []string{"id"},
	})
	// 按键删,不按整行匹配:整行匹配会在拷贝尚未搬到这一行时删不掉任何东西,
	// 而那一行随后会被拷贝写进来 —— 一条已经删掉的记录就这样复活了。
	want := "DELETE FROM `osc_test`.`_t_gho` WHERE `id` = ?"
	if q != want {
		t.Errorf("SQL = %q\n想要   = %q", q, want)
	}
	if !reflect.DeepEqual(args, []any{int64(7)}) {
		t.Errorf("参数 = %v,想要 [7]", args)
	}
}

// UPDATE 在 binlog 里带着改前与改后两份镜像。重放要用**改后**那份 ——
// 用改前的等于把这次更新原样丢掉,而且悄无声息。
func TestReplay_UpdateUsesTheImageAfterTheChange(t *testing.T) {
	_, args := replayStatement(RowEvent{
		Kind: RowUpdate, Schema: "osc_test", Table: "_t_gho",
		Columns: []string{"id", "memo"},
		Before:  []any{int64(7), "改之前"},
		After:   []any{int64(7), "改之后"},
	})
	if len(args) != 2 || args[1] != "改之后" {
		t.Errorf("参数 = %v —— UPDATE 必须重放改后的镜像", args)
	}
}

// 多列主键照样按键删。
func TestReplay_DeleteHandlesACompositeKey(t *testing.T) {
	q, args := replayStatement(RowEvent{
		Kind: RowDelete, Schema: "osc_test", Table: "_t_gho",
		Columns: []string{"a", "b", "memo"}, Before: []any{int64(1), "x", "旧值"},
		KeyColumns: []string{"a", "b"},
	})
	want := "DELETE FROM `osc_test`.`_t_gho` WHERE `a` = ? AND `b` = ?"
	if q != want {
		t.Errorf("SQL = %q\n想要   = %q", q, want)
	}
	if !reflect.DeepEqual(args, []any{int64(1), "x"}) {
		t.Errorf("参数 = %v,想要 [1 x]", args)
	}
}

// 认不出的事件必须返回空,让调用方跳过 —— 拿一条不认识的事件去猜着发 SQL,
// 比不发更危险。
func TestReplay_UnknownKindEmitsNothing(t *testing.T) {
	q, args := replayStatement(RowEvent{Kind: RowEventKind("rotate"), Schema: "osc_test", Table: "_t_gho"})
	if q != "" || args != nil {
		t.Errorf("不认识的事件该返回空,实际 q=%q args=%v", q, args)
	}
}
