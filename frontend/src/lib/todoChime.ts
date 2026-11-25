// 登录提示音该不该响 —— 判断本身。
//
// 拆成纯函数是为了能被钉住。这条规则有一个**很容易在重构里悄悄丢掉**的核心:
//
//   执行权早就放开给"够得到那台实例的人"了(ADR 0010 的 2026-09-09 修订),所以一个人的
//   待办里躺着的 execute,可能是**别人提的单**。那些是"你可以顺手帮着跑",不是"你的东西
//   卡在这儿"。值班的人一登录就被自己没提过的单子叫一通,几天之后这声音就只剩噪音 ——
//   而噪音里那件真正要紧的事,也跟着被忽略了。
//
// 所以:只数**自己发起**的、**等待执行**的那些。别的一律不响。

import type { ApprovalTodoItem } from '@/types'

/** 登录者的身份。只用到这两样,所以不收整个 Me —— 测试里不必造一个完整的用户。 */
export interface ChimeViewer {
  id?: number
  name?: string
}

/**
 * 这一条是不是**我发起的**。
 *
 * 有 initiatorId 就按 id 比;没有才退回比姓名。
 *
 * 为什么留着姓名这条退路:待办项一直带着发起人姓名,initiatorId 是后加的。这个功能整体
 * 在前端,不该因为一个精确性上的小改进就变成"必须先发后端才能用" —— 旧后端下走姓名,
 * 新后端上去之后自动变成精确的那一条。
 *
 * 为什么不干脆只比姓名:同名的两个人会**悄悄**比错,而那种错不报任何东西,表现只是某个人
 * 莫名其妙地听到、或者莫名其妙地听不到。所以 id 在就必须以 id 为准 —— 哪怕姓名也对得上。
 */
export function isMyTodo(it: ApprovalTodoItem, me: ChimeViewer | null | undefined): boolean {
  if (!me) return false
  if (typeof it.initiatorId === 'number' && it.initiatorId > 0) return it.initiatorId === me.id
  return !!me.name && it.initiator === me.name
}

/**
 * 登录之后该为几张单响铃 —— **我自己发起的、等我执行的**那些。
 *
 * 返回 0 就是不响。三类待办里只认 execute:
 *
 *   execute  我提的单被批了,授权是有时效的,而我不点那一下就什么都不会发生 → 响
 *   decide   等我审别人的单。已经有琥珀色徽章、脉冲和标签页计数在提醒了 → 不响
 *   confirm  发布单停在人工闸,同上 → 不响
 */
export function countMyExecuteTodos(
  items: ApprovalTodoItem[] | null | undefined,
  me: ChimeViewer | null | undefined,
): number {
  if (!items?.length || !me) return 0
  return items.filter((it) => it.kind === 'execute' && isMyTodo(it, me)).length
}
