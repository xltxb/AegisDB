// vue-i18n 的内核(@intlify/core-base,不依赖 Vue)包成一个带订阅的实例。
//
// 不持有词条、不碰 localStorage:单元测试在 Node 里直接用它装真实的词条文件
// (见 tests/unit/stubs/locales.ts),应用里由 locales/index.ts 装配。
import {
  compile, createCoreContext, fallbackWithLocaleChain, registerLocaleFallbacker,
  registerMessageCompiler, registerMessageResolver, resolveValue, translate,
} from '@intlify/core-base'

registerMessageCompiler(compile)
registerMessageResolver(resolveValue)
registerLocaleFallbacker(fallbackWithLocaleChain)

const tr = translate as unknown as (ctx: unknown, key: string, named?: unknown, opts?: unknown) => unknown

export type Named = Record<string, unknown> | unknown[] | number | null
/** opts.locale:只这一次用哪种语言,不改当前语言。 */
export type TFn = (key: string, named?: any, opts?: { locale?: string }) => string
export type TeFn = (key: string, locale?: string) => boolean

export interface I18nOptions {
  locale: string
  fallbackLocale: string
  /** 词条可以嵌套(如 ruleText.<code>),按 a.b 取。 */
  messages: Record<string, object>
}

export function createI18n(opts: I18nOptions) {
  // core-base 的消息 / 日期 / 数字格式泛型展开过深,这里只用到消息,按最窄的形状收口。
  const ctx = (createCoreContext as (o: unknown) => { locale: string })({
    locale: opts.locale,
    fallbackLocale: opts.fallbackLocale,
    messages: opts.messages,
    missingWarn: false,
    fallbackWarn: false,
    warnHtmlMessage: false,
  })
  const subs = new Set<() => void>()
  const t: TFn = (key, named, o) => {
    const r = o ? tr(ctx, key, named ?? {}, o) : named == null ? tr(ctx, key) : tr(ctx, key, named)
    return typeof r === 'string' ? r : key
  }
  // 与 vue-i18n 一致:只看当前语言有没有这个键,不看回退语言。
  const te: TeFn = (key, locale = ctx.locale) => resolveValue(opts.messages[locale] ?? {}, key) != null
  const locale = {
    get value(): string { return ctx.locale },
    set value(l: string) {
      if (ctx.locale === l) return
      ctx.locale = l
      subs.forEach((f) => f())
    },
  }
  return {
    /** 与 vue-i18n 的 i18n.global 同形:lib / api 里的纯函数直接用它。 */
    global: { t, te, locale, getLocaleMessage: (l: string) => opts.messages[l] },
    subscribe(f: () => void) { subs.add(f); return () => { subs.delete(f) } },
  }
}
