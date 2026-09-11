import { test, expect } from '@playwright/test'
import fs from 'node:fs'
import path from 'node:path'

import { BUILTIN_FLOW_NAME, BUILTIN_FLOW_DESC, builtinLabel } from '../../src/lib/builtinNames'
import { zh } from '../../src/locales/zh'
import { en } from '../../src/locales/en'

// 出厂自带的发布流程按读者的语言显示。
//
// 流程的名字和描述是**种子数据**,不是界面文案:后端 pipeline_seed.go 把它们插进
// tbl_pipeline,而页面上它们出现在可编辑的输入框里。匹配是按原串做的(这张表没有
// code 列),所以**种子文案一改,前端那张对照表就会静默失配** —— 表现是英文界面上
// 又冒出中文。因此这组用例直接读那个 Go 文件。
//
// 阶段名是另一回事,已经不是数据了:界面按 `stType_*` 只读显示(pages/changes),存进
// 库的那份由后端按类型给出。这里核对**两边说的是不是同一句话**。

const SEED = path.resolve('../backend/internal/bootstrap/pipeline_seed.go')
const PIPELINE = path.resolve('../backend/internal/service/pipeline.go')

const tOf = (cat: Record<string, string>) => (k: string) => String(cat[k] ?? '')

test('种子文件仍然存在于预期的位置', () => {
  expect(fs.existsSync(SEED), SEED).toBe(true)
})

// defaultPipelines 里的 name/desc 必须都在表里,否则英文界面上会漏出中文。
test('出厂流程的名字与描述都被覆盖到了', () => {
  const src = fs.readFileSync(SEED, 'utf8')
  const rows = [...src.matchAll(/name:\s*"([^"]+)",\s*desc:\s*"([^"]+)"/g)]
  expect(rows.length, '没能从种子文件里解析出流程行').toBeGreaterThan(0)
  for (const [, name, desc] of rows) {
    expect(BUILTIN_FLOW_NAME[name], `流程名未覆盖: ${name}`).toBeTruthy()
    expect(BUILTIN_FLOW_DESC[desc], `流程描述未覆盖: ${desc}`).toBeTruthy()
  }
})

// 阶段名不是数据:界面按 `stType_*` 只读显示,存进库的那份由后端 stageTypeLabel 按类型
// 给出。两者必须说同一句话 —— 对不上的表现是:同一个阶段,流程编辑器里叫一个名字,
// 发布日志和通知里叫另一个,而后者是记录。
//
// 这条落地就抓到一个:`stType_backup` 写的是「备份回滚点」,后端是「备份/回滚点」。
test('后端的规范阶段名与前端的中文文案一字不差', () => {
  const src = fs.readFileSync(PIPELINE, 'utf8')
  const fn = src.slice(src.indexOf('func stageTypeLabel'))
  const body = fn.slice(0, fn.indexOf('\n}'))
  const pairs = [...body.matchAll(/case model\.Stage(\w+):\s*return "([^"]+)"/g)]
  expect(pairs.length, '没能从 pipeline.go 解析出 stageTypeLabel 的分支').toBeGreaterThan(0)
  for (const [, konst, canonical] of pairs) {
    const key = 'stType_' + konst.toLowerCase()
    expect(String((zh as Record<string, string>)[key] ?? ''), `zh 缺 ${key}`).toBe(canonical)
    expect(String((en as Record<string, string>)[key] ?? ''), `en 缺 ${key}`).not.toBe('')
  }
})

// 中文那份必须与种子一字不差:对不上就换不出译名,而且中文界面上会显示成另一句话。
test('中文渲染结果与种子里的原串完全一致', () => {
  const t = tOf(zh as Record<string, string>)
  for (const stored of Object.keys(BUILTIN_FLOW_NAME)) {
    expect(builtinLabel(BUILTIN_FLOW_NAME, stored, t)).toBe(stored)
  }
  for (const stored of Object.keys(BUILTIN_FLOW_DESC)) {
    expect(builtinLabel(BUILTIN_FLOW_DESC, stored, t)).toBe(stored)
  }
})

test('英文下换成英文说法', () => {
  const t = tOf(en as Record<string, string>)
  expect(builtinLabel(BUILTIN_FLOW_NAME, '标准发布流程', t)).toBe('Standard release flow')
})

// 表里没有的原样返回 —— 用户自己建的流程、以及改过名的出厂流程,显示的必须是他键入
// 的那串。这条是整个取舍的落脚点:能翻译的只有随产品出厂的那几行。
test('用户自己的名字原样显示,不被翻译', () => {
  const t = tOf(en as Record<string, string>)
  expect(builtinLabel(BUILTIN_FLOW_NAME, '我们组的发布流程', t)).toBe('我们组的发布流程')
  expect(builtinLabel(BUILTIN_FLOW_NAME, 'Standard release flow ', t)).toBe('Standard release flow ')
  expect(builtinLabel(BUILTIN_FLOW_DESC, '', t)).toBe('')
})

// 两种语言都要有,否则英文下 t() 返回空串,输入框会变成空的 —— 比显示中文更糟。
// 移植时这四个键在 React 侧一个都不存在,是补上的。
test('用到的文案键在两种语言里都存在', () => {
  const keys = [...Object.values(BUILTIN_FLOW_NAME), ...Object.values(BUILTIN_FLOW_DESC)]
  expect(keys.length).toBeGreaterThan(0)
  for (const k of keys) {
    expect(String((zh as Record<string, string>)[k] ?? ''), `zh 缺 ${k}`).not.toBe('')
    expect(String((en as Record<string, string>)[k] ?? ''), `en 缺 ${k}`).not.toBe('')
  }
})
