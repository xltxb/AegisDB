package service

// 执行窗口(「班车」)的申请 → 审批 → 生效。
//
// 为什么窗口要走审批,而高危命令字典、能力矩阵不走:
//
// 后两者是把闸门**调紧或调松的规则**,改动本身仍然要经过其它闸门才会变成一次下发。
// 窗口不一样 —— 它一次性地**把闸门打开一段时间**,而开着的那几个小时里,本该有人
// 签字的中/高风险语句会一条不落地直接下发。一个人就能打开这样一扇门,等于给了他
// 一条"先开窗口、再从窗口里进去"的路,全程没有第二个人看过。
//
// 所以:申请人提申请,单子走同一条默认审批链,**通过之后窗口才开始生效**。
// 等待期间它一行都不放行(闸门在 repository.ExecWindowsFor 的 status 条件上)。

import (
	"log/slog"
	"strconv"
	"strings"
	"time"

	"aegisdb/internal/apimsg"
	"aegisdb/internal/model"
)

// windowApprovalCommand 是这张单在审批人眼前长什么样。
//
// 它是一句**描述**,不是可执行语句 —— 这张单授权的事情是"让这扇门在这段时间里开着",
// 不是"在终端里跑一条 SQL"。审批人要签的字就是这件事,所以摘要里必须同时出现
// 目标库、时间段和申请理由:少了任何一样,他签的就是一张空白支票。
func windowApprovalCommand(w *model.ExecWindow, connName string) string {
	return model.RenderApText(windowApprovalRef(w, connName))
}

// windowApprovalRef 是同一句描述的机器可读形式,随单存下(Approval.CommandRef),界面据此
// 用读者的语言显示。存下而不是现算:窗口改过之后会另起一张单,旧单描述的必须仍是当时那一版。
func windowApprovalRef(w *model.ExecWindow, connName string) *model.RuleRef {
	scope := connName
	if scope == "" {
		scope = "#" + strconv.FormatInt(w.ConnectionID, 10)
	}
	var r *model.RuleRef
	if w.Reason == "" {
		r = model.NewRuleRef(model.ApWindowOpenNoReason, "name", w.Name, "scope", scope, "db", windowDBLabel(w))
	} else {
		r = model.NewRuleRef(model.ApWindowOpen, "name", w.Name, "scope", scope, "db", windowDBLabel(w), "reason", w.Reason)
	}
	r.Parts = []model.RuleRef{*windowScheduleRef(w)}
	return r
}

// fitWindowDBCol:审批单 db_name 列(128)里写什么。
func fitWindowDBCol(w *model.ExecWindow) string {
	if all := strings.Join(w.DBs(), ","); len([]rune(all)) <= 128 {
		return all
	}
	return w.Database
}

// raiseWindowApproval 为一个待审批的窗口建单,并把单号写回窗口。
//
// 失败要让整次申请失败,而不是留下一个"没有单可批、状态却是 pending"的窗口 ——
// 那种窗口永远开不了,人却看不出为什么。
func (s *Services) raiseWindowApproval(u *model.User, w *model.ExecWindow) error {
	conn, err := s.Repo.GetConnection(w.ConnectionID)
	if err != nil {
		return ErrNotFound
	}
	tierCode := ""
	if t, terr := s.tierOf(conn); terr == nil {
		tierCode = t.Code
	}
	ap := &model.Approval{
		ApNo: s.nextApNo(), ConnectionID: conn.ID, Env: conn.Env, TierCode: tierCode,
		// 审批单的库名列只有 128 宽:多库时放得下就写全,放不下写第一个 —— 完整清单在单子的描述里
		Instance: conn.Name, Database: fitWindowDBCol(w),
		Command:    windowApprovalCommand(w, conn.Name),
		CommandRef: windowApprovalRef(w, conn.Name),
		Keyword:  "EXEC-WINDOW",
		// 申请人是发起人。审批链、超时策略、自审批开关全部复用既有那一套 ——
		// 这里不该长出第二套"谁能批"的规则。
		InitiatorID: u.ID, Initiator: u.Name,
		Reason: w.Reason,
		// 按高危记。它自己不改任何数据,但风险等级衡量的是后果,而这扇门的后果是
		// 一整段时间里的中/高风险语句都不再需要人签字。
		RiskLevel: model.RiskHigh,
		Status:    model.StatusPending,
		AuditID:   s.nextAuditID(),
		WindowID:  w.ID,
	}
	steps := s.defaultChainSteps()
	if len(steps) > 0 {
		steps[0].Status = "active"
	}
	if err := s.Repo.CreateApproval(ap, steps); err != nil {
		slog.Error("执行窗口建单失败", "window", w.ID, "err", err)
		return err
	}
	if err := s.Repo.LinkWindowApproval(w.ID, ap.ID, ap.ApNo); err != nil {
		return err
	}
	w.ApprovalID, w.ApNo = ap.ID, ap.ApNo
	// 申请本身也进审计链:一扇免审批的门被"申请"过,和它被"打开"过,都是事后要
	// 能查的事。
	s.recordAudit(u, conn, ap.Command, model.RiskHigh, model.ResultPending, ap.ApNo, "intercept")
	s.Webhook.SendLarkApproval(ap)
	s.queueChatCards(ap, false)
	return nil
}

