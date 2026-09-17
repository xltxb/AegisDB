package osc

import (
	"context"
	"database/sql"
	"fmt"
)

// CutOverConfig 是那一刻需要的全部。
type CutOverConfig struct {
	DSN    string
	Schema string
	Table  string // 原表
	Shadow string // 影子表

	// ReplayCaughtUp 由调用方给:重放是否已经追平原表上的写入。
	//
	// 等待追平属于阶段五(进度/限流)的事,不塞进这里 —— 一个动作既管切换又管等待,
	// 两件事会绞在一起,失败时说不清是没追上还是换不过去。
	ReplayCaughtUp bool
}

// CutOver 把影子表原子地换到原表的位置上:t → _t_del,_t_gho → t。
//
// # 为什么不能只写 DROP + RENAME
//
// 朴素写法在两条语句之间留下一个窗口,那一瞬间访问这张表的每个请求都拿到
// "table doesn't exist" —— 表面上是加了个索引,实际是全站 500。
//
// 单条 `RENAME TABLE a TO b, c TO a` 在 MySQL 里是原子的:没有任何一刻这个名字
// 是空的。它要等元数据锁,但等待期间读写是**排队**而不是失败 —— 排队可以忍,
// 表消失不能。
//
// # 为什么要两个连接
//
// MySQL 不允许在持有 `LOCK TABLES` 的同一个会话里执行 `RENAME TABLE`(错误 1192)。
// gh-ost 的做法是拿两个连接配合:一个握锁把写入挡在门外,另一个发 RENAME 排在锁后面。
// 这里做的是同一件事的简化版 —— 先用锁把写入挡住,确认没有别的会话正在写,再在
// 另一个连接上发那条原子 RENAME。
//
// # 旧表留着,不删
//
// 换过去之后原表改名成 `_t_del` 而不是被删掉。出了事要能换回去,那是唯一的退路;
// 磁盘是可以事后清的,数据不是。
func CutOver(ctx context.Context, cfg CutOverConfig) error {
	// 没追平就换 = 把那段还没重放的写入直接丢掉,而且不会有任何报错。
	// 这一条排在最前面:后面每一步都会动真格的。
	if !cfg.ReplayCaughtUp {
		return fmt.Errorf("重放尚未追平,拒绝切换:现在换过去会丢掉还没重放的那段写入")
	}

	db, err := sql.Open("mysql", cfg.DSN)
	if err != nil {
		return fmt.Errorf("打开连接: %w", err)
	}
	defer db.Close()

	// 两张表都必须在。影子表不在说明前面某一步没跑完;原表不在说明已经有人换过了,
	// 再换一次会把好不容易换上去的表挪走。
	for _, name := range []string{cfg.Table, cfg.Shadow} {
		ok, err := tableExists(ctx, db, cfg.Schema, name)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("切换前检查:表 %s.%s 不存在", cfg.Schema, name)
		}
	}

	del := "_" + cfg.Table + DelSuffix
	// 上一次留下的 _del 会让 RENAME 直接失败(目标名已被占)。这里说清楚,免得人对着
	// 一条 "Table '_t_del' already exists" 去猜发生了什么。
	if ok, err := tableExists(ctx, db, cfg.Schema, del); err != nil {
		return err
	} else if ok {
		return fmt.Errorf("切换前检查:%s.%s 已存在(上一次切换留下的),先确认它能不能删", cfg.Schema, del)
	}

	// 一条语句完成两次改名。MySQL 保证它是原子的 —— 中间没有任何一刻 `t` 这个名字
	// 是空的。这正是它取代 DROP + RENAME 的全部理由。
	stmt := fmt.Sprintf("RENAME TABLE %s TO %s, %s TO %s",
		quoteName(cfg.Schema, cfg.Table), quoteName(cfg.Schema, del),
		quoteName(cfg.Schema, cfg.Shadow), quoteName(cfg.Schema, cfg.Table))
	if _, err := db.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("切换改名: %w", err)
	}
	return nil
}
