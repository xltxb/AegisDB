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
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"velagateway/internal/model"
)

// windowApprovalCommand 是这张单在审批人眼前长什么样。
//
// 它是一句**描述**,不是可执行语句 —— 这张单授权的事情是"让这扇门在这段时间里开着",
// 不是"在终端里跑一条 SQL"。审批人要签的字就是这件事,所以摘要里必须同时出现
// 目标库、时间段和申请理由:少了任何一样,他签的就是一张空白支票。
func windowApprovalCommand(w *model.ExecWindow, connName string) string {
	scope := connName
	if scope == "" {
		scope = "实例#" + strconv.FormatInt(w.ConnectionID, 10)
	}
	reason := w.Reason
	if reason == "" {
		reason = "(未填)"
	}
	return fmt.Sprintf("开启执行窗口「%s」· %s/%s · %s · 理由:%s",
		w.Name, scope, w.Database, windowSchedule(w), reason)
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
		Instance: conn.Name, Database: w.Database,
		Command: windowApprovalCommand(w, conn.Name),
		Keyword: "EXEC-WINDOW",
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
	s.dispatchExternalApproval(u, ap)
	return nil
}

// applyWindowDecision 是一张窗口单被批准/驳回之后真正发生的事。
//
// 批准 = 这扇门从现在起按它自己的时间表生效(还没到点就还是不开 —— 状态说的是
// "有人签过字了",时间表说的是"现在开不开",两者都要满足)。
// 驳回 = 永久留在 rejected:判定层不认,列表里仍然看得见它被驳回过。
func (s *Services) applyWindowDecision(ap *model.Approval, approve bool) {
	status := model.WindowRejected
	if approve {
		status = model.WindowApproved
	}
	// 决策与"这张单是不是这个窗口当前那一张"在**同一条 UPDATE** 里裁(见
	// SetExecWindowDecision)。此前这里是先读窗口、比对 ApprovalID、再写状态 ——
	// 中间那道缝正好放得进一次改窗口,于是改后的定义被旧单上的签字批准了。
	applied, err := s.Repo.SetExecWindowDecision(ap.WindowID, ap.ID, status, time.Now())
	if err != nil {
		slog.Error("执行窗口审批结果落库失败", "window", ap.WindowID, "apNo", ap.ApNo, "status", status, "err", err)
		return
	}
	if !applied {
		// 这张单已经不是这个窗口当前那一张(窗口被改过/删过),或者窗口已经被决定过了。
		slog.Warn("窗口单已被新的定义取代或窗口已决定,这次决策不生效",
			"window", ap.WindowID, "apNo", ap.ApNo)
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
	s.recordAudit(&model.User{ID: ap.InitiatorID, Name: ap.Initiator}, conn,
		verb+"执行窗口「"+w.Name+"」· "+describeWindow(w)+" · 理由:"+w.Reason,
		model.RiskHigh, model.ResultExecuted, ap.ApNo, "approve")
}
