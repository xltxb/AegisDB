import { test, expect } from '@playwright/test'
import fs from 'node:fs'
import path from 'node:path'
import { parseForESLint, getStaticJSONValue } from 'jsonc-eslint-parser'

import {
  BUILTIN_FLOW_NAME, BUILTIN_FLOW_DESC, BUILTIN_ROLE_NAME, BUILTIN_ROLE_LAYER, BUILTIN_ENV_NAME, BUILTIN_CONN_LAYER, builtinLabel, reviewRuleName,
} from '../../src/lib/builtinNames'

// 出厂自带的发布流程按读者的语言显示。
//
// 流程的名字和描述是**种子数据**,不是界面文案:后端 pipeline_seed.go 把它们插进
// tbl_pipeline,而页面上它们出现在可编辑的输入框里。匹配是按原串做的(这张表没有
// code 列),所以**种子文案一改,前端那张对照表就会静默失配** —— 表现是英文界面上
// 又冒出中文。因此这组用例直接读那个 Go 文件。
//
// 阶段名是另一回事,已经不是数据了:界面按 plType_* 只读显示,存进库的那份由后端按
// 类型给出。这里改为核对**两边说的是不是同一句话**。

const SEED = path.resolve('../backend/internal/bootstrap/pipeline_seed.go')

function seedSource(): string {
  return fs.readFileSync(SEED, 'utf8')
}

function catalogue(file: string): Record<string, unknown> {
  const p = path.resolve('src/locales', file)
  const parsed = parseForESLint(fs.readFileSync(p, 'utf8'), { jsonSyntax: 'json5' } as never)
  return getStaticJSONValue(parsed.ast as never) as unknown as Record<string, unknown>
}

const zh = catalogue('zh.json5')
const en = catalogue('en.json5')
const tOf = (cat: Record<string, unknown>) => (k: string) => String(cat[k] ?? '')

test('种子文件仍然存在于预期的位置', () => {
  expect(fs.existsSync(SEED), SEED).toBe(true)
})

// defaultPipelines 里的 name/desc 必须都在表里,否则英文界面上会漏出中文。
test('出厂流程的名字与描述都被覆盖到了', () => {
  const src = seedSource()
  const rows = [...src.matchAll(/name:\s*"([^"]+)",\s*desc:\s*"([^"]+)"/g)]
  expect(rows.length, '没能从种子文件里解析出流程行').toBeGreaterThan(0)
  for (const [, name, desc] of rows) {
    expect(BUILTIN_FLOW_NAME[name], `流程名未覆盖: ${name}`).toBeTruthy()
    expect(BUILTIN_FLOW_DESC[desc], `流程描述未覆盖: ${desc}`).toBeTruthy()
  }
})

