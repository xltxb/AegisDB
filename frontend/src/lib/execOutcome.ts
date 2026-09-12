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
import type { ExecResp, RuleRef } from '@/types'

/**
 * 信封里那一坨 —— 就是 ExecResp,只是每一项都可能缺席。
 *
 * 原先这里另起了一份字段清单,于是同一个契约有了两份类型:后端加一个字段,两边
 * 各改各的,而漏改的那一边不会报错,只会悄悄把数据丢掉。派生出来就不会再分叉。
 */
export type ExecPayload = Partial<ExecResp>

export interface ExecEnvelope {
  code: number
  msg?: string
  data?: ExecPayload
}

export type ExecOutcome =
  | { kind: 'mfa' }
  | { kind: 'intercepted'; approvalNo?: string; rule?: string; ruleRef?: RuleRef }
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
    }
  }
  // Anything else non-zero is a refusal or an error — never a result. An empty
  // payload on a SUCCESS code stays a success (a DDL returns nothing), which is
  // precisely the distinction that was previously collapsed.
  if (env.code !== CODE_OK) return { kind: 'failed', message: env.msg || '' }
  return { kind: 'ok', data: env.data || {} }
}
