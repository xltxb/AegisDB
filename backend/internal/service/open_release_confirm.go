package service

// 开放接口的人工确认 —— 外部平台(发布平台的「待人工确认」卡片)替确认人点发布单上「人工确认」的「继续」。
//
// 2026-11-10 用户反馈:发布平台推出「DB Gateway SQL 待人工确认」卡片,但网关没有开放这一步,
// 平台只能叫人打开网关去点。审批阶段早有 /open/releases/:relNo/approval,人工确认却没有。
//
// 口径与外部审批(open_release_approve.go)一致,只是作用对象换成人工确认闸:
//
//   - 凭据不是确认人。请求必须点名一个网关账号(confirmer),能不能放行由 mayConfirmGate 判断 ——
//     与控制台按钮、右上角待办同一处(两人控制、确认角色都在里面)。
//   - 凭据能碰的发布单 = 服务账号实例范围内的,不要求是本凭据提交的。
//   - scope 沿用 release:approve:人工确认闸语义上就是审批(见 release_confirm.go),
//     不另开一个范围;没显式勾选的凭据照样碰不了。
//   - 先到先得:控制台已经放行了,外部再来拿到 alreadyConfirmed=true,不报错也不重复放行。
//
// **只管人工确认,不管执行变更。** 「确认执行」是打到目标库上的那一下,用户明确要求(2026-11-10)
// 它只能在网关上由人操作,不能经外部系统放行。所以这里只认 manual 阶段;发布单停在执行闸上时
// 明确拒绝并说清楚要去网关点,而不是"此刻停着哪道就放哪道" —— 人工确认放行后流水线常常紧接着
// 停在执行闸上,一次重试若顺手把它放了,就等于替人按下了执行。
//
// 只确认、不驳回:人工闸在控制台上也只有「继续」,不放行的单走终止(/abort)。

import (
	"errors"
	"fmt"
	"strings"

	"aegisdb/internal/apimsg"
	"aegisdb/internal/dto"
	"aegisdb/internal/model"
)

// ConfirmReleaseForClient 以确认人的名义放行发布单此刻停着的人工确认。
func (s *Services) ConfirmReleaseForClient(u *model.User, cl *model.APIClient, relNo string, req dto.OpenConfirmReq) (*dto.OpenConfirmResp, error) {
	if cl == nil || u == nil {
		return nil, ErrForbidden
	}
	account := strings.TrimSpace(req.Confirmer)
	if account == "" {
		return nil, errors.New("请提供确认人账号(confirmer,网关账号邮箱)")
	}
	rel, err := s.Repo.GetReleaseByNo(strings.TrimSpace(relNo))
	if err != nil {
		return nil, ErrNotFound
	}
	conn, cerr := s.Repo.GetConnection(rel.ConnectionID)
	if cerr != nil || !s.canAccessConn(u, conn) {
		return nil, ErrForbidden
	}
	comment := clip(strings.TrimSpace(req.Comment), 200)

	// 被拒的也留痕:外部系统替人放行,没放成的那一次同样值得事后追。
	audit := func(result, what string) {
		line := fmt.Sprintf("open.release.confirm 发布单 %s「%s」确认人 %s · %s", rel.RelNo, rel.Title, account, what)
		if comment != "" {
			line += " · 意见:" + comment
		}
		s.recordAuditFrom(model.AuditSourceRelease, u, apiOperator(cl), conn, line, orDefault(rel.Risk, model.RiskMid), result, rel.RelNo, "approve")
	}

	out := &dto.OpenConfirmResp{RelNo: rel.RelNo}
	st, passed, execWaiting := s.currentManualGate(rel.ID)
	if st == nil {
		if execWaiting {
			audit(model.ResultRejected, "停在执行变更,开放接口不能放行")
			return nil, errors.New("该发布单停在「执行变更」,确认执行须在网关上人工操作,开放接口不能放行")
		}
		audit(model.ResultRejected, "没有在等人工确认的阶段")
		return nil, errors.New("该发布单当前没有在等人工确认的阶段")
	}
	out.Stage = st.Name
	out.ExecutePending = execWaiting
	if passed {
		out.AlreadyConfirmed = true
		out.ReleaseStatus = s.releaseStatusOf(rel.ID)
		return out, nil
	}

	who, uerr := s.Repo.GetUserByEmailFold(account)
	if uerr != nil || who == nil {
		audit(model.ResultRejected, "确认人账号不存在")
		return nil, apimsg.Errorf("确认人账号不存在: %s", account)
	}
	account = who.Email
	if who.Kind == model.UserKindService || (who.Status != "" && who.Status != "active") {
		audit(model.ResultRejected, "确认人账号不可用")
		return nil, apimsg.Errorf("确认人账号 %s 是服务账号或已停用,不能确认", account)
	}
	if !s.mayConfirmGate(who, rel, st) {
		audit(model.ResultRejected, "确认人无权放行这一步")
		return nil, ErrConfirmNotAllowed
	}

	// 放行本身走控制台那条路:同一个原子认领、同一条决策审计(operator = 确认人)、同样续跑。
	if err := s.ContinueManualStage(who, rel.ID, st.ID); err != nil {
		if errors.Is(err, ErrAlreadyDecided) {
			// 和控制台(或另一次调用)抢输了:报已放行,不报错。
			out.AlreadyConfirmed = true
			out.ReleaseStatus = s.releaseStatusOf(rel.ID)
			_, _, out.ExecutePending = s.currentManualGate(rel.ID)
			return out, nil
		}
		audit(model.ResultRejected, err.Error())
		return nil, err
	}
	audit(model.ResultExecuted, "已放行「"+st.Name+"」")
	out.ReleaseStatus = s.releaseStatusOf(rel.ID)
	return out, nil
}

// ErrConfirmNotAllowed:点名的人不能放行这道闸(发起人本人、不在确认角色里)。
// 与「凭据够不到这张单」分开报 —— 两件事要找的人不一样。
var ErrConfirmNotAllowed = errors.New("确认人无权放行这一步(发起人本人,或不在确认角色里)")

// currentManualGate 找发布单此刻停着的人工确认。没有在等的,退到最近一道已经放行过的
// (passed=true)—— 那是"已经有人点过了",调用方该拿到结论(重试是安全的),而不是一句"没有在等"。
// execWaiting 报告发布单是不是正停在执行闸上:那一步开放接口不管,调用方据此提示人去网关点。
func (s *Services) currentManualGate(releaseID int64) (st *model.ReleaseStage, passed, execWaiting bool) {
	stages, err := s.Repo.StagesOfRelease(releaseID)
	if err != nil {
		return nil, false, false
	}
	var last *model.ReleaseStage
	for i := range stages {
		g := &stages[i]
		if isExecuteStage(g.Type) && g.Status == model.RunWaiting {
			execWaiting = true
		}
		if g.Type != model.StageManual {
			continue
		}
		if g.Status == model.RunWaiting {
			return g, false, false
		}
		if g.Status == model.RunSuccess {
			last = g
		}
	}
	return last, last != nil, execWaiting
}
