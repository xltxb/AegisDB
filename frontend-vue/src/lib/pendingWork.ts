// pendingWork — 哪些单据在等一个人去**执行**。
//
// "等人处理"和"等人执行"是两回事,而升级单上它们长得一样:两种都是 status
// 'waiting'。一张停在人工审批节点的升级单在等审批人点头,一张停在执行闸的在等有人
// 把变更真正落进库里。把前者算进"待执行",看板就会催人去做一件他还做不了的事 ——
// 而且那张单子同时还躺在"等我审批"里,同一件事被数了两遍。
//
// 分辨靠的是**停在哪一类节点**,不是那个笼统的 waiting。
//
// 审批工单那一半不在这里:能不能执行由服务端算好(service.CanExecuteApproved),
// 前端照着 canExecute 那一位走,不自己去拼 status / executedAt / 是不是发起人。

import type { Release, ReleaseStage } from '@/types'

/**
 * 需要有人按下按钮才会往下走的节点类型。
 *
 * `execute` 是内置执行闸 —— 按下去变更就落库了。`manual` 是流程里配置的确认点,
 * 同样卡着整条流水线不动。两者共用发布页上的同一个"继续"入口,所以对看板来说是
 * 同一件事:有人得去点它。
 *
 * `approve` 刻意不在其中:它等的是审批人,而不是执行。
 */
const HUMAN_GATES = ['manual', 'execute']

/** 这张升级单此刻停在的人工闸;没停在人工闸上就是 undefined。 */
export function humanGateOf(r: Release): ReleaseStage | undefined {
  return (r.stages || []).find((s) => s.status === 'waiting' && HUMAN_GATES.includes(s.type))
}

/** 这张升级单是不是在等人去执行。 */
export function awaitsExecution(r: Release): boolean {
  return !!humanGateOf(r)
}
