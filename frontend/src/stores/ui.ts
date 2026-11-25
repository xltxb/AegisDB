import { create } from 'zustand'
import { i18n } from '@/locales'
import { CODE_ADMIN_IP_BLOCKED, CODE_ADMIN_IDLE } from '@/api/codes'

/** 页眉副标题:待渲染的词条,或一段本来就不用翻译的文本(实例名之类)。 */
export type PageSub = string | { key: string; params?: Record<string, unknown> }
export type Lang = 'zh' | 'en'
export type Theme = 'dark' | 'light' | 'system'
export type ToastKind = 'success' | 'error' | 'info'
export interface Toast { id: number; kind: ToastKind; msg: string }

// <html lang> 跟着界面语言走。浏览器**原生控件**认的是它:datetime-local 的
// 「年/月/日」占位、日期选择器的月份名、时间控件的 AM/PM,都不归 i18n 管,
// 只看这个属性。它同时也是给读屏软件和翻译工具的声明。
function applyHtmlLang(l: Lang) {
  document.documentElement.lang = l === 'zh' ? 'zh-CN' : 'en'
}

const prefersDark = () => typeof window !== 'undefined' && window.matchMedia
  ? window.matchMedia('(prefers-color-scheme: dark)').matches : true

/** The effective (resolved) theme actually applied to the DOM. */
const resolve = (theme: Theme): 'dark' | 'light' => (theme === 'system' ? (prefersDark() ? 'dark' : 'light') : theme)

interface UIState {
  lang: Lang
  /** Per-user preference (stored in this browser). Defaults to light. */
  theme: Theme
  /** 此刻真正生效的主题(theme 为 system 时跟着系统变)。组件里读它,不要自己算。 */
  resolved: 'dark' | 'light'
  // 页眉副标题(每个视图自己设),空 = 用路由的 i18n 默认值。
  //
  // 存的是**词条 key + 参数**,不是渲染好的字符串。视图通常在数据加载完那一刻设它,
  // 而那是一次性的:存字符串的话,之后切换语言这行字就永远停在当时那个语言 —— 页面
  // 其余部分都翻过去了,只有它还是中文(这正是它被发现的方式)。渲染推迟到 AppLayout,
  // 那里 t() 跟着 locale 走,切语言就重算。
  //
  // 允许直接给字符串,是给那些本来就不该翻译的副标题用的(终端页写的是实例名和角色)。
  pageSub: PageSub
  toasts: Toast[]
  setPageSub(s: PageSub): void
  setLang(l: Lang): void
  toggleLang(): void
  setTheme(t: Theme): void
  toggleTheme(): void
  applyTheme(): void
  resolvedTheme(): 'dark' | 'light'
  notify(msg: string, kind?: ToastKind, ttl?: number): void
  notifyError(e: unknown, fallback: string): void
  dismissToast(id: number): void
}

// UI store: language + theme (matches the prototype's lang toggle & dark theme).
export const useUIStore = create<UIState>()((set, get) => {
  const lang = (localStorage.getItem('aegisdb_lang') as Lang) || 'zh'
  const theme = (localStorage.getItem('aegisdb_theme') as Theme) || 'light'
  let toastSeq = 0

  // Follow OS light/dark changes live while "system" is selected.
  if (typeof window !== 'undefined' && window.matchMedia) {
    window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', () => {
      if (get().theme === 'system') get().applyTheme()
    })
  }

  // 首次装载:index.html 里写死的是 zh,而这个浏览器上存着的可能是 en。
  applyHtmlLang(lang)

  return {
    lang,
    theme,
    resolved: resolve(theme),
    pageSub: '',
    toasts: [],

    setPageSub: (s) => set({ pageSub: s }),

    setLang(l) {
      i18n.global.locale.value = l
      localStorage.setItem('aegisdb_lang', l)
      applyHtmlLang(l)
      set({ lang: l })
    },
    toggleLang: () => get().setLang(get().lang === 'zh' ? 'en' : 'zh'),

    // 只管主题。<html lang> 归 applyHtmlLang —— 它过去藏在这里,于是"谁在写这个属性"
    // 这件事没人能一眼看出来,后来者很容易再加一个写点(我就差点)。
    applyTheme() {
      const resolved = resolve(get().theme)
      document.documentElement.setAttribute('data-theme', resolved)
      set({ resolved })
    },
    resolvedTheme: () => resolve(get().theme),
    setTheme(t) {
      set({ theme: t })
      localStorage.setItem('aegisdb_theme', t)
      get().applyTheme()
    },
    // Quick toggle for the topbar: flip to the opposite of what's currently shown
    // (works whether the current preference is explicit or "system").
    toggleTheme: () => get().setTheme(get().resolvedTheme() === 'dark' ? 'light' : 'dark'),

    // ---- Toasts (transient user feedback for async success/failure) ----
    notify(msg, kind = 'info', ttl = 3500) {
      const id = ++toastSeq
      set((s) => ({ toasts: [...s.toasts, { id, kind, msg }] }))
      setTimeout(() => get().dismissToast(id), ttl)
    },
    dismissToast: (id) => set((s) => ({ toasts: s.toasts.filter((x) => x.id !== id) })),
    // notifyError extracts a human message from a thrown API error and toasts it.
    // The fallback is a parameter with no default text: a literal here would print
    // in one language regardless of the active locale. Callers pass t('actionFailed').
    //
    // 管理后台的 IP 白名单拒绝(40303)与空闲超时(40304)不在这里报:拦截器已经交给布局统一提示并退回
    // 用户后台(api/http.ts → AppLayout)。这里再报一次,同一件事就弹两遍(2026-10 审查)。
    notifyError(e, fallback) {
      const code = (e as any)?.code
      if (code === CODE_ADMIN_IP_BLOCKED || code === CODE_ADMIN_IDLE) return
      const msg = (e as any)?.message || (e as any)?.msg || fallback
      get().notify(String(msg), 'error')
    },
  }
})
