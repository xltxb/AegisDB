package service

import (
	"strconv"
	"strings"
	"time"

	"velagateway/internal/gateway"
	"velagateway/internal/model"
)

// 执行窗口(「班车」)的判定。
//
// 窗口只做一件事:在指定时间、对指定的库,把本来需要**审批**的中/高风险语句直接放行。
// 它改的是"要不要人来批",不是"有没有权限" —— deny 仍然 deny。见 relaxByWindow。
//
// 这是全系统唯一一处**主动放宽**闸门的地方,所以每一个判断都往严的方向倒:
//   · 读不到窗口表 → 当作没有窗口(照常审批),而不是当作开着
//   · 时区解析不出来 → 该窗口不生效,而不是退回服务器时区
//   · 库名对不上(空、大小写不同)→ 不生效
//   · 窗口放行的每一条命令都在审计里指回是哪个窗口放的

// windowRelaxTimeout 之外没有别的超时:判定发生在每条命令的执行路径上,查询必须廉价。
// 表按 (connection_id, db_name) 建了索引,命中的行数是个位数。

// activeWindow 返回此刻覆盖 (conn, 目标库) 的窗口,没有则返回 nil。
//
// 目标库取的是"这条命令真正会落到哪个库":别的引擎是 conn.Database(已由
// applyTargetDatabase 换成用户选的那个),Oracle 是 TargetSchema —— 它的 Database
// 装的是服务名,不是库。两者用的是同一个口径,审计里记的也是同一个值。
func (s *Services) activeWindow(conn *model.Connection, now time.Time) *model.ExecWindow {
	if conn == nil {
		return nil
	}
	db := effectiveDatabase(conn)
	if db == "" {
		return nil // 说不清是哪个库,就不放行
	}
	wins, err := s.Repo.ExecWindowsFor(conn.ID, db)
	if err != nil {
		// 读不到就当没有窗口。少放行一次是一次多余的审批,多放行一次是一条没人看过
		// 的高风险语句落在生产上 —— 两个方向不对等。
		return nil
	}
	for i := range wins {
		if windowCovers(&wins[i], now) {
			return &wins[i]
		}
	}
	return nil
}

// effectiveDatabase 是这条命令真正作用的库名。
func effectiveDatabase(conn *model.Connection) string {
	if conn == nil {
		return ""
	}
	if gateway.IsOracleEngine(conn.Engine) {
		return strings.TrimSpace(conn.TargetSchema)
	}
	return strings.TrimSpace(conn.Database)
}

// relaxByWindow 在窗口生效时把 approve 降为 allow,并把是哪个窗口放的写进裁决,
// 让审计与终端提示都能说清楚。deny 原样返回。
//
// 返回的第二个值是生效的窗口(没有则为 nil),调用方用它写审计。
func (s *Services) relaxByWindow(conn *model.Connection, stmts []string, v gateway.Verdict, now time.Time) (gateway.Verdict, *model.ExecWindow) {
	if v.Action != gateway.ActionApprove {
		return v, nil // allow 无需放宽;deny 是"没权限",不在窗口的职责范围内
	}
	w := s.windowFor(conn, stmts, now)
	if w == nil {
		return v, nil
	}
	v.Action = gateway.ActionAllow
	// 风险等级**不降**:它描述这条语句有多危险,而窗口改变的是流程,不是事实。
	// 降级会让审计里这条 DROP 看起来像一次普通查询。
	ref := model.NewRuleRef(model.RuleExecWindow, "window", w.Name)
	if v.Ref != nil {
		ref.Parts = []model.RuleRef{*v.Ref} // 原判嵌在里面,两半都能各自翻译
	}
	v.Ref = ref
	v.Rule = model.RenderRule(ref)
	return v, w
}

// windowFor 返回此刻放行这批语句的窗口:覆盖目标库、且语句没有越出那个库。没有则为 nil。
//
// 判定层的放宽(relaxByWindow)和在线变更免审批(execOSC)问的是同一个问题,所以共用它 ——
// 两处各写一份,迟早一处查了越库、另一处没查。
func (s *Services) windowFor(conn *model.Connection, stmts []string, now time.Time) *model.ExecWindow {
	w := s.activeWindow(conn, now)
	// 不越库查的是**这次的目标库**(它一定在窗口勾的库里,activeWindow 按它取的窗口)。
	// 多库窗口里在两个已勾选的库之间跨库操作,同样当越库走审批:判不准的一律多审一次。
	if w == nil || !staysInDatabase(conn, effectiveDatabase(conn), stmts) {
		return nil
	}
	return w
}

// staysInDatabase 报告这批语句是否都落在窗口开给的那个库里。
//
// 窗口按库开,而"这条命令落到哪个库"不只由终端里选中的库决定:MySQL 上库就是 schema,
// `DROP TABLE other_db.t` 落在 other_db;`USE other_db` 则把后面整批都挪了过去。Oracle
// 的窗口开给 TargetSchema,限定名同样能越出去。不查这一步,开给一个库的窗口等于开给
// 整台实例 —— 正是"库名必填"想挡住的那件事。
//
// PG 系只查 dblink:schema 在库**里面**,限定名到不了别的库。
//
// 判不准的一律当越库(gateway.StaysInDatabase)。此前是"认不出引用的不拦",靠
// sqlutil.ObjectRefs 只认得关键字后面紧跟的那个名字 —— 2026-10 黑盒里 `UPDATE B . t2`、
// JOIN / 逗号里的第二张表、REPLACE、PREPARE、带 CTE 的 UPDATE 全都免审批地改到了别库
// (工单 01 第 1 条)。窗口是唯一一处主动放宽的地方,宁可多走一次审批。
func staysInDatabase(conn *model.Connection, db string, stmts []string) bool {
	if conn == nil {
		return true // 没有连接就没有窗口(activeWindow 已返回 nil)
	}
	return gateway.StaysInDatabase(conn.Engine, db, stmts)
}

