package storemigrate

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// MovedMarkerTable 是搬迁成功后留在**源 MySQL** 里的标记表。
//
// 网关切到 PG 靠的是 vela.env 里的 VELA_DB_DRIVER=postgres。哪天这一行丢了(换机器、手改、
// 被模板覆盖),网关会悄悄连回 MySQL —— 那是一份停在搬迁时刻的旧数据,而它会照常接受写入,
// 两边从此分叉。所以搬完在 MySQL 里留个记号,网关在 MySQL 上启动时见到它就拒绝(见
// bootstrap.RefuseIfStoreMoved)。要真的回退到 MySQL,删掉这张表即可,这是一个有意的动作。
const MovedMarkerTable = "vela_store_moved"

func markerExists(ctx context.Context, src *sql.DB) (bool, error) {
	var n int
	err := src.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.tables
		WHERE table_schema = DATABASE() AND table_name = ?`, MovedMarkerTable).Scan(&n)
	return n > 0, err
}

func writeMarker(ctx context.Context, src *sql.DB, pgDSN string) error {
	target := "postgres"
	if c, err := pgx.ParseConfig(pgDSN); err == nil {
		target = fmt.Sprintf("%s:%d/%s", c.Host, c.Port, c.Database) // 不带口令
	}
	if _, err := src.ExecContext(ctx, "CREATE TABLE "+MovedMarkerTable+
		" (moved_at DATETIME(3) NOT NULL, target VARCHAR(255) NOT NULL)"); err != nil {
		return fmt.Errorf("在源库留搬迁标记: %w", err)
	}
	_, err := src.ExecContext(ctx, "INSERT INTO "+MovedMarkerTable+" (moved_at, target) VALUES (?, ?)", time.Now(), target)
	return err
}
