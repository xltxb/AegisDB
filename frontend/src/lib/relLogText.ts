// relLogText — 发布阶段日志与发布单错误条按读者的语言显示(`rlLog.`)。
//
// 日志是服务端在 Go 里拼的中文,它仍是规范记录(对外接口、Webhook、审计读的都是它);同一段话
// 另以逐行 RuleRef 下发(stage.logRefs / release.errorRef,见后端 model/release_log_ref.go)。
// 一个 ref 一行,args.cat === '1' 的接在上一行末尾。
//
// 嵌套:带执行器那句话的行,parts[0] 是那句话的 ref,命名空间在 args.ns(outText / ruleText /
// rlLog);渲染不出来就用 args.out / args.rule —— 驱动报错这类原文本来也不翻。
//
// 回落:任何一行的 code 这一版认不出,整段回落到中文原串,不拼半句(与收单检查 preNote 同一
// 取舍);0059 之前的老单没有 refs,同样显示原串。

import type { RuleRef } from '@/types'
import { renderRule, renderOutText } from './ruleText'
import { oscItemText } from './oscText'

type Translate = (key: string, named?: Record<string, unknown>) => string
type HasKey = (key: string) => boolean

export interface RelLogI18n {
  t: Translate
  te: HasKey
  locale: string
  /** 阶段类型 → 读者语言的阶段名;stored 是库里的规范名。 */
  stage: (type: string, stored: string) => string
  /** 规范审查规则名(code, 库里的名字)→ 读者语言。 */
  rule: (code: string, stored: string) => string
}

const UNKNOWN = Symbol('unknown')

function one(ref: RuleRef, i18n: RelLogI18n): string | typeof UNKNOWN {
  const key = 'rlLog.' + ref.code
  if (!ref.code || !i18n.te(key)) return UNKNOWN
  const a: Record<string, string> = { ...(ref.args || {}) }
  const parts = ref.parts || []
  let inner = a.out ?? a.rule ?? a.note ?? ''
  if (ref.code === 'oscBlocked') {
    inner = parts.length
      ? parts.map((p) => oscItemText({ code: p.args?.code || '', reason: p.args?.reason || '' }, i18n.locale, i18n.t, i18n.te)).join('; ')
      : a.reason || ''
  } else if (ref.code === 'errStageFailed') {
    a.stage = i18n.stage(a.type || '', a.stage || '')
    const lines = parts.map((p) => one(p, i18n))
    inner = lines.length && !lines.includes(UNKNOWN) ? (lines as string[]).map((s) => s.trim()).join(' ') : a.log || ''
  } else if (parts.length) {
    const ns = a.ns || 'rlLog'
    // 审查发现的消息(apiMsg):填的是 {msg},不是 {inner};认不出就保留原文
    if (ns === 'apiMsg') a.msg = renderOutText(parts[0], a.msg || '', i18n)
    else if (ns === 'outText') inner = renderOutText(parts[0], inner, i18n)
    else if (ns === 'ruleText') inner = renderRule(parts[0], inner, i18n)
    else {
      const r = one(parts[0], i18n)
      if (r !== UNKNOWN) inner = r
    }
  }
  if (ref.code === 'reviewFinding') a.name = i18n.rule(a.rule || '', a.name || '')
  if (ref.code === 'oscWarn') a.reason = oscItemText({ code: a.code || '', reason: a.reason || '' }, i18n.locale, i18n.t, i18n.te)
  return i18n.t(key, { ...a, inner })
}

function parse<T>(raw: string | undefined | null): T | null {
  if (!raw) return null
  try { return JSON.parse(raw) as T } catch { return null }
}

/** 一段阶段日志。refs 缺失或有认不出的 code → 原串。 */
export function renderRelLog(refsJSON: string | undefined | null, canonical: string, i18n: RelLogI18n): string {
  const refs = parse<RuleRef[]>(refsJSON)
  if (!Array.isArray(refs) || !refs.length) return canonical
  let out = ''
  for (let i = 0; i < refs.length; i++) {
    const s = one(refs[i], i18n)
    if (s === UNKNOWN) return canonical
    if (i > 0 && refs[i].args?.cat !== '1') out += '\n'
    out += s
  }
  return out
}

/** 发布单的错误条。 */
export function renderRelErr(refJSON: string | undefined | null, canonical: string, i18n: RelLogI18n): string {
  const ref = parse<RuleRef>(refJSON)
  if (!ref || typeof ref !== 'object') return canonical
  const s = one(ref, i18n)
  return s === UNKNOWN ? canonical : s
}
