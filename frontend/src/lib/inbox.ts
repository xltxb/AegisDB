import type { Approval, ApprovalStep } from '@/types'

/**
 * 审批待办里那几条判断规则。
 *
 * 抽出来不是为了复用(只有一页在用),是为了它们能被单独钉住:一张单该不该进
 * 批量、此刻卡在谁手里,这些是会被人据以签字的结论,不该埋在 JSX 的过滤表达式里。
 */

/**
 * 审批链上一级的状态。取值来自后端,**不是工单本身的那一套**:
 *
 *   waiting  —— 排在后面,还没轮到
 *   active   —— 此刻正等这个人签(建链时第一级就置成它,见 service/gateway.go)
 *   approved / rejected —— 已经签过
 *
 * 里面没有 `pending`。工单的状态才叫 pending,两套词长得像而含义不同 —— 把步骤
 * 按工单那一套去认,结果是「当前等待」永远显示破折号,而链上每一级都印着未翻译
 * 的 `active`。
 */
export type StepStatus = 'waiting' | 'active' | 'approved' | 'rejected'

/** 这一级签过了没有。approved / rejected 都算签过,其余都还没。 */
export function stepDone(status: string): boolean {
  return status === 'approved' || status === 'rejected'
}

/**
 * 这张单此刻在等谁签。
 *
 * 先找 `active` —— 那是后端明确标出来的"正在等这个人"。找不到才退到第一个
 * `waiting`(链已建好但还没激活的那一瞬)。两个都没有,说明没人在等:全签完了,
 * 或者这张单本来就没有链。这时返回空串,编一个名字比留白更容易误导。
 */
export function waitingOn(steps: ApprovalStep[] | undefined): string {
  if (!steps?.length) return ''
  const ordered = [...steps].sort((a, b) => a.stepOrder - b.stepOrder)
  const cur = ordered.find((s) => s.status === 'active') ??
    ordered.find((s) => s.status === 'waiting')
  return cur?.approver ?? ''
}

/**
 * 能被「批量通过」带走的那些。
 *
 * 三个条件缺一不可:
 *  - 还在待审 —— 已经处理过的单子再发一次只会换来「该工单已被处理」;
 *  - 服务端说可以决定(canDecide)—— 自审、不在链上这两种都由后端判,前端不重算;
 *  - 不是高危 —— 批量的本意是清掉一串无须逐条细看的单子,一个 DROP 不该靠这种
 *    方式被放行。后端并不禁止批量批高危,这一条是产品自己划的线。
 */
export function batchableOf(rows: Approval[]): Approval[] {
  return rows.filter((a) => a.status === 'pending' && !!a.canDecide && a.riskLevel !== 'high')
}

/**
 * 手上这一页里有几张待审的高危单。
 *
 * 数的是**这一页**,不是全部:分页之外的行不在手上,把它说成总数就是编。
 */
export function highPendingCount(rows: Approval[]): number {
  return rows.filter((a) => a.status === 'pending' && a.riskLevel === 'high').length
}

/** 名字的首字。取一个字符而不是首字母缩写 —— 中文名没有首字母。 */
export function initialOf(name: string): string {
  return name ? (Array.from(name)[0] as string) : '?'
}

/**
 * 命令里关键字的位置,给高亮用。命中不了(关键字为空、或没出现在命令里)返回 -1,
 * 调用方原样显示整条命令。大小写不敏感:后端存的关键字是规范化过的大写。
 */
export function keywordAt(command: string, keyword: string): number {
  if (!keyword) return -1
  return command.toUpperCase().indexOf(keyword.toUpperCase())
}

/**
 * 列表里那一列时间。
 *
 * 显示的是**服务端发来的墙上时钟**,不做时区换算 —— 后端给的是带偏移的 RFC3339
 * (`2026-09-15T23:26:21.525018+08:00`),这里按字面切,与 export / scripts /
 * asyncJobs 三页同一套规矩。换成 `new Date(...)` 本地化不是不行,但那要四个页面
 * 一起改,否则同一条记录在不同页面显示不同的时刻。
 *
 * 跨年才补年份。原来的写法(`s.slice(5, 16)`)一律切掉年份,于是「全部」里一张
 * 2025-09-15 的单和一张 2026-09-15 的单长得一模一样 —— 审批记录是要被追溯的
 * 东西,差一年不是小事。而给每一行都挂上「2026-」又会把真正要看的月日挤窄,
 * 所以只在不是今年时才写出来。
 *
 * `now` 是参数而不是在函数里读 `new Date()`:一个读当前时间的纯函数没法被钉住,
 * 它的测试会在明年自己变色。
 */
export function formatWhen(iso: string | null | undefined, now: Date): string {
  if (!iso) return '—'
  const sameYear = iso.slice(0, 4) === String(now.getFullYear())
  return iso.slice(sameYear ? 5 : 0, 16).replace('T', ' ')
}
