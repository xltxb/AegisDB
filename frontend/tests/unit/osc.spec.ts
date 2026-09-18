import { test, expect } from '@playwright/test'
import {
  copyPercent,
  isLive,
  leftovers,
  startRefusal,
  type OscJob,
  type OscStatus,
} from '../../src/lib/osc'

const job = (over: Partial<OscJob>): OscJob => ({
  id: 1, connectionId: 1, schema: 'app', table: 't_order', alter: 'ADD INDEX i (c)',
  status: 'copying', shadow: '', copiedRows: 0, totalRows: 0, err: '',
  createdBy: 'Lin Wei', createdAt: '', updatedAt: '', finishedAt: null, running: false,
  ...over,
})

// ---- 发起前的拒绝 ----

// 开关关着时不能让按钮可点。ADR 0011 的原话是「半成品绝不能从界面上点得到」。
test('特性关闭时拒绝发起,并给出理由', () => {
  const st: OscStatus = { enabled: false, caveats: ['没有从库延迟限流'] }
  const r = startRefusal(st)
  expect(r).not.toBeNull()
  expect(r!.length).toBeGreaterThan(0)
})

test('特性打开时不拦', () => {
  expect(startRefusal({ enabled: true, caveats: ['没有从库延迟限流'] })).toBeNull()
})

// 状态还没读回来的时候也不能点 —— undefined 被读成"可以"是这类开关最常见的漏法。
test('状态未知时同样拒绝', () => {
  expect(startRefusal(undefined)).not.toBeNull()
})

// ---- 进度 ----

// 总行数是 information_schema 的**估算值**,可能比实际小。真拷过头时进度条不能
// 越过 100% —— 一个显示 137% 的进度条会让人以为程序算错了什么更要紧的东西。
test('拷贝进度按行数算,并夹在 0..100', () => {
  expect(copyPercent(job({ copiedRows: 250, totalRows: 1000 }))).toBe(25)
  expect(copyPercent(job({ copiedRows: 1370, totalRows: 1000 }))).toBe(100)
})

// 估算值为 0 的表(刚建的、或统计信息没更新)不能显示成"已完成 100%",
// 那是最危险的一种误报:人会以为可以走了。
test('总行数未知时没有百分比,而不是 100%', () => {
  expect(copyPercent(job({ copiedRows: 500, totalRows: 0 }))).toBeNull()
})

// ---- 残局 ----

test('还在跑的状态算 live,终态不算', () => {
  expect(isLive(job({ status: 'copying' }))).toBe(true)
  expect(isLive(job({ status: 'replaying' }))).toBe(true)
  expect(isLive(job({ status: 'cutover' }))).toBe(true)
  expect(isLive(job({ status: 'done' }))).toBe(false)
  expect(isLive(job({ status: 'failed' }))).toBe(false)
  expect(isLive(job({ status: 'aborted' }))).toBe(false)
})

// 重启之后要看得见的「残局」有两种,而且是两种不同的麻烦:
//
//  1. 状态停在中间态,但**没有进程在推进它** —— 它永远不会自己走完;
//  2. 已经失败/中止,但影子表名还在 —— 库里躺着一张表,占着磁盘。
//
// 两种都得列出来。只列第一种的话,一次自动清理失败的中止会彻底静音。
test('中间态但没人在推进的任务算残局', () => {
  const rows = [job({ id: 7, status: 'copying', running: false }), job({ id: 8, status: 'done' })]
  expect(leftovers(rows).map((j) => j.id)).toEqual([7])
})

// 这一条是整件事的要害,也是最容易判反的地方。
//
// 库里一条 copying 的记录,和一条正在跑的 copying 记录,**状态字段一模一样**。只按
// 状态判断的话,重启之后那条死在半路的任务会被当成"有人在管",于是它既不进残局清单,
// 界面还会给出一个按下去只会报"任务不在运行中"的中止按钮 —— 而那张影子表就一直躺在
// 库里,直到磁盘报警。running 由接口层从进程自己手上的取消钩子填,不是从状态推出来的。
test('正在跑的任务不是残局,死在半路的同状态任务是', () => {
  const alive = job({ id: 20, status: 'copying', running: true })
  const dead = job({ id: 21, status: 'copying', running: false })
  expect(leftovers([alive, dead]).map((j) => j.id)).toEqual([21])
})

test('失败但影子表还在的任务也算残局', () => {
  const rows = [
    job({ id: 9, status: 'failed', shadow: 't_order_gho' }),
    job({ id: 10, status: 'failed', shadow: '' }),
    job({ id: 11, status: 'aborted', shadow: 't_x_gho' }),
  ]
  expect(leftovers(rows).map((j) => j.id)).toEqual([9, 11])
})

// 正常完成的任务**不是**残局,哪怕它带着影子表名 —— 切换成功之后那个名字指的是
// 已经上位的新表。把它列成待清理会诱导人去删掉刚刚换上去的表。
test('已完成的任务不算残局,即使记着影子表名', () => {
  expect(leftovers([job({ id: 12, status: 'done', shadow: 't_order_gho' })])).toEqual([])
})
