import type { ReleaseStage } from '@/types'

// 一张发布单「谁执行的变更」。
//
// 后端在执行阶段(execute / auto_execute)被人点「确认执行」时把点击的人写进
// stage.confirmedBy(见 migrations/0024)—— 那一下才是真正让变更落库的人,
// 审批人只回答"可不可以做"。自动执行阶段在允许自动执行的分层上不经人手,
// confirmedBy 为空且阶段已经跑过,界面要写"自动执行",不能写成"—"冒充没执行。
export interface ReleaseExecutor {
  /** 点过「确认执行」的人,按阶段顺序去重。 */
  names: string[]
  /** 有自动执行阶段没经人手就跑了。 */
  auto: boolean
  /** 有执行阶段正停在等人确认。 */
  waiting: boolean
}

const EXEC_TYPES = new Set(['execute', 'auto_execute'])
const RAN = new Set(['running', 'success', 'failed'])

export function releaseExecutor(stages: Pick<ReleaseStage, 'type' | 'status' | 'confirmedBy'>[] | undefined): ReleaseExecutor {
  const out: ReleaseExecutor = { names: [], auto: false, waiting: false }
  for (const s of stages ?? []) {
    if (!EXEC_TYPES.has(s.type)) continue
    const who = (s.confirmedBy ?? '').trim()
    if (who) {
      if (!out.names.includes(who)) out.names.push(who)
    } else if (s.type === 'auto_execute' && RAN.has(s.status)) {
      out.auto = true
    } else if (s.status === 'waiting') {
      out.waiting = true
    }
  }
  return out
}
