import { test, expect } from '@playwright/test'
import { countMyExecuteTodos, isMyTodo } from '../../src/lib/todoChime'
import type { ApprovalTodoItem } from '../../src/types'

// 登录提示音只为**自己发起的**待办响。
//
// 这一组钉的是一件很容易在重构里悄悄丢掉的事:执行权是放开的 —— 够得到那台实例的人都
// 可以接手别人的单(ADR 0010 的 2026-09-09 修订)。于是一个人的待办里会躺着别人提的
// execute,而那些是"你可以顺手帮着跑",不是"你的东西卡在这儿"。值班的人一登录就被自己
// 没提过的单子叫一通,几天之后这声音就只剩噪音。
//
// 漏了这一条不会报任何错,表现只是"铃响得有点勤" —— 没有人会把它当成 bug 报上来,
// 直到所有人都把提示音关掉。所以它必须有测试。

const row = (over: Partial<ApprovalTodoItem>): ApprovalTodoItem => ({
  id: 1, apNo: 'AP-1', kind: 'execute', initiator: 'Lin Wei', initiatorId: 1,
  instance: 'prod-sqlite', database: 'orders', riskLevel: 'high',
  command: 'DROP TABLE t', createdAt: '2026-09-17T10:00:00Z', ...over,
})

const ME = { id: 1, name: 'Lin Wei' }

test('自己发起、等我执行的 —— 响', () => {
  expect(countMyExecuteTodos([row({})], ME)).toBe(1)
})

test('**别人发起**的,哪怕我能执行 —— 不响', () => {
  const peers = [
    row({ apNo: 'AP-2', initiator: 'Peer Tester', initiatorId: 5 }),
    row({ apNo: 'AP-3', initiator: 'Someone Else', initiatorId: 9 }),
  ]
  expect(countMyExecuteTodos(peers, ME)).toBe(0)
})

test('自己的和别人的混在一起 —— 只数自己那几张', () => {
  const mixed = [
    row({ apNo: 'AP-1', initiatorId: 1 }),
    row({ apNo: 'AP-2', initiator: 'Peer Tester', initiatorId: 5 }),
    row({ apNo: 'AP-3', initiatorId: 1 }),
  ]
  expect(countMyExecuteTodos(mixed, ME)).toBe(2)
})

test('等我审批 / 等我确认的不算 —— 那两类另有徽章和标题计数在提醒', () => {
  const others = [
    row({ kind: 'decide', initiatorId: 1 }),
    row({ kind: 'confirm', initiatorId: 1 }),
  ]
  expect(countMyExecuteTodos(others, ME)).toBe(0)
})

// id 与姓名分歧时,**以 id 为准**。
//
// 这一条是 initiatorId 存在的全部理由:同名的两个人,只比姓名会比错,而那种错不报任何
// 东西 —— 表现只是某个人莫名其妙地听到、或者莫名其妙地听不到。
test('同名但不同人 —— 按 id 判,不响', () => {
  const sameName = row({ initiator: 'Lin Wei', initiatorId: 42 })
  expect(isMyTodo(sameName, ME)).toBe(false)
  expect(countMyExecuteTodos([sameName], ME)).toBe(0)
})

test('id 对得上而姓名对不上(改过名) —— 照样算我的', () => {
  expect(isMyTodo(row({ initiator: '林伟', initiatorId: 1 }), ME)).toBe(true)
})

// 旧后端还没下发 initiatorId 的那一段时间:退回比姓名,功能照常可用。
test('没有 initiatorId 时退回比姓名', () => {
  expect(isMyTodo(row({ initiatorId: undefined }), ME)).toBe(true)
  expect(isMyTodo(row({ initiator: 'Peer Tester', initiatorId: undefined }), ME)).toBe(false)
  // 0 也当作"没有":它不是一个真实的用户 id
  expect(isMyTodo(row({ initiatorId: 0 }), ME)).toBe(true)
  expect(isMyTodo(row({ initiator: 'Peer Tester', initiatorId: 0 }), ME)).toBe(false)
})

test('没有登录者 / 没有待办 —— 不响,也不炸', () => {
  expect(countMyExecuteTodos([row({})], null)).toBe(0)
  expect(countMyExecuteTodos([], ME)).toBe(0)
  expect(countMyExecuteTodos(null, ME)).toBe(0)
  expect(countMyExecuteTodos(undefined, undefined)).toBe(0)
  // 登录者没有 id 也没有姓名(理论上不该发生)时,不要把所有单子都算成他的
  expect(countMyExecuteTodos([row({ initiatorId: undefined })], {})).toBe(0)
})
