package service

// 审批人配置自检 —— 启动时回答一个问题:**这些审批环节,到底还有没有人能批?**
//
// 它存在的理由是一次真实的死锁:owner 角色里只有一个人,而工单都由他发起。
// 每一层校验都通过了(角色有成员、审批链建得出来、工单创建成功),然后两人控制
// 让它们一张都批不掉 —— 单子静静地堆在待办里,没有任何一处报错。
//
// 已有的校验都是**事到临头**才说话:保存流程时校验角色存在,创建工单时校验链非空。
// 它们答的是"这一步能不能往下走",而不是"往下走之后还有没有人接得住"。中间那段
// 差额,就是这份自检要填的:
//
//   - 链上一个能批的人都没有  → 工单根本建不出来(blocked)
//   - 链上恰好一个人、且未开自审批 → 他自己发起的工单永远没人能批(deadlock)
//   - 已经卡在待办里的存量工单     → 现在就已经没人能处理了
//
// 它只**报告**,不改配置也不阻止启动。审批人是谁是组织的决定,一个自检程序没有
// 资格替人拿主意;它该做的是在人还看得见的时候把话说清楚。

import (
	"fmt"
	"log/slog"
	"strings"

	"velagateway/internal/model"
)

// 自检结论的严重度。分三档而不是一个布尔,是因为它们要人做的事不同:
// blocked 是现在就断的,deadlock 是等有人提单才断的,warn 只是值得看一眼。
const (
	StaffingBlocked  = "blocked"  // 现在就没人能批 —— 工单建不出来
	StaffingDeadlock = "deadlock" // 建得出来,但只要是本人发起的就批不掉
	StaffingWarn     = "warn"     // 值得看一眼
)

// StaffingIssue 是一条隐患。Fix 是必填的:一条只说"有问题"却不说怎么办的告警,
// 读它的人只能去翻代码。
type StaffingIssue struct {
	Severity string
	Where    string // 哪个环节,如 "默认审批链" / "流程「标准发布」的审批阶段"
	Problem  string
	Fix      string
}

// chainStaffingIssue judges ONE approval chain: given everyone assigned to it and
// whether self-approval is on, can anybody actually decide a ticket here?
//
// all 是角色的**全部**成员(含服务账号),不是过滤后的 —— 因为"角色里有两个人却
// 只有一个能批"正是最容易看走眼的一种,自检必须能说出这句话。
func chainStaffingIssue(where string, all []model.User, selfApprove bool) *StaffingIssue {
	people := decidableApprovers(all)
	switch {
	case len(people) == 0 && len(all) > 0:
		return &StaffingIssue{
			Severity: StaffingBlocked, Where: where,
			Problem: fmt.Sprintf("有 %d 个成员,但全部是服务账号 —— 服务账号登录不了控制台,无法审批", len(all)),
			Fix:     "为该角色加入真人成员",
		}
	case len(people) == 0:
		return &StaffingIssue{
			Severity: StaffingBlocked, Where: where,
			Problem: "没有成员,审批单将无人可批(创建工单时会直接失败)",
			Fix:     "为该角色添加成员",
		}
	case len(people) == 1 && !selfApprove:
		who := people[0].Name
		extra := ""
		if len(all) > len(people) {
			// 成员列表里看起来不止一个人,要说破多出来的那些为什么不算数。
			extra = fmt.Sprintf("(成员共 %d 个,其余是服务账号,不能审批)", len(all))
		}
		return &StaffingIssue{
			Severity: StaffingDeadlock, Where: where,
			Problem: fmt.Sprintf("链上只有 %s 一个人能批%s。两人控制下,由他本人发起的工单将没有任何人可以审批 —— 单子会停在待办里,不报错也不推进", who, extra),
			Fix:     "为该角色再加入一名真人成员;若确为单人运维,可在「设置」中打开「允许自审批」",
		}
	}
	return nil
}

// ApprovalStaffingReport 把当前配置下所有"将来会没人能批"的地方找出来。
//
// 顺序是有意的:先默认链(影响面最大),再各流程的审批/确认角色,最后是已经卡住的
// 存量工单 —— 从"以后会出事"到"现在已经出事"。
func (s *Services) ApprovalStaffingReport() []StaffingIssue {
	selfApprove := s.settingBool("approval.allowSelfApprove", false)
	out := []StaffingIssue{}

	// ---- 默认审批链:owner,没有可批的人则回落 admin ----
	if issue := s.roleChainIssue(defaultChainWhere(), "owner", selfApprove); issue != nil {
		// owner 不可用时系统会回落到 admin,所以要看回落之后到底行不行。
		if fb := s.roleChainIssue("默认审批链(回落到平台管理员)", "admin", selfApprove); fb != nil {
			out = append(out, *fb)
		} else if issue.Severity == StaffingDeadlock {
			// owner 只有一个人,但 admin 那边是健康的 —— 回落救不了这种情况:
			// approverPool 只在 owner **没有可批的人**时才回落。
			out = append(out, *issue)
		}
	}

	// ---- 流程里显式指定的审批角色 / 确认角色 ----
	out = append(out, s.pipelineRoleIssues(selfApprove)...)

	// ---- 已经卡在待办里的存量工单 ----
	if issue := s.stuckPendingIssue(); issue != nil {
		out = append(out, *issue)
	}
	return out
}

