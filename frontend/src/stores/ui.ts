import { create } from 'zustand'

export type Theme = 'dark' | 'light' | 'system'
export type Lang = 'zh' | 'en'

export interface Toast {
  id: number
  text: string
  kind: 'ok' | 'error' | 'info'
}

interface UIState {
  theme: Theme
  lang: Lang
  sidebarCollapsed: boolean
  toasts: Toast[]
  setTheme: (t: Theme) => void
  setLang: (l: Lang) => void
  toggleSidebar: () => void
  notify: (text: string, kind?: Toast['kind']) => void
  dismiss: (id: number) => void
}

const THEME_KEY = 'aegis_theme'
const LANG_KEY = 'aegis_lang'

/**
 * 把语言写到 <html lang>。
 *
 * 与 applyTheme 成对:两者都是"store 里的值要落到 DOM 属性上"。原先只有 setLang
 * 里写一次,启动时没人写 —— 于是刷新之后 <html lang> 一直是文档里那个默认值,
 * 跟着它走的原生控件(审计页两个 datetime-local 的占位与日历文案)就一直是错的
 * 语言,直到这次会话里有人手动切过一次语言为止。
 */
export function applyLang(lang: Lang) {
  document.documentElement.setAttribute('lang', lang === 'zh' ? 'zh-CN' : 'en')
}

/** 把主题写到 <html data-theme>,业务样式只认语义 token,不各自判主题。 */
export function applyTheme(theme: Theme) {
  const dark =
    theme === 'dark' ||
    (theme === 'system' && window.matchMedia?.('(prefers-color-scheme: dark)').matches)
  document.documentElement.setAttribute('data-theme', dark ? 'dark' : 'light')
}

let seq = 0

export const useUIStore = create<UIState>()((set, get) => ({
  // 原型是深色优先的,默认跟它一致;切换仍然可用。
  theme: (localStorage.getItem(THEME_KEY) as Theme) || 'dark',
  lang: (localStorage.getItem(LANG_KEY) as Lang) || 'zh',
  sidebarCollapsed: false,
  toasts: [],

  setTheme(t) {
    localStorage.setItem(THEME_KEY, t)
    applyTheme(t)
    set({ theme: t })
  },

  setLang(l) {
    localStorage.setItem(LANG_KEY, l)
    applyLang(l)
    set({ lang: l })
  },

  toggleSidebar() {
    set({ sidebarCollapsed: !get().sidebarCollapsed })
  },

  notify(text, kind = 'info') {
    const id = ++seq
    set({ toasts: [...get().toasts, { id, text, kind }] })
    setTimeout(() => get().dismiss(id), 4000)
  },

  dismiss(id) {
    set({ toasts: get().toasts.filter((t) => t.id !== id) })
  },
}))
