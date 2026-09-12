import { test, expect } from '@playwright/test'
import fs from 'node:fs'
import path from 'node:path'
import ts from 'typescript'

// 用户可见的文案一律走 i18n,不写死在组件里。
//
// 这条规则和 localeParity 是一对:那一条管"两份词条的键要对齐",这一条管"文案有没有
// 进词条"。少了这一条,切到英文的界面上仍会冒出中文 —— 而写的人看不见,因为他本来
// 就在用中文界面开发。
//
// 用 TS 的 AST 而不是 grep:中文绝大多数出现在**注释**里(这个代码库的注释就是中文
// 写的),grep 分不开注释和字面量,而 AST 里根本没有注释这回事。
//
// 例外只有一类:**那串中文是数据,不是文案**。
const DATA_NOT_COPY = new Set([
  // 后端内置名 → i18n 键的映射。键是**存进库的那串中文**,用来判断"这个名字是不是
  // 我们自己写的"(用户改过名就不再匹配,于是显示他自己的名字)。翻译它等于把这张
  // 表本身翻译掉,映射当场失效。
  'src/lib/builtinNames.ts',
  'src/lib/envTierLabels.ts',
])
// 语言自己的名字按它自己的语言写 —— 英文界面上的中文选项也该写「中文」,那正是给
// 看不懂当前界面语言的人找路用的。
const SELF_NAMED = new Set(['中文'])

const CJK = /[\u4e00-\u9fff]/

function sourceFiles(dir: string): string[] {
  return fs.readdirSync(dir, { withFileTypes: true }).flatMap((e) => {
    const p = path.join(dir, e.name)
    if (e.isDirectory()) return e.name === 'locales' ? [] : sourceFiles(p)
    return /\.(ts|tsx)$/.test(e.name) ? [p] : []
  })
}

/** 一个文件里所有**字面量**(含 JSX 文本、模板串)中出现的中文。注释不在 AST 里。 */
function chineseLiterals(file: string): { line: number; text: string }[] {
  const src = fs.readFileSync(file, 'utf8')
  const sf = ts.createSourceFile(
    file, src, ts.ScriptTarget.Latest, true,
    file.endsWith('.tsx') ? ts.ScriptKind.TSX : ts.ScriptKind.TS,
  )
  const out: { line: number; text: string }[] = []
  const visit = (n: ts.Node) => {
    let text: string | null = null
    if (ts.isStringLiteral(n) || ts.isNoSubstitutionTemplateLiteral(n)) text = n.text
    else if (ts.isJsxText(n)) text = n.text
    else if (ts.isTemplateHead(n) || ts.isTemplateMiddle(n) || ts.isTemplateTail(n)) text = n.text
    if (text && CJK.test(text) && !SELF_NAMED.has(text.trim())) {
      out.push({ line: sf.getLineAndCharacterOfPosition(n.getStart(sf)).line + 1, text: text.trim() })
    }
    ts.forEachChild(n, visit)
  }
  visit(sf)
  return out
}

test('用户可见文案不写死在组件里', () => {
  const offenders: string[] = []
  for (const file of sourceFiles('src')) {
    const rel = file.replace(/\\/g, '/')
    if (DATA_NOT_COPY.has(rel)) continue
    for (const h of chineseLiterals(file)) {
      offenders.push(`${rel}:${h.line}  ${h.text.slice(0, 40)}`)
    }
  }
  expect(offenders, `这些文案绕过了 i18n:\n${offenders.join('\n')}`).toEqual([])
})
