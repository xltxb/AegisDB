package service

// 撤回一张**还没跑过**的工单 —— 等审批的,以及已批准但还没执行的。
//
// 后一种是"批是批了,但我不打算跑了"。它同样只有发起人自己能做:批准解锁的是**他**
// 的一次执行,放不放弃这次执行是他的事。
//
// 撤回和驳回是两件不同的事,刻意分开:
//
//   驳回 = 审批人看过之后说"不行"。它是一次**决定**,要留在记录里,并且要通知发起人。
//   撤回 = 发起人自己说"这张不用了"。没有人对它做过判断,所以它不该显示成被驳回过。
//
// 把两者合成一个动作会带来一个很难发现的后果:审批人可以用"撤回"把一张他本该驳回的
// 工单悄悄抹掉,而记录上看不出有人拒绝过什么。所以**审批人不能撤回** —— 他手里的
// 动作是驳回。能撤回的只有发起人自己,以及平台管理员(清理误提交的存量)。

import (
	"fmt"
	"log/slog"
	"time"

	"velagateway/internal/model"
)

// CancelApproval 撤回一张 pending 工单。
func (s *Services) CancelApproval(actor *model.User, id int64) error {
	if actor == nil {
		return ErrForbidden
	}
	ap, err := s.Repo.GetApproval(id)
	if err != nil {
		return ErrNotFound
	}
	if err := s.canCancel(actor, ap); err != nil {
		return err
	}

	// 一把同时管住"还没被决定"和"还没跑过"的原子闸(见 ClaimApprovalCancel)。
	// 抢不到只有三种可能:已被决定、已被清扫过期、或者**执行已经先一步占住了**——
	// 最后一种正是待执行的单子可撤之后新出现的赛跑。
	claimed, cerr := s.Repo.ClaimApprovalCancel(ap.ID)
	if cerr != nil {
		return cerr
	}
	if !claimed {
		return ErrAlreadyDecided
	}
	now := time.Now()
	// 把还"active"着的那一步一起收掉,否则审批链上会永远留着一个等不到人的节点。
	_ = s.Repo.DecideActiveStep(ap.ID, model.StatusCancelled, now)
	_ = s.Repo.SetApprovalResult(ap.ID, "", 0, now)

	// 这张单挂着的东西也要跟着收场,否则它会停在一个永远等不到结果的中间态。
	switch {
	case ap.WindowID > 0:
		// 窗口记成 cancelled 而不是 rejected:没有人驳回过它,是申请人自己收回的。
		// 对判定层两者一样(都不是 approved),对读记录的人不一样。
		ok, e := s.Repo.SetExecWindowDecision(ap.WindowID, ap.ID, model.WindowCancelled, now)
		if e != nil {
			slog.Error("撤回窗口申请时落库失败", "window", ap.WindowID, "apNo", ap.ApNo, "err", e)
		} else if !ok {
			// 撤回的是这张单,而窗口此刻已经不指着它了(它被改过,另建了新单) ——
			// 那扇门的去留归新单管,这次撤回只收掉这张单本身。
			slog.Info("撤回的窗口单已不是该窗口当前那一张,窗口状态不动",
				"window", ap.WindowID, "apNo", ap.ApNo)
		}
	case ap.ExportJobID > 0:
		// 导出任务停在 awaiting 等这张单;撤回之后没有人会再放行它。
		s.failExport(ap.ExportJobID, "发起人撤回了导出申请")
	}

	conn, _ := s.Repo.GetConnection(ap.ConnectionID)
	// 进审计链:一张工单消失了,事后要看得出是被人撤回的,而不是不知去向。
	s.recordAuditBy(actor, actor.Name, conn, ap.Command, ap.RiskLevel, model.ResultCancelled, ap.ApNo, "approve")
	// 管理员替别人撤的要通知到发起人 —— 他自己撤的不必再收一条自己发的通知。
	if ap.InitiatorID != actor.ID {
		s.notify(ap.InitiatorID, model.NotifApprovalRejected, "工单已被撤回",
			fmt.Sprintf("%s 撤回了你的工单：%s", actor.Name, safeClip(ap.Command, 80)), ap.ApNo)
	}
	return nil
}

// canCancel 说明这个人此刻能不能撤回这张单,不能的话为什么。
//
// 理由要具体:三种拒绝要人去做的事完全不同 —— 等别人处理、去驳回、去管流水线。
func (s *Services) canCancel(actor *model.User, ap *model.Approval) error {
	// 两种可撤:还在等审批的,以及已批准但还没执行的。
	//
	// 跑过的不能撤 —— 那条命令已经落到库上了,把工单改成"已撤回"只会让记录与事实
	// 对不上;要收回它得再发一条变更,而不是改一行状态。
	switch {
	case ap.Status == model.StatusPending:
	case ap.Status == model.StatusApproved && ap.ExecutedAt == nil:
	default:
		return ErrAlreadyDecided
	}
	if ap.ReleaseID > 0 {
		// 升级单的审批阶段正等着这张单的结论。撤回不是一种流水线认识的结论,单子
		// 一撤,那条流水线就停在一个没有任何东西能推进的状态上。要停就停发布单本身,
		// 要否掉就驳回 —— 两者流水线都处理得了。
		return fmt.Errorf("这是升级单里的审批工单,不能单独撤回;请驳回它,或去发布单上取消整条流程")
	}
	if ap.InitiatorID == actor.ID {
		return nil
	}
	for _, code := range s.Repo.RoleCodesForIDs(s.Repo.EffectiveRoleIDs(actor)) {
		if code == "admin" {
			return nil
		}
	}
	// 审批人走到这里是对的:他手里的动作是驳回,不是让这张单无声消失。
	return fmt.Errorf("只有发起人自己可以撤回;你可以驳回它")
}

// CanCancelApproval 是给列表用的那一位:按钮亮不亮,和点下去放不放行,说的是同一件事。
func (s *Services) CanCancelApproval(actor *model.User, ap *model.Approval) bool {
	return actor != nil && s.canCancel(actor, ap) == nil
}
