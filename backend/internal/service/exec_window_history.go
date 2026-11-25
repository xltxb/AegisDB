package service

import (
	"sort"
	"time"

	"aegisdb/internal/dto"
	"aegisdb/internal/model"
)

// 执行窗口的回看 —— 「这扇门开着的那几个小时里,到底跑了什么」。
//
// 这个问题此前只能到审计页去拼:先记住窗口是哪台实例的哪个库、几点到几点,再手动
// 按时间筛一遍;周期班车更是要一周一周地翻。而它恰恰是事后最该被问到的一句话 ——
// 一扇免审批的门开过之后,没有人签过字,唯一的凭据就是这段时间里的审计行。
//
// 两条口径写在这里,别处不重复:
//   · 窗口期由 scheduleCovers 判(与判定层同一套跨午夜/时区换算),不在 SQL 里重算
//   · 这一页**不按人收窄**:能打开这扇门的回看,就看得见这段时间里所有人跑过的语句
//     原文,不只是自己的。
//
// 第二条是刻意与审计页分道的(审计页仍按 auditActor 收窄,没有监督角色的人只看得见
// 自己的)。理由在这扇门本身:它免掉的是审批,也就免掉了签字的人 —— 事后没有任何一
// 个人天然知道那几个小时里发生过什么。一份只剩下"我自己跑的那两条"的回看,读起来
// 和"这扇门没出什么事"一模一样,而它恰恰是这一页存在的反面。窗口是**大家共用**的一
// 扇门,它放行过什么就该是共同可见的。
//
// 范围仍然是收着的:只有这扇门自己那台实例、那个库、那段窗口期里的行(见
// AuditForWindow 的 WHERE),换句话说放开的是"谁跑的",不是"哪些库"。
//
// 正因为放开了"谁跑的",另一头要立一道门槛:谁打得开这一页(canReviewWindow)。

// windowScanCap 是一次回看最多扫多少条审计行。
//
// 周期班车没有终点,"窗口期"是它自建起以来的每一个班次,天然没有上界。给一个上限
// 换来一句确定的查询代价;够不够看由 Truncated 如实说出来,而不是把列表默默截短。
const windowScanCap = 1000

// canReviewWindow 判谁打得开回看这一页。
//
// 两拨人,各有各的理由:
//   - 持有 execwindow 菜单 —— 班车是他们自己的页,共用的那扇门放行过什么,本来就该
//     一起看得见(这正是上面放开"谁跑的"要服务的人:l2 够得到这个菜单,却不是监督角色);
//   - 有监督角色(admin/owner/audit)—— 查旧账是本职。默认矩阵里审计角色**没有**
//     execwindow 键(申请窗口的是接下来要执行的人,审计员不执行),只认菜单会把唯一
//     一个以查旧账为业的角色挡在门外。
//
// 监督用**角色**判而不是 audit 菜单键:默认矩阵里连只读角色都带着 audit 键(他们看得
// 见审计页,只是被 auditActor 收窄到自己那几行),拿菜单键判,这道门槛等于没立。
//
// 这条判断放在 service 而不是路由上,和改/撤窗口同一个理由:路由只认得菜单键,认不出
// 角色。挡掉的是两边都不沾的账号 —— 他们在界面上本来就进不了班车页(路由 meta.menuKey),
// 此前只有直接打接口这一条路够得着。
func (s *Services) canReviewWindow(u *model.User) bool {
	if u == nil {
		return false
	}
	if s.canSeeAllActivity(u) {
		return true
	}
	menus, _ := s.Repo.MenusForRoles(s.Repo.EffectiveRoleIDs(u))
	return menus["execwindow"]
}

