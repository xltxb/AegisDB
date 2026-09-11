import { test, expect } from '@playwright/test'

import {
  batchableOf, highPendingCount, initialOf, keywordAt, stepDone, waitingOn,
} from '../../src/lib/inbox'
import type { Approval, ApprovalStep } from '../../src/types'

// 审批待办(原型 v3)里的四条判断。它们决定的是**人据以签字的东西**:哪几张单
// 会被一次批量通过带走、一张单此刻在等谁。所以每一条都单独钉住。
//
// 有一条边界值得先说明白,因为它是这一组里最容易被"优化"掉的:
// `batchableOf` 读的是服务端算好的 `canDecide`,**不自己去拼**"是不是我发起的"
// 或者"我在不在链上"。后端那两条规则带开关(approval.allowSelfApprove),前端
// 重算一遍的下场是:管理员开了自审批之后,界面仍然把自己的单子排除在批量之外,
// 而后端明明会放行 —— 两份判断不一致,表现为一个说不出理由的漏网。

// 步骤的状态取值是 waiting / active / approved / rejected —— **不是** 工单那套
// pending / approved / …。这组用例第一版正是照工单那套写的,跑起来「当前等待」
// 一律是破折号,链上每一级印着未翻译的 `active`。默认值特意写成 waiting,好让
// 任何一次照工单词汇重写都在这里当场变红。
const step = (o: Partial<ApprovalStep> & { stepOrder: number }): ApprovalStep => ({
  id: o.id ?? o.stepOrder,
  stepOrder: o.stepOrder,
  approverId: o.approverId ?? 0,
  approver: o.approver ?? '',
  status: o.status ?? 'waiting',
})

const ap = (o: Partial<Approval>): Approval => ({
  id: 1, apNo: 'AP-1', connectionId: 1, env: 'prod', instance: 'i', database: 'd',
  command: 'SELECT 1', keyword: '', initiatorId: 9, initiator: '林伟', reason: '',
  riskLevel: 'mid', status: 'pending', auditId: '', result: '', resultRows: 0,
  decidedAt: null, createdAt: '2026-09-12T10:00:00Z', steps: [],
  ...o,
})

// ───────────────────────────────────────────── waitingOn

test('等待人是链上标了 active 的那一级', () => {
  expect(waitingOn([
    step({ stepOrder: 1, approver: '甲', status: 'approved' }),
    step({ stepOrder: 2, approver: '乙', status: 'active' }),
    step({ stepOrder: 3, approver: '丙', status: 'waiting' }),
  ])).toBe('乙')
})

test('链的顺序按 stepOrder 认,不按数组顺序', () => {
  // 后端按 id 取,并不保证顺序;照数组头一条读会把第 3 级说成当前等待人。
  expect(waitingOn([
    step({ stepOrder: 3, approver: '丙', status: 'waiting' }),
    step({ stepOrder: 1, approver: '甲', status: 'approved' }),
    step({ stepOrder: 2, approver: '乙', status: 'active' }),
  ])).toBe('乙')
})

test('没有 active 时退到第一个 waiting —— 链建好了还没激活的那一瞬', () => {
  expect(waitingOn([
    step({ stepOrder: 2, approver: '乙', status: 'waiting' }),
    step({ stepOrder: 1, approver: '甲', status: 'waiting' }),
  ])).toBe('甲')
})

test('步骤状态不认工单那套词 —— pending 不是等待人', () => {
  // 这一条钉的是上面那个默认值的用意:工单叫 pending,步骤从来不叫。若哪天
  // 有人把两套词合并,这里会立刻说出来。
  expect(waitingOn([step({ stepOrder: 1, approver: '甲', status: 'pending' })])).toBe('')
})

test('全签完了就说不出等待人,返回空串而不是编一个', () => {
  expect(waitingOn([
    step({ stepOrder: 1, approver: '甲', status: 'approved' }),
    step({ stepOrder: 2, approver: '乙', status: 'rejected' }),
  ])).toBe('')
})