// windowExpired 判断这扇门是不是再也不会开了。
//
// 只看**时间表**,不看审批状态、也不看启用与否 —— 一张还在等审批、时段却已经过去的
// 窗口同样是到期的,而那恰恰是最该被看见的一种(见 model.ExecWindow.Expired)。
//
// 边界与 windowCovers 一致:左闭右开,所以到了结束时刻的那一瞬,窗口既不覆盖也已到期,
// 中间不留一个"两边都不是"的缝。
func windowExpired(w *model.ExecWindow, now time.Time) bool {
	if w == nil {
		return false
	}
	switch w.Kind {
	case model.WindowOnce:
		return w.EndsAt != nil && !now.Before(*w.EndsAt)
	case model.WindowRecurring:
		// 没设停运时刻的班车每周都会再来一次,本来就没有到期这回事。
		return w.NotAfter != nil && !now.Before(*w.NotAfter)
	}
	return false
}

// windowCovers 判断某一时刻是否落在窗口内 —— 判定层用的那一个,所以它看 Enabled:
// 一扇被运维关掉的门,此刻不开。
func windowCovers(w *model.ExecWindow, now time.Time) bool {
	if w == nil || !w.Enabled {
		return false
	}
	return scheduleCovers(w, now)
}

// scheduleCovers 只问**时间表**覆不覆盖这一刻,不问这扇门现在开着没有。
//
// 回看历史要的是这一个:一张昨天放行过语句、今天被关掉的窗口,它昨天的窗口期不会
// 因为今天被关而消失。放行与否用 windowCovers,回看用这一个,两者共用下面同一套
// 跨午夜与时区换算 —— 分成两份实现迟早分叉。
func scheduleCovers(w *model.ExecWindow, now time.Time) bool {
	if w == nil {
		return false
	}
	switch w.Kind {
	case model.WindowOnce:
		if w.StartsAt == nil || w.EndsAt == nil {
			return false
		}
		// 左闭右开:22:00-02:00 与紧随其后的 02:00-04:00 不会同时命中。
		return !now.Before(*w.StartsAt) && now.Before(*w.EndsAt)

	case model.WindowRecurring:
		if w.NotAfter != nil && !now.Before(*w.NotAfter) {
			return false // 班车已停运
		}
		loc, err := time.LoadLocation(w.Timezone)
		if err != nil || loc == nil {
			return false // 时区认不出来就不生效,而不是拿服务器时区顶上
		}
		return recurringCovers(w, now.In(loc))

	default:
		return false
	}
}

// recurringCovers 判断本地时刻是否落在周期班车里。
//
// 跨午夜是这里唯一需要小心的地方:22:00-02:00 这样的窗口,凌晨一点属于**前一天**
// 发的那班车。所以星期几要按"这班车是哪天发的"来算,而不是按当前是星期几 ——
// 否则一个"仅周五 22:00-02:00"的窗口会在周五凌晨误开,而周六凌晨反倒关着。
func recurringCovers(w *model.ExecWindow, local time.Time) bool {
	mins := local.Hour()*60 + local.Minute()
	if w.StartMin < w.EndMin {
		return weekdayAllowed(w.Weekdays, local.Weekday()) && mins >= w.StartMin && mins < w.EndMin
	}
	// 跨午夜(含 StartMin == EndMin 的退化写法,视为不覆盖任何时间)
	if w.StartMin == w.EndMin {
		return false
	}
	if mins >= w.StartMin { // 今天发的车,还没到午夜
		return weekdayAllowed(w.Weekdays, local.Weekday())
	}
	if mins < w.EndMin { // 昨天发的车,已过午夜
		return weekdayAllowed(w.Weekdays, local.AddDate(0, 0, -1).Weekday())
	}
	return false
}

// weekdayAllowed 判断星期几是否在班次里。空串表示每天。
// 存的是 ISO 编号:1=周一 … 7=周日(Go 的 Sunday 是 0,这里换算过)。
func weekdayAllowed(spec string, d time.Weekday) bool {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return true
	}
	iso := int(d)
	if iso == 0 {
		iso = 7 // Sunday
	}
	for _, part := range strings.Split(spec, ",") {
		if n, err := strconv.Atoi(strings.TrimSpace(part)); err == nil && n == iso {
			return true
		}
	}
	return false
}

// windowOperator 是审计 Operator 字段里记下的那行字。
//
// Operator 的语义是"当授权者不是操作者本人时,是谁授权的" —— 窗口正好是这个角色:
// 这条命令没有人批过,是这扇门当时开着。它在审计链的哈希里,事后改不了。
func windowOperator(w *model.ExecWindow) string {
	if w == nil {
		return ""
	}
	return "执行窗口 · " + w.Name + " (#" + strconv.FormatInt(w.ID, 10) + ")"
}
