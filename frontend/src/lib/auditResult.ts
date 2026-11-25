// auditResult —— 审计行的"结果"怎么说。审计页、审计详情里的操作记录、总览的最近审计共用这一份。
//
// 同一个 `pending` 在两种事件上是两个意思,只看结果值就会说错:
//
//   - 拦截(intercept)+ pending:命令被拦下、提了审批单,**等人审批** → 审批中
//   - 审批(approve)  + pending:审批人**已经通过**(或发布单的人工确认已放行),命令
//     还没跑 —— 执行由发起人 / 流水线另行完成,那一刻自己写一条 exec
//
// 后者曾被一律显示成"审批中",于是一张已经批了的单子,在审计里看起来像没人理过。
// 这里按事件类型分开说,不改存下的值:审计链的哈希覆盖 result,旧行也不该被改写。

export type AuditTone = 'ok' | 'pass' | 'wait' | 'bad' | 'info' | 'mute' | 'warn'

export interface AuditResultView {
  /** 文案键 */
  key: string
  tone: AuditTone
}

export function auditResult(result: string, eventType?: string): AuditResultView {
  switch (result) {
    case 'executed': return { key: 'rExecuted', tone: 'ok' }
    case 'pending':
      return eventType === 'approve' ? { key: 'rApproved', tone: 'pass' } : { key: 'rPending', tone: 'wait' }
    case 'rejected': return { key: 'rRejected', tone: 'bad' }
    // 发起人撤回:没有人拒绝过它,也没有东西出错 —— 不是驳回,更不是告警。
    case 'cancelled': return { key: 'rCancelled', tone: 'mute' }
    case 'exported': return { key: 'rExported', tone: 'info' }
    case 'simulated': return { key: 'rSimulated', tone: 'mute' }
    default: return { key: 'rWarn', tone: 'warn' }
  }
}
