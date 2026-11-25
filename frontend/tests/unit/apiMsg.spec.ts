import { test, expect } from '@playwright/test'

import { i18n } from '@/locales'
import { renderApiMsg, type ApiMsgI18n } from '../../src/lib/apiMsg'
import { renderOutText, renderRule } from '../../src/lib/ruleText'
import { renderRelLog } from '../../src/lib/relLogText'

// 接口报错按读者语言渲染(apiMsg.)。
//
// 用户在英文界面提交发布单,toast 是整句中文 —— 报错 msg 是服务端拼好的中文。现在同一句话另以
// msgRef(code + 按位置的参数)下发。这里用**真的消息编译器 和真的词条文件**渲染:挡的是
// "词条转义写错、占位符对不上、嵌套没填进去"。中文模板与服务端一字不差、每句都有英文,
// 由后端 internal/apimsg 的测试核对。

// 按参数指定语言,不改单例的 locale:同一 worker 里的其它用例(transcript 等)断言的是中文。
function i18nFor(locale: 'zh' | 'en'): ApiMsgI18n {
  return {
    t: (k, list) => i18n.global.t(k, list, { locale }),
    te: (k) => i18n.global.te(k, locale),
  }
}

// 用户报上来的那一句(code 由后端按中文格式串算出,见 TestAPIMsgReleaseChangeTypeCarriesRef)
const CHANGE_TYPE = {
  code: 'me8434ce3',
  args: { 0: 'DDL', 1: 'DML', 2: '1', 3: 'UPDATE' },
}
const ZH = '变更类型为 DDL 的升级单不允许 DML 语句:第 1 条(UPDATE)。请拆成两张单,或改正变更类型'

test('the reported sentence renders in English', () => {
  expect(renderApiMsg(CHANGE_TYPE, ZH, i18nFor('en'))).toBe(
    'A release with change type DDL may not contain DML statements: statement 1 (UPDATE). Split it into two releases, or correct the change type',
  )
})

test('the zh template reproduces the server sentence exactly', () => {
  expect(renderApiMsg(CHANGE_TYPE, ZH, i18nFor('zh'))).toBe(ZH)
})

test('a nested catalogued sentence and raw driver text fill their slots', () => {
  // 创建失败:%s ← 连接不存在
  const nested = { code: 'mb2ab7128', args: { 0: '连接不存在' }, parts: [{ code: 'm08d21309', args: { _slot: '0' } }] }
  expect(renderApiMsg(nested, '创建失败:连接不存在', i18nFor('en'))).toBe('Create failed: Connection not found')

  // 驱动原文不翻译
  const raw = { code: 'mb2ab7128', args: { 0: 'Error 1062' }, parts: [{ code: 'raw', args: { text: 'Error 1062', _slot: '0' } }] }
  expect(renderApiMsg(raw, '创建失败:Error 1062', i18nFor('en'))).toBe('Create failed: Error 1062')
})

test('an unknown code anywhere keeps the whole server sentence', () => {
  expect(renderApiMsg({ code: 'mffffffff' }, '某句新报错', i18nFor('en'))).toBe('某句新报错')
  const halfKnown = { code: 'mb2ab7128', args: { 0: 'x' }, parts: [{ code: 'mffffffff', args: { _slot: '0' } }] }
  expect(renderApiMsg(halfKnown, '创建失败:x', i18nFor('en'))).toBe('创建失败:x')
  expect(renderApiMsg(null, '原句', i18nFor('en'))).toBe('原句')
})

test('every entry compiles and renders in both locales', () => {
  // 扫全部词条:转义写错的会在编译时抛错,或把 {'x'} 原样留在结果里;缺词条会回成键名
  for (const locale of ['zh', 'en'] as const) {
    const msgs = (i18n.global.getLocaleMessage(locale) as Record<string, Record<string, unknown>>).apiMsg
    expect(Object.keys(msgs).length).toBeGreaterThan(500)
    const tr = i18nFor(locale)
    for (const code of Object.keys(msgs)) {
      const list = Array.from({ length: 10 }, (_, i) => `<A${i}>`)
      const out = tr.t('apiMsg.' + code, list)
      expect(out, `${locale} ${code}`).not.toContain("{'")
      expect(out, `${locale} ${code}`).not.toBe('apiMsg.' + code)
    }
  }
})