// 阶段名不再是数据:界面按 plType_* 只读显示,存进库的那份由后端 stageTypeLabel 按类型
// 给出。两者必须说同一句话 —— 对不上的表现是:同一个阶段,流程编辑器里叫一个名字,
// 发布日志和通知里叫另一个,而后者是记录。
test('后端的规范阶段名与前端的中文文案一字不差', () => {
  const src = fs.readFileSync(path.resolve('../backend/internal/service/pipeline.go'), 'utf8')
  const fn = src.slice(src.indexOf('func stageTypeLabel'))
  const body = fn.slice(0, fn.indexOf(String.fromCharCode(10) + '}'))
  // 阶段名同时收进接口文案表(apimsg.Text 包一层,见 .scratch/api-msg-i18n/PRD-batch4.md)
  const pairs = [...body.matchAll(/case model\.Stage(\w+):\s*return (?:apimsg\.Text\()?"([^"]+)"/g)]
  expect(pairs.length, '没能从 pipeline.go 解析出 stageTypeLabel 的分支').toBeGreaterThan(0)
  for (const [, konst, canonical] of pairs) {
    // 常量名 → 类型值:StageAutoExecute → auto_execute
    const key = 'plType_' + konst.replace(/([a-z])([A-Z])/g, '$1_$2').toLowerCase()
    expect(String(zh[key] ?? ''), `zh 缺 ${key}`).toBe(canonical)
    expect(String(en[key] ?? ''), `en 缺 ${key}`).not.toBe('')
  }
})

// 中文那份必须与种子一字不差:对不上就换不出译名,而且中文界面上会显示成另一句话。
test('中文渲染结果与种子里的原串完全一致', () => {
  const t = tOf(zh)
  for (const [stored] of Object.entries(BUILTIN_FLOW_NAME)) {
    expect(builtinLabel(BUILTIN_FLOW_NAME, stored, t)).toBe(stored)
  }
  for (const [stored] of Object.entries(BUILTIN_FLOW_DESC)) {
    expect(builtinLabel(BUILTIN_FLOW_DESC, stored, t)).toBe(stored)
  }
})

test('英文下换成英文说法', () => {
  const t = tOf(en)
  expect(builtinLabel(BUILTIN_FLOW_NAME, '标准发布流程', t)).toBe('Standard release flow')
})

// 表里没有的原样返回 —— 用户自己建的流程、以及改过名的出厂流程,显示的必须是他键入
// 的那串。这条是整个取舍的落脚点:能翻译的只有随产品出厂的那几行。
test('用户自己的名字原样显示,不被翻译', () => {
  const t = tOf(en)
  expect(builtinLabel(BUILTIN_FLOW_NAME, '我们组的发布流程', t)).toBe('我们组的发布流程')
  expect(builtinLabel(BUILTIN_FLOW_NAME, 'Standard release flow ', t)).toBe('Standard release flow ')
  expect(builtinLabel(BUILTIN_FLOW_DESC, '', t)).toBe('')
})

// 两种语言都要有,否则英文下 t() 返回空串,输入框会变成空的 —— 比显示中文更糟。
test('用到的文案键在两种语言里都存在', () => {
  const keys = [
    ...Object.values(BUILTIN_FLOW_NAME),
    ...Object.values(BUILTIN_FLOW_DESC),
  ]
  for (const k of keys) {
    expect(String(zh[k] ?? ''), `zh 缺 ${k}`).not.toBe('')
    expect(String(en[k] ?? ''), `en 缺 ${k}`).not.toBe('')
  }
})

// ---- 出厂角色、环境、连接层级 ----
//
// 与流程名同一个取舍,同一个风险:种子文案一改,对照表静默失配,英文界面上又冒出中文。
// 所以同样直接读 Go 种子文件,把里面每一个非纯 ASCII 的串都核一遍。

const HAN = /[一-鿿]/
const goSrc = (rel: string) => fs.readFileSync(path.resolve('../backend/internal/bootstrap', rel), 'utf8')
const grab = (src: string, re: RegExp) => [...src.matchAll(re)].map((m) => m[1]).filter((v) => HAN.test(v))

const SEEDS = {
  roleName: [
    ...grab(goSrc('seed.go'), /Code: "\w+", Name: "([^"]+)"/g),
    ...grab(goSrc('bizquery_role_seed.go'), /Name: "([^"]+)", Layer:/g),
  ],
  roleLayer: [
    ...grab(goSrc('seed.go'), /Name: "[^"]+", Layer: "([^"]+)"/g),
    ...grab(goSrc('bizquery_role_seed.go'), /Layer: "([^"]+)"/g),
  ],
  envName: grab(goSrc('seed.go'), /DisplayName: "([^"]+)"/g),
  connLayer: grab(goSrc('seed.go'), /ConnLayer: "([^"]+)"/g),
}
const TABLES = {
  roleName: BUILTIN_ROLE_NAME, roleLayer: BUILTIN_ROLE_LAYER, envName: BUILTIN_ENV_NAME, connLayer: BUILTIN_CONN_LAYER,
} as const

for (const [kind, strs] of Object.entries(SEEDS) as [keyof typeof TABLES, string[]][]) {
  test(`出厂${kind}都被覆盖,中文一字不差,英文里不再有中文`, () => {
    expect(strs.length, `没能从种子文件里解析出 ${kind}`).toBeGreaterThan(0)
    const table = TABLES[kind]
    for (const stored of strs) {
      const key = table[stored]
      expect(key, `${kind} 未覆盖: ${stored}`).toBeTruthy()
      expect(builtinLabel(table, stored, tOf(zh))).toBe(stored)
      const english = builtinLabel(table, stored, tOf(en))
      expect(english, `en 缺 ${key}`).not.toBe('')
      expect(HAN.test(english), `en 的 ${key} 还是中文: ${english}`).toBe(false)
    }
  })
}

