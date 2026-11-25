// execOutcome — decide what a /terminal/exec response actually means.
//
// The API answers every business outcome with HTTP 200 and a code in the
// envelope, so the code is the only thing distinguishing "executed" from
// "refused". The terminal's REST fallback used to special-case just the MFA
// challenge and treat everything else as a result to render; because a rejection
// carries no data, the renderer fell through to its "nothing to print" branch
// and reported a green "✓ executed" for a command the capability matrix had
// REFUSED — while the audit log recorded it as rejected (EF2).
//
// Keeping this as a pure function (rather than branching inline in the terminal
// component) is what makes the distinction testable.

import { CODE_OK, CODE_INTERCEPTED, CODE_MFA_REQUIRED } from '@/api/codes'
import type { RuleRef } from '@/types'
import type { OSCItem } from './oscText'

/** 终端命令被改成在线变更工单时的说明(生产大表加 / 删索引)。 */
export interface OSCInfo {
  jobId: number
  table: string
  rows: number
  bytes: number
  warnings?: OSCItem[]
}

export interface ExecPayload {
  /** 要特别留意的一句话(生产大表直接 DDL,可能长时间锁表) */
  notice?: string
  noticeRef?: RuleRef
  /** 在线变更前置检查的阻塞项(没生成工单) */
  oscBlockers?: OSCItem[]
  osc?: OSCInfo
  output?: string
  rows?: number
  ms?: number
  /** 耗时分段(毫秒):取连接 / 库端执行 / 读取结果 */
  connMs?: number
  queryMs?: number
  fetchMs?: number
  processMs?: number
  columns?: string[]
  data?: string[][]
  truncated?: boolean
  /** 分批显示:还有后续行时的结果编号、读到的总行数、是否超过网关一次读取的上限 */
  resultId?: string
  totalRows?: number
  beyondCap?: boolean
  outputRef?: RuleRef
  // DBMS_OUTPUT(终端开了 SET SERVEROUTPUT ON,Oracle)
  serverOutput?: string[]
  serverOutputTruncated?: boolean
  // 绑定变量的新值(SQL*Plus 的 VARIABLE,Oracle)
  binds?: { name: string; type: string; value: string; null: boolean }[]
  intercepted?: boolean
  approvalNo?: string
  rule?: string
  ruleRef?: RuleRef
}

export interface ExecEnvelope {
  code: number
  msg?: string
  data?: ExecPayload
}

export type ExecOutcome =
  | { kind: 'mfa' }
  | { kind: 'intercepted'; approvalNo?: string; rule?: string; ruleRef?: RuleRef; osc?: OSCInfo; notice?: string; noticeRef?: RuleRef }
  | { kind: 'ok'; data: ExecPayload }
  | { kind: 'failed'; message: string }

export function classifyExecEnvelope(env: ExecEnvelope): ExecOutcome {
  if (env.code === CODE_MFA_REQUIRED) return { kind: 'mfa' }
  // Interception is signalled either by its own code or by a flag inside an
  // otherwise-successful envelope, depending on the entry point.
  if (env.code === CODE_INTERCEPTED || env.data?.intercepted) {
    return {
      kind: 'intercepted', approvalNo: env.data?.approvalNo,
      rule: env.data?.rule, ruleRef: env.data?.ruleRef,
      osc: env.data?.osc, notice: env.data?.notice, noticeRef: env.data?.noticeRef,
    }
  }
  // Anything else non-zero is a refusal or an error — never a result. An empty
  // payload on a SUCCESS code stays a success (a DDL returns nothing), which is
  // precisely the distinction that was previously collapsed.
  if (env.code !== CODE_OK) return { kind: 'failed', message: env.msg || '' }
  return { kind: 'ok', data: env.data || {} }
}
