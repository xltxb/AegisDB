package osc

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"velagateway/internal/apimsg"
)

// copier moves the table's existing rows into the shadow table, one key range
// at a time.
//
// 这里的写法是另一半不变式:**拷贝永不覆盖**。INSERT IGNORE —— 这一行若已经被 binlog 重放
// 写成了新值,拷贝不能把它盖回旧值,撞了它的定位键就跳过。
//
// 但 IGNORE 跳过的不只"已经在了"的行:撞了变更新增的唯一键的行、放不进新类型被截断的值、
// NOT NULL 列遇到 NULL 被填成 0,也都只变成一条警告(工单 08)—— 改列类型、加唯一约束时就是
// 静默丢数据 / 改数据。所以每块拷完立刻读 SHOW WARNINGS:只有"撞了自己的定位键"是预期的,
// 其余任何警告都让迁移失败。不改用"NOT EXISTS 影子表"的写法:那要在影子表上加共享锁,与重放
// 的插入互相死锁得厉害(实测)。
//
// FOR SHARE 同样不可省:没有它,拷贝可能先读到
// 一行、随后这一行被删并完成重放、拷贝再把读到的旧值写进影子表,留下一行原表里已经
// 不存在的幽灵数据。加了共享锁,删除要么先提交(拷贝读不到),要么等拷贝提交之后
// (删除随后被重放)。
type copier struct {
	m     *tableMeta // 原表(分块键已由 colMap 定好)
	cm    *colMap
	ghost string
	chunk int

	keyCols  string // `a`, `b`
	keyTuple string // (?, ?) 的类型化版本
	boundSQL string // 下一块的上界
	copySQL  [2]string

	max   []any // 迁移开始时的最大键;之后插入的行由重放负责
	lower []any // 上一块的上界(不含);nil = 从 min 开始(含)
	done  bool
}

func newCopier(cm *colMap, ghost string, chunk int) *copier {
	m := cm.src
	c := &copier{m: m, cm: cm, ghost: ghost, chunk: chunk}
	names := make([]string, len(m.keyIdx))
	exprs := make([]string, len(m.keyIdx))
	for i, j := range m.keyIdx {
		names[i] = quoteIdent(m.Columns[j].Name)
		exprs[i] = keyExpr(m.Columns[j])
	}
	c.keyCols = strings.Join(names, ", ")
	c.keyTuple = "(" + strings.Join(exprs, ", ") + ")"
	src := quoteIdent(m.Table)
	dcols := make([]string, len(cm.dstIdx))
	scols := make([]string, len(cm.srcIdx))
	for i := range cm.dstIdx {
		dcols[i] = quoteIdent(cm.dst.Columns[cm.dstIdx[i]].Name)
		scols[i] = quoteIdent(m.Columns[cm.srcIdx[i]].Name)
	}
	// 两种下界:第一块从最小键开始(含),之后每块从上一块的上界开始(不含)。
	for i, op := range []string{">=", ">"} {
		c.copySQL[i] = fmt.Sprintf("INSERT IGNORE INTO %s (%s) SELECT %s FROM %s FORCE INDEX (%s) WHERE (%s) %s %s AND (%s) <= %s FOR SHARE",
			quoteIdent(ghost), strings.Join(dcols, ", "), strings.Join(scols, ", "), src, quoteIdent(m.ChunkKey),
			c.keyCols, op, c.keyTuple, c.keyCols, c.keyTuple)
	}
	c.boundSQL = fmt.Sprintf("SELECT %s FROM %s FORCE INDEX (%s) WHERE (%s) %%s %s AND (%s) <= %s ORDER BY %s LIMIT 1 OFFSET %d",
		c.keyCols, src, quoteIdent(m.ChunkKey), c.keyCols, c.keyTuple, c.keyCols, c.keyTuple, c.keyCols, chunk-1)
	return c
}

// start fixes the copy range: [min, max] as of now. 必须在 binlog 订阅起点确定之后
// 调用 —— 这之后插入的行一定出现在 binlog 里,由重放负责。
func (c *copier) start(ctx context.Context, q queryer) error {
	src := quoteIdent(c.m.Table)
	desc := strings.ReplaceAll(c.keyCols, ",", " DESC,") + " DESC"
	max, err := c.scanKey(ctx, q, fmt.Sprintf("SELECT %s FROM %s ORDER BY %s LIMIT 1", c.keyCols, src, desc))
	if err != nil {
		return err
	}
	if max == nil {
		c.done = true // 空表
	}
	c.max = max
	return nil
}

