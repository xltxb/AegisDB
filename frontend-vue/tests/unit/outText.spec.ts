import { test, expect } from '@playwright/test'
import fs from 'node:fs'
import path from 'node:path'
import { parseForESLint, getStaticJSONValue } from 'jsonc-eslint-parser'

import { renderOutText } from '../../src/lib/ruleText'
import type { RuleRef } from '../../src/types'

// 终端**打出来**的那句话按语言渲染。
//
// 成因是具体的:`执行成功 · N 行受影响` 是服务端在 Go 里拼的中文,而它会原样打进一个
// 英文会话的终端里 —— 用户看到的是一行英文中间嵌着一句中文。规范串仍然照旧下发并进
// 审计,code 只是让界面用读者的语言把同一件事再讲一遍(与 ruleText 同一套机制)。
//
// 这里**读真实的词条文件**,不是自造一份 stub:这组用例要挡的正是"后端加了 code、
// 前端忘了加文案",而 stub 版本永远发现不了那件事。

function catalogue(file: string): Record<string, unknown> {
  const p = path.resolve('src/locales', file)
  const parsed = parseForESLint(fs.readFileSync(p, 'utf8'), { jsonSyntax: 'json5' } as never)
  return getStaticJSONValue(parsed.ast as never) as unknown as Record<string, unknown>
}

const zh = catalogue('zh.json5')
const en = catalogue('en.json5')

/** vue-i18n 的极简替身:按 `outText.<code>` 取值并做参数替换。 */
function i18nOf(cat: Record<string, unknown>) {
  const out = (cat.outText ?? {}) as Record<string, string>
  return {
    te: (k: string) => k.startsWith('outText.') && k.slice(8) in out,
    t: (k: string, named?: Record<string, unknown>) =>
      out[k.slice(8)].replace(/\{(\w+)\}/g, (_, n) => String(named?.[n] ?? '')),
  }
}

test('写语句的结果按读者的语言显示,而不是服务端的中文', () => {
  const ref: RuleRef = { code: 'execAffected', args: { n: '3' } }
  expect(renderOutText(ref, '执行成功 · 3 行受影响', i18nOf(en)))
    .toBe('Executed successfully · 3 row(s) affected')
  expect(renderOutText(ref, '执行成功 · 3 行受影响', i18nOf(zh)))
    .toBe('执行成功 · 3 行受影响')
})

// 中文那份必须与服务端的规范串一字不差。两种说法比没有翻译更糟:同一次执行,界面上
// 一句、审计里另一句,追查的时候没人说得清哪句是真的。
test('中文渲染结果与服务端规范串完全一致', () => {
  const canonical = '执行成功 · 7 行受影响'
  expect(renderOutText({ code: 'execAffected', args: { n: '7' } }, canonical, i18nOf(zh)))
    .toBe(canonical)
})

// 认不出的 code 回落到服务端原串 —— 那是降级,不是出错。前端比后端晚一个版本时,
// 操作员看到的应当是一句中文,而不是 "outText.somethingNew"。
test('认不出的 code 回落到服务端自己的那句话', () => {
  expect(renderOutText({ code: 'shippedAfterThisBuild' }, '· 某条新提示', i18nOf(en)))
    .toBe('· 某条新提示')
  expect(renderOutText(undefined, '执行成功 · 0 行受影响', i18nOf(en)))
    .toBe('执行成功 · 0 行受影响')
})

// 后端加了 code、前端忘了加文案 —— 这组用例存在的主要理由。
test('两种语言的 outText 词条一一对应', () => {
  const zk = Object.keys((zh.outText ?? {}) as object).sort()
  const ek = Object.keys((en.outText ?? {}) as object).sort()
  expect(ek).toEqual(zk)
  expect(zk.length).toBeGreaterThan(0)
})

// 终端靠开头的 `·` 区分「提示」和「执行结果」:提示用黄色显示且不追加耗时。翻译时
// 丢掉这个前缀,一条"实例处于维护态"的提示就会被显示成一次成功的执行。
test('提示类文案在两种语言里都保留开头的 ·', () => {
  const NOTICES = ['maintenance', 'apWindowLive', 'apExportQueued', 'apPipeline', 'apAwaitingExec', 'execFailed']
  for (const cat of [zh, en]) {
    const out = (cat.outText ?? {}) as Record<string, string>
    for (const code of NOTICES) {
      expect(out[code], code).toBeTruthy()
      expect(out[code].trimStart().startsWith('·'), `${code}: ${out[code]}`).toBe(true)
    }
  }
  // 反过来:执行结果**不能**以 · 开头,否则它会被当成提示,连耗时都不显示。
  for (const cat of [zh, en]) {
    const out = (cat.outText ?? {}) as Record<string, string>
    expect(out.execAffected.trimStart().startsWith('·')).toBe(false)
  }
})
