package osc

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// CopyOptions 控制拷贝的节奏 —— ADR 0011 开篇说这套东西买的第一件事就是
// 「可限流、可暂停、可中止」,这些字段就是那些旋钮。
type CopyOptions struct {
	// ChunkSize 是一块搬多少行。它同时是两件事的旋钮:一次事务写多少(太大占锁久),
	// 以及多久检查一次中止信号(太大就迟迟停不下来)。
	ChunkSize int

	// Resume 从上一次停下的游标接着跑。中止之后能续跑是中止本身可用的前提 ——
	// 停一次就前功尽弃的话,那个中止键没人敢按。
	Resume any

	// OnProgress 每搬完一块调一次。
	OnProgress func(Progress)

	// ---- 限流 ----
	// ReplicaLag 报告从库此刻落后多少。为空 = 不限流。
	//
	// 做成可注入的函数而不是在这里读 SHOW REPLICA STATUS:读延迟与「该不该等」是
	// 两件事,分开之后限流逻辑不必搭一套主从复制就能测。
	ReplicaLag func(context.Context) (time.Duration, error)
	// MaxLag 是能容忍的延迟上限。0 = 不限流。
	MaxLag time.Duration
	// ThrottleInterval 是被限住时每轮等多久,默认 1 秒。
	ThrottleInterval time.Duration
}

// Progress 是一次拷贝走到哪了。
type Progress struct {
	Copied int64 // 到此刻为止写进影子表的行数
	Cursor any   // 这一块的末尾键值 —— 也是续跑要用的那个
}

// CopyResult 是一次拷贝的结果。
//
// Interrupted 让调用方分得清「跑完了」和「被停了」—— 两者都没有报错的话,一次被
// 中断的迁移会被当成完成的,而它的影子表里少着一截数据。
type CopyResult struct {
	Copied      int64
	Cursor      any
	Interrupted bool
}

const (
	defaultChunkSize        = 1000
	defaultThrottleInterval = time.Second
)

// shouldPause 判断此刻该不该让拷贝等一等。
//
// 阈值为 0 表示没开限流 —— 把它当成「一有延迟就停」会让拷贝永远动不了。
// 正好等于阈值不算超,否则一个恰好卡在线上的稳定延迟会让它一直等下去。
func shouldPause(lag, max time.Duration) bool {
	if max <= 0 {
		return false
	}
	return lag > max
}

// CopyAll 把原表的存量行分块搬进影子表,返回实际写入的行数。
//
// **这里用 INSERT IGNORE,不是 INSERT,也不是 REPLACE。** 这不是容错写法,是 ADR 0011
// 那条不变式的一半:
//
//	拷贝 INSERT IGNORE —— 永不覆盖
//	重放 REPLACE INTO  —— 永远覆盖
//
// 拷贝与 binlog 重放是同时在跑的,它们会交错。一行若已被重放写成新值,拷贝这边读到
// 的是原表里那份可能已经过时的值 —— 盖回去就等于把一次已经发生的更新悄悄回滚掉,
// 而且不会有任何报错。让步的必须是拷贝:重放拿到的永远是"这一行现在的样子"。
//
// 分块按主键范围走而不是 LIMIT OFFSET:OFFSET 每一块都要从头扫一遍,在大表上是
// O(n²);更要紧的是它对并发写入不稳定 —— 中间插进来一行,后面每一块的边界都会错位。
func CopyAll(ctx context.Context, db *sql.DB, schema, table, shadow string, opt CopyOptions) (CopyResult, error) {
	chunk := opt.ChunkSize
	if chunk <= 0 {
		chunk = defaultChunkSize
	}

	cols, err := columnNames(ctx, db, schema, table)
	if err != nil {
		return CopyResult{}, err
	}
	if len(cols) == 0 {
		return CopyResult{}, fmt.Errorf("表 %s.%s 没有列", schema, table)
	}
	key, err := chunkKey(ctx, db, schema, table)
	if err != nil {
		return CopyResult{}, err
	}

	colList := quoteCols(cols)
	var total int64
	// 游标是「上一块搬到哪个键值」。从 NULL 起步(或从 Resume 给的那个接着),用 `> ?`
	// 往前推 —— 这样中断之后拿着游标续跑就是天然可重入的,不必记住已经搬了多少块。
	cursor := opt.Resume
	for {
		// 中止检查放在每块之前:块越大停得越迟,这也是 ChunkSize 的另一重含义。
		if err := ctx.Err(); err != nil {
			return CopyResult{Copied: total, Cursor: cursor, Interrupted: true},
				fmt.Errorf("拷贝被中止(已拷 %d 行,可从游标续跑): %w", total, err)
		}
		// 限流:从库落后太多时先等一等。等待本身也要能被中止,否则一个被限住的
		// 迁移会对中止信号无动于衷。
		if err := waitForReplica(ctx, opt); err != nil {
			return CopyResult{Copied: total, Cursor: cursor, Interrupted: true}, err
		}
		q := fmt.Sprintf(
			"INSERT IGNORE INTO %s (%s) SELECT %s FROM %s WHERE %s ORDER BY %s LIMIT ?",
			quoteName(schema, shadow), colList, colList, quoteName(schema, table),
			cursorPredicate(key, cursor), quoteIdent(key))

		var res sql.Result
		if cursor == nil {
			res, err = db.ExecContext(ctx, q, chunk)
		} else {
			res, err = db.ExecContext(ctx, q, cursor, chunk)
		}
		if err != nil {
			return CopyResult{Copied: total, Cursor: cursor}, fmt.Errorf("拷贝一块: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return CopyResult{Copied: total, Cursor: cursor}, fmt.Errorf("读写入行数: %w", err)
		}
		total += n

		// 游标要从**原表**推进,不能看写入行数:INSERT IGNORE 跳过的行同样算走过了,
		// 拿写入数判断会在重放先到过的那一块上原地打转。
		next, more, err := advanceCursor(ctx, db, schema, table, key, cursor, chunk)
		if err != nil {
			return CopyResult{Copied: total, Cursor: cursor}, err
		}
		if next != nil {
			cursor = next
		}
		if opt.OnProgress != nil {
			opt.OnProgress(Progress{Copied: total, Cursor: cursor})
		}
		if !more {
			return CopyResult{Copied: total, Cursor: cursor}, nil
		}
	}
}

// waitForReplica 在从库落后超过阈值时把拷贝按住,直到追回来或调用方叫停。
//
// 等待要能被中止 —— 否则一个被限住的迁移对中止信号无动于衷,而"能中止"正是这套
// 东西相对原生 DDL 的卖点之一。
func waitForReplica(ctx context.Context, opt CopyOptions) error {
	if opt.ReplicaLag == nil || opt.MaxLag <= 0 {
		return nil
	}
	interval := opt.ThrottleInterval
	if interval <= 0 {
		interval = defaultThrottleInterval
	}
	for {
		lag, err := opt.ReplicaLag(ctx)
		if err != nil {
			// 读不到延迟不等于从库健康,但也不该让迁移就此卡死。放行并把判断留给
			// 调用方的监控 —— 与 Preflight 对「拿不到磁盘余量」的立场一致。
			return nil
		}
		if !shouldPause(lag, opt.MaxLag) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("限流等待被中止: %w", ctx.Err())
		case <-time.After(interval):
		}
	}
}

