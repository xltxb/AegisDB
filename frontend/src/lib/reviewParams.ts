// 规范规则的参数表单与"出处按库显示"。
//
// 参数说明书在后端(review/params_spec.go,经 /sql-review/catalog 的 params 下发):哪些键、
// 什么类型、留空时按什么跑。这里只负责两头的转换 —— 存储用的 JSON ↔ 表单里一格一格的
// 文本 —— 和展示用的默认值。纯函数,不碰组件状态,单测直接调。
import type { ReviewCatalog, ReviewParamSpec, ReviewRule } from '@/types'

/** 这条规则有哪些参数。内置规则不在说明书里 = 没有可调参数。 */
export function paramSpecsFor(catalog: ReviewCatalog, r: Pick<ReviewRule, 'code' | 'kind'>): ReviewParamSpec[] {
  const all = catalog.params || {}
  return (r.kind === 'regex' ? all._regex : all[r.code]) || []
}

/** 留空时生效的值;选了具体的库就按那个库(Oracle 的标识符上限是 30)。 */
export function effectiveDefault(ps: ReviewParamSpec, dialect: string): unknown {
  if (dialect && dialect !== 'all' && ps.dialectDefaults && dialect in ps.dialectDefaults) {
    return ps.dialectDefaults[dialect]
  }
  return ps.default
}

/** 把一个参数值写成表单里的文本:列表一行一个。 */
export function formatParamValue(ps: ReviewParamSpec, v: unknown): string {
  if (v === undefined || v === null) return ''
  if (ps.type === 'list') return Array.isArray(v) ? v.map(String).join('\n') : String(v)
  if (ps.type === 'bool') return v === true || v === 'true' ? 'true' : v === false || v === 'false' ? 'false' : ''
  return String(v)
}

function parseObject(raw: unknown): Record<string, unknown> {
  // 接口给的是 JSON 字符串;但别处(旧数据、测试桩)递来已解析的对象也照收,不让整页渲染崩掉。
  if (raw && typeof raw === 'object' && !Array.isArray(raw)) return { ...(raw as Record<string, unknown>) }
  if (typeof raw !== 'string' || !raw.trim()) return {}
  try {
    const v = JSON.parse(raw)
    return v && typeof v === 'object' && !Array.isArray(v) ? v : {}
  } catch {
    return {}
  }
}

/** 存储的 JSON → 表单草稿。没写的键是空串(= 用默认值)。 */
export function paramsToDraft(raw: string, specs: ReviewParamSpec[]): Record<string, string> {
  const obj = parseObject(raw)
  const out: Record<string, string> = {}
  for (const ps of specs) out[ps.key] = formatParamValue(ps, obj[ps.key])
  return out
}

/**
 * 表单草稿 → 存储的 JSON。空格子不写(留给默认值);说明书之外的键原样保留(不替人删
 * 不认识的东西,保存时后端会说清楚)。全空返回 '',表示"全部按默认"。
 */
export function draftToParams(draft: Record<string, string>, specs: ReviewParamSpec[], raw = ''): string {
  const obj = parseObject(raw)
  for (const ps of specs) {
    const s = (draft[ps.key] ?? '').trim()
    delete obj[ps.key]
    if (!s) continue
    switch (ps.type) {
      case 'int': {
        const n = Number(s)
        obj[ps.key] = Number.isFinite(n) && /^\d+$/.test(s) ? n : s // 非数字原样交给后端报错
        break
      }
      case 'list':
        obj[ps.key] = s.split(/[\n,，]/).map((x) => x.trim()).filter(Boolean)
        break
      case 'bool':
        obj[ps.key] = s === 'true'
        break
      default:
        obj[ps.key] = s
    }
  }
  return Object.keys(obj).length ? JSON.stringify(obj) : ''
}

/** 列表里一行显示参数的生效值:写了的用写的,没写的用默认值。 */
export function effectiveParams(raw: string, specs: ReviewParamSpec[], dialect: string) {
  const obj = parseObject(raw)
  return specs.map((ps) => {
    const set = obj[ps.key] !== undefined && obj[ps.key] !== ''
    return { spec: ps, value: set ? obj[ps.key] : effectiveDefault(ps, dialect), isDefault: !set }
  })
}

// ---------------------------------------------------------------- 出处按库

// 出处写成 "MySQL規範 §十一 / TIDB規範 §十 / DWS对象设计和业务开发规范 RULE 58"。每段开头的
// 文档名说明它是哪个库的规范;没有文档名的段(如 "RULE 39")沿用前一段的。
function docDialect(part: string): string | null {
  const p = part.trim()
  if (/^mysql/i.test(p)) return 'mysql'
  if (/^tidb/i.test(p)) return 'tidb'
  if (/^oracle/i.test(p)) return 'oracle'
  if (/^dws/i.test(p)) return 'dws'
  return null
}

/**
 * 选了某个库时,只显示这个库的规范出处。一条通用规则若只有别的库的规范要求它
 * (例:"UPDATE/DELETE 必须带 WHERE"只出自 DWS RULE 46),在 MySQL 页上它就是平台内置,
 * 分级也随之显示为"平台内置" —— 不再拿 DWS 的【強制】去说 MySQL。
 * 运维手写的、认不出文档名的出处照常显示。
 */
export function specForDialect(r: Pick<ReviewRule, 'spec' | 'specRef'>, dialect: string): { spec: string; specRef: string } {
  const spec = r.spec || ''
  const ref = r.specRef || ''
  if (!dialect || dialect === 'all' || !ref) return { spec, specRef: ref }
  const parts = ref.split(' / ')
  let cur: string | null = null
  let known = false
  const kept: string[] = []
  for (const part of parts) {
    const d = docDialect(part)
    if (d) {
      cur = d
      known = true
    } else if (cur === null) {
      kept.push(part) // 认不出的手写出处
      continue
    }
    if (cur === dialect) kept.push(part)
  }
  if (!known) return { spec, specRef: ref }
  if (!kept.length) return { spec: '', specRef: '' }
  return { spec, specRef: kept.join(' / ') }
}
