package service

// 审批通过 ≠ 已经执行。
//
// 原先审批人一点通过,网关就代替发起人把命令跑了。对**升级单**来说早已不是这样
// (流水线的执行阶段拥有执行,审批只是授权),但终端里那条被拦下来的命令仍然是
// "批了就跑"。两个问题:
//
//   - 命令在**审批人点下去的那一刻**执行,而发起人可能已经不在现场。一条 DROP 在
//     半夜被批准并执行,没有人在看着它 —— 出了事,发现得比谁都晚。
//   - 审批人按下的是"我同意",不是"现在就跑"。绑在一起,等于让审批人替发起人选择了
//     执行时机,而那是发起人才知道的事(业务低峰、应用是否已停、备份是否就绪)。
//
// 所以拆成两步:审批通过 → 通知发起人 → 由发起人自己执行。
//
// 谁可以按下那一下?
//
// 一开始是"只有发起人"。现实把它推翻了:变更在凌晨两点批下来,发起人已经下班,而
// 值班的同事接不了手 —— 于是这张单要么干等到第二天,要么有人拿发起人的账号去跑,
// 后者比放开更糟。所以现在是:**谁本来就能在这台实例上跑这条命令,谁就能执行它。**
//
// 要紧的是这句话没有放松任何一道闸。下面每一道都按**实际按下按钮的人**(actor)算,
// 而不是按发起人:
//   - canAccessConn(actor) —— 标签授权,够不到这台实例的人连看都看不到这张单
//   - checkMFA(actor)      —— 生产步进验证走他自己的 TOTP
//   - EvaluateFor(actor)   —— 能力矩阵按他的角色复判,只读角色照样 deny
// 批准授权的是"这条命令可以跑",不是"任何人可以绕过访问控制"。
//
// 三条边界,每一条都有测试钉住:
//   - 执行的人必须**自己就有权在那台实例上跑这条命令**(不是"随便谁都行")
//   - 一张工单只能执行**一次**(批准是对一次执行的授权,不是可反复使用的通行证)
//   - **升级单的工单不走这条路**(它的执行归流水线所有,插队执行会把同一个变更应用
//     两次 —— 一次在这里,一次在还以为自己没跑过的流水线里)

import (
	"context"
	"fmt"
	"log/slog"

	"velagateway/internal/dto"
	"velagateway/internal/gateway"
	"velagateway/internal/model"
)

