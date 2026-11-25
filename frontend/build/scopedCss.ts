// 组件级样式隔离,等价于 Vue 的 <style scoped>。
//
// 用法:Foo.tsx 里 `import './Foo.scoped.css'`。构建时:
//   - Foo.tsx 里的每个 JSX 元素带上 data-s-<hash>(组件元素也带,由组件 spread 到自己的根上 ——
//     父组件因此能像在 Vue 里一样给子组件的根元素写样式);
//   - Foo.scoped.css 里的选择器在最后一个复合选择器上加 [data-s-<hash>],@keyframes 改名。
// 支持 :deep(x) 与 :global(x),语义同 Vue。
import { createHash } from 'node:crypto'
import path from 'node:path'
import MagicString from 'magic-string'
import postcss from 'postcss'
import selectorParser from 'postcss-selector-parser'
import { parseSync, type Plugin } from 'vite'

const SCOPED = /\.scoped\.css$/
const IMPORT = /import\s+['"](\.\/[^'"]+\.scoped\.css)['"]/

// 不会把多余 props 交给 DOM 的内建组件,带上属性只会触发告警。
const SKIP = new Set(['Fragment', 'Suspense', 'StrictMode', 'Activity', 'Profiler'])

function scopeId(root: string, cssFile: string): string {
  const rel = path.relative(root, cssFile).split(path.sep).join('/')
  return `data-s-${createHash('sha256').update(rel).digest('hex').slice(0, 8)}`
}

// <n.icon/> 这类取自变量的组件也要带上;<X.Provider> / <X.Consumer> 不渲染 DOM。
function takesScope(name: any): boolean {
  if (name.type === 'JSXIdentifier') return !SKIP.has(name.name)
  return name.type === 'JSXMemberExpression' && !/^(Provider|Consumer)$/.test(name.property.name)
}

/** 给一个 .tsx 文件里的 JSX 开标签加上 scope 属性。 */
export function scopeJsx(code: string, file: string, id: string): MagicString {
  const { program, errors } = parseSync(file, code)
  if (errors.length) throw new Error(`${file}: ${errors[0].message}`)
  const out = new MagicString(code)
  const visit = (node: any): void => {
    if (!node || typeof node !== 'object') return
    if (Array.isArray(node)) { node.forEach(visit); return }
    if (node.type === 'JSXOpeningElement' && takesScope(node.name)) {
      const end = node.typeArguments?.end ?? node.name.end
      out.appendLeft(end, ` ${id}=""`)
    }
    for (const k in node) if (k !== 'parent') visit(node[k])
  }
  visit(program)
  return out
}

/** 把一份样式表的选择器限定到 scope 里。 */
export function scopeCss(css: string, file: string, id: string): string {
  const suffix = id.slice('data-s-'.length)
  const frames = new Map<string, string>()
  const root = postcss.parse(css, { from: file })

  root.walkAtRules(/keyframes$/, (r) => {
    frames.set(r.params, `${r.params}-${suffix}`)
    r.params = `${r.params}-${suffix}`
  })
  if (frames.size) {
    root.walkDecls(/^(-\w+-)?animation(-name)?$/, (d) => {
      d.value = d.value.replace(/[\w-]+/g, (w) => frames.get(w) ?? w)
    })
  }

  const attr = () => selectorParser.attribute({ attribute: id, value: undefined, raws: {}, quoteMark: null } as never)
  const rewrite = selectorParser((sels) => {
    sels.each((sel) => {
      const globalAt = sel.nodes.find((n) => n.type === 'pseudo' && n.value === ':global')
      if (globalAt && globalAt.type === 'pseudo') {
        const inner = String(globalAt.nodes[0] ?? '')
        sel.removeAll()
        sel.append(selectorParser.tag({ value: inner.trim() }))
        return
      }
      let anchor: selectorParser.Node | null = null
      for (const n of [...sel.nodes]) {
        if (n.type === 'pseudo' && n.value === ':deep') {
          const inner = n.nodes[0]
          const prev = n.prev()
          // `:deep(x)` 打头、或紧跟在选择器后面:与 Vue 一样当作后代选择器
          if (!prev || prev.type !== 'combinator') n.parent!.insertBefore(n as never, selectorParser.combinator({ value: ' ' }))
          for (const c of [...inner.nodes]) n.parent!.insertBefore(n as never, c as never)
          n.remove()
            break
        }
        if (n.type !== 'combinator' && n.type !== 'comment' && (n.type !== 'pseudo' || n.value === ':is' || n.value === ':where')) anchor = n
      }
      if (anchor) {
        // 属性必须是最后一个复合选择器的一部分,放在它的伪类 / 伪元素之前
        anchor.spaces.after = ''
        sel.insertAfter(anchor as never, attr() as never)
      } else {
        sel.prepend(attr() as never)
      }
    })
  })

  root.walkRules((rule) => {
    const p = rule.parent
    if (p && p.type === 'atrule' && /keyframes$/.test((p as postcss.AtRule).name)) return
    if (p && p.type === 'rule') return // 嵌套规则:外层已经限定
    rule.selector = rewrite.processSync(rule.selector)
  })
  return root.toString()
}

export function scopedCss(): Plugin {
  let root = process.cwd()
  return {
    name: 'aegisdb:scoped-css',
    enforce: 'pre',
    configResolved(c) { root = c.root },
    transform(code, rawId) {
      const id = rawId.split('?')[0]
      if (SCOPED.test(id)) return { code: scopeCss(code, id, scopeId(root, id)), map: null }
      if (!id.endsWith('.tsx')) return null
      const m = IMPORT.exec(code)
      if (!m) return null
      const s = scopeJsx(code, id, scopeId(root, path.resolve(path.dirname(id), m[1])))
      return { code: s.toString(), map: s.generateMap({ hires: true, source: id }) }
    },
  }
}
