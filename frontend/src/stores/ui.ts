import { defineStore } from 'pinia'
import { ref } from 'vue'
import { i18n } from '@/locales'

// UI store: language + theme (matches the prototype's lang toggle & dark theme).
export const useUIStore = defineStore('ui', () => {
  const lang = ref<'zh' | 'en'>((localStorage.getItem('vela_lang') as 'zh' | 'en') || 'zh')
  type Theme = 'dark' | 'light' | 'system'
  const theme = ref<Theme>((localStorage.getItem('vela_theme') as Theme) || 'dark')
  // Per-view dynamic topbar subtitle; empty = use the route's i18n default.
  const pageSub = ref('')

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
  function notifyError(e: unknown, fallback = '操作失败') {
    const msg = (e as any)?.message || (e as any)?.msg || fallback
    notify(String(msg), 'error')
  }

  const prefersDark = () => typeof window !== 'undefined' && window.matchMedia
    ? window.matchMedia('(prefers-color-scheme: dark)').matches : true
  // The effective (resolved) theme actually applied to the DOM.
  function resolvedTheme(): 'dark' | 'light' {
    return theme.value === 'system' ? (prefersDark() ? 'dark' : 'light') : theme.value
  }

  function applyTheme() {
    document.documentElement.setAttribute('data-theme', resolvedTheme())
    document.documentElement.setAttribute('lang', lang.value)
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
    applyTheme()
  }

  function toggleLang() {
    setLang(lang.value === 'zh' ? 'en' : 'zh')
  }

  function setTheme(t: Theme) {
    theme.value = t
    localStorage.setItem('vela_theme', t)
    applyTheme()
  }

  return {
    lang, theme, pageSub, toasts, setLang, toggleLang, setTheme, applyTheme, resolvedTheme,
    notify, notifyError, dismissToast,
  }
})
