package service

// 发布单停在**人工闸**上等人点「继续」—— 谁能点，以及此刻有哪些在等他。
//
// 这是待办的第三类。前两类都挂在审批工单上（等我审批、等我执行），而一条流水线还有
// 第三种停法：它跑到一个人工确认点就停住了，等一个人来放行。这种停法原先没有任何地方
// 提示过 —— 发布单列表要自己翻，而一条停在凌晨的流水线没人翻。
//
// ## 为什么判断要集中在这里
//
// 两道闸的规则**不一样**，而这正是它必须只有一处的理由：
//
//   - manual（人工确认）：两人控制 —— 发起人自己不能把自己的发布单挥过去，除非管理员
//     显式开了自审批开关（和审批链同一个开关）。配了确认角色就以角色为准；没配就看
//     审批能力。
//   - execute（执行确认）：默认**允许发起人** —— 「何时执行」归发起人。一旦显式配了
//     角色，那正是要把这个决定收走（变更窗口统一把关），所以配置优先于发起人身份。
//
// 把这两条抄一份到待办那边去，两份判断迟早会不一致，而不一致的表现是：待办里亮着一条
// 「等你确认」，点进去按钮是灰的；或者反过来，真正在等他的那张单从来不出现。所以放行
// 动作（ContinueManualStage / ConfirmExecuteStage）、待办清单、以及发布单详情的可见性
// 三处读的都是下面这一个函数。

import (
	"aegisdb/internal/dto"
	"aegisdb/internal/model"
)

// isConfirmGate 判断一个阶段是不是**人工闸**(要一个人点「继续」才过的那种)。
//
// 只认 manual 与 execute 两种：approve 阶段也会停,但它等的是一张审批工单,那条路已经
// 由待办的第一类覆盖了 —— 在这里再算一次,同一件事会出现两遍。
func isConfirmGate(st *model.ReleaseStage) bool {
	// 自动执行阶段在分层不允许时会退回等人确认(见 stageExecute),那时它就是一道执行闸。
	return st != nil && (st.Type == model.StageManual || isExecuteStage(st.Type))
}

// isWaitingConfirmGate 再加一条：它**此刻正停在那里等人**。
func isWaitingConfirmGate(st *model.ReleaseStage) bool {
	return isConfirmGate(st) && st.Status == model.RunWaiting
}

// mayConfirmGate 回答「这个人能不能放行这道闸」。只管授权，不管阶段在不在等
// （那是 isWaitingConfirmGate 的事）—— 一个函数回答一个问题。
func (s *Services) mayConfirmGate(u *model.User, rel *model.Release, st *model.ReleaseStage) bool {
	if u == nil || rel == nil || st == nil {
		return false
	}
	// 执行确认:够不到这台实例的人不能替它放行(2026-10 安全审计 L-AUTH2),与审批单执行
	// 同一道闸(approval_execute.go 的 canAccessConn)。
	//
	// 人工确认闸**不**看实例标签(2026-10-26 回归 04):它语义上是审批,与审批单的「批准」
	// (DecideBlockFor 不要求标签)同口径。从前一并要求标签,没标签的确认人放行不了,这一步
	// 还从他的待办里消失,发布单就无声地卡住。真正碰数据库的执行阶段仍要标签。
	if isExecuteStage(st.Type) && !s.releaseConnReachable(u, rel) {
		return false
	}
	role := cfgConfirmRole(st.Config)
	if st.Type == model.StageManual {
		// 两人控制：发起人不能挥过自己的发布单。这一条在角色判断之前 —— 哪怕他就在
		// 那个确认角色里，自己批自己仍然不行。
		if u.ID == rel.CreatorID && !s.settingBool("approval.allowSelfApprove", false) {
			return false
		}
		if role != "" {
			return s.mayPassGate(u, role)
		}
		return s.canApproveReleases(u)
	}
	// execute：配置优先于发起人身份。
	if role != "" {
		return s.mayPassGate(u, role)
	}
	return u.ID == rel.CreatorID || s.canApproveReleases(u)
}

// releaseConnReachable:u 的标签授权够得到这张发布单的目标实例。
func (s *Services) releaseConnReachable(u *model.User, rel *model.Release) bool {
	conn, err := s.Repo.GetConnection(rel.ConnectionID)
	if err != nil {
		return false
	}
	return s.canAccessConn(u, conn)
}

// ConfirmTodo 是一条「某张发布单的某道闸在等你」。
type ConfirmTodo struct {
	Release model.Release
	Stage   model.ReleaseStage
}