// 第二批:终端输出(outText)与网关的话(apiMsg)互相嵌套。
test('terminal output and apiMsg nest both ways', () => {
  const en = { t: (k: string, a?: unknown) => i18n.global.t(k, a as never, { locale: 'en' }), te: (k: string) => i18n.global.te(k, 'en') }
  // 执行失败(outText)里嵌着网关自己的话:只读守卫拒绝(apiMsg)
  const execFailed = {
    code: 'execFailed', args: { err: '只读执行不能运行这条语句:经 PKG 调用了有库外副作用的包' },
    parts: [{ code: 'm7ca29310', args: { 0: '经 PKG 调用了有库外副作用的包' }, parts: [{ code: 'm76ad63bb', args: { 0: 'PKG', _slot: '0' } }] }],
  }
  const out = renderOutText(execFailed, '中文原句', en)
  expect(out).toContain('Read-only execution cannot run this statement: Calls a package with side effects outside the database via PKG')
  expect(out).not.toMatch(/[一-鿿]/)

  // 网关的话里嵌着终端输出:脚本第 3 条失败,失败那句是 outText
  const scriptFailed = {
    code: 'm98a0f0ad', args: { 0: '2', 1: '5', 2: '3', 3: '执行成功 · 0 行受影响' },
    parts: [{ code: 'execAffected', args: { n: '0', _slot: '3' } }],
  }
  const s = renderOutText(scriptFailed, '原句', en)
  expect(s.startsWith('· Failed after 2/5 statements · statement 3: ')).toBe(true)
  expect(s).not.toMatch(/[一-鿿]/)
})

// 第三批:规范审查发现的消息 —— 发布阶段日志里的那一行(rlLog.reviewFinding)嵌着它的 apiMsg 身份。
test('review finding message renders in the release log', () => {
  const en = {
    t: (k: string, a?: unknown) => i18n.global.t(k, a as never, { locale: 'en' }),
    te: (k: string) => i18n.global.te(k, 'en'),
    locale: 'en',
    stage: (_t: string, s: string) => s,
    rule: (_c: string, s: string) => 'Require WHERE',
  }
  const refs = JSON.stringify([{
    code: 'reviewFinding',
    args: { level: 'ERROR', stmt: '1', rule: 'dml.require.where', name: 'DML 必须带 WHERE', msg: 'DELETE 未带 WHERE 条件,将作用于全表', ns: 'apiMsg' },
    parts: [{ code: 'mcea43d4d', args: { 0: 'DELETE' } }],
  }])
  const out = renderRelLog(refs, '原文', en)
  expect(out).toContain('DELETE has no WHERE clause and will affect the whole table')
  expect(out).not.toMatch(/[一-鿿]/)
})

// 收尾批:句中嵌的拦截规则(ruleText)交给规则渲染器 —— 脚本被禁止时的「第 N 条:<规则>」。
test('a nested interception rule renders through ruleText', () => {
  const en = { t: (k: string, a?: unknown) => i18n.global.t(k, a as never, { locale: 'en' }), te: (k: string) => i18n.global.te(k, 'en') }
  const blocked = {
    code: 'm1158a3af', args: { 0: '3', 1: '高危命令字典 · PROD 禁止直接执行' },
    parts: [{ code: 'dictDeny', args: { tier: 'PROD', _slot: '1' } }],
  }
  const out = renderApiMsg(blocked, '原句', en, (r) => renderRule(r, '', en))
  expect(out.startsWith('statement 3: ')).toBe(true)
  expect(out).not.toMatch(/[一-鿿]/)
})
