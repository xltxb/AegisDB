import { test, expect } from '@playwright/test'
import fs from 'node:fs'
import path from 'node:path'
import { parseForESLint, getStaticJSONValue } from 'jsonc-eslint-parser'
import { createI18n } from 'vue-i18n'

import { renderApText, renderPreText } from '../../src/lib/ruleText'

// 审批单描述(apText.)与发布预检结论(preText.)按读者语言渲染 —— 与 ruleText / bizText 同一套机制。
// 读真实词条文件;中文与服务端规范串逐字一致由后端 text_ref_test.go 钉住。

function catalogue(file: string): Record<string, unknown> {
  const p = path.resolve('src/locales', file)
  const parsed = parseForESLint(fs.readFileSync(p, 'utf8'), { jsonSyntax: 'json5' } as never)
  return getStaticJSONValue(parsed.ast as never) as unknown as Record<string, unknown>
}

const zh = catalogue('zh.json5')
const en = catalogue('en.json5')

// 用真正的 vue-i18n 编译词条:「」、→、括号在消息语法里会不会出事,只有它说了算。
function realI18n(cat: Record<string, unknown>) {
  const i = createI18n({ legacy: false, locale: 'x', messages: { x: { apText: cat.apText, preText: cat.preText } as never } })
  return { t: i.global.t as never, te: i.global.te as never }
}

const HAN = /[一-鿿]/

for (const ns of ['apText', 'preText']) {
  test(`${ns}:两种语言一一对应,英文里没有中文`, () => {
    const z = (zh[ns] ?? {}) as Record<string, string>
    const e = (en[ns] ?? {}) as Record<string, string>
    expect(Object.keys(e).sort()).toEqual(Object.keys(z).sort())
    expect(Object.keys(z).length).toBeGreaterThan(5)
    const han = Object.entries(e).filter(([, v]) => HAN.test(v))
    expect(han, '英文词条里还有中文').toEqual([])
  })
}

const windowRef = {
  code: 'apWindowOpen',
  args: { name: '十月班车', scope: 'hk-billing', db: 'db_orders', reason: '季度维护' },
  parts: [{ code: 'winDaily', args: { start: '02:00', end: '04:00', tz: 'Asia/Shanghai' } }],
}

test('窗口单:中文与服务端原句一致,时间表作为嵌套片段渲染', () => {
  expect(renderApText(windowRef, '', realI18n(zh)))
    .toBe('开启执行窗口「十月班车」· hk-billing/db_orders · 班车 每天 02:00-04:00 (Asia/Shanghai) · 理由:季度维护')
})

test('窗口单:英文讲英文,用户填的名字与理由原样带进去', () => {
  expect(renderApText(windowRef, '', realI18n(en)))
    .toBe('Open execution window “十月班车” · hk-billing/db_orders · Recurring daily 02:00-04:00 (Asia/Shanghai) · Reason: 季度维护')
})

test('SQL 单子没有 ref,原样显示命令;认不出的 code 回落到原串', () => {
  expect(renderApText(undefined, 'DELETE FROM t WHERE id = 1', realI18n(en))).toBe('DELETE FROM t WHERE id = 1')
  expect(renderApText({ code: 'apFromTheFuture' }, '开启执行窗口「x」', realI18n(en))).toBe('开启执行窗口「x」')
})

test('预检:操作名作为嵌套片段翻译,驱动原文不翻译', () => {
  const i = realI18n(en)
  expect(renderPreText({ code: 'preColMissing', parts: [{ code: 'preOpRename' }] }, '', i))
    .toBe('Column does not exist; the rename will fail')
  expect(renderPreText({ code: 'preSyntaxError', args: { err: 'ORA-00942: table or view does not exist' } }, '', i))
    .toBe('ORA-00942: table or view does not exist')
  expect(renderPreText({ code: 'preSyntaxVerb', args: { verb: 'ALTER' } }, '', realI18n(zh)))
    .toBe('ALTER 语句无法在不执行的前提下校验语法(DDL 隐式提交或加表锁),未做语法检查')
})
