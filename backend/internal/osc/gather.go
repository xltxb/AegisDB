package osc

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
)

// Gather collects everything Preflight needs from a live instance.
//
// 采集与判定分开,是为了让判定留在纯函数里 —— 那 14 条拒绝规则不该需要一个 MySQL
// 才能测。这一层反过来:它除了「把实例说的话如实搬过来」之外不做任何决定,连
// 「这算不算问题」都不判断。
//
// 一个字段读不到时,写入零值而不是让整次采集失败。Preflight 对未知有自己的立场
// (例如磁盘余量拿不到就不拦),把那个决定留给它。
func Gather(ctx context.Context, db *sql.DB, schema, table string) (Facts, error) {
	f := Facts{Engine: "mysql", Schema: schema, Table: table}

	if err := db.QueryRowContext(ctx, "SELECT VERSION()").Scan(&f.Version); err != nil {
		return f, fmt.Errorf("读版本: %w", err)
	}
	f.VersionMajor, f.VersionMinor = parseVersion(f.Version)

	if err := gatherServerVars(ctx, db, &f); err != nil {
		return f, err
	}
	if err := gatherGrants(ctx, db, &f); err != nil {
		return f, err
	}
	if err := gatherTable(ctx, db, schema, table, &f); err != nil {
		return f, err
	}
	return f, nil
}

// parseVersion 从 "8.0.36" / "26.7.0" 这类串里取前两段。
// 取不到就留 0 —— Preflight 会把 0 当成「太旧」而拒绝,这是安全的方向。
func parseVersion(v string) (major, minor int) {
	// 版本串可能带后缀,如 "8.0.36-log"。
	if i := strings.IndexAny(v, "-+ "); i > 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) > 0 {
		major, _ = strconv.Atoi(parts[0])
	}
	if len(parts) > 1 {
		minor, _ = strconv.Atoi(parts[1])
	}
	return major, minor
}

// 三个实例参数一次问完。它们是同一件事的三个面:没有 ROW 格式的完整行镜像,
// 就无法把一条 UPDATE 准确地重放到影子表上。
func gatherServerVars(ctx context.Context, db *sql.DB, f *Facts) error {
	var logBin int
	err := db.QueryRowContext(ctx,
		"SELECT @@log_bin, @@binlog_format, @@binlog_row_image").
		Scan(&logBin, &f.BinlogFormat, &f.BinlogRowImg)
	if err != nil {
		return fmt.Errorf("读 binlog 参数: %w", err)
	}
	f.LogBin = logBin == 1
	return nil
}

// 复制权限从 SHOW GRANTS 里读。information_schema 在不同版本/发行版里对全局权限的
// 表达不一致,而 SHOW GRANTS 是所有版本都认的那一种。
func gatherGrants(ctx context.Context, db *sql.DB, f *Facts) error {
	rows, err := db.QueryContext(ctx, "SHOW GRANTS FOR CURRENT_USER()")
	if err != nil {
		return fmt.Errorf("读权限: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var g string
		if err := rows.Scan(&g); err != nil {
			return fmt.Errorf("读权限行: %w", err)
		}
		up := strings.ToUpper(g)
		// ALL PRIVILEGES ON *.* 同样含这两项。
		all := strings.Contains(up, "ALL PRIVILEGES ON *.*")
		if all || strings.Contains(up, "REPLICATION SLAVE") {
			f.HasReplSlave = true
		}
		if all || strings.Contains(up, "REPLICATION CLIENT") {
			f.HasReplClient = true
		}
	}
	return rows.Err()
}

func gatherTable(ctx context.Context, db *sql.DB, schema, table string, f *Facts) error {
	// 表是否存在,以及它的体量。体量取自 information_schema,是估算值 —— 名字里的
	// Estimated 就是这个意思,不要拿它做精确判断。
	var rowsEst, dataLen, idxLen sql.NullInt64
	err := db.QueryRowContext(ctx, `
		SELECT TABLE_ROWS, DATA_LENGTH, INDEX_LENGTH
		  FROM information_schema.TABLES
		 WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? AND TABLE_TYPE = 'BASE TABLE'`,
		schema, table).Scan(&rowsEst, &dataLen, &idxLen)
	switch {
	case err == sql.ErrNoRows:
		return nil // TableExists 保持 false,后面的表级采集没有意义
	case err != nil:
		return fmt.Errorf("读表信息: %w", err)
	}
	f.TableExists = true
	f.EstimatedRows = rowsEst.Int64
	f.DataBytes = dataLen.Int64 + idxLen.Int64

	if err := gatherKeys(ctx, db, schema, table, f); err != nil {
		return err
	}
	return gatherConstraints(ctx, db, schema, table, f)
}

// 主键,以及能替代它做分块的唯一非空键。
//
// 分块要的是「稳定、唯一、非空」,主键只是最常见的那一种 —— 一张只有唯一非空索引
// 的表照样分得了块,一并拒掉会挡住一批本可以做的表(见 ADR 0011)。
func gatherKeys(ctx context.Context, db *sql.DB, schema, table string, f *Facts) error {
	rows, err := db.QueryContext(ctx, `
		SELECT s.INDEX_NAME, MAX(c.IS_NULLABLE = 'YES') AS any_nullable
		  FROM information_schema.STATISTICS s
		  JOIN information_schema.COLUMNS c
		    ON c.TABLE_SCHEMA = s.TABLE_SCHEMA
		   AND c.TABLE_NAME   = s.TABLE_NAME
		   AND c.COLUMN_NAME  = s.COLUMN_NAME
		 WHERE s.TABLE_SCHEMA = ? AND s.TABLE_NAME = ? AND s.NON_UNIQUE = 0
		 GROUP BY s.INDEX_NAME`, schema, table)
	if err != nil {
		return fmt.Errorf("读索引: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var anyNullable sql.NullInt64
		if err := rows.Scan(&name, &anyNullable); err != nil {
			return fmt.Errorf("读索引行: %w", err)
		}
		if name == "PRIMARY" {
			f.HasPK = true
			continue
		}
		// 只有整把键都非空,才能拿来分块:可空列上的唯一索引允许多行 NULL。
		if anyNullable.Int64 == 0 {
			f.UniqueNotNull = append(f.UniqueNotNull, name)
		}
	}
	return rows.Err()
}

// 外键(两个方向)、触发器、生成列。这三样都会让影子表方案做不干净,见 ADR 0011。
func gatherConstraints(ctx context.Context, db *sql.DB, schema, table string, f *Facts) error {
	err := db.QueryRowContext(ctx, `
		SELECT
		  (SELECT COUNT(*) FROM information_schema.KEY_COLUMN_USAGE
		    WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? AND REFERENCED_TABLE_NAME IS NOT NULL),
		  (SELECT COUNT(*) FROM information_schema.KEY_COLUMN_USAGE
		    WHERE REFERENCED_TABLE_SCHEMA = ? AND REFERENCED_TABLE_NAME = ?),
		  (SELECT COUNT(*) FROM information_schema.TRIGGERS
		    WHERE EVENT_OBJECT_SCHEMA = ? AND EVENT_OBJECT_TABLE = ?),
		  (SELECT COUNT(*) FROM information_schema.COLUMNS
		    WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? AND EXTRA LIKE '%GENERATED%')`,
		schema, table, schema, table, schema, table, schema, table).
		Scan(&f.ForeignKeysOut, &f.ForeignKeysIn, &f.Triggers, &f.GeneratedCols)
	if err != nil {
		return fmt.Errorf("读约束: %w", err)
	}
	return nil
}
