import { http, ok, type Envelope } from '../shared'
import type {
  Approval, ExecWindow,
} from '@/types'

export const approvalsApi = {
  // ---- approvals ----
  // Paged listing {items,total,page,pageSize}.
  // status 按状态筛,空串是不筛。总览的"待执行"必须靠它:通过了的工单不会过期,
  // 一张等着执行的单子可以停很久,早就被新工单挤出了任何一页 —— 取一页回来自己筛
  // 会漏报,而那张卡片漏报等于没有。
  // q 是控制台的搜索框(单号/实例/库/命令/发起人),和 status 一样在**服务端**筛:
  // 列表是分页的,只搜当前页的搜索框会对一张躺在第三页的工单回答"没有"。
  approvals: (scope: 'mine' | 'all', page = 1, pageSize = 50, status = '', q = '') =>
    http
      .get<any, Envelope<{ items: Approval[]; total: number; pending: number }>>(
        `/approvals?scope=${scope}&page=${page}&pageSize=${pageSize}`
        + `&status=${encodeURIComponent(status)}&q=${encodeURIComponent(q)}`,
      )
      .then(ok),
  // Look one ticket up by number, independent of paging — the audit log links
  // tickets that may sit on any page.
  approvalByNo: (apNo: string) =>
    http
      .get<any, Envelope<{ items: Approval[]; total: number }>>(`/approvals?ap=${encodeURIComponent(apNo)}`)
      .then(ok)
      .then((r) => r.items[0] || null),
  // 返回信封而不是 .then(ok):调用方要拿 msg 原样显示给人看 —— 服务端拒绝的
  // 理由(不能自审 / 不在审批链 / 已被处理)各自要人做的事完全不同。
  approve: (id: number) => http.post<any, Envelope<any>>(`/approvals/${id}/approve`).then(ok),
  reject: (id: number) => http.post<any, Envelope<any>>(`/approvals/${id}/reject`).then(ok),
  // 撤回:发起人自己的动作,和 approve/reject 是两码事(见后端 approval_cancel.go)。
  // 走 ok():服务端的拒绝理由会被抛成 Error(msg),原样进 toast —— 而那三种理由
  // 要人去做的事完全不同。
  cancelApproval: (id: number) => http.post<any, Envelope<any>>(`/approvals/${id}/cancel`).then(ok),
  // 执行是发起人的动作,不是审批的副作用 —— 所以它是独立的一个调用。
  executeApproval: (id: number) => http.post<any, Envelope<any>>(`/approvals/${id}/execute`).then(ok),

  // ---- 执行窗口(「班车」) ----
  execWindows: () => http.get<any, Envelope<ExecWindow[]>>('/exec-windows').then(ok),
  createExecWindow: (body: Partial<ExecWindow>) =>
    http.post<any, Envelope<ExecWindow>>('/exec-windows', body),
  updateExecWindow: (id: number, body: Partial<ExecWindow>) =>
    http.put<any, Envelope<ExecWindow>>(`/exec-windows/${id}`, body),
  deleteExecWindow: (id: number) => http.delete<any, Envelope<any>>(`/exec-windows/${id}`),
}
