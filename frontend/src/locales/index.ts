import zh from './zh.json5'
import en from './en.json5'
import { createI18n } from './core'

export type LocaleKey = keyof typeof zh

export const i18n = createI18n({
  locale: localStorage.getItem('aegisdb_lang') || 'zh',
  fallbackLocale: 'zh',
  messages: { zh, en },
})

export default i18n
