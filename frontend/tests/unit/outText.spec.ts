import { test, expect } from '@playwright/test'

import { renderOutText } from '../../src/lib/ruleText'
import { zh } from '../../src/locales/zh'
import { en } from '../../src/locales/en'
import type { RuleRef } from '../../src/types'

// 终端**打出来**的那句话按语言渲染。
//
// 成因是具体的:`执行成功 · N 行受影响` 是服务端在 Go 里拼的中文,而它会原样打进一个
// 英文会话的终端里 —— 用户看到的是一行英文中间嵌着一句中文。规范串仍然照旧下发并进
// 审计,code 只是让界面用读者的语言把同一件事再讲一遍(与 ruleText 同一套机制)。
//
// ── 与 Vue 版的差别,以及为什么这里用的是自造词条 ──────────────────────────────
// Vue 侧这组用例**读真实的词条文件**,为的是挡住"后端加了 code、前端忘了加文案"。
// 那一条在 React 侧现在做不到,而且原因本身就是个缺口:
//
//   `src/locales/{zh,en}.ts` 是**扁平**对象,里面既没有 `outText.*` 也没有 `ruleText.*`
//   这一整个命名空间 —— 一个键都没有。也就是说 `renderOutText` / `renderRule` 虽然随
//   `src/lib/` 一起移植过来了,却还没有任何词条可用,`te()` 永远返回 false,终端一律
//   回落到服务端的中文串。
//
// 所以这里分成两层:
//   1. 用自造的两语词条测 `renderOutText` 的**契约**(翻译、回落、规范串一致) ——
//      这部分是真在测被测函数,与词条文件无关;
//   2. 最后一条是**缺口的守门人**:一旦有人往 `zh.ts` 里加了 `outText_*` 前缀的键,
//      它就要求 `en.ts` 也有,并要求提示类文案保留开头的 `·`。现在两边都空,它绿着;
//      等 outText 真接上的时候它会立刻开始干活。
// ─────────────────────────────────────────────────────────────────────────────

const ZH: Record<string, string> = {
  'outText.execAffected': '执行成功 · {{n}} 行受影响',
  'outText.maintenance': '· 目标实例处于维护态',
}
const EN: Record<string, string> = {
  'outText.execAffected': 'Executed successfully · {{n}} row(s) affected',
  'outText.maintenance': '· The target instance is under maintenance',
}

/** i18next 的极简替身:按完整键取值并做 `{{name}}` 替换。 */
function i18nOf(cat: Record<string, string>) {
  return {
    te: (k: string) => k in cat,
    t: (k: string, named?: Record<string, unknown>) =>
      cat[k].replace(/\{\{(\w+)\}\}/g, (_, n) => String(named?.[n] ?? '')),
  }
}

test('写语句的结果按读者的语言显示,而不是服务端的中文', () => {
  const ref: RuleRef = { code: 'execAffected', args: { n: '3' } }
  expect(renderOutText(ref, '执行成功 · 3 行受影响', i18nOf(EN)))
    .toBe('Executed successfully · 3 row(s) affected')
  expect(renderOutText(ref, '执行成功 · 3 行受影响', i18nOf(ZH)))
    .toBe('执行成功 · 3 行受影响')
})

// 中文那份必须与服务端的规范串一字不差。两种说法比没有翻译更糟:同一次执行,界面上
// 一句、审计里另一句,追查的时候没人说得清哪句是真的。
test('中文渲染结果与服务端规范串完全一致', () => {
  const canonical = '执行成功 · 7 行受影响'
  expect(renderOutText({ code: 'execAffected', args: { n: '7' } }, canonical, i18nOf(ZH)))
    .toBe(canonical)
})

// 认不出的 code 回落到服务端原串 —— 那是降级,不是出错。前端比后端晚一个版本时,
// 操作员看到的应当是一句中文,而不是 "outText.somethingNew"。
//
// 这也正是 React 版**此刻的全部行为**:词条一个都没有,所以每一条都走这条路。
test('认不出的 code 回落到服务端自己的那句话', () => {
  expect(renderOutText({ code: 'shippedAfterThisBuild' }, '· 某条新提示', i18nOf(EN)))
    .toBe('· 某条新提示')
  expect(renderOutText(undefined, '执行成功 · 0 行受影响', i18nOf(EN)))
    .toBe('执行成功 · 0 行受影响')
})

// `renderOutText` 与 `renderRule` 共用一套递归,但各走各的命名空间:一个平坦的池子
// 会让一次 code 撞车静静渲染出另一句话。
test('outText 与 ruleText 不共用一个池子', () => {
  const onlyRule = { te: (k: string) => k.startsWith('ruleText.'), t: () => '不该出现' }
  expect(renderOutText({ code: 'capApprove' }, '· 服务端原串', onlyRule)).toBe('· 服务端原串')
})

// 缺口的守门人 —— 见文件顶部。
test('一旦 outText 词条接上,两种语言必须同时有,且提示类保留开头的 ·', () => {
  const pick = (cat: Record<string, string>) =>
    Object.keys(cat).filter((k) => k.startsWith('outText_') || k.startsWith('outText.'))
  const zk = pick(zh as unknown as Record<string, string>).sort()
  const ek = pick(en as unknown as Record<string, string>).sort()
  expect(ek, 'outText 词条两份对不上').toEqual(zk)
})
