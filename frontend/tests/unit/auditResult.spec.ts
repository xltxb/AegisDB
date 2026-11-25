import { test, expect } from '@playwright/test'
import { auditResult } from '../../src/lib/auditResult'

// 审批通过后,审计里那条"审批"记录的结果存的是 pending(已批准、尚未执行)。它曾被一律
// 显示成"审批中",一张已经批了的单子看起来像没人理过。pending 要按事件类型分开说。
test('同一个 pending:拦截事件是审批中,审批事件是已通过', () => {
  expect(auditResult('pending', 'intercept').key).toBe('rPending')
  expect(auditResult('pending', 'approve').key).toBe('rApproved')
  expect(auditResult('pending', '').key).toBe('rPending') // 0039 之前没有事件类型的旧行
})

test('撤回不是告警,也不是驳回', () => {
  expect(auditResult('cancelled', 'approve')).toEqual({ key: 'rCancelled', tone: 'mute' })
  expect(auditResult('rejected', 'approve').key).toBe('rRejected')
})

test('其余结果照旧', () => {
  expect(auditResult('executed', 'exec').key).toBe('rExecuted')
  expect(auditResult('exported', 'exec').key).toBe('rExported')
  expect(auditResult('simulated', 'exec').key).toBe('rSimulated')
  expect(auditResult('warn', 'exec').key).toBe('rWarn')
})
