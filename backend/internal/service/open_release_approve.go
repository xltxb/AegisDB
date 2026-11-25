package service

// 开放接口的发布单审批 —— 外部审批系统(OA、工单平台)替网关的审批人点「通过 / 驳回」。
//
// 它不是第二套审批规则。三条口径与控制台那条完全一样,只是"谁在点"换了一种证明方式:
//
//   - 凭据只证明"这个外部系统可信",不证明"这个人可以批"。请求里必须点名审批人(账号邮箱),
//     那个人必须在这张审批单的链上、不是发起人 —— 判断走 DecideBlockFor,和控制台按钮同一处。
//     凭据本身从来不是审批人:拿到一把凭据不等于能以任何人的名义批任何单。
//   - 凭据能碰哪些发布单,按它的服务账号的实例范围算(和 /open/instances 同一个口径),
//     不要求是本凭据提交的 —— 外部审批系统通常不是提单的那个系统。
//   - 控制台照样能批,先到先得:决定落在 finalizeApproval 的原子认领上,后到的一方原样拿到
//     已有结果(alreadyDecided=true),不会把同一张单决定两次。
//
// scope 是 release:approve,不在默认集合里 —— 一把没留意勾选的 CI 凭据不该顺带拿到审批能力。

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"velagateway/internal/apimsg"
	"velagateway/internal/dto"
	"velagateway/internal/model"
)

// OpenDecideActions 是 action 认的两个值。不用布尔:漏传的 approved 会被读成 false,
// 一次没写全的"通过"请求就悄悄变成了驳回。
const (
	OpenActionApprove = "approve"
	OpenActionReject  = "reject"
)

// DecideReleaseForClient 让外部系统以审批链上某个人的名义决定一张发布单当前的审批阶段。
func (s *Services) DecideReleaseForClient(u *model.User, cl *model.APIClient, relNo string, req dto.OpenDecideReq) (*dto.OpenDecideResp, error) {
	if cl == nil || u == nil {
		return nil, ErrForbidden
	}
	action := strings.ToLower(strings.TrimSpace(req.Action))
	if action != OpenActionApprove && action != OpenActionReject {
		return nil, errors.New("action 只能是 approve 或 reject")
	}
	account := strings.TrimSpace(req.Approver)
	if account == "" {
		return nil, errors.New("请提供审批人账号(approver,网关账号邮箱)")
	}
	rel, err := s.Repo.GetReleaseByNo(strings.TrimSpace(relNo))
	if err != nil {
		return nil, ErrNotFound
	}
	conn, cerr := s.Repo.GetConnection(rel.ConnectionID)
	if cerr != nil || !s.canAccessConn(u, conn) {
		// 够不到的实例上的单,和不存在说成两件事:调用方要知道该去找谁加实例范围。
		return nil, ErrForbidden
	}
	comment := clip(strings.TrimSpace(req.Comment), 200)

	// 失败也留痕:外部系统替人做决定,被拒的那一次和成功的一样值得事后追。
	audit := func(result, what string) {
		line := fmt.Sprintf("open.release.%s 发布单 %s「%s」审批人 %s · %s", action, rel.RelNo, rel.Title, account, what)
		if comment != "" {
			line += " · 意见:" + comment
		}
		s.recordAuditFrom(model.AuditSourceRelease, u, apiOperator(cl), conn, line, orDefault(rel.Risk, model.RiskMid), result, rel.RelNo, "approve")
	}

	ap, err := s.currentReleaseApproval(rel.ID)
	if err != nil {
		audit(model.ResultRejected, err.Error())
		return nil, err
	}
	out := &dto.OpenDecideResp{RelNo: rel.RelNo, ApprovalNo: ap.ApNo}

	approver, uerr := s.Repo.GetUserByEmailFold(account)
	if uerr != nil || approver == nil {
		audit(model.ResultRejected, "审批人账号不存在")
		return nil, apimsg.Errorf("审批人账号不存在: %s", account)
	}
	account = approver.Email // 审计记网关里的账号原样,不记调用方的大小写写法
	if approver.Kind == model.UserKindService || (approver.Status != "" && approver.Status != "active") {
		audit(model.ResultRejected, "审批人账号不可用")
		return nil, apimsg.Errorf("审批人账号 %s 是服务账号或已停用,不能审批", account)
	}

	switch block := s.DecideBlockFor(approver, ap); block {
	case BlockNone:
	case BlockNotPending:
		return s.openDecided(out, ap.ID, rel.ID), nil
	default:
		audit(model.ResultRejected, block.Reason())
		return nil, &DecideRefusal{Block: block}
	}

	approve := action == OpenActionApprove
	if _, ferr := s.finalizeApproval(ap, approve, approver.Name, approver.ID); ferr != nil {
		if errors.Is(ferr, ErrAlreadyDecided) {
			// 和控制台(或另一次调用)抢输了:报对方的结论,不报错。
			return s.openDecided(out, ap.ID, rel.ID), nil
		}
		audit(model.ResultRejected, ferr.Error())
		return nil, ferr
	}
	status, decided := model.StatusRejected, "已驳回"
	if approve {
		status, decided = model.StatusApproved, "已通过"
	}
	audit(model.ResultExecuted, decided)
	out.Status = status
	out.ReleaseStatus = s.releaseStatusOf(rel.ID)
	return out, nil
}

