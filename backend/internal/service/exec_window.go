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
func (s *Services) relaxByWindow(conn *model.Connection, v gateway.Verdict, now time.Time) (gateway.Verdict, *model.ExecWindow) {
	if v.Action != gateway.ActionApprove {
		return v, nil // allow 无需放宽;deny 是"没权限",不在窗口的职责范围内
	}
	w := s.activeWindow(conn, now)
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

// windowCovers 判断某一时刻是否落在窗口内。
func windowCovers(w *model.ExecWindow, now time.Time) bool {
	if w == nil || !w.Enabled {
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
	return windowOperatorPrefix + w.Name + windowOperatorIDSuffix(w.ID)
}

// 构造与查询共用的两个零件。
//
// 拆出来不是为了少打几个字,是因为**两边各写一遍字符串是这个功能最可能坏掉的方式**:
// 改了 windowOperator 的措辞之后,按窗口取审计会安静地返回空列表 —— 不报错,只是那扇
// 门看起来从没放行过任何东西,而"这扇门放行了什么"正是要问的问题。
// TestWindowOperator_MatchesItsOwnQueryPattern 钉住这两半对得上。
const windowOperatorPrefix = "执行窗口 · "

func windowOperatorIDSuffix(id int64) string {
	return " (#" + strconv.FormatInt(id, 10) + ")"
}

// windowOperatorLike 是按窗口 id 取审计行的 LIKE 模式。
//
// 认 **id 不认名字**:窗口改名之后,它此前放行过的记录仍然归它 —— 那些记录写下时是
// 什么样就是什么样(Operator 在链哈希里,本来也改不了)。中间那个 % 吃掉的正是当时的
// 名字,连名字里带 % 或 _ 的情况一并盖住,因为那段根本不参与匹配。
func windowOperatorLike(id int64) string {
	return windowOperatorPrefix + "%" + windowOperatorIDSuffix(id)
}

// operatorBelongsToWindow 判断一条审计的 Operator 是不是这扇门放行留下的。
//
// 与 windowOperatorLike 是同一个判断的两种写法:一个给数据库,一个给 Go。
func operatorBelongsToWindow(op string, id int64) bool {
	return strings.HasPrefix(op, windowOperatorPrefix) &&
		strings.HasSuffix(op, windowOperatorIDSuffix(id))
}

// AuditForWindow 取这扇门放行过的命令,新的在前。
//
// Operator 上没有索引,所以这是一次全表扫。按窗口回看是合规审查的动作,不在任何热
// 路径上,今天这么做是对的;等审计表长到让这一次扫描变得难受时,该做的是给审计行加
// 一个真正的 window 列,而不是在这里加缓存 —— 那时历史行可以从 Operator 解析回填。
func (s *Services) AuditForWindow(u *model.User, id int64) ([]model.AuditLog, error) {
	// 与审计本身同一把尺子(canSeeAllActivity),不是"能管窗口就能看"。
	//
	// 这个入口交出来的是**别人执行过的命令原文** —— 和审计列表是同一类东西,只是换了
	// 个问法。自己判一套的话,它就成了绕过审计可见性的后门:一个够格管窗口、却不够格
	// 看全量活动的人,能从这里把命令一条条读走。
	if !s.canSeeAllActivity(u) {
		return nil, ErrForbidden
	}
	return s.Repo.AuditByOperatorLike(windowOperatorLike(id))
}
