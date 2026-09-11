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

/** 把主题写到 <html data-theme>,业务样式只认语义 token,不各自判主题。 */
export function applyTheme(theme: Theme) {
  const dark =
    theme === 'dark' ||
    (theme === 'system' && window.matchMedia?.('(prefers-color-scheme: dark)').matches)
  document.documentElement.setAttribute('data-theme', dark ? 'dark' : 'light')
}

let seq = 0

export const useUIStore = create<UIState>()((set, get) => ({
  theme: (localStorage.getItem(THEME_KEY) as Theme) || 'light',
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
    // 原生日期控件跟着语言走,靠的是 <html lang>。
    document.documentElement.setAttribute('lang', l === 'zh' ? 'zh-CN' : 'en')
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
