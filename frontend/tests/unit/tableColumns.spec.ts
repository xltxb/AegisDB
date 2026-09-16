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

// 列宽可以带空格 —— `minmax(88px, 1fr)` 这类值本仓库已经在用
// (见 components/permission/CapabilityMatrix.tsx)。模板必须原样拼接它们,
// 期望值是手写的字面量,不从被测函数推导,否则这条测试就只是在复述实现。
test('带空格的列宽原样拼进模板', () => {
  const cols: ColSpec[] = [
    { key: 'head', width: 'minmax(150px, 2fr)' },
    { key: 'a', width: 'minmax(88px, 1fr)' },
    { key: 'b', width: '96px', priority: 3 },
  ]
  expect(gridTemplate(visibleCols(cols, 'wide'))).toBe('minmax(150px, 2fr) minmax(88px, 1fr) 96px')
  expect(gridTemplate(visibleCols(cols, 'mid'))).toBe('minmax(150px, 2fr) minmax(88px, 1fr)')
})

test('模板按列序拼接宽度', () => {
  expect(gridTemplate(visibleCols(COLS, 'narrow'))).toBe('1.5fr 1fr')
})