// cursorPredicate 第一块无条件,之后按键值往前推。
func cursorPredicate(key string, cursor any) string {
	if cursor == nil {
		return "1 = 1"
	}
	return quoteIdent(key) + " > ?"
}

// advanceCursor 报告这一块的末尾键值,以及后面还有没有行。
// 它查的是原表,与 INSERT IGNORE 实际写进去多少无关。
func advanceCursor(ctx context.Context, db *sql.DB, schema, table, key string, cursor any, chunk int) (any, bool, error) {
	q := fmt.Sprintf(
		"SELECT %s FROM %s WHERE %s ORDER BY %s LIMIT ?",
		quoteIdent(key), quoteName(schema, table), cursorPredicate(key, cursor), quoteIdent(key))

	var rows *sql.Rows
	var err error
	if cursor == nil {
		rows, err = db.QueryContext(ctx, q, chunk)
	} else {
		rows, err = db.QueryContext(ctx, q, cursor, chunk)
	}
	if err != nil {
		return nil, false, fmt.Errorf("推进游标: %w", err)
	}
	defer rows.Close()

	var last any
	var seen int
	for rows.Next() {
		var v any
		if err := rows.Scan(&v); err != nil {
			return nil, false, fmt.Errorf("读游标值: %w", err)
		}
		last, seen = v, seen+1
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	// 这一块没取满,说明表已经走到头了。
	return last, seen == chunk, nil
}

func columnNames(ctx context.Context, db *sql.DB, schema, table string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT COLUMN_NAME FROM information_schema.COLUMNS
		 WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? ORDER BY ORDINAL_POSITION`, schema, table)
	if err != nil {
		return nil, fmt.Errorf("读列名: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, fmt.Errorf("读列名行: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// chunkKey 挑一个用来切块的键:优先主键,否则第一个唯一非空索引。
//
// 这与 Preflight 放行的条件是同一条 —— 那里说「有主键或唯一非空索引就可以做」,
// 这里就必须真的能从这两者里挑出一个。两处若不一致,会出现「前置检查放行了、
// 拷贝却找不到键」这种自相矛盾的失败。
//
// 只支持单列键:多列键的游标是一个元组,`> ?` 要改写成字典序比较。加索引这个场景
// 里单列主键覆盖了绝大多数表,先把能做对的做对。
func chunkKey(ctx context.Context, db *sql.DB, schema, table string) (string, error) {
	var name string
	err := db.QueryRowContext(ctx, `
		SELECT s.COLUMN_NAME
		  FROM information_schema.STATISTICS s
		  JOIN information_schema.COLUMNS c
		    ON c.TABLE_SCHEMA = s.TABLE_SCHEMA AND c.TABLE_NAME = s.TABLE_NAME
		   AND c.COLUMN_NAME = s.COLUMN_NAME
		 WHERE s.TABLE_SCHEMA = ? AND s.TABLE_NAME = ? AND s.NON_UNIQUE = 0
		   AND c.IS_NULLABLE = 'NO'
		   AND s.INDEX_NAME IN (
		       SELECT INDEX_NAME FROM information_schema.STATISTICS
		        WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? AND NON_UNIQUE = 0
		        GROUP BY INDEX_NAME HAVING COUNT(*) = 1)
		 ORDER BY (s.INDEX_NAME = 'PRIMARY') DESC, s.INDEX_NAME
		 LIMIT 1`, schema, table, schema, table).Scan(&name)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("表 %s.%s 没有可用来分块的单列唯一非空键", schema, table)
	}
	if err != nil {
		return "", fmt.Errorf("挑分块键: %w", err)
	}
	return name, nil
}

func quoteIdent(s string) string { return "`" + strings.ReplaceAll(s, "`", "``") + "`" }

func quoteCols(cols []string) string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = quoteIdent(c)
	}
	return strings.Join(out, ", ")
}