test('签过的只有 approved 与 rejected', () => {
  expect(stepDone('approved')).toBe(true)
  expect(stepDone('rejected')).toBe(true)
  expect(stepDone('active')).toBe(false)
  expect(stepDone('waiting')).toBe(false)
})

test('没有链记录时返回空串,不抛', () => {
  expect(waitingOn([])).toBe('')
  expect(waitingOn(undefined)).toBe('')
})

// ───────────────────────────────────────────── batchableOf

test('批量只带走待审、服务端说可决定、且非高危的单', () => {
  const rows = [
    ap({ id: 1, apNo: 'A', status: 'pending', canDecide: true, riskLevel: 'mid' }),
    ap({ id: 2, apNo: 'B', status: 'pending', canDecide: true, riskLevel: 'low' }),
    ap({ id: 3, apNo: 'C', status: 'pending', canDecide: true, riskLevel: 'high' }),
    ap({ id: 4, apNo: 'D', status: 'pending', canDecide: false, riskLevel: 'mid' }),
    ap({ id: 5, apNo: 'E', status: 'approved', canDecide: true, riskLevel: 'mid' }),
  ]
  expect(batchableOf(rows).map((a) => a.apNo)).toEqual(['A', 'B'])
})

test('高危单永远不进批量,哪怕服务端说这个人可以决定它', () => {
  // 后端不禁止批量批高危 —— 这一条是产品自己划的线,所以要有测试守着,
  // 否则它会在某次"统一过滤条件"里被顺手抹平。
  const rows = [ap({ status: 'pending', canDecide: true, riskLevel: 'high' })]
  expect(batchableOf(rows)).toHaveLength(0)
})

test('canDecide 缺省(老接口没带这一位)时按不可决定处理', () => {
  // 少一位就当成"可以批"是最坏的默认:它会让人点下去,再被后端逐张拒绝。
  const rows = [ap({ status: 'pending', riskLevel: 'mid' })]
  expect(batchableOf(rows)).toHaveLength(0)
})

test('批量不重算自审规则,只认服务端的 canDecide', () => {
  // 自己发起、而服务端放行(管理员开了 approval.allowSelfApprove)的那一张,
  // 必须仍然进批量 —— 前端自己判一遍"是不是我发起的"就会把它漏掉。
  const rows = [ap({ initiatorId: 9, status: 'pending', canDecide: true, riskLevel: 'mid' })]
  expect(batchableOf(rows)).toHaveLength(1)
})

// ───────────────────────────────────────────── highPendingCount

test('高危提示只数待审的那些', () => {
  const rows = [
    ap({ status: 'pending', riskLevel: 'high' }),
    ap({ status: 'approved', riskLevel: 'high' }),
    ap({ status: 'rejected', riskLevel: 'high' }),
    ap({ status: 'pending', riskLevel: 'mid' }),
  ]
  // 已经批过、驳过的高危不该再提醒 —— 它要人做的事已经做完了。
  expect(highPendingCount(rows)).toBe(1)
})

// ───────────────────────────────────────────── initialOf

test('首字取一个字符,中文名不做首字母缩写', () => {
  expect(initialOf('林伟')).toBe('林')
  expect(initialOf('Wei Lin')).toBe('W')
})

test('名字为空时给占位,不给空白圆点', () => {
  expect(initialOf('')).toBe('?')
})

// ───────────────────────────────────────────── keywordAt

test('关键字高亮大小写不敏感', () => {
  expect(keywordAt('drop table t_order', 'DROP')).toBe(0)
  expect(keywordAt('DELETE FROM t', 'delete')).toBe(0)
})

test('关键字为空或没出现在命令里,不高亮', () => {
  expect(keywordAt('SELECT 1', '')).toBe(-1)
  expect(keywordAt('SELECT 1', 'DROP')).toBe(-1)
})

test('命中的是命令里那一段的真实位置,不是从头算起', () => {
  // 位置用来切字符串;错一位就会把高亮画在相邻的字符上。
  expect(keywordAt('  TRUNCATE TABLE x', 'TRUNCATE')).toBe(2)
})