// windowTicketCurrent 报告这张窗口单是不是窗口**此刻挂着的那一张**。
//
// 窗口改一次就另起一张单(UpdateExecWindow),旧单描述的是旧定义。批准旧单若也能让窗口
// 生效,审批人签的是"1 小时",生效的却是改过之后的样子(2026-10 黑盒,工单 01 第 2 条)。
// 窗口已经删了的,当然也不是当前的单(第 3 条)。
func (s *Services) windowTicketCurrent(ap *model.Approval) bool {
	if ap == nil || ap.WindowID <= 0 {
		return false
	}
	w, err := s.Repo.GetExecWindow(ap.WindowID)
	return err == nil && w.ApprovalID == ap.ID
}

// withdrawWindowTicket 把窗口挂着的那张还在等审批的单撤回 —— 窗口被改或被删时调用。
//
// 不走 CancelApproval:那里校验"只有发起人或管理员能撤",而这里的权限已经由
// canManageWindow 判过了(改窗、删窗的人),单子是跟着窗口一起收场的。只撤 pending 的:
// 已批准的单记的是一次真实的签字,不能事后改成"撤回"。
func (s *Services) withdrawWindowTicket(actor *model.User, apID int64, why string) {
	if apID <= 0 {
		return
	}
	ap, err := s.Repo.GetApproval(apID)
	if err != nil {
		return
	}
	s.cancelStaleWindowTicket(actor, ap, why)
}

// cancelStaleWindowTicket 原子地把一张 pending 的窗口单改成撤回,并收掉它挂着的审批步骤。
// 抢不到(已被决定)就什么都不做。
func (s *Services) cancelStaleWindowTicket(actor *model.User, ap *model.Approval, why string) {
	claimed, err := s.Repo.ClaimApproval(ap.ID, model.StatusPending, model.StatusCancelled)
	if err != nil || !claimed {
		return
	}
	now := time.Now()
	_ = s.Repo.DecideActiveStep(ap.ID, model.StatusCancelled, now)
	_ = s.Repo.SetApprovalResult(ap.ID, "", nil, 0, now)
	conn, _ := s.Repo.GetConnection(ap.ConnectionID)
	name := "系统"
	if actor != nil {
		name = actor.Name
	} else {
		actor = &model.User{ID: ap.InitiatorID, Name: ap.Initiator}
	}
	// 进审计链:单子从待办里消失了,事后要看得出是因为窗口被改 / 被删。
	s.recordAuditFrom(model.AuditSourceApproval, actor, name, conn, ap.Command+" · "+why, ap.RiskLevel, model.ResultCancelled, ap.ApNo, "approve")
	if ap.InitiatorID != actor.ID {
		s.notify(ap.InitiatorID, model.NotifApprovalRejected, apimsg.T("窗口申请已撤回"),
			apimsg.Sprintf("%s:%s", apimsg.Of(why), safeClip(ap.Command, 80)), ap.ApNo)
	}
}

// applyWindowDecision 是一张窗口单被批准/驳回之后真正发生的事。
//
// 批准 = 这扇门从现在起按它自己的时间表生效(还没到点就还是不开 —— 状态说的是
// "有人签过字了",时间表说的是"现在开不开",两者都要满足)。
// 驳回 = 永久留在 rejected:判定层不认,列表里仍然看得见它被驳回过。
func (s *Services) applyWindowDecision(ap *model.Approval, approve bool) {
	// 兜底:finalizeApproval 已经挡掉了旧单,这里再认一次 —— 状态只跟着当前那张单走。
	if !s.windowTicketCurrent(ap) {
		slog.Warn("执行窗口:忽略非当前单的审批结果", "window", ap.WindowID, "apNo", ap.ApNo)
		return
	}
	status := model.WindowRejected
	if approve {
		status = model.WindowApproved
	}
	if err := s.Repo.SetExecWindowDecision(ap.WindowID, status, time.Now()); err != nil {
		slog.Error("执行窗口审批结果落库失败", "window", ap.WindowID, "apNo", ap.ApNo, "status", status, "err", err)
		return
	}
	w, err := s.Repo.GetExecWindow(ap.WindowID)
	if err != nil {
		return
	}
	conn, _ := s.Repo.GetConnection(w.ConnectionID)
	verb := "驳回"
	if approve {
		verb = "批准并开启"
	}
	// 门什么时候被谁打开过,是事后唯一能回答"当时凭什么不用审批"的依据,所以它
	// 进的是同一条不可篡改的审计链。
	s.recordAuditSrc(model.AuditSourceApproval, &model.User{ID: ap.InitiatorID, Name: ap.Initiator}, conn,
		verb+"执行窗口「"+w.Name+"」· "+describeWindow(w)+" · 理由:"+w.Reason,
		model.RiskHigh, model.ResultExecuted, ap.ApNo, "approve")
}
