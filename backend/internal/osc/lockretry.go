package osc

import (
	"context"
	"errors"
	"time"

	gosqlmysql "github.com/go-sql-driver/mysql"
)

// 拷贝块与重放事务都写影子表,彼此会撞锁:InnoDB 检出死锁时挑一方回滚(1213),
// 或者等锁超时(1205)。两者都是"这一次没做成、原样再做一次就行",不是迁移失败的理由。
const (
	lockRetries = 5
	lockBackoff = 20 * time.Millisecond
)

func isLockConflict(err error) bool {
	var me *gosqlmysql.MySQLError
	return errors.As(err, &me) && (me.Number == 1213 || me.Number == 1205)
}

// retryLockConflict 执行 f,遇到死锁 / 锁等待超时就退避后重来,最多 lockRetries 次。
// f 必须能整体重做:单条语句,或者一个失败时已整体回滚的事务。
func retryLockConflict(ctx context.Context, f func() error) error {
	var err error
	for i := 0; i < lockRetries; i++ {
		if err = f(); err == nil || !isLockConflict(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(lockBackoff * time.Duration(i+1)):
		}
	}
	return err
}
