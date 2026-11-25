import { useCallback, useSyncExternalStore } from 'react'
import { i18n } from '@/locales'
import type { TFn, TeFn } from '@/locales/core'

const getLocale = () => i18n.global.locale.value

/** 当前语言的 t / te。切换语言时组件重新渲染,t 也换一个新引用(可放心进依赖数组)。 */
export function useI18n(): { t: TFn; te: TeFn; locale: string } {
  const locale = useSyncExternalStore(i18n.subscribe, getLocale)
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const t = useCallback<TFn>((k, n, o) => i18n.global.t(k, n, o), [locale])
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const te = useCallback<TeFn>((k, l) => i18n.global.te(k, l), [locale])
  return { t, te, locale }
}