func defaultChainWhere() string { return "默认审批链(DBA 负责人)" }

// roleChainIssue 读一个角色的成员并判定。角色不存在按"没有成员"处理 —— 对使用者
// 来说结果是一样的:这条链上没有人。
func (s *Services) roleChainIssue(where, roleCode string, selfApprove bool) *StaffingIssue {
	role, err := s.Repo.GetRoleByCode(roleCode)
	if err != nil {
		return &StaffingIssue{
			Severity: StaffingBlocked, Where: where,
			Problem: fmt.Sprintf("角色 %q 不存在", roleCode),
			Fix:     "检查角色配置,或修正引用它的发布流程",
		}
	}
	all, _ := s.Repo.MembersOfRole(role.ID)
	return chainStaffingIssue(where, all, selfApprove)
}

// pipelineRoleIssues 检查各发布流程里显式指定的审批角色与确认角色。
//
// 只看**启用中**的流程:停用的流程跑不起来,为它告警只会稀释真正要紧的那几条。
// 同一个角色被多个流程引用时只报一次 —— 重复十遍不会让人更快去修。
func (s *Services) pipelineRoleIssues(selfApprove bool) []StaffingIssue {
	pipelines, err := s.Repo.ListPipelines()
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	out := []StaffingIssue{}
	for _, p := range pipelines {
		if !p.Enabled {
			continue
		}
		stages, _ := s.Repo.StagesOfPipeline(p.ID)
		for _, st := range stages {
			var roleCode, kind string
			switch st.Type {
			case model.StageApprove:
				roleCode, kind = cfgApproverRole(st.Config), "审批"
			case model.StageManual, model.StageExecute:
				roleCode, kind = cfgConfirmRole(st.Config), "确认"
			}
			if roleCode == "" || seen[kind+":"+roleCode] {
				continue // 未指定 = 沿用默认链,上面已经查过
			}
			seen[kind+":"+roleCode] = true
			where := fmt.Sprintf("流程「%s」的%s角色 %s", p.Name, kind, roleCode)
			// 确认节点不受两人控制约束(它不是审批),只要有人就行。
			skipSelf := kind == "确认"
			if issue := s.roleChainIssue(where, roleCode, selfApprove || skipSelf); issue != nil {
				out = append(out, *issue)
			}
		}
	}
	return out
}

// stuckPendingIssue counts pending tickets that NOBODY can decide right now.
//
// 这一条和上面几条不同:它不是预测,是现状。启动自检只看得到启动那一刻的快照 ——
// 之后堆积起来的卡单它不会知道,所以这条只是"顺手看一眼",不能当成监控。
func (s *Services) stuckPendingIssue() *StaffingIssue {
	aps, err := s.Repo.PendingApprovals()
	if err != nil {
		return nil
	}
	stuck := 0
	for i := range aps {
		ap := aps[i]
		if !s.anyoneCanDecide(&ap) {
			stuck++
		}
	}
	if stuck == 0 {
		return nil
	}
	return &StaffingIssue{
		Severity: StaffingDeadlock, Where: "待办中的存量工单",
		Problem: fmt.Sprintf("有 %d 张待审批工单当前没有任何人可以处理", stuck),
		Fix:     "补齐审批链成员后,这些工单即可正常审批;或在「设置」中打开「允许自审批」",
	}
}

// anyoneCanDecide asks whether ANY chain member could decide this ticket now.
func (s *Services) anyoneCanDecide(ap *model.Approval) bool {
	steps, err := s.Repo.StepsOf(ap.ID)
	if err != nil {
		return true // 读不出来就不下结论 —— 自检不该因为一次查询失败而制造假警报
	}
	for _, st := range steps {
		u, err := s.Repo.GetUserByID(st.ApproverID)
		if err != nil || u == nil {
			continue
		}
		if s.DecideBlockFor(u, ap) == BlockNone {
			return true
		}
	}
	return false
}

// LogApprovalStaffing 在启动时把自检结果写进日志。
//
// 用 Warn 而不是 Error:这些都不是启动失败,网关照常服务;但它们也不是 Info ——
// 一条淹没在启动刷屏里的提示,和没有这条提示没什么区别。
func (s *Services) LogApprovalStaffing() {
	issues := s.ApprovalStaffingReport()
	if len(issues) == 0 {
		slog.Info("审批人自检通过:每个审批环节都至少有两名可审批的真人,或已开启自审批")
		return
	}
	for _, is := range issues {
		slog.Warn("审批人自检: "+is.Where,
			"severity", is.Severity, "problem", is.Problem, "fix", is.Fix)
	}
	slog.Warn(fmt.Sprintf("审批人自检发现 %d 处隐患 —— 命中的审批环节会让工单停在待办里,既不报错也不推进", len(issues)),
		"severities", strings.Join(severityList(issues), ","))
}

func severityList(issues []StaffingIssue) []string {
	out := make([]string, 0, len(issues))
	for _, is := range issues {
		out = append(out, is.Severity)
	}
	return out
}