// next copies one chunk and reports how many rows it inserted.
func (c *copier) next(ctx context.Context, conn *sql.Conn) (int64, error) {
	if c.done {
		return 0, nil
	}
	op, sqlText := ">", c.copySQL[1]
	lowerArgs := c.lower
	if lowerArgs == nil {
		// 第一块从最小键开始(含)。不同类型没有统一的"负无穷",所以查一次真实的最小键。
		op, sqlText = ">=", c.copySQL[0]
		min, err := c.scanKey(ctx, conn, fmt.Sprintf("SELECT %s FROM %s ORDER BY %s LIMIT 1", c.keyCols, quoteIdent(c.m.Table), c.keyCols))
		if err != nil {
			return 0, err
		}
		if min == nil { // 拷贝开始前整张表都被删空了
			c.done = true
			return 0, nil
		}
		lowerArgs = min
	}

	upper, err := c.scanKey(ctx, conn, fmt.Sprintf(c.boundSQL, op), append(append([]any{}, lowerArgs...), c.max...)...)
	if err != nil {
		return 0, err
	}
	last := upper == nil
	if last {
		upper = c.max
	}
	// 拷贝块与重放在影子表上可能互相死锁(InnoDB 挑一方回滚):同一块重来即可,
	// INSERT IGNORE 让已经被重放写进去的行不受影响。
	var res sql.Result
	err = retryLockConflict(ctx, func() error {
		var e error
		res, e = conn.ExecContext(ctx, sqlText, append(append([]any{}, lowerArgs...), upper...)...)
		if e != nil {
			return e
		}
		return c.checkWarnings(ctx, conn)
	})
	if err != nil {
		return 0, apimsg.Errorf("拷贝数据块: %w", err)
	}
	c.lower, c.done = upper, last // 成功之后才推进:失败重来时还是这一块
	n, _ := res.RowsAffected()
	return n, nil
}

// scanKey reads one key tuple (nil if no row) as driver arguments. 会话是
// SET NAMES binary,读出来的都是原始字节;整数要转成真正的整数再传回去 —— 以字符串
// 和 BIGINT 比较会被 MySQL 转成 double,超过 2^53 的键就比不准了。
func (c *copier) scanKey(ctx context.Context, q queryer, query string, args ...any) ([]any, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, apimsg.Errorf("读取分块边界: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	raw := make([]sql.RawBytes, len(c.m.keyIdx))
	ptrs := make([]any, len(raw))
	for i := range raw {
		ptrs[i] = &raw[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return nil, err
	}
	out := make([]any, len(raw))
	for i, j := range c.m.keyIdx {
		col := c.m.Columns[j]
		s := string(raw[i])
		switch {
		case isInteger(col.DataType) && col.Unsigned:
			out[i], err = strconv.ParseUint(s, 10, 64)
		case isInteger(col.DataType):
			out[i], err = strconv.ParseInt(s, 10, 64)
		default:
			out[i] = append([]byte(nil), raw[i]...)
		}
		if err != nil {
			return nil, apimsg.Errorf("分块键 %s 的值 %q 无法解析: %w", col.Name, s, err)
		}
	}
	return out, rows.Err()
}

// checkWarnings 读刚才那条 INSERT IGNORE 的警告:撞了自己的定位键是预期的(那一行已被重放写过),
// 其余一律算失败 —— 那是被 IGNORE 吞掉的丢行或改值。Note 级别(如 CHAR 尾部空格被去掉)不算。
func (c *copier) checkWarnings(ctx context.Context, conn *sql.Conn) error {
	rows, err := conn.QueryContext(ctx, "SHOW WARNINGS")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var level, msg string
		var code int
		if err := rows.Scan(&level, &code, &msg); err != nil {
			return err
		}
		if strings.EqualFold(level, "Note") {
			continue
		}
		if code == 1062 {
			if idx := dupKeyIndex(msg); c.cm.isOwn(idx) {
				continue
			} else {
				return &ErrUniqueConflict{Index: idx, Detail: msg}
			}
		}
		return apimsg.Errorf("拷贝时数据库给出警告(%d %s):值放不进变更后的表结构,迁移中止,原表未动", code, msg)
	}
	return rows.Err()
}
