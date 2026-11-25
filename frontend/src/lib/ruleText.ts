// ruleText — render a verdict's rule in the reader's language.
//
// The gateway assembles the rule name in Go ("高危命令字典 · PROD 禁止直接执行"),
// and that string is the canonical record: it is what the approval ticket and the
// audit chain store, and it must not change with whoever happens to be looking at
// it. But it was also the only thing the terminal had to print, so an English
// session showed one Chinese sentence in the middle of an English line.
//
// So the server now sends the same statement twice: the canonical string, and a
// `RuleRef` — a stable code plus its arguments — which this renders through i18n.
// Rules nest (a batch holds one ref per gated statement; an execution window
// holds the verdict it relaxed), so this recurses and both halves get translated.
//
// The fallback matters as much as the rendering: a ref whose code this build does
// not know renders as the server's string rather than as a key name. New codes
// ship with the backend, and a frontend one deploy behind must still be able to
// tell an operator why their command was stopped.

import type { RuleRef } from '@/types'
import { isApiMsgCode, tryRenderApiMsg } from './apiMsg'

type Translate = (key: string, named?: Record<string, unknown>) => string
type HasKey = (key: string) => boolean

/** Separator between the per-statement hits of a batch — matches the server's. */
const BATCH_SEP = ' + '

export interface RuleI18n {
  t: Translate
  te: HasKey
}

/**
 * Render `ref` in the current locale, falling back to `canonical` (the server's
 * own string) when the ref is missing or names a code this build has no text for.
 */
export function renderRule(ref: RuleRef | undefined | null, canonical: string, i18n: RuleI18n): string {
  return renderRef(ref, i18n, 'ruleText.') || canonical || ''
}

/**
 * Same mechanism, second use: the text the terminal PRINTS after a command runs
 * ("执行成功 · N 行受影响", "· 目标实例处于维护态"). Assembled in Go for the record,
 * so an English session used to get one Chinese line in the middle of its output.
 *
 * Kept in its own `outText.` namespace rather than sharing `ruleText.`: these are
 * not rules, and one flat pool would make a code collision silently render the
 * wrong sentence.
 */
export function renderOutText(ref: RuleRef | undefined | null, canonical: string, i18n: RuleI18n): string {
  return renderRef(ref, i18n, 'outText.') || canonical || ''
}

/**
 * Third use: why a business user's query was refused (`bizText.`). The server's
 * `msg` is the canonical Chinese sentence; `data.reasonRef` is the same reason as
 * a code. The data-query page also keys its "refresh my grants" decision on the
 * code (see BIZ_GRANT_GONE) instead of pattern-matching the Chinese wording.
 */
export function renderBizReason(ref: RuleRef | undefined | null, canonical: string, i18n: RuleI18n): string {
  return renderRef(ref, i18n, 'bizText.') || canonical || ''
}

/**
 * Fourth use: an approval's description (`apText.`). For an execution-window
 * ticket the server's `command` is a Chinese sentence ("开启执行窗口「…」"), stored
 * for the record and the Lark card; `commandRef` is the same sentence as a code,
 * with the window's schedule nested as its part. SQL tickets carry no ref — their
 * command is the statement itself and falls straight through.
 */
export function renderApText(ref: RuleRef | undefined | null, canonical: string, i18n: RuleI18n): string {
  return renderRef(ref, i18n, 'apText.') || canonical || ''
}

/**
 * Fifth use: a release pre-check finding (`preText.`). `err` arguments carry the
 * driver's own words and are interpolated untranslated.
 */
export function renderPreText(ref: RuleRef | undefined | null, canonical: string, i18n: RuleI18n): string {
  return renderRef(ref, i18n, 'preText.') || canonical || ''
}

/** Refusal codes meaning the grant or its instance is gone — the grant list is stale. */
export const BIZ_GRANT_GONE = new Set(['bizNoGrant', 'bizNotSource', 'bizNoSource'])

function renderRef(ref: RuleRef | undefined | null, i18n: RuleI18n, prefix: string): string {
  if (!ref || !ref.code) return ''

  // A batch is a list, not a sentence: it has no text of its own, only its parts.
  if (ref.code === 'batch') {
    const parts = (ref.parts || []).map((p) => renderRef(p, i18n, prefix)).filter(Boolean)
    return parts.join(BATCH_SEP)
  }

  // 网关自己的话(apimsg,code 是中文原句的哈希)也会出现在这里:脚本执行结果、编译错误清单……
  // 它有自己的命名空间与按位置的参数;嵌在里面的终端输出再回到这个命名空间渲染。
  if (isApiMsgCode(ref.code)) {
    return tryRenderApiMsg(ref, { t: (k, list) => i18n.t(k, list as never), te: i18n.te }, (r) => renderRef(r, i18n, prefix))
  }

  const key = prefix + ref.code
  if (!i18n.te(key)) return '' // unknown code → caller falls back to the canonical string

  // `inner` is the nested rule — the statement's own rule inside a batch hit, the
  // pre-relaxation verdict inside an execution window. Rendered first so the
  // outer message can interpolate it.
  // With no nested ref (an older server, or an outcome that never had a code) a
  // message that carries the raw `err` shows that instead of an empty tail.
  const part = ref.parts && ref.parts.length ? renderRef(ref.parts[0], i18n, prefix) : ''
  const inner = part || ref.args?.err || ''
  // `err` 是原文;服务端认得出这句原文时(网关自己的话,apimsg)另附一个 part —— 有译文就用译文。
  const named: Record<string, unknown> = { ...(ref.args || {}), inner }
  if (part && ref.args?.err !== undefined) named.err = part
  return i18n.t(key, named)
}
