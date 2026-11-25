package osc

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"velagateway/internal/apimsg"
)

// Gather collects the Facts for schema.table from a live MySQL. 采集与判断分开:
// 判断(Preflight)是纯函数、可以穷举地测;采集只负责如实回答"是什么",拿不到的
// 就留零值并在字段注释里写清零值的含义,不在这里替判断做决定。
func Gather(ctx context.Context, db queryer, schema, table string) (Facts, error) {
	f := Facts{Engine: "mysql", Schema: schema, Table: table}

	var version string
	if err := db.QueryRowContext(ctx, "SELECT VERSION()").Scan(&version); err != nil {
		return f, apimsg.Errorf("读取版本: %w", err)
	}
	f.Version = version
	// MariaDB 走的是 mysql 驱动,但 binlog 与 cut-over 的语义都不同 —— 按另一个引擎对待,
	// 让前置检查在第一条就停下。
	if strings.Contains(strings.ToLower(version), "mariadb") {
		f.Engine = "mariadb"
		return f, nil
	}
	// TiDB 自称 8.0.11-TiDB-vX:不是 MySQL,别按"版本太旧"去说它(引擎标签写错了才会走到这里)
	if strings.Contains(strings.ToLower(version), "tidb") {
		f.Engine = "tidb"
		return f, nil
	}
	f.VersionMajor, f.VersionMinor, f.VersionPatch = parseVersion(version)

	var logBin int
	var format, rowImg string
	if err := db.QueryRowContext(ctx, "SELECT @@log_bin, @@binlog_format, @@binlog_row_image").
		Scan(&logBin, &format, &rowImg); err != nil {
		return f, apimsg.Errorf("读取 binlog 参数: %w", err)
	}
	f.LogBin, f.BinlogFormat, f.BinlogRowImg = logBin == 1, strings.ToUpper(format), strings.ToUpper(rowImg)

	if err := gatherGrants(ctx, db, &f); err != nil {
		return f, err
	}

	m, exists, err := loadMeta(ctx, db, schema, table)
	if err != nil {
		return f, err
	}
	f.TableExists = exists
	if !exists {
		return f, nil
	}
	f.TableEngine, f.EstimatedRows, f.DataBytes = m.Engine, m.EstRows, m.DataBytes
	f.HasPK, f.ChunkKey = m.hasPK(), m.ChunkKey
	for _, ix := range m.uniqueNotNull() {
		f.UniqueNotNull = append(f.UniqueNotNull, ix.Name)
	}
	for _, ix := range m.Indexes {
		f.Indexes = append(f.Indexes, ix.Name)
	}
	for _, c := range m.Columns {
		f.Columns = append(f.Columns, c.Name)
		if c.Generated {
			f.GeneratedCols++
		}
		if !replayTypeOK(c.DataType) {
			f.UnsupportedCols = append(f.UnsupportedCols, fmt.Sprintf("%s (%s)", c.Name, c.DataType))
		}
	}

	counts := []struct {
		dst *int
		q   string
	}{
		{&f.ForeignKeysOut, `SELECT COUNT(DISTINCT CONSTRAINT_NAME) FROM information_schema.KEY_COLUMN_USAGE
			WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? AND REFERENCED_TABLE_NAME IS NOT NULL`},
		// 自引用外键两个方向都算一次,本来就该被拒,重复计数无妨。
		{&f.ForeignKeysIn, `SELECT COUNT(DISTINCT CONSTRAINT_SCHEMA, CONSTRAINT_NAME) FROM information_schema.KEY_COLUMN_USAGE
			WHERE REFERENCED_TABLE_SCHEMA = ? AND REFERENCED_TABLE_NAME = ?`},
		{&f.Triggers, `SELECT COUNT(*) FROM information_schema.TRIGGERS
			WHERE EVENT_OBJECT_SCHEMA = ? AND EVENT_OBJECT_TABLE = ?`},
	}
	for _, c := range counts {
		if err := db.QueryRowContext(ctx, c.q, schema, table).Scan(c.dst); err != nil {
			return f, apimsg.Errorf("读取表的约束信息: %w", err)
		}
	}

	for _, lo := range []struct {
		dst  *bool
		name string
	}{{&f.LeftoverGhost, ghostName(table)}, {&f.LeftoverDel, delName(table)}} {
		var n int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.TABLES
			WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?`, schema, lo.name).Scan(&n); err != nil {
			return f, apimsg.Errorf("检查残留表: %w", err)
		}
		*lo.dst = n > 0
	}

	// 需要 PROCESS 权限。拿不到只是少一条提示,不影响能不能做,所以吞掉错误。
	var longest sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT MAX(TIMESTAMPDIFF(SECOND, trx_started, NOW()))
		FROM information_schema.INNODB_TRX`).Scan(&longest); err == nil {
		f.LongestTrxSeconds = int(longest.Int64)
	}

	// FreeDiskBytes 留 0:SQL 层没有可靠的办法拿到数据目录所在盘的余量,0 在 Preflight
	// 里的含义就是"不知道",不拦。
	return f, nil
}

// gatherGrants reads the replication privileges from SHOW GRANTS. 通过角色授予的
// 权限在这里看不到 —— 那种情况会被判成"缺权限"而拒绝,方向是安全的。
func gatherGrants(ctx context.Context, db queryer, f *Facts) error {
	rows, err := db.QueryContext(ctx, "SHOW GRANTS")
	if err != nil {
		return apimsg.Errorf("读取账号权限: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var g string
		if err := rows.Scan(&g); err != nil {
			return err
		}
		g = strings.ToUpper(g)
		if !strings.Contains(g, " ON *.* ") {
			continue // 复制权限只能是全局的
		}
		all := strings.Contains(g, "ALL PRIVILEGES")
		f.HasReplSlave = f.HasReplSlave || all || strings.Contains(g, "REPLICATION SLAVE")
		f.HasReplClient = f.HasReplClient || all || strings.Contains(g, "REPLICATION CLIENT")
	}
	return rows.Err()
}

// parseVersion reads "8.0.36-log" / "8.4.0" as (8, 0, 36).
//
// Aurora MySQL 有时报成 "8.0.mysql_aurora.3.05.2":第三段不是补丁号。Aurora 3 全系基于 8.0.23 及以上,
// 按 8.0.23 算 —— 够过在线变更的版本下限;判断加 / 删列是不是 INSTANT 时偏保守(多走在线变更,不会少)。
func parseVersion(v string) (major, minor, patch int) {
	if i := strings.Index(strings.ToLower(v), "mysql_aurora"); i >= 0 {
		major, minor, _ = parseVersion(strings.TrimRight(v[:i], "."))
		if major == 8 && minor == 0 {
			return 8, 0, 23
		}
		return major, minor, 0
	}
	if i := strings.IndexAny(v, "-+ "); i >= 0 {
		v = v[:i]
	}
	parts := strings.SplitN(v, ".", 3)
	nums := []*int{&major, &minor, &patch}
	for i, p := range parts {
		n, _ := strconv.Atoi(p)
		*nums[i] = n
	}
	return
}
