import { test, expect } from '@playwright/test'
import fs from 'node:fs'
import path from 'node:path'
import ts from 'typescript'

import { zh } from '../../src/locales/zh'
import { en } from '../../src/locales/en'

// 两份词条必须有完全相同的键。
//
// 这条规则是被反复报上来的同一件事换来的:界面切到英文,某几处仍然是中文。它的成因
// 几乎总是同一个 —— 加了新文案,只加进了 zh.ts。
//
// 与 Vue 版的区别值得说清楚,因为它决定了这个文件还剩下什么职责:
//
//   - 词条从 `.json5` 换成了 `.ts`,而 `en.ts` 写的是 `export const en: Dict = { … }`
//     (`Dict = typeof zh`)。**键少一个,tsc 就报错;多一个,对象字面量的多余属性检查
//     也报错。** 也就是说"键集合一致"这件事已经由类型系统挡住了,而且挡得比测试早。
//   - 所以这里的第一条用例是**对那道类型闸门的自检**:它在 tsc 之外再验一次,万一哪天
//     有人把 `en` 的类型标注去掉、或者换成 `Record<string, string>`,这条会当场变红,
//     而不是等某个英文界面上冒出一个键名才被发现。
//   - 第二条用例挡的是类型系统**挡不住的**那件事:同一个对象里的**重复键**。TS 对重复的
//     字面量属性只在少数情形下报错,而 `typeof zh` 取到的是最后那个 —— 表现是"我明明改
//     了文案,界面没变"。要看见它必须回到语法树上数一遍。
//
// 它不检查两件事,说清楚以免被当成挡住了:
//   1. 代码里 `t('x')` 用到但两份都没有的键 —— 那要静态扫组件,而项目里有 `t(\`stType_${type}\`)`
//      这类动态键,扫出来的东西真假混杂,比没有更糟;
//   2. 英文那一份里**其实还是中文**的值。翻译质量不是键结构能表达的。

const DIR = path.resolve('src/locales')

test('中英词条的键完全一致 —— 漏一个,那一处就在另一种语言下露出键名', () => {
  const zhKeys = Object.keys(zh)
  const enKeys = Object.keys(en as Record<string, string>)
  const zhSet = new Set(zhKeys)
  const enSet = new Set(enKeys)
  const onlyZh = zhKeys.filter((k) => !enSet.has(k))
  const onlyEn = enKeys.filter((k) => !zhSet.has(k))

  expect(onlyZh, `这些键只有 zh.ts 有,英文界面会显示成键名本身:\n  ${onlyZh.join('\n  ')}`).toEqual([])
  expect(onlyEn, `这些键只有 en.ts 有,中文界面会显示成键名本身:\n  ${onlyEn.join('\n  ')}`).toEqual([])
  // 自检:词条一个都没读到时上面两条都是绿的,而那时这条用例等于不存在。
  expect(zhKeys.length, '一个词条都没读到').toBeGreaterThan(500)
})

/** 词条对象里重复出现的键,连同它出现过的行号。 */
function duplicateKeys(file: string): { dups: string[]; counted: number } {
  const src = ts.createSourceFile(
    file, fs.readFileSync(path.join(DIR, file), 'utf8'), ts.ScriptTarget.ES2022, true,
  )
  const dups: string[] = []
  let counted = 0

  const walk = (node: ts.Node) => {
    if (ts.isObjectLiteralExpression(node)) {
      const seen = new Map<string, number>()
      for (const prop of node.properties) {
        if (!prop.name) continue
        const name = ts.isIdentifier(prop.name) || ts.isStringLiteral(prop.name)
          ? prop.name.text : prop.name.getText(src)
        const line = src.getLineAndCharacterOfPosition(prop.getStart(src)).line + 1
        counted++
        if (seen.has(name)) dups.push(`${name}(第 ${seen.get(name)} 行与第 ${line} 行)`)
        else seen.set(name, line)
      }
    }
    ts.forEachChild(node, walk)
  }
  walk(src)
  return { dups, counted }
}

test('同一份词条里没有重复键 —— 后一个会静静盖掉前一个', () => {
  for (const file of ['zh.ts', 'en.ts']) {
    const { dups, counted } = duplicateKeys(file)
    // 自检:一个属性都没走到就说明 AST 的形状和这里假设的不一样,
    // 而那时这条用例检查的是空集、永远绿着。
    expect(counted, `${file} 里一个属性都没走到,这条用例等于没生效`).toBeGreaterThan(500)
    expect(dups, `${file} 里有重复键,后一个会盖掉前一个:\n  ${dups.join('\n  ')}`).toEqual([])
  }
})

test('这一轮补进来的终端日志与导入表文案两份都在', () => {
  // 一条具体的用例,钉住上面那条通用规则确实覆盖了新代码 —— 通用断言在两份都漏掉
  // 同一个键时是绿的,而那正是"整块功能没文案"的样子(`src/lib/transcript.ts` 与
  // `src/lib/connectionImport.ts` 此前就是:它们 t() 出来的全是键名)。
  const keys = [
    'tsTitle', 'tsInstance', 'tsDb', 'tsUser', 'tsExportedAt', 'tsCount', 'tsDropped', 'tsRedacted',
    'ciMissingCols', 'ciRowMissing', 'ciBadEnv', 'ciBadPolicy',
  ]
  for (const k of keys) {
    expect(zh, `zh.ts 缺 ${k}`).toHaveProperty(k)
    expect(en, `en.ts 缺 ${k}`).toHaveProperty(k)
  }
})
