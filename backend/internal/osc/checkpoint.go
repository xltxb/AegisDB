package osc

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"

	"velagateway/internal/apimsg"
)

// Checkpoint 是一次迁移可以续跑的最小状态(.scratch/osc-product 工单 07)。
//
// 影子表的内容 = 拷贝到 Lower 为止的所有块 + binlog 重放到 Applied 为止的所有事件。两者在
// 同一把锁下取(都是已提交的状态),所以续跑时:拷贝从 Lower 之后接着拷(INSERT IGNORE,已被
// 重放写进去的行不受影响),binlog 从 Applied 重新开流 —— Applied 之后有些事件可能已经重放过,
// 但重放是按完整行镜像依次 upsert / 按键删,再放一遍终态不变。
type Checkpoint struct {
	Lower        []KeyPart `json:"lower,omitempty"` // 已拷贝的最大键;空 = 还没拷第一块
	CopyDone     bool      `json:"copyDone"`
	Applied      Position  `json:"applied"`
	RowsCopied   int64     `json:"rowsCopied"`
	Chunks       int64     `json:"chunks"`
	RowsReplayed int64     `json:"rowsReplayed"`
	// GhostShape:影子表的结构摘要(SHOW CREATE TABLE 去掉表名与 AUTO_INCREMENT=)。续跑时影子表
	// 必须还是这个样子 —— 变更是任意 ALTER 之后,"有没有那个索引"已经说明不了什么。
	GhostShape string `json:"ghostShape,omitempty"`
}

// KeyPart 是分块键的一列。分块键只会是整数(有 / 无符号)或按原始字节比较的值(见 scanKey)。
type KeyPart struct {
	I *int64  `json:"i,omitempty"`
	U *uint64 `json:"u,omitempty"`
	B []byte  `json:"b,omitempty"` // JSON 里是 base64
}

func encodeKey(k []any) ([]KeyPart, error) {
	if k == nil {
		return nil, nil
	}
	out := make([]KeyPart, len(k))
	for i, v := range k {
		switch x := v.(type) {
		case int64:
			out[i].I = &x
		case uint64:
			out[i].U = &x
		case []byte:
			out[i].B = append([]byte{}, x...) // 空串也要非 nil,才和"没有值"区分开
		default:
			return nil, apimsg.Errorf("分块键的值类型 %T 无法写进检查点", v)
		}
	}
	return out, nil
}

func decodeKey(ps []KeyPart) ([]any, error) {
	if len(ps) == 0 {
		return nil, nil
	}
	out := make([]any, len(ps))
	for i, p := range ps {
		switch {
		case p.I != nil:
			out[i] = *p.I
		case p.U != nil:
			out[i] = *p.U
		case p.B != nil:
			out[i] = p.B
		default:
			return nil, apimsg.Errorf("检查点里的分块键第 %d 列是空的", i+1)
		}
	}
	return out, nil
}

// Checkpoint 返回此刻可以续跑的状态;影子表建好、binlog 起点定下之前返回 nil。
func (m *Migration) Checkpoint() *Checkpoint {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.ckReady || m.cutDone {
		return nil
	}
	lower, err := encodeKey(m.ckLower)
	if err != nil {
		return nil
	}
	return &Checkpoint{
		Lower: lower, CopyDone: m.ckCopyDone, Applied: m.applied, GhostShape: m.ckShape,
		RowsCopied: m.st.RowsCopied, Chunks: m.st.Chunks, RowsReplayed: m.st.RowsReplayed,
	}
}

// ErrBinlogPurged:检查点的位点所在的 binlog 已被清理,续跑接不上,只能放弃重来。
var ErrBinlogPurged = errors.New("续跑所需的 binlog 已被清理")

// binlogFileExists 看 SHOW BINARY LOGS 里还有没有这个文件。
func binlogFileExists(ctx context.Context, db *sql.DB, file string) (bool, error) {
	rows, err := db.QueryContext(ctx, "SHOW BINARY LOGS")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return false, err
	}
	for rows.Next() {
		vals := make([]sql.RawBytes, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return false, err
		}
		if string(vals[0]) == file {
			return true, nil
		}
	}
	return false, rows.Err()
}

// ghostShape 是影子表结构的摘要:SHOW CREATE TABLE 去掉表名行与 AUTO_INCREMENT= 计数。
func ghostShape(ctx context.Context, db *sql.DB, schema, ghost string) (string, error) {
	var name, ddl string
	if err := db.QueryRowContext(ctx, "SHOW CREATE TABLE "+quoteIdent(schema)+"."+quoteIdent(ghost)).Scan(&name, &ddl); err != nil {
		return "", apimsg.Errorf("读取影子表结构: %w", err)
	}
	if i := strings.IndexByte(ddl, '\n'); i >= 0 {
		ddl = ddl[i+1:]
	}
	return reAutoIncCounter.ReplaceAllString(ddl, ""), nil
}

var reAutoIncCounter = regexp.MustCompile(` AUTO_INCREMENT=\d+`)

// ghostReady 确认续跑要接着用的影子表还在,且已经是"原表 + 这次的变更"。
func (m *Migration) ghostReady(ctx context.Context, ghost, shape string) error {
	var n int
	if err := m.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?",
		m.schema, ghost).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return apimsg.Errorf("影子表 %s 已不在,无法续跑", ghost)
	}
	if shape != "" {
		cur, err := ghostShape(ctx, m.db, m.schema, ghost)
		if err != nil {
			return err
		}
		if cur != shape {
			return apimsg.Errorf("影子表 %s 的结构与中断前不一致,无法续跑", ghost)
		}
		return nil
	}
	if m.change.Action == ActionAlter {
		return nil // 没有结构摘要的老检查点:只能确认它还在
	}
	if err := m.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? AND INDEX_NAME = ?",
		m.schema, ghost, m.change.Index).Scan(&n); err != nil {
		return err
	}
	if has := n > 0; has != (m.change.Action == ActionAddIndex) {
		return apimsg.Errorf("影子表 %s 的结构不是这次变更之后的样子(索引 %s),无法续跑", ghost, m.change.Index)
	}
	return nil
}
