import { test, expect } from '@playwright/test'
import fs from 'node:fs'
import path from 'node:path'

// 可复用组件的**根节点**不能叫一个页面也会拿去用的通用名字。
//
// 这条规则是从 Vue 侧一次具体的故障搬过来的:标签选择弹窗的根节点写着 `class="overlay"`,
// 从用户卡片里打开时继承了页面自己那条 `.overlay { z-index: 50 }`,而它在 DOM 里排在
// 卡片前面 —— 同层级下就被压在**下面**。它每次都渲染正确,只是看不见:没有报错,没有
// 警告,没有任何可察觉的线索。
//
// 换到 React 之后,Vue 的 scoped attribute 那套机制没有了,但**危险变大了而不是变小**:
// 这里的样式全是 `src/styles/theme.css` 里的**全局**类,没有任何作用域。页面写一条
// `.overlay`、组件也用 `.overlay`,两者直接抢同一条规则,谁的选择器更具体 / 谁写在
// 后面谁赢 —— 而这取决于 CSS 里的书写顺序,和组件树没关系。
//
// 现存组件已经在守这条约定了(`c-*` / `cap-*` / `sg-*` / `scan-*` 前缀),所以这条用例
// 把它固定下来,而不是提出一个新要求。
const GENERIC_ROOT_CLASSES = [
  'overlay', 'mask', 'modal', 'dialog', 'card', 'page', 'head', 'body', 'foot',
  'row', 'table', 'badge', 'notice', 'fld', 'panel', 'list', 'item', 'title',
]

function tsxFiles(dir: string): string[] {
  return fs.readdirSync(dir, { withFileTypes: true }).flatMap((e) => {
    const p = path.join(dir, e.name)
    if (e.isDirectory()) return tsxFiles(p)
    return e.name.endsWith('.tsx') ? [p] : []
  })
}

/**
 * 每个 `return` 直接吐出的那个元素上的 class 列表。
 *
 * 一个文件里可以有好几个组件(`Modal` 与它的小零件常常同处一文件),每个都有自己的根,
 * 所以按 `return` 收集而不是只取第一个。模板字符串 / 条件表达式拼出来的 className 跳过:
 * 那种情况下"根类名"不是一个静态事实,硬猜会误报 —— 这条用例宁可漏报也不误报。
 */
function rootClasses(src: string): string[] {
  const out: string[] = []
  for (const m of src.matchAll(/return\s*\(?\s*<([a-zA-Z][\w.]*)([^>]*?)>/g)) {
    const cm = /className="([^"]+)"/.exec(m[2])
    if (cm) out.push(...cm[1].split(/\s+/).filter(Boolean))
  }
  return out
}

test('no reusable component roots itself on a class its pages may style', () => {
  const offenders: string[] = []
  for (const file of tsxFiles('src/components')) {
    for (const cls of rootClasses(fs.readFileSync(file, 'utf8'))) {
      if (GENERIC_ROOT_CLASSES.includes(cls)) {
        offenders.push(`${file} → className="${cls}"`)
      }
    }
  }
  expect(offenders, 'give these roots a component-specific class').toEqual([])
})

// 自检:正则一旦对不上 React 的写法,上面那条就检查空集、永远绿着。
test('the scan actually reaches the component roots', () => {
  const all = tsxFiles('src/components').flatMap((f) => rootClasses(fs.readFileSync(f, 'utf8')))
  expect(all.length, '一个组件根类名都没扫到,这条用例等于没生效').toBeGreaterThan(5)
  // 弹窗是这条规则的由来,它必须在扫描范围内,并且用的是带前缀的名字。
  expect(all).toContain('c-overlay')
})
