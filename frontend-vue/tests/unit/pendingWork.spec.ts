import { test, expect } from '@playwright/test'

import { awaitsExecution, humanGateOf } from '../../src/lib/pendingWork'
import type { Release, ReleaseStage } from '../../src/types'

function stage(type: string, status: string, name = type): ReleaseStage {
  return { type, status, name } as unknown as ReleaseStage
}
function release(...stages: ReleaseStage[]): Release {
  return { status: 'waiting', stages } as unknown as Release
}

// 这是整块功能里唯一容易搞错的判断。一张停在人工审批节点的升级单和一张停在执行闸
// 的,状态都是 'waiting' —— 按状态筛就会把前者也叫成"待执行",催人去做一件他还做不
// 了的事,而且那张单子同时还躺在"等我审批"里,同一件事被数了两遍。
test('停在审批节点的升级单不算待执行', () => {
  const r = release(stage('review', 'success'), stage('approve', 'waiting'), stage('execute', 'pending'))
  expect(awaitsExecution(r)).toBe(false)
})

test('停在执行闸上的才算待执行', () => {
  const r = release(stage('review', 'success'), stage('approve', 'success'), stage('execute', 'waiting', '执行变更'))
  expect(awaitsExecution(r)).toBe(true)
  expect(humanGateOf(r)?.name).toBe('执行变更')
})

// manual 是流程里配置的确认点,同样卡着整条流水线不动,发布页上和执行闸共用同一个
// "继续"入口 —— 对看板来说是同一件事:有人得去点它。
test('停在人工确认点上也算', () => {
  const r = release(stage('manual', 'waiting', '变更窗口确认'))
  expect(awaitsExecution(r)).toBe(true)
  expect(humanGateOf(r)?.name).toBe('变更窗口确认')
})

test('跑完的、跑挂的、还没跑到的都不算', () => {
  expect(awaitsExecution(release(stage('execute', 'success')))).toBe(false)
  expect(awaitsExecution(release(stage('execute', 'failed')))).toBe(false)
  expect(awaitsExecution(release(stage('execute', 'pending')))).toBe(false)
})

// 没有 stages 的升级单不该让这一页崩掉 —— 列表接口带 stages,但历史单或降级返回
// 都可能没有,而看板是登录后第一眼看到的东西。
test('没有 stages 时安静地返回 false', () => {
  expect(awaitsExecution({ status: 'waiting' } as unknown as Release)).toBe(false)
  expect(humanGateOf({ status: 'waiting', stages: [] } as unknown as Release)).toBeUndefined()
})
