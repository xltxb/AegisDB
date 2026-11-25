import { test, expect } from '@playwright/test'
import { releaseExecutor } from '@/lib/releaseExecutor'

type S = { type: 'review' | 'approve' | 'execute' | 'auto_execute' | 'manual'; status: string; confirmedBy?: string }
const run = (stages: S[]) => releaseExecutor(stages as never)

test.describe('releaseExecutor', () => {
  test('执行阶段的确认人就是执行人,不看其它阶段', () => {
    expect(run([
      { type: 'manual', status: 'success', confirmedBy: '' },
      { type: 'execute', status: 'success', confirmedBy: 'Chen Hao' },
    ])).toEqual({ names: ['Chen Hao'], auto: false, waiting: false })
  })

  test('自动执行跑过且没人点 → 自动执行;没跑到不算', () => {
    expect(run([{ type: 'auto_execute', status: 'success' }]).auto).toBe(true)
    expect(run([{ type: 'auto_execute', status: 'pending' }]).auto).toBe(false)
    expect(run([{ type: 'auto_execute', status: 'skipped' }]).auto).toBe(false)
  })

  test('自动执行退回等人确认后被点 → 记人', () => {
    expect(run([{ type: 'auto_execute', status: 'success', confirmedBy: 'Lin Wei' }]))
      .toEqual({ names: ['Lin Wei'], auto: false, waiting: false })
  })

  test('停在执行闸 → waiting;多个执行阶段同一人去重', () => {
    expect(run([{ type: 'execute', status: 'waiting' }]).waiting).toBe(true)
    expect(run([
      { type: 'execute', status: 'success', confirmedBy: 'A' },
      { type: 'execute', status: 'success', confirmedBy: 'A' },
      { type: 'execute', status: 'success', confirmedBy: 'B' },
    ]).names).toEqual(['A', 'B'])
  })

  test('没有执行阶段或老数据无 stages → 空', () => {
    expect(run([{ type: 'review', status: 'success' }])).toEqual({ names: [], auto: false, waiting: false })
    expect(releaseExecutor(undefined)).toEqual({ names: [], auto: false, waiting: false })
  })
})