// WindowStatements 回看一扇门的窗口期里下发过的语句,新的在前。
func (s *Services) WindowStatements(u *model.User, id int64) (*dto.WindowStatements, error) {
	if !s.canReviewWindow(u) {
		return nil, ErrForbidden
	}
	w, err := s.Repo.GetExecWindow(id)
	if err != nil {
		return nil, ErrNotFound
	}
	// 回看里是那台实例上跑过的语句原文:够不到那台实例的人不该看得到(与 /terminal/exec
	// 同一道闸,2026-10 黑盒,工单 01 第 4 条)。监督角色查旧账是本职,不受实例标签限制。
	// 够不到与不存在回同一个错误,不泄露窗口 / 实例存不存在。
	overseer := s.canSeeAllActivity(u)
	if !overseer && !s.windowConnReachable(u, w.ConnectionID) {
		return nil, ErrNotFound
	}
	// "不按人收窄"的前提是这扇门真的开过。非监督角色只能回看批准过的窗口,且只看批准之后:
	// 否则建一扇起点倒填的窗口、不等批准,就能翻看那台实例上所有人过去的语句原文
	// (2026-10 安全审计 M1)。监督角色查旧账是本职,不受此限。
	if !overseer && (w.Status != model.WindowApproved || w.DecidedAt == nil) {
		return nil, ErrForbidden
	}
	// actor 传 0 = 不按人收窄(见上面的第二条口径)。门槛在 canReviewWindow 那一道:
	// 打得开这一页的人,看得见的是全部,而不是各自的一角。
	const actor = 0
	since, until := windowScanRange(w)
	if !overseer && since.Before(*w.DecidedAt) {
		since = *w.DecidedAt
	}
	rows, err := s.Repo.AuditForWindow(actor, w.ConnectionID, w.DBs(), since, until, windowScanCap)
	if err != nil {
		return nil, err
	}
	truncated := len(rows) >= windowScanCap
	// 第二趟按 Operator 精确捞这扇门放行过的行(理由见 AuditRelaxedBy):它们不受
	// 时间区间与时间表的摆布,一条都不能少。两趟按审计 ID 去重。
	operator := windowOperator(w)
	relaxed, err := s.Repo.AuditRelaxedBy(actor, w.ConnectionID, w.DBs(), operator, windowScanCap)
	if err != nil {
		return nil, err
	}

	out := &dto.WindowStatements{WindowID: w.ID, Items: []dto.WindowStatement{}, Truncated: truncated}
	seen := make(map[int64]bool, len(rows)+len(relaxed))
	for _, batch := range [][]model.AuditLog{relaxed, rows} {
		for i := range batch {
			r := &batch[i]
			if seen[r.ID] {
				continue
			}
			by := r.Operator == operator
			// 落在时间表里,或者被这扇门亲自放行过 —— 后者不看时间表。
			if !by && !scheduleCovers(w, r.OccurredAt) {
				continue
			}
			seen[r.ID] = true
			if by {
				out.ByWindow++
			}
			out.Items = append(out.Items, dto.WindowStatement{
				AuditID: r.ID, OccurredAt: r.OccurredAt, Actor: r.ActorName, Command: r.Command,
				Risk: r.Risk, Result: r.Result, DataAction: r.DataAction, ApprovalNo: r.ApprovalNo,
				ByWindow: by,
			})
		}
	}
	// 两趟合起来之后顺序是乱的(各自有序,拼起来不是),这里重新按时间倒排 ——
	// 列表要能一眼看出"那天夜里先跑了什么"。
	sort.Slice(out.Items, func(i, j int) bool {
		a, b := out.Items[i], out.Items[j]
		if a.OccurredAt.Equal(b.OccurredAt) {
			return a.AuditID > b.AuditID
		}
		return a.OccurredAt.After(b.OccurredAt)
	})
	return out, nil
}

// windowScanRange 把扫描收窄到这扇门可能覆盖的时间区间。
//
// 一次性窗口就是它自己的起止 —— 区间之外一行都不可能属于它,交给数据库用时间索引挡掉。
// 周期班车没有这样的区间,只能从**它存在的那一刻**起算:更早的语句跑的时候这扇门还
// 不存在,那段时间不是它的窗口期。上界留空(即"到现在"):语句不会发生在将来,而
// 停运时刻(NotAfter)由 scheduleCovers 逐行判,不必在这里再写一遍。
func windowScanRange(w *model.ExecWindow) (since, until time.Time) {
	if w.Kind == model.WindowOnce {
		if w.StartsAt != nil {
			since = *w.StartsAt
		}
		if w.EndsAt != nil {
			until = *w.EndsAt
		}
		return since, until
	}
	return w.CreatedAt, time.Time{}
}
