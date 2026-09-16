import { test, expect } from '@playwright/test'
import { gridTemplate, visibleCols, type ColSpec } from '../../src/lib/tableColumns'

const COLS: ColSpec[] = [
  { key: 'name', width: '1.5fr' },                 // 不写优先级 = 1
  { key: 'engine', width: '0.9fr', priority: 2 },
  { key: 'addr', width: '1.6fr', priority: 3 },
  { key: 'status', width: '1fr', priority: 1 },
]

test('wide 下所有列都在,顺序不变', () => {
  expect(visibleCols(COLS, 'wide').map((c) => c.key)).toEqual(['name', 'engine', 'addr', 'status'])
})

test('mid 收起优先级 3', () => {
  expect(visibleCols(COLS, 'mid').map((c) => c.key)).toEqual(['name', 'engine', 'status'])
})

test('narrow 只留优先级 1', () => {
  expect(visibleCols(COLS, 'narrow').map((c) => c.key)).toEqual(['name', 'status'])
})

// 不写 priority 的列必须留到最后 —— 9 张表里有大量列没显式标注,
// 把「没标注」读成「最低优先级」会让它们在窄屏集体消失。
test('不写 priority 视同 1,narrow 下仍在场', () => {
  expect(visibleCols([{ key: 'a', width: '1fr' }], 'narrow').map((c) => c.key)).toEqual(['a'])
})

// 这条是整套改动的核心约束。grid 轨道数和渲染出的单元格数一旦脱节,
// 表格会整体错位一列,而类型检查与构建都看不见。
test('模板的轨道数恒等于可见列数', () => {
  for (const bp of ['wide', 'mid', 'narrow'] as const) {
    const vis = visibleCols(COLS, bp)
    expect(gridTemplate(vis).split(' ').length, `${bp} 档轨道数对不上`).toBe(vis.length)
  }
})

test('模板按列序拼接宽度', () => {
  expect(gridTemplate(visibleCols(COLS, 'narrow'))).toBe('1.5fr 1fr')
})
