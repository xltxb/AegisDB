package osc

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"

	gosqlmysql "github.com/go-sql-driver/mysql"

	"velagateway/internal/apimsg"
)

// ProbeAlter 在一张空的探测表(_vela_ocp_<随机>,原表的结构克隆)上真跑一遍这条 ALTER,再按变更后的真实
// 结构做列映射检查(工单 08)。生成工单之前就能告诉人:语句本身有错、删了定位行的键、新加的
// 非空列没写默认值……而不是等审批通过、开跑之后才失败。
//
// 探测表是空的,建、改、删都是毫秒级;它会进 binlog、复制到从库,但不碰任何数据。
// 只对 ActionAlter 有意义;老的加 / 删索引变更返回 nil。
func ProbeAlter(ctx context.Context, db *sql.DB, schema, table string, c Change) ([]Blocker, error) {
	if c.Action != ActionAlter {
		return nil, nil
	}
	q := func(t string) string { return quoteIdent(schema) + "." + quoteIdent(t) }
	// 每次一个随机名,建之前不 DROP IF EXISTS(2026-10 安全审计 L-OSC3):从前是固定的 _<表>_ocp,
	// 先删后建 —— 库里恰好有一张同名的业务表就被删了;两个人同时检查同一张表,也会删掉对方
	// 正在用的探测表。现在只删自己建成的那一张。
	probe, err := probeName()
	if err != nil {
		return nil, err
	}
	if _, err := db.ExecContext(ctx, fmt.Sprintf("CREATE TABLE %s LIKE %s", q(probe), q(table))); err != nil {
		return nil, apimsg.Errorf("创建探测表: %w", err)
	}
	defer func() {
		cctx, cancel := context.WithTimeout(context.Background(), 30e9)
		defer cancel()
		_, _ = db.ExecContext(cctx, "DROP TABLE IF EXISTS "+q(probe))
	}()
	if _, err := db.ExecContext(ctx, "ALTER TABLE "+q(probe)+" "+c.Spec); err != nil {
		msg := err.Error()
		var me *gosqlmysql.MySQLError
		if errors.As(err, &me) {
			msg = me.Message
		}
		return []Blocker{{Code: "alter_failed", Reason: "这条变更在结构相同的空表上执行失败:" + msg}}, nil
	}
	src, ok, err := loadMeta(ctx, db, schema, table)
	if err != nil || !ok {
		return nil, apimsg.Errorf("读取原表结构: %w", err)
	}
	dst, ok, err := loadMeta(ctx, db, schema, probe)
	if err != nil || !ok {
		return nil, apimsg.Errorf("读取探测表结构: %w", err)
	}
	_, bs := newColMap(src, dst, c.Renames)
	return bs, nil
}

// probeName 生成一个不会撞上别人表名的探测表名(与原表名无关,也就不受表名长度限制)。
func probeName() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return probePrefix + hex.EncodeToString(b), nil
}

const probePrefix = "_vela_ocp_"
