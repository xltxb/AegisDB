package service

// 发布流水线与 OSC 的接缝。
//
// 单独一个文件,因为 pipeline.go 已经很大,而这里的每一段都只为一件事服务:
// 让执行阶段能把一条语句交给一个要跑几小时的外部任务,然后在它结束时接着往下走。

import (
	"context"
	"fmt"

	"velagateway/internal/model"
	"velagateway/internal/osc"
	"velagateway/internal/oscroute"
)

// AttachOSC 把在线变更接进来。不接的话自动路由整个不存在,执行阶段照旧直发。
//
// enabled 是**和控制台那道闸同一个闭包** —— 急停开关(ADR 0011)要挡住的是"发起",
// 而流水线是发起的另一条路。各写一份判断的话,两条路迟早会分叉,而分叉的那一刻
// 没有人会发现:界面上写着"已挡住",任务却还在一个个地起来。
func (s *Services) AttachOSC(r *osc.Runner, connect osc.ConnectFunc, enabled func() bool) {
	s.osc = r
	s.oscEnabled = enabled
	s.tableRowsFn = func(conn *model.Connection, schema, table string) int64 {
		return gatherRows(connect, conn.ID, schema, table)
	}
}

// oscPolicy 读平台对自动路由的立场。
//
// 默认开着是安全的:OSC 的总开关默认关着,所以在没打开 OSC 的部署上它只会走到
// "直发并说明"那条路 —— 不改变任何现有行为,只多一行日志。
func (s *Services) oscPolicy() oscroute.Policy {
	return oscroute.Policy{
		AutoRoute: s.Repo.SettingBool("osc.autoRoute.enabled", true),
		MinRows:   int64(s.Repo.SettingInt("osc.autoRoute.minRows", 2_000_000)),
	}
}

// tableRows 是这张表的**估算**行数。
//
// 走 information_schema(osc.Gather 读的就是它),不做 COUNT(*):八百万行上要跑
// 几十秒,而它换来的精度在这里没有价值 —— 边界上误判的后果是"走了/没走 OSC",
// 两边都不危险。
//
// 拿不到就返回 0,也就是"当它是小表" —— 与 Preflight 对"拿不到磁盘余量不拦"
// 相反的方向,但同一个立场:未知的时候选那个不会把事情搞砸的答案。这里直发是
// 安全的那一边。
func gatherRows(connect osc.ConnectFunc, connID int64, schema, table string) int64 {
	db, _, err := connect(connID)
	if err != nil {
		return 0
	}
	facts, err := osc.Gather(context.Background(), db, schema, table)
	if err != nil {
		return 0
	}
	return facts.EstimatedRows
}

// routeStatement 决定一条语句的去向,并在决定走 OSC 时把任务发起出来。
//
// 返回的 jobID 为 0 表示"这条语句直发" —— **包括决定走 OSC 但发起失败的情况**。
// 那是决定 2:该走却走不了时直发,并把原因说出来。原生加索引本来就是在线的,
// "没走 OSC"是失去了限流/从库友好/MDL 可重试这三件事,不是干了一件危险的事;
// 让一个本来能跑的发布单卡死,理由却是"我们本想用个更温和的办法",不成立。
func (s *Services) routeStatement(rel *model.Release, conn *model.Connection, sql string) (jobID int64, note string) {
	if s.osc == nil {
		// 只有索引 DDL 才值得解释"为什么没走 OSC"。一条 UPDATE 旁边写"本部署没有
		// 接入 OSC"是噪音,而阶段日志有 20000 字的上限 —— 一次跨几小时、分几段
		// 恢复的执行,这点额度要留给真正说明了什么的行。
		if _, isIndexDDL := oscroute.ParseIndexDDL(sql); !isIndexDDL {
			return 0, ""
		}
		return 0, "直发:本部署没有接入 OSC"
	}
	if s.oscEnabled == nil || !s.oscEnabled() {
		// 急停开关(配置里的 osc.enabled 前提 + tbl_setting 里的 osc.enabled 急停)
		// 关着,和控制台那道闸走的是同一个闭包 —— 见 AttachOSC。同上,只有索引
		// DDL 才值得解释,普通 DML 不写这一行。
		if _, isIndexDDL := oscroute.ParseIndexDDL(sql); !isIndexDDL {
			return 0, ""
		}
		return 0, "直发:在线变更已被关闭(配置或急停开关)"
	}
	// 行数是懒查的:Decide 只在认出这是索引 DDL、且没被覆盖或策略拦下时才回调它。
	d := oscroute.Decide(sql, conn.Engine, s.oscPolicy(), oscroute.Override(rel.OSCMode),
		func(table string) int64 {
			if s.tableRowsFn == nil {
				return 0
			}
			return s.tableRowsFn(conn, rel.Database, table)
		})
	if !d.UseOSC {
		// 同上:只有索引 DDL 才值得解释"为什么没走 OSC"。一条 UPDATE 旁边写
		// "不是索引变更"是噪音——它本来就不该走 OSC,这不是新闻。
		if _, isIndexDDL := oscroute.ParseIndexDDL(sql); !isIndexDDL {
			return 0, ""
		}
		return 0, d.Reason
	}
	job, err := s.osc.Start(context.Background(), osc.StartRequest{
		ConnectionID: conn.ID, Schema: rel.Database, Table: d.Table,
		Alter: d.Alter, CreatedBy: rel.Creator,
	})
	if err != nil {
		return 0, fmt.Sprintf("直发:本该走 OSC(%s),但发起失败 —— %v", d.Reason, err)
	}
	return job.ID, fmt.Sprintf("%s · 任务 #%d", d.Reason, job.ID)
}
