package service

// 审批节点的审批人从哪儿来。
//
// 原本只有一个来源:全局默认链(owner 角色全部成员,没有则回落 admin)。于是
// 生产发布与开发发布找的是同一批人,也无法按业务线分派。审批阶段的 config 里
// 加一个 approverRole(角色 code)即可指定,不配则沿用默认链 —— 阶段配置本来
// 就是自由 JSON,不必为此改表。
//
// 用角色 CODE 而不是 id:这份配置是要给人读、给人改的,而且本仓库一贯用 code
// 标识这类东西(tier_code)。角色改名不影响它,角色被删则在保存流程时就拦下。

import (
	"encoding/json"
	"fmt"
	"strings"

	"velagateway/internal/model"
)

// cfgApproverRole reads the approve stage's configured role code ("" = default
// chain). A config that fails to parse yields "" — the same fallback a missing
// key gets, which is the safe direction: the global chain always has someone.
func cfgApproverRole(cfgJSON string) string {
	if strings.TrimSpace(cfgJSON) == "" {
		return ""
	}
	var c struct {
		ApproverRole string `json:"approverRole"`
	}
	if err := json.Unmarshal([]byte(cfgJSON), &c); err != nil {
		return ""
	}
	return strings.TrimSpace(c.ApproverRole)
}

// approvalStepsFor builds the approver chain for one approve stage.
//
// It NEVER returns an empty chain. An approval whose chain has no members is
// un-actionable by anyone (isChainMember returns false for every user), so the
// release would sit in `waiting` until someone noticed — a dead ticket wearing
// the costume of a pending one. Failing the stage says what is wrong while
// somebody is still looking at it.
func (s *Services) approvalStepsFor(roleCode string) ([]model.ApprovalStep, error) {
	if roleCode == "" {
		steps := s.defaultChainSteps()
		if len(steps) == 0 {
			return nil, fmt.Errorf("默认审批链上没有人(DBA 负责人与平台管理员角色都没有成员),无法创建审批单")
		}
		return steps, nil
	}
	role, err := s.Repo.GetRoleByCode(roleCode)
	if err != nil {
		return nil, fmt.Errorf("审批角色 %q 不存在,请修正发布流程的审批阶段配置", roleCode)
	}
	all, merr := s.Repo.MembersOfRole(role.ID)
	if merr != nil {
		return nil, fmt.Errorf("读取审批角色 %s 的成员失败: %w", role.Name, merr)
	}
	members := decidableApprovers(all)
	if len(members) == 0 {
		if len(all) > 0 {
			// 有成员却一个都不能批 —— 说清楚是为什么,否则配置的人会盯着
			// "角色里明明有人"百思不解。
			return nil, fmt.Errorf("审批角色「%s」(%s)里只有服务账号,而服务账号登录不了控制台、无法审批 —— 请加入真人成员,或改用其它审批角色", role.Name, roleCode)
		}
		return nil, fmt.Errorf("审批角色「%s」(%s)没有成员,审批单将无人可批 —— 请先为该角色添加成员,或改用其它审批角色", role.Name, roleCode)
	}
	steps := make([]model.ApprovalStep, 0, len(members))
	for i, m := range members {
		steps = append(steps, model.ApprovalStep{
			StepOrder: i + 1, ApproverID: m.ID, Approver: m.Name, Status: "waiting",
		})
	}
	return steps, nil
}

// decidableApprovers keeps only the members who can actually decide a ticket.
//
// A SERVICE account cannot: it is a machine principal with no console login, so
// a chain step assigned to it can never be acted on. Leaving it in produces a
// ticket that LOOKS staffed — worse than an empty one, because the empty case is
// at least obviously broken. If a role holds nothing but service accounts, it is
// an empty chain as far as approval is concerned.
func decidableApprovers(users []model.User) []model.User {
	out := make([]model.User, 0, len(users))
	for _, u := range users {
		if u.Kind == model.UserKindService {
			continue
		}
		out = append(out, u)
	}
	return out
}

// cfgConfirmRole reads a manual/execute gate's configured role code ("" = keep
// the default rule: any approve-capable member, plus the creator for execute).
func cfgConfirmRole(cfgJSON string) string {
	if strings.TrimSpace(cfgJSON) == "" {
		return ""
	}
	var c struct {
		ConfirmRole string `json:"confirmRole"`
	}
	if err := json.Unmarshal([]byte(cfgJSON), &c); err != nil {
		return ""
	}
	return strings.TrimSpace(c.ConfirmRole)
}

// gateStaffing reports who may pass a human gate routed to roleCode, and refuses
// a role nobody could ever pass it with.
//
// A gate that parks forever is the same dead end as an approval nobody is on the
// chain of: the release sits in `waiting` looking like it is merely early, and
// nothing ever arrives. Say it while someone is still watching.
func (s *Services) gateStaffing(roleCode string) ([]model.User, error) {
	role, err := s.Repo.GetRoleByCode(roleCode)
	if err != nil {
		return nil, fmt.Errorf("确认角色 %q 不存在,请修正发布流程的阶段配置", roleCode)
	}
	all, merr := s.Repo.MembersOfRole(role.ID)
	if merr != nil {
		return nil, fmt.Errorf("读取确认角色 %s 的成员失败: %w", role.Name, merr)
	}
	people := decidableApprovers(all)
	if len(people) == 0 {
		if len(all) > 0 {
			return nil, fmt.Errorf("确认角色「%s」(%s)里只有服务账号,而服务账号登录不了控制台、点不了确认 —— 请加入真人成员,或改用其它角色", role.Name, roleCode)
		}
		return nil, fmt.Errorf("确认角色「%s」(%s)没有成员,这一步将无人可确认 —— 请先为该角色添加成员,或改用其它角色", role.Name, roleCode)
	}
	return people, nil
}

// mayPassGate answers whether u may pass a gate routed to roleCode.
func (s *Services) mayPassGate(u *model.User, roleCode string) bool {
	if u == nil {
		return false
	}
	people, err := s.gateStaffing(roleCode)
	if err != nil {
		return false
	}
	for _, m := range people {
		if m.ID == u.ID {
			return true
		}
	}
	return false
}

// gateWhoLabel renders the staffed names for a stage log, so the release detail
// says who is being waited on instead of just "waiting".
func gateWhoLabel(people []model.User) string {
	names := make([]string, 0, len(people))
	for i, m := range people {
		if i == 5 {
			names = append(names, "…")
			break
		}
		names = append(names, m.Name)
	}
	return strings.Join(names, "、")
}

// validateConfirmRole is the save-time check for manual/execute gates.
func (s *Services) validateConfirmRole(roleCode string) error {
	if roleCode == "" {
		return nil
	}
	if _, err := s.Repo.GetRoleByCode(roleCode); err != nil {
		return fmt.Errorf("确认角色 %q 不存在", roleCode)
	}
	return nil
}

// validateApproverRole is the save-time check: a role that does not exist must
// be refused when the flow is edited, not discovered by the first release that
// happens to run through it.
func (s *Services) validateApproverRole(roleCode string) error {
	if roleCode == "" {
		return nil
	}
	if _, err := s.Repo.GetRoleByCode(roleCode); err != nil {
		return fmt.Errorf("审批角色 %q 不存在", roleCode)
	}
	return nil
}
