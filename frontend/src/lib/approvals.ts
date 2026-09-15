import type { ApprovalStatus } from '@/api/modules/approvals'

/*
 * 审批两页(/inbox 审批待办 · /approvals 我的申请)共用的常量。
 */

/**
 * 一次审批决定会让哪些查询过期 —— **所有**审批相关的根键,一个不落。
 *
 * 放在 lib 而不是 hooks 里,是为了它能被单独测:hooks/useApprovals.ts 顶上就是
 * axios 单例(`import.meta.env`),在没有 Vite 的单测运行器里 import 不进来。
 *
 * 这份清单是一个整体,不是三行可以各自增删的代码。三颗数字(列表自己、顶栏那颗、
 * 侧栏那颗)口径不同因而不能合并成一个查询(见 api/modules/approvals.ts 的注释),
 * 而 TanStack 的前缀失效**逐个比较数组元素**:`['approvals']` 匹配的是第一个元素
 * 恰好等于 `'approvals'` 的查询 —— `'approvals-inbox-pending'` 是另一个字符串,
 * 不在其中。两个键长得像而匹配规则一点也不像,所以侧栏那颗被漏掉时毫无征兆:
 * 表现只是批完最后一张待办、红点还挂着,直到 60 秒后它自己轮询才消。
 *
 * tests/unit/approvalInvalidation.spec.ts 扫源码守着它:再加一个
 * `queryKey: ['approvals-…']` 而没写进这里,测试当场变红。
 */
export const APPROVAL_QUERY_KEYS = [
  ['approvals'],
  ['approvals-pending'],
  ['approvals-inbox-pending'],
] as const

/**
 * 状态分段的取值 —— 就是后端认的那几个 status(handler.ListApprovals 只放行
 * 这五种,拼错的值它回 400),不另立一套 tab 名。
 *
 * 两页共用一份:审批待办和我的申请筛的是同一批工单、同一组状态,各写一份的
 * 下场是有一天一页能筛「已过期」而另一页不能,而没人说得出为什么。
 *
 * 中间三格的文案直接复用工单状态那套词(apApproved / apRejected / apExpired),
 * 因为它们本来就是同一个东西。
 */
export const APPROVAL_STATUS_TABS: { value: ApprovalStatus; key: string }[] = [
  { value: 'pending', key: 'ibPending' },
  { value: 'approved', key: 'apApproved' },
  { value: 'rejected', key: 'apRejected' },
  { value: 'expired', key: 'apExpired' },
  { value: '', key: 'ibAll' },
]
