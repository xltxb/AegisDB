// Package schemadiff 比较同一个库在两个环境里的表结构(见 .scratch/schema-diff/PRD.md)。
//
// 这里只有纯计算:读取在 gateway.RealSchemaSnapshot,它把各引擎的系统目录翻成这里的 Snapshot。
// 两边的原文都保留在差异里,报告显示原文;比较用的是归一化之后的值(见 normalize.go)——
// 否则 int(11) 对 int、'0' 对 0 这类写法差异会把报告淹没。
package schemadiff

import "time"

// Snapshot 是一个库在某一刻的结构。Database 对 MySQL 是库名,对 Oracle 是 owner,对 PG 是库名。
type Snapshot struct {
	Engine   string    `json:"engine"`
	Database string    `json:"database"`
	ReadAt   time.Time `json:"readAt"`
	Tables   []Table   `json:"tables"`
}

// Table 是一张表或视图。PG 非 public schema 的表名带 schema 前缀。
type Table struct {
	Name       string   `json:"name"`
	Kind       string   `json:"kind"` // table | view
	Comment    string   `json:"comment"`
	Columns    []Column `json:"columns"`
	PrimaryKey []string `json:"primaryKey"`
	Indexes    []Index  `json:"indexes"`
}

// Column 是一列。Default 是目录里的原文;HasDefault 区分"没有默认值"与"默认值是空串"。
type Column struct {
	Name       string `json:"name"`
	DataType   string `json:"dataType"`
	Nullable   bool   `json:"nullable"`
	HasDefault bool   `json:"hasDefault"`
	Default    string `json:"default"`
	Comment    string `json:"comment"`
	Ordinal    int    `json:"ordinal"`
}

// Index 是一个索引或唯一约束。SystemNamed:名字是库自动生成的(Oracle 的 SYS_C…),两边
// 不可能相同,只能按列组成匹配。
type Index struct {
	Name        string   `json:"name"`
	Columns     []string `json:"columns"`
	Unique      bool     `json:"unique"`
	SystemNamed bool     `json:"systemNamed,omitempty"`
	// Primary:这是支撑主键的那个索引(MySQL 的 PRIMARY、PG 的 indisprimary、Oracle 主键约束用的索引)。
	// 主键单独比,这个索引不在索引差异里重复报;与主键同列、但不是它的唯一索引照常比。
	Primary bool `json:"primary,omitempty"`
}

// Options 是可勾选的忽略项(PRD 3.4)。默认都不忽略。
type Options struct {
	IgnoreColumnOrder bool `json:"ignoreColumnOrder"`
	IgnoreComments    bool `json:"ignoreComments"`
	IgnoreIndexNames  bool `json:"ignoreIndexNames"`
}

// 差异的状态。
const (
	OnlyLeft  = "onlyLeft"
	OnlyRight = "onlyRight"
	Changed   = "changed"
)

// Result 是一次对比的结果。Same / OnlyLeft / OnlyRight 是表名;Changed 是结构不同的表。
type Result struct {
	Summary   Summary     `json:"summary"`
	Same      []string    `json:"same"`
	OnlyLeft  []string    `json:"onlyLeft"`
	OnlyRight []string    `json:"onlyRight"`
	Changed   []TableDiff `json:"changed"`
}

type Summary struct {
	Left      int `json:"left"`
	Right     int `json:"right"`
	Same      int `json:"same"`
	OnlyLeft  int `json:"onlyLeft"`
	OnlyRight int `json:"onlyRight"`
	Changed   int `json:"changed"`
}

// Pair 是一处属性两边的原文。
type Pair struct {
	Left  string `json:"left"`
	Right string `json:"right"`
}

// TableDiff 是一张两边都有、但结构不同的表。只列出有差异的部分。
type TableDiff struct {
	Table      string       `json:"table"`
	Kind       *Pair        `json:"kind,omitempty"`
	Comment    *Pair        `json:"comment,omitempty"`
	PrimaryKey *Pair        `json:"primaryKey,omitempty"`
	Columns    []ColumnDiff `json:"columns,omitempty"`
	Indexes    []IndexDiff  `json:"indexes,omitempty"`
	// 两边的完整定义,页面据此做列 / 主键 / 索引的左右对照。只有结构不同的表带。
	LeftDef  *Table `json:"leftDef,omitempty"`
	RightDef *Table `json:"rightDef,omitempty"`
}

// ColumnDiff 是一列的差异:只在一边(Status = onlyLeft / onlyRight,Left / Right 是那一边的
// 列定义摘要),或两边都有但属性不同(Status = changed,逐项在 Changes 里)。
type ColumnDiff struct {
	Column  string   `json:"column"`
	Status  string   `json:"status"`
	Left    string   `json:"left,omitempty"`
	Right   string   `json:"right,omitempty"`
	Changes []Change `json:"changes,omitempty"`
}

// Change 是一项属性的差异。Attr:type | nullable | default | comment | position。
type Change struct {
	Attr  string `json:"attr"`
	Left  string `json:"left"`
	Right string `json:"right"`
}

// IndexDiff 是一个索引的差异。Left / Right 是两边的定义摘要("UNIQUE (a, b)"),只在一边时另一边为空。
type IndexDiff struct {
	Index  string `json:"index"`
	Status string `json:"status"`
	Left   string `json:"left,omitempty"`
	Right  string `json:"right,omitempty"`
}
