// Test double for `@/locales`, wired in by tsconfig.unit.json.
//
// Why this exists
// ---------------
// The pure logic under test (transcript rendering, import-sheet validation)
// reports in the operator's language, so it reads the message catalogue through
// the i18n singleton. That singleton lives in `src/locales/index.ts`, and
// importing it runs two side effects at module load that a bare Node runner
// cannot survive or does not want:
//
//   1. `localStorage.getItem('aegis_lang')` — no such global in Node, so the
//      import THROWS. Not one failing spec: collection aborts and every test in
//      the file silently stops existing.
//   2. `i18n.use(initReactI18next)` — binds the singleton to React's context
//      machinery, which nothing here has or needs.
//
// So only the *wiring* is stubbed. The catalogues are the real `src/locales/zh`
// and `src/locales/en` off disk, and the instance is a real i18next one, because
// the assertions that matter check the actual Chinese an operator will read
// ("# 实例: …", "已丢弃最早的 50 条记录"). A stub that returned key names, or
// hand-rolled `{{n}}` interpolation, would turn those into tests of the stub.
//
// (The Vue implementation needed a much heavier version of this file: its
// catalogues were `.json5`, which Node's TypeScript transform could not parse at
// all. Moving to plain `.ts` catalogues in the React rewrite is what lets this
// one just import them.)
import i18n from 'i18next'

import { zh } from '../../../src/locales/zh'
import { en } from '../../../src/locales/en'

i18n.init({
  resources: { zh: { translation: zh }, en: { translation: en } },
  // No localStorage in Node, and no user to have picked anything: the fallback
  // locale is the one these tests assert against.
  lng: 'zh',
  fallbackLng: 'zh',
  interpolation: { escapeValue: false },
})

export default i18n