// ExecuteApproved runs the command of a ticket that has been approved, on behalf
// of — and at the request of — its initiator.
func (s *Services) ExecuteApproved(actor *model.User, id int64, mfaCode string) (*dto.ExecResp, error) {
	ap, err := s.Repo.GetApproval(id)
	if err != nil {
		return nil, ErrNotFound
	}
	if err := s.canExecuteApproved(actor, ap); err != nil {
		return nil, err
	}

	conn, _ := s.Repo.GetConnection(ap.ConnectionID)
	if conn == nil {
		return nil, fmt.Errorf("目标连接已不存在,无法执行")
	}
	applyTargetDatabase(conn, ap.Database)

	// 执行是一次**真实的下发**,所以它要过和终端同一组前置检查。
	//
	// 这一段原先没有,而"已批准但未执行"的工单目前不会过期 —— 于是一张上周批准的
	// 工单,在发起人的标签权限被收窄、实例进了维护态、MFA 被重置之后,今天照样跑得
	// 掉。批准授权的是"这条命令",不是"绕过此后一切访问控制"。
	if !s.canAccessConn(actor, conn) {
		return nil, ErrForbidden
	}
	if conn.Status == "maint" {
		return nil, fmt.Errorf("目标实例处于维护态,暂不能执行")
	}
	if err := s.checkMFA(actor, conn, mfaCode); err != nil {
		return nil, err
	}

	// 下发前**再判一次**。工单可能在待执行状态里放了很久,期间字典、实例归属、
	// 发起人的角色都可能变 —— 这与发布流水线执行阶段的规则一致(发布不是执行旁路)。
	//
	// tier 取**实时**的,不是工单上的快照:快照记的是"当时按哪一层判的",那是给人
	// 事后看的;而这一次是真的要下发,该按实例**现在**属于哪一层来判。实例被挪进
	// 更严的分层之后,拿旧快照复判等于按已经作废的规则放行。发布流水线的执行阶段
	// 一直用实时 tier,两条路不该给出不同答案。
	tier, terr := s.tierCodeOf(conn)
	if terr != nil {
		return nil, ErrBadRequest // 分层解析不出来就不执行,不按"放行"处理
	}
	// 只在结论变成"拒绝"时拦下:命中"需审批"是正常的,这张工单正是那次审批的结果。
	if v := s.Engine.EvaluateFor(s.Repo.EffectiveRoleIDs(actor), conn.Engine, tier, ap.Command); v.Action == gateway.ActionDeny {
		return nil, fmt.Errorf("规则已变化,该命令现在被禁止执行(%s),请重新提交", v.Rule)
	}

	// 原子地占住这次执行 —— 并发两个请求只有一个能成。
	//
	// 占位在执行**之前**,而且失败也不退回:执行失败时命令到底跑没跑是不确定的
	// (连接在语句中途断开是最常见的一种),退回让人重试就可能把变更应用两次。
	// 这与发布流水线在网关重启时把 running 判失败、不重跑,是同一条理由。
	claimed, cerr := s.Repo.ClaimApprovalExecution(ap.ID)
	if cerr != nil {
		return nil, cerr
	}
	if !claimed {
		return nil, fmt.Errorf("该工单已经执行过了")
	}

	var res gateway.ExecResult
	if ap.ScriptUploadID > 0 {
		// 脚本工单带的是引用而不是正文:重新读文件、校验哈希仍与审查时一致、逐条
		// 重判再逐条执行(见 runApprovedScript)。放在执行这一步做,恰恰是它该在的
		// 位置 —— 校验的是"即将执行的这些字节"。
		res = s.runApprovedScript(ap, conn)
	} else {
		// 工单里可能是一整批语句(终端粘贴的那种):判定当初看的是整批,下发时同样
		// 逐条给驱动 —— 见 execCommand。
		res = s.execCommand(context.Background(), conn, ap.Command, s.execTimeout())
	}

	// 这张单最后怎么样了,以**库那边收没收下**为准。从前只存了输出和行数,失败的
	// 原因确实写在输出里,却没有任何东西说那段文字是一次失败 —— 于是一条跑挂的
	// DROP 和一条跑成的,在列表上是同一个"已通过"。
	execStatus := model.ExecStatusSuccess
	if res.Err != nil {
		execStatus = model.ExecStatusFailed
	}
	// 写不进去就等于把结果丢了,工单会永远停在"已执行、结果不明"。这里不能让它
	// 静悄悄地发生 —— 原先是 `_ =`,那时丢掉的只是一段输出,现在丢掉的是结论。
	if err := s.Repo.SetApprovalExecResult(ap.ID, res.Output, res.Rows, execStatus); err != nil {
		slog.Error("failed to record approval execution result",
			"apNo", ap.ApNo, "execStatus", execStatus, "err", err)
	}
	s.recordAuditBy(actor, actor.Name, conn, ap.Command, ap.RiskLevel, execResultStatus(res), ap.ApNo, "exec")
	// 别人替你跑了,你必须知道。审计里当然记着,但发起人不会天天去翻审计 —— 而
	// "我的变更什么时候生效的"是他一定会问的问题。
	if actor.ID != ap.InitiatorID {
		title := "你的工单已由他人执行"
		if res.Err != nil {
			title = "你的工单由他人执行,但失败了"
		}
		s.notify(ap.InitiatorID, model.NotifApprovalApproved, title,
			fmt.Sprintf("%s 执行了你的工单 %s：%s", actor.Name, ap.ApNo, safeClip(ap.Command, 60)), ap.ApNo)
	}
	return &dto.ExecResp{Risk: ap.RiskLevel, Output: res.Output, Rows: res.Rows, Ms: res.Ms,
		Columns: res.Columns, Data: res.Data, Truncated: res.Truncated}, nil
}

// canExecuteApproved says whether this person may run this ticket now, and if
// not, why — the reason is the payload, because the four refusals ask for
// completely different things: wait for approval, ask the initiator, look at the
// release, or raise a new ticket.
func (s *Services) canExecuteApproved(actor *model.User, ap *model.Approval) error {
	if actor == nil {
		return ErrForbidden
	}
	if ap.WindowID > 0 {
		// 窗口单的 Command 是一句描述("开启执行窗口「…」"),不是可执行语句。
		// 让它走到这里,网关会把那句话当 SQL 发给数据库。
		return fmt.Errorf("这是执行窗口的申请单,批准即生效,没有需要手动执行的命令")
	}
	if ap.ReleaseID > 0 {
		return fmt.Errorf("这是升级单的审批工单,执行由发布流水线的执行阶段完成,不能在这里执行")
	}
	if ap.ExportJobID > 0 {
		// 导出单的 Command 是一句描述("EXPORT [含敏感字段(原值)] …"),不是可执行的
		// 语句。放它走这条路,网关会把那句描述当成命令发给数据库。
		return fmt.Errorf("这是导出申请的审批工单,批准后由导出任务后台执行,不能在这里执行")
	}
	if ap.Status != model.StatusApproved {
		switch ap.Status {
		case model.StatusPending:
			return fmt.Errorf("该工单还在审批中,通过后才能执行")
		case model.StatusRejected:
			return fmt.Errorf("该工单已被驳回,不能执行")
		default:
			return fmt.Errorf("该工单已失效(%s),不能执行", ap.Status)
		}
	}
	if ap.ExecutedAt != nil {
		return fmt.Errorf("该工单已经执行过了")
	}
	// 到这里不再问"你是不是发起人"。真正的门在 ExecuteApproved 里,而且都按 actor 算:
	// 标签授权、MFA、能力矩阵复判。列表页的可见性同样收在标签授权上 —— 够不到这台
	// 实例的人根本看不到这张单(见 repository.approvalScope)。
	return nil
}

// CanExecuteApproved is the console's read of the same rule, so the button is
// only live when pressing it would work.
func (s *Services) CanExecuteApproved(actor *model.User, ap *model.Approval) bool {
	return s.canExecuteApproved(actor, ap) == nil
}