// ConfirmTodos 列出此刻在等 u 去确认的人工闸。
//
// 不分页、不设上限：停在人工闸上的流水线天然是小集合（每张发布单最多停在一处），
// 而分页会让一条停了很久的流水线被新的挤出去 —— 那恰恰是最需要被看见的一条。
func (s *Services) ConfirmTodos(u *model.User) []ConfirmTodo {
	if u == nil {
		return nil
	}
	stages, err := s.Repo.WaitingConfirmStages()
	if err != nil {
		return nil
	}
	rels := map[int64]*model.Release{}
	out := []ConfirmTodo{}
	for i := range stages {
		st := stages[i]
		rel, seen := rels[st.ReleaseID]
		if !seen {
			rel, _ = s.Repo.GetRelease(st.ReleaseID)
			rels[st.ReleaseID] = rel
		}
		if rel == nil {
			continue
		}
		// 终态发布单上残留的 waiting 阶段不算待办：它点不动（ClaimReleaseStage 会失败），
		// 列出来只会让人反复点一条已经死掉的流水线。终止时 skipUnrunStages 会清掉它们，
		// 这里是兜底 —— 一条永远点不动的待办比没有待办更糟。
		switch rel.Status {
		case model.RunPending, model.RunRunning, model.RunWaiting:
		default:
			continue
		}
		if !s.mayConfirmGate(u, rel, &st) {
			continue
		}
		out = append(out, ConfirmTodo{Release: *rel, Stage: st})
	}
	return out
}

// staffsConfirmGate 报告这张发布单有没有把某道人工闸交到 u 手上。
//
// 它服务于一件具体的事：**被系统点名要去确认的人，必须看得见那张单**。发布单详情原先
// 只对发起人和监管角色开放，而一个由 confirmRole 指定的确认人常常两者都不是 —— 待办里
// 给他一条「等你确认」，点进去却是「发布单不存在或无权查看」。
//
// 这里**不要求那道闸此刻还在等**,与待办的口径有意不同。要求"正在等"的话,他点完
// 「继续」的那一瞬间就失去了可见性 —— 页面刷新回来是一句无权查看,而他刚刚才被要求
// 对这张单负责。判据是职责:这条流水线把一道闸路由给了他,这张单就是他的事,点之前
// 点之后都一样。
func (s *Services) staffsConfirmGate(u *model.User, rel *model.Release) bool {
	if u == nil || rel == nil {
		return false
	}
	stages, err := s.Repo.StagesOfRelease(rel.ID)
	if err != nil {
		return false
	}
	for i := range stages {
		if isConfirmGate(&stages[i]) && s.mayConfirmGate(u, rel, &stages[i]) {
			return true
		}
	}
	return false
}

// openGateConfirmers 列出此刻能放行这道人工确认闸的人,给 GET /open/releases/:relNo 用:
// 外部平台据此决定「待人工确认」卡片发给谁,confirm 接口的 confirmer 填其中一人的 account。
//
// 只给**正停在那里等人**的 manual 阶段 —— 名单是此刻的,放行之后就没有意义了;执行变更闸
// 不给,开放接口放行不了它,给了名单只会让对方以为能调 confirm(2026-11 与用户确认的口径)。
//
// 名单不另立规则:候选人按阶段配置取(配了确认角色 = 该角色在职真人成员,没配 = 全体在职
// 真人),再逐个过 mayConfirmGate —— 与控制台「继续」按钮、待办、confirm 接口同一个判断,
// 所以发起人(未开自审批时)不会出现在名单上,名单上的人调 confirm 也一定过得了授权。
func (s *Services) openGateConfirmers(rel *model.Release, st *model.ReleaseStage) []dto.OpenApprover {
	if rel == nil || st == nil || st.Type != model.StageManual || st.Status != model.RunWaiting {
		return nil
	}
	var candidates []model.User
	if role := cfgConfirmRole(st.Config); role != "" {
		people, err := s.gateStaffing(role)
		if err != nil {
			return nil
		}
		candidates = people
	} else {
		all, err := s.Repo.ListUsers()
		if err != nil {
			return nil
		}
		candidates = decidableApprovers(all)
	}
	out := []dto.OpenApprover{}
	for i := range candidates {
		u := &candidates[i]
		if !s.mayConfirmGate(u, rel, st) {
			continue
		}
		out = append(out, dto.OpenApprover{Name: u.Name, Account: u.Email, Status: "active"})
	}
	return out
}
