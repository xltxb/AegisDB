import { defineStore } from 'pinia'
import { ref } from 'vue'
import { i18n } from '@/locales'

/** 页眉副标题:待渲染的词条,或一段本来就不用翻译的文本(实例名之类)。 */
export type PageSub = string | { key: string; params?: Record<string, unknown> }

// <html lang> 跟着界面语言走。浏览器**原生控件**认的是它:datetime-local 的
// 「年/月/日」占位、日期选择器的月份名、时间控件的 AM/PM,都不归 vue-i18n 管,
// 只看这个属性。它同时也是给读屏软件和翻译工具的声明。
function applyHtmlLang(l: 'zh' | 'en') {
  document.documentElement.lang = l === 'zh' ? 'zh-CN' : 'en'
}

// UI store: language + theme (matches the prototype's lang toggle & dark theme).
export const useUIStore = defineStore('ui', () => {
  const lang = ref<'zh' | 'en'>((localStorage.getItem('vela_lang') as 'zh' | 'en') || 'zh')
  type Theme = 'dark' | 'light' | 'system'
  // Per-user preference (stored in this browser). Defaults to light.
  const theme = ref<Theme>((localStorage.getItem('vela_theme') as Theme) || 'light')
  // 页眉副标题(每个视图自己设),空 = 用路由的 i18n 默认值。
  //
  // 存的是**词条 key + 参数**,不是渲染好的字符串。视图通常在数据加载完那一刻设它,
  // 而那是一次性的:存字符串的话,之后切换语言这行字就永远停在当时那个语言 —— 页面
  // 其余部分都翻过去了,只有它还是中文(这正是它被发现的方式)。渲染推迟到 AppLayout
  // 的 computed 里,那里 t() 跟着 locale 走,切语言就重算。
  //
  // 允许直接给字符串,是给那些本来就不该翻译的副标题用的(终端页写的是实例名和角色)。
  const pageSub = ref<PageSub>('')

  // ---- Toasts (transient user feedback for async success/failure) ----
  type ToastKind = 'success' | 'error' | 'info'
  interface Toast { id: number; kind: ToastKind; msg: string }
  const toasts = ref<Toast[]>([])
  let toastSeq = 0
  function notify(msg: string, kind: ToastKind = 'info', ttl = 3500) {
    const id = ++toastSeq
    toasts.value.push({ id, kind, msg })
    setTimeout(() => dismissToast(id), ttl)
  }
  function dismissToast(id: number) {
    toasts.value = toasts.value.filter((x) => x.id !== id)
  }
  // notifyError extracts a human message from a thrown API error and toasts it.
  // The fallback is a parameter with no default text: a Pinia store has no i18n
  // context, so a literal here would print in one language regardless of the
  // active locale. Callers pass t('actionFailed').
  function notifyError(e: unknown, fallback: string) {
    const msg = (e as any)?.message || (e as any)?.msg || fallback
    notify(String(msg), 'error')
  }

  const prefersDark = () => typeof window !== 'undefined' && window.matchMedia
    ? window.matchMedia('(prefers-color-scheme: dark)').matches : true
  // The effective (resolved) theme actually applied to the DOM.
  function resolvedTheme(): 'dark' | 'light' {
    return theme.value === 'system' ? (prefersDark() ? 'dark' : 'light') : theme.value
  }

  // 只管主题。<html lang> 归 applyHtmlLang —— 它过去藏在这里,于是"谁在写这个属性"
  // 这件事没人能一眼看出来,后来者很容易再加一个写点(我就差点)。
  function applyTheme() {
    document.documentElement.setAttribute('data-theme', resolvedTheme())
  }

  // Follow OS light/dark changes live while "system" is selected.
  if (typeof window !== 'undefined' && window.matchMedia) {
    window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', () => {
      if (theme.value === 'system') applyTheme()
    })
  }

  function setLang(l: 'zh' | 'en') {
    lang.value = l
    i18n.global.locale.value = l
    localStorage.setItem('vela_lang', l)
    applyHtmlLang(l)
  }

  // 首次装载:index.html 里写死的是 zh,而这个浏览器上存着的可能是 en。
  applyHtmlLang(lang.value)

  function toggleLang() {
    setLang(lang.value === 'zh' ? 'en' : 'zh')
  }

  function setTheme(t: Theme) {
    theme.value = t
    localStorage.setItem('vela_theme', t)
    applyTheme()
  }

  // Quick toggle for the topbar: flip to the opposite of what's currently shown
  // (works whether the current preference is explicit or "system").
  function toggleTheme() {
    setTheme(resolvedTheme() === 'dark' ? 'light' : 'dark')
  }

  return {
    lang, theme, pageSub, toasts, setLang, toggleLang, setTheme, toggleTheme, applyTheme, resolvedTheme,
    notify, notifyError, dismissToast,
  }
})