test('管理员改过名的角色 / 环境原样显示', () => {
  const t = tOf(en)
  expect(builtinLabel(BUILTIN_ROLE_NAME, '平台管理员(旧)', t)).toBe('平台管理员(旧)')
  expect(builtinLabel(BUILTIN_ENV_NAME, '香港生产', t)).toBe('香港生产')
  expect(builtinLabel(BUILTIN_ROLE_NAME, '平台管理员', t)).toBe('Platform admin')
})

// ---- 规范审查内置规则名 ----
//
// 规则有 code,按 code 取词条;"没被改过名"= 库里的名字与中文词条一字不差。所以中文词条
// 必须与 review/builtin.go 的种子逐字一致,否则所有出厂规则在英文界面上都认不出、仍显示中文。


const RULE_SEED = [...fs.readFileSync(path.resolve('../backend/internal/review/builtin.go'), 'utf8')
  .matchAll(/^\s*\{"([a-z0-9._]+)", "([^"]+)",/gm)].map((m) => [m[1], m[2]] as const)

test('每条内置规则都有两种语言的名字,中文与种子一字不差,英文里没有中文', () => {
  expect(RULE_SEED.length, '没能从 builtin.go 解析出规则').toBeGreaterThan(50)
  const zr = (zh.srRule ?? {}) as Record<string, string>
  const er = (en.srRule ?? {}) as Record<string, string>
  for (const [code, name] of RULE_SEED) {
    const k = code.replace(/\./g, '_')
    expect(zr[k], `zh 缺 srRule.${k}`).toBe(name)
    expect(er[k], `en 缺 srRule.${k}`).toBeTruthy()
    expect(HAN.test(er[k]), `en 的 srRule.${k} 还是中文: ${er[k]}`).toBe(false)
  }
})

test('规则名:没改过的换成英文,改过的原样显示', () => {
  const zr = (zh.srRule ?? {}) as Record<string, string>
  const zhOf = (k: string) => zr[k.replace(/^srRule\./, '')]
  const tEn = (k: string) => String(((en.srRule ?? {}) as Record<string, string>)[k.replace(/^srRule\./, '')] ?? '')
  expect(reviewRuleName('dml.forbid.select.star', '禁止 SELECT *', tEn, zhOf)).toBe('No SELECT *')
  expect(reviewRuleName('dml.forbid.select.star', '我们组:禁止 SELECT *', tEn, zhOf)).toBe('我们组:禁止 SELECT *')
  expect(reviewRuleName('my.custom.rule', '自定义规则', tEn, zhOf)).toBe('自定义规则')
})

test('种子改过文案的规则,老库里的旧出厂名也照样翻译', () => {
  const zr = (zh.srRule ?? {}) as Record<string, string>
  const zhOf = (k: string) => zr[k.replace(/^srRule\./, '')]
  const tEn = (k: string) => String(((en.srRule ?? {}) as Record<string, string>)[k.replace(/^srRule\./, '')] ?? '')
  expect(reviewRuleName('ddl.forbid.drop', '禁止 DROP 表/库', tEn, zhOf)).toBe('No DROP of tables / databases / columns')
  // 时间类型豁免 NOT NULL 后改过名:老库里存的旧名要显示成新文案,而不是停在旧说法上。
  expect(reviewRuleName('tidb.require.col.not.null', '所有字段必须 NOT NULL(除非业务明确需要为空)', tEn, zhOf))
    .toBe('All columns must be NOT NULL (except date/time types, or where the business needs NULL)')
  expect(reviewRuleName('ddl.require.col.not.null', '字段建议 NOT NULL', tEn, zhOf)).toBe('Columns should be NOT NULL (date/time types exempt)')
  expect(reviewRuleName('dws.require.col.not.null', '明确不存在 NULL 值的字段必须加 NOT NULL', tEn, zhOf))
    .toBe('Columns that never hold NULL must be NOT NULL (date/time types exempt)')
})