// currentReleaseApproval 找发布单此刻要决定的那张审批单:正在等的 approve 阶段。
// 没有在等的,退到最近一个已经生成过审批单的 approve 阶段 —— 那是"已经被人决定过了",
// 调用方应拿到结论,而不是一句"不在审批阶段"。
func (s *Services) currentReleaseApproval(releaseID int64) (*model.Approval, error) {
	stages, err := s.Repo.StagesOfRelease(releaseID)
	if err != nil {
		return nil, err
	}
	var last *model.ReleaseStage
	for i := range stages {
		st := &stages[i]
		if st.Type != model.StageApprove || st.ApprovalID == 0 {
			continue
		}
		if st.Status == model.RunWaiting {
			last = st
			break
		}
		last = st
	}
	if last == nil {
		return nil, errors.New("该发布单还没有走到审批阶段(或流程里没有审批阶段)")
	}
	ap, err := s.Repo.GetApproval(last.ApprovalID)
	if err != nil {
		return nil, ErrNotFound
	}
	return ap, nil
}

// openDecided 是"已经有人决定过了"的回答:当前结论,而不是错误。
func (s *Services) openDecided(out *dto.OpenDecideResp, approvalID, releaseID int64) *dto.OpenDecideResp {
	if cur, err := s.Repo.GetApproval(approvalID); err == nil {
		out.Status = cur.Status
	}
	out.AlreadyDecided = true
	out.ReleaseStatus = s.releaseStatusOf(releaseID)
	return out
}

func (s *Services) releaseStatusOf(id int64) string {
	if r, err := s.Repo.GetRelease(id); err == nil {
		return r.Status
	}
	return ""
}

// openApprovers 列出一张审批单的审批人,给 GET /open/releases/:relNo 的审批阶段用:
// 外部审批系统据此知道该把单子推给谁,approver 填哪个账号。
func (s *Services) openApprovers(approvalID int64) []dto.OpenApprover {
	steps, err := s.Repo.StepsOf(approvalID)
	if err != nil || len(steps) == 0 {
		return nil
	}
	out := make([]dto.OpenApprover, 0, len(steps))
	for _, st := range steps {
		one := dto.OpenApprover{Name: st.Approver, Status: st.Status}
		if usr, uerr := s.Repo.GetUserByID(st.ApproverID); uerr == nil {
			one.Account = usr.Email
		}
		if st.ActedAt != nil {
			one.ActedAt = st.ActedAt.Format(time.RFC3339)
		}
		out = append(out, one)
	}
	return out
}
