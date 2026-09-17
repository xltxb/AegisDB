package osc

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// CopyOptions 控制拷贝的节奏。留成结构体是因为阶段五的限流要往这里加旋钮,
// 而那时改的应该是这里,不是每个调用点的参数表。
type CopyOptions struct {
	// ChunkSize 是一块搬多少行。它同时是两件事的旋钮:一次事务写多少(太大占锁久),
	// 以及多久检查一次中止信号(太大就迟迟停不下来)。
	ChunkSize int
}

const defaultChunkSize = 1000

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
func CopyAll(ctx context.Context, db *sql.DB, schema, table, shadow string, opt CopyOptions) (int64, error) {
	chunk := opt.ChunkSize
	if chunk <= 0 {
		chunk = defaultChunkSize
	}

	cols, err := columnNames(ctx, db, schema, table)
	if err != nil {
		return 0, err
	}
	if len(cols) == 0 {
		return 0, fmt.Errorf("表 %s.%s 没有列", schema, table)
	}
	key, err := chunkKey(ctx, db, schema, table)
	if err != nil {
		return 0, err
	}

	colList := quoteCols(cols)
	var total int64
	// 游标是「上一块搬到哪个键值」。从 NULL 起步,用 `> ?` 往前推 —— 这样中断之后
	// 拿着游标续跑就是天然可重入的,不必记住已经搬了多少块。
	var cursor any
	for {
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
			return total, fmt.Errorf("拷贝一块: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return total, fmt.Errorf("读写入行数: %w", err)
		}
		total += n

		// 游标要从**原表**推进,不能看写入行数:INSERT IGNORE 跳过的行同样算走过了,
		// 拿写入数判断会在重放先到过的那一块上原地打转。
		next, more, err := advanceCursor(ctx, db, schema, table, key, cursor, chunk)
		if err != nil {
			return total, err
		}
		if !more {
			return total, nil
		}
		cursor = next
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
