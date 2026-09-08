// Test double for `@/locales`, wired in by tsconfig.unit.json.
//
// Why this exists
// ---------------
// The pure logic under test (transcript rendering, import-sheet validation)
// reports in the operator's language, so it reads the message catalogue through
// the i18n singleton. That singleton lives in `src/locales/index.ts`, which does
// two things the bare Node runner cannot do:
//
//   1. `import zh from './zh.json5'` — JSON5 is a Vite/unplugin-vue-i18n
//      concern; Node's TypeScript transform hands the file to the JS parser and
//      dies on line 4 ("Missing semicolon"), taking the WHOLE run down with it.
//      Not two specs — collection aborts, and every test in the suite silently
//      stops existing.
//   2. `localStorage.getItem('vela_lang')` — no such global in Node.
//
// So the *loading* is stubbed and nothing else. The catalogue is the real
// `zh.json5` / `en.json5` off disk, and the i18n instance is a real vue-i18n
// one, because the assertions that matter here check the actual Chinese a user
// will read ("# 实例: …", "已丢弃最早的 50 条记录"). A stub that returned keys,
// or hand-rolled `{n}` interpolation, would turn those into tests of the stub.
import fs from 'fs'
import path from 'path'
import { createI18n } from 'vue-i18n'
// eslint-disable-next-line import/no-extraneous-dependencies
import { parseForESLint, getStaticJSONValue } from 'jsonc-eslint-parser'

// jsonc-eslint-parser is what @intlify/bundle-utils (under the repo's own
// unplugin-vue-i18n) parses these very files with, so it is the same reader the
// build uses — but it arrives transitively, not from package.json. json5 itself
// would be the honest direct dependency; installing one is not possible from
// this network. If the transitive copy ever goes away this throws by name rather
// than failing as another cryptic parse error.
function readCatalogue(file: string): Record<string, string> {
  const p = path.resolve('src/locales', file)
  const text = fs.readFileSync(p, 'utf8')
  const parsed = parseForESLint(text, { jsonSyntax: 'json5' } as any)
  return getStaticJSONValue(parsed.ast as any) as unknown as Record<string, string>
}

const zh = readCatalogue('zh.json5')
const en = readCatalogue('en.json5')

export type LocaleKey = string

export const i18n = createI18n({
  legacy: false,
  // No localStorage in Node, and no user to have picked anything: the fallback
  // locale is the one these tests assert against.
  locale: 'zh',
  fallbackLocale: 'zh',
  messages: { zh, en },
})

export default i18n
