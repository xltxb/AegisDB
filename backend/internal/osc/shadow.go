package osc

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// ShadowSuffix / DelSuffix 沿用 gh-ost 的命名。不是为了像它,是因为残留检查
// (Facts.LeftoverGhost / LeftoverDel)认的就是这两个后缀 —— 改名字会让"上次没清
// 干净"这条检查失明。
const (
	ShadowSuffix = "_gho"
	DelSuffix    = "_del"
)

// ShadowName 是 t 的影子表名。
func ShadowName(table string) string { return "_" + table + ShadowSuffix }

// CreateShadow 建出影子表并把这次的 ALTER 应用在它身上,返回影子表名。
//
// 用 `CREATE TABLE ... LIKE` 而不是自己拼列定义:LIKE 会把列、类型、字符集、默认值、
// 索引、AUTO_INCREMENT 属性一并带过来,而手拼这些等于把 MySQL 的建表语义重实现一遍,
// 每漏一样都是影子表与原表的一处静默差异 —— 那正是拷贝会把数据写进错列的来源。
//
// **只碰影子表,绝不碰原表。** 这一步是准备,cut-over 才是那个会动原表的动作。
func CreateShadow(ctx context.Context, db *sql.DB, schema, table, alter string) (string, error) {
	if strings.TrimSpace(alter) == "" {
		return "", fmt.Errorf("没有给出要应用的 ALTER 子句")
	}
	shadow := ShadowName(table)

	// 残留的影子表必须先被发现、由人决定怎么处理,不能在这里悄悄覆盖 —— 它可能是上
	// 一次迁移跑到一半留下的,里面有别人还没看过的数据。Preflight 的 leftover 检查
	// 负责拦住这种情况,这里只做最后一道断言。
	exists, err := tableExists(ctx, db, schema, shadow)
	if err != nil {
		return "", err
	}
	if exists {
		return "", fmt.Errorf("影子表 %s.%s 已存在:上一次迁移没有清理干净,先确认它能不能删", schema, shadow)
	}

	if _, err := db.ExecContext(ctx, fmt.Sprintf("CREATE TABLE %s LIKE %s",
		quoteName(schema, shadow), quoteName(schema, table))); err != nil {
		return "", fmt.Errorf("建影子表: %w", err)
	}

	// ALTER 应用在影子表上,原表始终不动。
	if _, err := db.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s %s",
		quoteName(schema, shadow), alter)); err != nil {
		// 应用失败就把影子表收走,否则下一次会撞上自己留下的残留。
		_, _ = db.ExecContext(context.Background(), "DROP TABLE IF EXISTS "+quoteName(schema, shadow))
		return "", fmt.Errorf("在影子表上应用 ALTER: %w", err)
	}
	return shadow, nil
}

func tableExists(ctx context.Context, db *sql.DB, schema, table string) (bool, error) {
	var n int
	err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM information_schema.TABLES
		 WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?`, schema, table).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("查表是否存在: %w", err)
	}
	return n > 0, nil
}

// quoteName 给库名与表名加反引号。名字里的反引号按 MySQL 的规矩翻倍转义 —— 这些名字
// 来自 information_schema 与调用方,不是用户直接输入的 SQL,但拼进语句的东西一律转义,
// 不靠"这里应该不会有奇怪字符"过日子。
func quoteName(schema, table string) string {
	return "`" + strings.ReplaceAll(schema, "`", "``") + "`.`" + strings.ReplaceAll(table, "`", "``") + "`"
}
