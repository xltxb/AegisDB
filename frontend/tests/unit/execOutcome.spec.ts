import { test, expect } from '@playwright/test'

import { classifyExecEnvelope } from '../../src/lib/execOutcome'

// EF2: the backend answers every business outcome with HTTP 200 and a code in
// the envelope, so the REST fallback must read that code. It only special-cased
// "MFA required" and treated everything else as a result to render — and a
// rejection carries no data, so the renderer fell through to its "no output"
// branch and printed a green "✓ executed". A command the capability matrix had
// REFUSED was reported to the operator as having succeeded, while the audit log
// recorded it as rejected. Anything that is not success must classify as a
// failure carrying the server's message.
test('a rejected command is classified as a failure, never as success', () => {
  const out = classifyExecEnvelope({ code: 40300, msg: '能力矩阵禁止:命令被拒绝' })
  expect(out.kind).toBe('failed')
  if (out.kind === 'failed') expect(out.message).toContain('能力矩阵')
})

test('other non-zero codes are failures too', () => {
  for (const code of [40001, 40400, 50000]) {
    expect(classifyExecEnvelope({ code, msg: 'boom' }).kind).toBe('failed')
  }
})

test('an MFA challenge is its own outcome, not a failure', () => {
  expect(classifyExecEnvelope({ code: 42800, msg: 'need mfa' }).kind).toBe('mfa')
})

test('an intercepted command is its own outcome', () => {
  const byCode = classifyExecEnvelope({ code: 42200, data: { approvalNo: 'AP-1', rule: 'r' } })
  expect(byCode.kind).toBe('intercepted')
  // The backend also flags interception inside a success envelope.
  const byFlag = classifyExecEnvelope({ code: 0, data: { intercepted: true, approvalNo: 'AP-2' } })
  expect(byFlag.kind).toBe('intercepted')
})

test('a successful execution carries its result payload', () => {
  const out = classifyExecEnvelope({ code: 0, data: { output: '+ 3 rows', rows: 3 } })
  expect(out.kind).toBe('ok')
  if (out.kind === 'ok') expect(out.data.output).toBe('+ 3 rows')
})

test('a success envelope with no payload is still a success, not a failure', () => {
  // e.g. a DDL that returns nothing — this must stay distinguishable from a
  // rejection, which is exactly the distinction that was lost.
  expect(classifyExecEnvelope({ code: 0, data: {} }).kind).toBe('ok')
})
