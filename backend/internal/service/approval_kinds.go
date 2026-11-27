package service

import (
	"aegisdb/internal/apimsg"
	"aegisdb/internal/model"
)

// approvalFlow 是一类工单批准前后**各不相同**的那几件事。原先它们是 finalizeApproval 里三条、
// canExecuteApproved 里一条按外键排的 if 链,新增一种类型要逐条补(#182)。现在按类型查这张表,
// 表的完整性由测试钉住:每种类型都必须有一行。
//
// 共同的部分(原子落库、审计、发通知)仍在 finalizeApproval,不进表。
type approvalFlow struct {
	// precheck 在批准落库之前跑;返回错误就不让批(审批人看到原因)。可空。
	precheck func(s *Services, ap *model.Approval) error
	// output / outputRef 是批准时写进结果列的那句话。
	output    string
	outputRef string
	// title / body 是给发起人的通知。body 拿到审批人和截短的命令(描述)。
	title func() apimsg.M
	body  func(actor, cmd string) apimsg.M
	// after 在批准落库之后跑:让窗口生效、导出入队、生成授权。可空。
	after func(s *Services, ap *model.Approval, d decision)
	// noManualExec 不为空表示这类工单不能在审批页手动执行,返回的是原因 —— 它们的 Command
	// 是一句描述,放它走执行那条路,网关会把那句话当 SQL 发给数据库。
	noManualExec func() error
}

// awaitingExecution 是命令单(以及在线变更单)批准后的样子:通过**不再顺带执行**。命令在审批人
// 点下去的那一刻跑,等于让审批人替发起人选择了执行时机 —— 而业务低峰、应用是否已停、备份是否
// 就绪,只有发起人知道。审批人按下的是"我同意",不是"现在就跑"。工单停在这里等发起人来执行
// (ExecuteApproved),审计行同样记 pending。
var awaitingExecution = approvalFlow{
	output:    model.AwaitingExecution,
	outputRef: model.OutApAwaitingExec,
	title:     func() apimsg.M { return apimsg.T("审批已通过,请前往执行") },
	// 话要说清"还需要你去执行" —— 一句"已通过"会让人以为事情办完了。
	body: func(actor, cmd string) apimsg.M {
		return apimsg.Sprintf("%s 通过了你的命令：%s\n请到「审批」页找到这张工单并执行。", actor, cmd)
	},
}

var approvalFlows = map[string]approvalFlow{
	model.ApTypeCommand: awaitingExecution,
	// 在线变更单的"执行"= 启动那次迁移(executeApprovedOSC),同样等发起人来点。
	model.ApTypeOSC: awaitingExecution,

	// A release ticket authorises the pipeline; it does not run anything. The
	// execute stage owns execution (and re-judges the statement before applying
	// it), so running it here as well would apply the same change twice.
	model.ApTypeRelease: {
		output: "· 已批准,由发布流水线继续执行", outputRef: model.OutApPipeline,
		title: func() apimsg.M { return apimsg.T("审批已通过,发布流水线继续") },
		body: func(actor, cmd string) apimsg.M {
			return apimsg.Sprintf("%s 通过了发布单里的这一步：%s\n流水线将继续执行,无需手动操作。", actor, cmd)
		},
		noManualExec: func() error {
			return apimsg.Errorf("这是升级单的审批工单,执行由发布流水线的执行阶段完成,不能在这里执行")
		},
	},

	// 窗口单授权的是"让那扇门在它自己的时间表内生效",不是"执行一条命令"。批准即生效。
	model.ApTypeWindow: {
		output: "· 已批准,执行窗口已生效", outputRef: model.OutApWindowLive,
		title: func() apimsg.M { return apimsg.T("执行窗口审批已通过") },
		body: func(actor, cmd string) apimsg.M {
			return apimsg.Sprintf("%s 通过了你的执行窗口申请：%s\n窗口已生效,到点自动开启、到点自动关闭。", actor, cmd)
		},
		after: func(s *Services, ap *model.Approval, _ decision) { s.auditWindowDecision(ap, true) },
		noManualExec: func() error {
			return apimsg.Errorf("这是执行窗口的申请单,批准即生效,没有需要手动执行的命令")
		},
	},

	// 导出单授权的是"让导出 worker 去跑这次导出",批准即入队。
	model.ApTypeExport: {
		output: "· 已批准,导出任务已进入队列", outputRef: model.OutApExportQueued,
		title: func() apimsg.M { return apimsg.T("导出审批已通过,任务已开始") },
		body: func(actor, cmd string) apimsg.M {
			return apimsg.Sprintf("%s 通过了你的导出申请：%s\n任务已进入队列,完成后会再通知你。", actor, cmd)
		},
		after: func(s *Services, ap *model.Approval, _ decision) { s.releaseApprovedExport(ap) },
		noManualExec: func() error {
			return apimsg.Errorf("这是导出申请的审批工单,批准后由导出任务后台执行,不能在这里执行")
		},
	},

	// 权限申请批的是"生成那条查询授权"。批准之前按此刻的设置重新校验:校验不过就不让批,
	// 不能出现"批准了、却没有授权"的单子。
	model.ApTypeGrant: {
		precheck: (*Services).precheckGrantApproval,
		output:   "· 已批准,查询授权已生效", outputRef: model.OutApGrantLive,
		title: func() apimsg.M { return apimsg.T("数据查询权限申请已通过") },
		body: func(actor, cmd string) apimsg.M {
			return apimsg.Sprintf("%s 通过了你的数据查询权限申请：%s\n授权已生效,可以到「数据查询」页查询了。", actor, cmd)
		},
		after: func(s *Services, ap *model.Approval, d decision) { s.grantApprovedRequest(ap, d.ActorName) },
		noManualExec: func() error {
			return apimsg.Errorf("这是数据查询权限的申请单,批准即生效,没有需要手动执行的命令")
		},
	},
}

// flowOf returns the flow for ap's type. 表的完整性由测试保证;万一缺了,按命令单处理 ——
// 那是最保守的一种:停下来等人,不替任何人做任何事。
func flowOf(ap *model.Approval) approvalFlow {
	if f, ok := approvalFlows[model.ApprovalType(ap)]; ok {
		return f
	}
	return awaitingExecution
}
