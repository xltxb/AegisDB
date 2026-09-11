import { test, expect } from '@playwright/test'

import { initialsOf } from '../../src/lib/initials'

// 服务端(bootstrap.abbrev)算的是首字母:"Lin Wei" → "LW",而审批列表里那句
// name.slice(0, 2) 给的是 "LI"。同一个人的头像,导航栏上是 LW,审批列表里是 LI。
test('两个词的名字取两个首字母,和服务端一致', () => {
  expect(initialsOf('Lin Wei')).toBe('LW')
  expect(initialsOf('  zhang  wei  ')).toBe('ZW')
  expect(initialsOf('Ada Lovelace King')).toBe('AL') // 只看前两个词
})

test('单个词取前两个字符', () => {
  expect(initialsOf('admin')).toBe('AD')
  expect(initialsOf('升级单平台')).toBe('升级')
})

test('空名字给一个占位,而不是空白圆圈', () => {
  expect(initialsOf('')).toBe('AD')
  expect(initialsOf('   ')).toBe('AD')
})

// 按字符取而不是按 UTF-16 码元:名字里带一个星标或表情时,下标切片会把代理对
// 从中间劈开,吐出半个字符(渲染成 �)。
test('不会把一个字符劈成两半', () => {
  expect(initialsOf('🐟鱼')).toBe('🐟鱼')
  expect(initialsOf('🐟')).toBe('🐟')
})

test('单字名字只给一个字', () => {
  expect(initialsOf('赵')).toBe('赵')
})
