import { test, expect } from '@playwright/test'
import fs from 'node:fs'
import path from 'node:path'
import { parseForESLint, getStaticJSONValue } from 'jsonc-eslint-parser'

// 两份词条必须有完全相同的键。
//
// 这条规则是被反复报上来的同一件事换来的:界面切到英文,某几处仍然是中文。它的成因
// 几乎总是同一个 —— 加了新文案,只加进了 zh.json5。
//
// 为什么单靠人看不住:
//
//   - vue-i18n 取不到键时**不报错**,它把键名本身当文案打出来。于是英文界面上出现的
//     是 `setMetaEnabled` 这样一个词,而不是一句报错 —— 而漏掉的那半屏文案在中文下
//     一切正常,写代码的人根本不会切过去看。
//   - 反方向同样会:只加进 en.json5,中文界面就露出英文。
//   - 键数已经到 1397,靠 diff 两个文件对齐是不现实的。
//
// 这里**读真实的词条文件**,理由和 outText.spec.ts 一样:要挡的正是"漏了一份",
// 而一份自造的 stub 永远发现不了那件事。
//
// 它不检查两件事,说清楚以免被当成挡住了:
//   1. 模板里 $t('x') 用到但两份都没有的键 —— 那要静态扫模板,而项目里有 $t(tb.label)、
//      labelOf(k, $t) 这类动态键,扫出来的东西真假混杂,比没有更糟;
//   2. 英文那一份里**其实还是中文**的值。翻译质量不是键结构能表达的。

function catalogue(file: string): Record<string, unknown> {
  const p = path.resolve('src/locales', file)
  const parsed = parseForESLint(fs.readFileSync(p, 'utf8'), { jsonSyntax: 'json5' } as never)
  return getStaticJSONValue(parsed.ast as never) as unknown as Record<string, unknown>
}

/** 把嵌套的词条摊平成 `outText.execAffected` 这样的完整键路径。 */
function flatten(o: Record<string, unknown>, prefix = ''): string[] {
  return Object.entries(o).flatMap(([k, v]) =>
    v && typeof v === 'object' && !Array.isArray(v)
      ? flatten(v as Record<string, unknown>, `${prefix}${k}.`)
      : [`${prefix}${k}`],
  )
}

const zh = flatten(catalogue('zh.json5'))
const en = flatten(catalogue('en.json5'))

test('中英词条的键完全一致 —— 漏一个,那一处就在另一种语言下露出键名', () => {
  const zhSet = new Set(zh)
  const enSet = new Set(en)
  const onlyZh = zh.filter((k) => !enSet.has(k))
  const onlyEn = en.filter((k) => !zhSet.has(k))
  expect(onlyZh, `这些键只有 zh.json5 有,英文界面会显示成键名本身:\n  ${onlyZh.join('\n  ')}`).toEqual([])
  expect(onlyEn, `这些键只有 en.json5 有,中文界面会显示成键名本身:\n  ${onlyEn.join('\n  ')}`).toEqual([])
})

test('同一份词条里没有重复键 —— 后一个会静静盖掉前一个', () => {
  // JSON5 允许重复键,解析之后只剩最后那个,所以摊平的键路径里是看不出来的:
  // 得回到语法树上数一遍。重复键的表现是"我明明改了文案,界面没变"。
  //
  // **必须按对象分组**,不能按行数文本。顶层的 capApprove 和 ruleText.capApprove 是
  // 两个不同的键,而按文本数会把它们报成重复 —— 第一版就是这么错的,它报出来的两条
  // 全是误报。只有同一个对象里的同名键才会互相覆盖。
  for (const file of ['zh.json5', 'en.json5']) {
    const raw = fs.readFileSync(path.resolve('src/locales', file), 'utf8')
    const ast = parseForESLint(raw, { jsonSyntax: 'json5' } as never).ast
    const dup: string[] = []
    let objects = 0

    const walk = (node: any, prefix: string) => {
      if (!node || typeof node !== 'object') return
      if (node.type === 'JSONObjectExpression') {
        objects++
        const seen = new Map<string, number>()
        for (const prop of node.properties ?? []) {
          const k: string = prop.key?.type === 'JSONIdentifier' ? prop.key.name : String(prop.key?.value)
          const line: number = prop.loc?.start?.line ?? 0
          if (seen.has(k)) dup.push(`${prefix}${k}(第 ${seen.get(k)} 行与第 ${line} 行)`)
          else seen.set(k, line)
          walk(prop.value, `${prefix}${k}.`)
        }
        return
      }
      if (node.type === 'JSONArrayExpression') {
        for (const el of node.elements ?? []) walk(el, prefix)
        return
      }
      if (node.type === 'Program' || node.type === 'JSONExpressionStatement') {
        walk(node.body?.[0] ?? node.expression, prefix)
      }
    }
    walk(ast, '')

    // 自检:一个对象都没走到就说明 AST 的形状和这里假设的不一样,
    // 而那时这条用例检查的是空集、永远绿着。
    expect(objects, `${file} 里一个对象都没走到,这条用例等于没生效`).toBeGreaterThan(1)
    expect(dup, `${file} 里有重复键,后一个会盖掉前一个:\n  ${dup.join('\n  ')}`).toEqual([])
  }
})

test('这一轮新加的元数据同步文案两份都在', () => {
  // 一条具体的用例,钉住上面那条通用规则确实覆盖了新代码 —— 通用断言在两份都漏掉
  // 同一个键时是绿的,而那正是"整块功能没文案"的样子。
  const keys = [
    'setMeta', 'setMetaSub', 'setMetaEnabled', 'setMetaEnabledD',
    'setMetaInterval', 'setMetaIntervalD', 'setMetaConcurrency', 'setMetaConcurrencyD',
    'setMetaNote', 'setMetaNoteFirst', 'setMetaNoteSkip', 'setMetaNoteStale', 'setMetaGo',
    'connMetaSync', 'connMetaSyncOk', 'connMetaSyncBad', 'connMetaTables',
  ]
  for (const k of keys) {
    expect(zh, `zh.json5 缺 ${k}`).toContain(k)
    expect(en, `en.json5 缺 ${k}`).toContain(k)
  }
})
