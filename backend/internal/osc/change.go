package osc

import (
	"fmt"
	"strings"
)

// IndexColumn is one column of an index being added. Length > 0 makes it a
// prefix index (only meaningful for string/blob columns — MySQL rejects it
// elsewhere, and we let it: the shadow table is empty, so the error is cheap).
type IndexColumn struct {
	Name   string `json:"name"`
	Length int    `json:"length,omitempty"`
}

// Change is the schema change a migration applies.
//
// ActionAlter(工单 08):Spec 是要在影子表上执行的 ALTER 子句原文,Renames 是其中 CHANGE /
// RENAME COLUMN 的列改名(旧名 → 新名),拷贝与重放据此对列。变更后的样子不靠解析 SQL 去
// 推,而是读影子表的真实结构(见 newColMap)—— MySQL 自己执行出来的结果才是准的。
//
// ActionAddIndex / ActionDropIndex 是工单 08 之前的写法,老任务照旧可读、可续跑。
type Change struct {
	Action  string            `json:"action"` // ActionAlter | ActionAddIndex | ActionDropIndex
	Spec    string            `json:"spec,omitempty"`
	Renames map[string]string `json:"renames,omitempty"`
	Index   string            `json:"index,omitempty"`
	Columns []IndexColumn     `json:"columns,omitempty"` // add 才需要
	Unique  bool              `json:"unique,omitempty"`  // 老写法:永远被拒,见 checkChange
}

// ghostName / delName follow gh-ost's naming so an operator who has seen gh-ost
// recognises the leftovers on sight.
func ghostName(table string) string { return "_" + table + "_gho" }
func delName(table string) string   { return "_" + table + "_del" }

// MySQL 表名上限 64。影子表名比原表名长 5,原表名超过 59 时影子表根本建不出来。
const maxTableNameForShadow = 64 - 5

func quoteIdent(s string) string { return "`" + strings.ReplaceAll(s, "`", "``") + "`" }

// alterShadowSQL is the ALTER applied to the (still empty) shadow table.
func (c Change) alterShadowSQL(ghost string) string {
	if c.Action == ActionAlter {
		return "ALTER TABLE " + quoteIdent(ghost) + " " + c.Spec
	}
	if c.Action == ActionDropIndex {
		return fmt.Sprintf("ALTER TABLE %s DROP INDEX %s", quoteIdent(ghost), quoteIdent(c.Index))
	}
	cols := make([]string, len(c.Columns))
	for i, col := range c.Columns {
		cols[i] = quoteIdent(col.Name)
		if col.Length > 0 {
			cols[i] += fmt.Sprintf("(%d)", col.Length)
		}
	}
	return fmt.Sprintf("ALTER TABLE %s ADD INDEX %s (%s)", quoteIdent(ghost), quoteIdent(c.Index), strings.Join(cols, ", "))
}

// checkChange holds the blockers that depend on what is being changed, not on
// the instance or the table in general.
func checkChange(f Facts, c Change, deny func(code, format string, a ...any)) {
	if c.Action == ActionAlter {
		// 子句本身对不对、改完之后还能不能拷贝 / 重放,要在探测表上真跑一遍才知道(ProbeAlter)
		if strings.TrimSpace(c.Spec) == "" {
			deny("unsupported_action", "没有要执行的变更。")
		}
		return
	}
	if c.Action != ActionAddIndex && c.Action != ActionDropIndex {
		deny("unsupported_action", "在线变更目前只支持加索引 / 删索引,其它变更请走原生 DDL。")
		return
	}
	if strings.TrimSpace(c.Index) == "" {
		deny("no_index_name", "没有给出索引名。")
		return
	}
	has := func(list []string, name string) bool {
		for _, s := range list {
			if strings.EqualFold(s, name) {
				return true
			}
		}
		return false
	}

	if c.Action == ActionDropIndex {
		switch {
		case strings.EqualFold(c.Index, "PRIMARY"):
			deny("drop_primary", "不能在线删除主键:分块拷贝与重放都靠它定位行。")
		case !has(f.Indexes, c.Index):
			deny("no_such_index", "表上没有名为 %s 的索引。", c.Index)
		case strings.EqualFold(c.Index, f.ChunkKey):
			deny("drop_chunk_key", "索引 %s 是这张表唯一可用来分块的键,删掉它拷贝与重放就无法定位行。", c.Index)
		}
		return
	}

	// 加唯一索引看上去只是多一个 UNIQUE,实际会悄悄删数据:拷贝用 INSERT IGNORE,遇到
	// 重复值直接丢掉那一行;重放用 REPLACE INTO,遇到冲突会把另一行删掉。两条路都不报错。
	// gh-ost 要求人显式确认,我们直接不做 —— 这正是"做不干净"的那一类。
	if c.Unique {
		deny("unique_index", "不支持在线加唯一索引:拷贝与重放遇到重复值会静默丢行而不报错。请先确认数据无重复后走原生 DDL。")
	}
	if has(f.Indexes, c.Index) {
		deny("index_exists", "表上已有名为 %s 的索引。", c.Index)
	}
	if len(c.Columns) == 0 {
		deny("no_columns", "新索引没有指定列。")
	}
	for _, col := range c.Columns {
		if !has(f.Columns, col.Name) {
			deny("no_such_column", "表上没有列 %s。", col.Name)
		}
	}
}
