import { test, expect } from '@playwright/test'
import fs from 'node:fs'
import path from 'node:path'
import { parseForESLint, getStaticJSONValue } from 'jsonc-eslint-parser'
import { createI18n } from 'vue-i18n'

import { renderBizReason, BIZ_GRANT_GONE } from '../../src/lib/ruleText'

// 业务方查询被拒的原因按读者语言渲染(bizText.,与 ruleText / outText 同一套机制)。
// 读真实词条文件:要挡的正是"后端加了 code、前端忘了加文案"。
// 中文与服务端规范串逐字一致由后端的 TestBizReasonMatchesFrontendZh 钉住。

function catalogue(file: string): Record<string, unknown> {
  const p = path.resolve('src/locales', file)
  const parsed = parseForESLint(fs.readFileSync(p, 'utf8'), { jsonSyntax: 'json5' } as never)
  return getStaticJSONValue(parsed.ast as never) as unknown as Record<string, unknown>
}

const zh = catalogue('zh.json5')
const en = catalogue('en.json5')

// 用真正的 vue-i18n 编译词条:文案里的引号、反斜杠在消息语法里会不会被吃掉,只有它说了算。
function realI18n(cat: Record<string, unknown>) {
  const i = createI18n({ legacy: false, locale: 'x', messages: { x: { bizText: cat.bizText } as never } })
  return { t: i.global.t as never, te: i.global.te as never }
}

test('两种语言的 bizText 词条一一对应', () => {
  const zk = Object.keys((zh.bizText ?? {}) as object).sort()
  const ek = Object.keys((en.bizText ?? {}) as object).sort()
  expect(ek).toEqual(zk)
  expect(zk.length).toBeGreaterThan(0)
})

test('英文界面讲英文,参数照样带进去', () => {
  const s = renderBizReason({ code: 'bizOtherDB', args: { name: 'finance_db', db: 'orders_db' } }, '不能查询…', realI18n(en))
  expect(s).toBe('You cannot query finance_db — your grant covers orders_db only')
})

test('反斜杠与引号原样显示,不被消息语法吃掉', () => {
  const s = renderBizReason({ code: 'bizBackslash' }, '', realI18n(zh))
  expect(s).toContain(`(\\' 或 \\")`)
  expect(s).toContain(`'O''Brien'`)
})

test('认不出的 code 回落到服务端的原话', () => {
  expect(renderBizReason({ code: 'bizShippedLater' }, '某条新原因', realI18n(en))).toBe('某条新原因')
  expect(renderBizReason(undefined, '某条原因', realI18n(en))).toBe('某条原因')
})

test('授权已经没了的几种 code 才触发刷新授权列表', () => {
  expect([...BIZ_GRANT_GONE].sort()).toEqual(['bizNoGrant', 'bizNoSource', 'bizNotSource'])
  for (const code of BIZ_GRANT_GONE) expect(Object.keys(zh.bizText as object)).toContain(code)
})
