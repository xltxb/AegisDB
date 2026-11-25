import { test, expect } from '@playwright/test'
import {
  paramSpecsFor, effectiveDefault, paramsToDraft, draftToParams, effectiveParams, specForDialect,
} from '@/lib/reviewParams'
import type { ReviewCatalog, ReviewParamSpec } from '@/types'

const idLen: ReviewParamSpec = { key: 'max', type: 'int', default: 64, dialectDefaults: { oracle: 30 } }
const words: ReviewParamSpec = { key: 'words', type: 'list', default: ['order', 'group'] }
const dropCol: ReviewParamSpec = { key: 'dropColumn', type: 'bool', default: true }

test.describe('reviewParams', () => {
  test('说明书里没有的内置规则 = 无参数;自定义规则取 _regex', () => {
    const cat: ReviewCatalog = {
      dialects: [], categories: [], levels: [],
      params: { 'naming.identifier.length': [idLen], _regex: [{ key: 'pattern', type: 'regex', default: '' }] },
    }
    expect(paramSpecsFor(cat, { code: 'dml.require.where', kind: 'builtin' })).toEqual([])
    expect(paramSpecsFor(cat, { code: 'naming.identifier.length', kind: 'builtin' })).toEqual([idLen])
    expect(paramSpecsFor(cat, { code: 'custom.x', kind: 'regex' })[0].key).toBe('pattern')
  })

  test('默认值按所选的库:Oracle 30,其余与"全部"取 64', () => {
    expect(effectiveDefault(idLen, 'oracle')).toBe(30)
    expect(effectiveDefault(idLen, 'mysql')).toBe(64)
    expect(effectiveDefault(idLen, 'all')).toBe(64)
  })

  test('JSON ↔ 表单:列表一行一个,空格子不写,不认识的键原样留着', () => {
    const specs = [idLen, words, dropCol]
    const draft = paramsToDraft('{"words":["a","b"],"dropColumn":false,"legacy":1}', specs)
    expect(draft).toEqual({ max: '', words: 'a\nb', dropColumn: 'false' })

    draft.max = '128'
    draft.words = 'x, y\nz\n'
    const out = JSON.parse(draftToParams(draft, specs, '{"words":["a"],"legacy":1}'))
    expect(out).toEqual({ legacy: 1, max: 128, words: ['x', 'y', 'z'], dropColumn: false })

    // 全清空 = 全部按默认
    expect(draftToParams({ max: '', words: '', dropColumn: '' }, specs, '{"max":3}')).toBe('')
    // 非数字原样交给后端报错,不悄悄丢掉
    expect(JSON.parse(draftToParams({ max: 'abc' }, [idLen]))).toEqual({ max: 'abc' })
  })

  test('列表行摘要:写了的用写的,没写的标成默认', () => {
    const rows = effectiveParams('{"words":["x"]}', [idLen, words], 'oracle')
    expect(rows.map((r) => [r.spec.key, r.value, r.isDefault])).toEqual([['max', 30, true], ['words', ['x'], false]])
  })

  test('出处按库显示:别的库的规范不拿来说这个库', () => {
    const where = { spec: 'mandatory', specRef: 'DWS对象设计和业务开发规范 RULE 46' }
    expect(specForDialect(where, 'mysql')).toEqual({ spec: '', specRef: '' })
    expect(specForDialect(where, 'dws')).toEqual(where)
    expect(specForDialect(where, 'all')).toEqual(where)

    const drop = { spec: 'mandatory', specRef: 'MySQL規範 §十一 / TIDB規範 §十 / DWS对象设计和业务开发规范 RULE 58' }
    expect(specForDialect(drop, 'tidb')).toEqual({ spec: 'mandatory', specRef: 'TIDB規範 §十' })
    expect(specForDialect(drop, 'oracle')).toEqual({ spec: '', specRef: '' })

    // 没文档名的段沿用前一段:"RULE 39" 仍是 DWS 的
    const fn = { spec: 'mandatory', specRef: 'DWS对象设计和业务开发规范 RULE 38 / RULE 39' }
    expect(specForDialect(fn, 'dws').specRef).toBe('DWS对象设计和业务开发规范 RULE 38 / RULE 39')

    // 运维手写、认不出文档名的出处照常显示
    const own = { spec: 'mandatory', specRef: '团队约定 第 3 条' }
    expect(specForDialect(own, 'mysql')).toEqual(own)
  })
})
