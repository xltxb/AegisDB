import { createI18n } from 'vue-i18n'
import zh from './zh'
import en from './en'

export type LocaleKey = keyof typeof zh

export const i18n = createI18n({
  legacy: false,
  locale: localStorage.getItem('vela_lang') || 'zh',
  fallbackLocale: 'zh',
  messages: { zh, en },
})

export default i18n
