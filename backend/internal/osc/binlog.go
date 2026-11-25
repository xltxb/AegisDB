package osc

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"velagateway/internal/apimsg"
)

// Position is a point in the binlog. 只比 File 的数字后缀与 Pos,不看 GTID:
// 同一台实例上的 file/pos 是全序的,而迁移全程只连这一台。
type Position struct {
	File string
	Pos  uint64
}

// Compare returns -1, 0, 1. Binlog 文件名是 <basename>.<递增序号>,序号位数会变长
// (.999999 之后是 .1000000),所以按数字比而不是按字符串比。
func (p Position) Compare(o Position) int {
	if p.File != o.File {
		if seq(p.File) < seq(o.File) {
			return -1
		}
		return 1
	}
	switch {
	case p.Pos < o.Pos:
		return -1
	case p.Pos > o.Pos:
		return 1
	}
	return 0
}

func seq(file string) uint64 {
	n, _ := strconv.ParseUint(file[strings.LastIndexByte(file, '.')+1:], 10, 64)
	return n
}

func (p Position) String() string { return fmt.Sprintf("%s:%d", p.File, p.Pos) }

// RowKind is what happened to a row.
type RowKind int

const (
	RowInsert RowKind = iota
	RowUpdate
	RowDelete
)

// RowChange is one row from a binlog rows event: full images, one value per
// column in table order — 生成列也占一个位置,重放时按下标跳过。值的 Go 类型沿用
// binlog 解码库的约定(整数有符号、DECIMAL/时间是文本、字符串是列字符集的原始字节),
// 由 normalize 统一收口。
type RowChange struct {
	Kind   RowKind
	Before []any // Update / Delete
	After  []any // Insert / Update
}

// Event is one thing read from the binlog. 不涉及本表的事件也要交出来(Rows 为空),
// 因为 Pos 在前进 —— cut-over 要知道"读到哪了",而不只是"本表改到哪了"。
type Event struct {
	Pos    Position // 这个事件之后的位置
	Schema string
	Table  string
	Rows   []RowChange
	// DDL is set when a statement changes Schema.Table's structure. 重放按下标对列,
	// 表结构一变就会错位 —— 迁移唯一正确的反应是中止。
	DDL bool
}

// Binlog is where a migration reads the table's concurrent writes from.
type Binlog interface {
	// Head is the position up to which the server has committed right now.
	Head(ctx context.Context) (Position, error)
	// Stream starts reading from `from`.
	Stream(ctx context.Context, from Position) (Stream, error)
}

// Stream yields binlog events in order.
type Stream interface {
	// Next returns the next event, or ctx.Err() if none arrives before ctx is
	// done. 超时之后流仍然可用 —— 重放循环靠短超时让出锁给 cut-over。
	Next(ctx context.Context) (Event, error)
	Close() error
}

// binlogHead reads the server's current binlog position. 8.4 删掉了 SHOW MASTER
// STATUS,先试新名字再退回旧名字。
func binlogHead(ctx context.Context, q queryer) (Position, error) {
	var lastErr error
	for _, stmt := range []string{"SHOW BINARY LOG STATUS", "SHOW MASTER STATUS"} {
		rows, err := q.QueryContext(ctx, stmt)
		if err != nil {
			lastErr = err
			continue
		}
		p, err := scanHead(rows)
		if err != nil {
			return Position{}, err
		}
		return p, nil
	}
	return Position{}, apimsg.Errorf("读取 binlog 位点: %w", lastErr)
}

func scanHead(rows *sql.Rows) (Position, error) {
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return Position{}, err
	}
	if !rows.Next() {
		return Position{}, apimsg.Errorf("读取 binlog 位点:实例未开启 binlog")
	}
	vals := make([]sql.RawBytes, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return Position{}, err
	}
	pos, err := strconv.ParseUint(string(vals[1]), 10, 64)
	if err != nil {
		return Position{}, apimsg.Errorf("binlog 位点不是数字: %q", vals[1])
	}
	return Position{File: string(vals[0]), Pos: pos}, rows.Err()
}
