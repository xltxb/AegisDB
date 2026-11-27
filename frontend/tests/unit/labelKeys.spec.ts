import { test, expect } from '@playwright/test'
import fs from 'node:fs'
import path from 'node:path'
import { parseForESLint, getStaticJSONValue } from 'jsonc-eslint-parser'
import { APPROVAL_TYPE_LABEL } from '../../src/lib/approvalTypes'
import { GRANT_REQUEST_STATUS_LABEL } from '../../src/lib/grantRequestStatus'

// 类型 / 状态 → 词条键的表里,每个键都得真有词条(issue #188)。缺了不会报错,界面直接露出键名。
// 只查 zh:两份词条的键一致由 localeParity.spec.ts 负责。
const zh = getStaticJSONValue(
  parseForESLint(fs.readFileSync(path.resolve('src/locales/zh.json5'), 'utf8'), { jsonSyntax: 'json5' } as never).ast as never,
) as unknown as Record<string, unknown>

for (const [name, table] of Object.entries({ APPROVAL_TYPE_LABEL, GRANT_REQUEST_STATUS_LABEL })) {
  test(`${name} 里的词条键都存在`, () => {
    const missing = Object.values(table).filter((k) => typeof zh[k] !== 'string')
    expect(missing, `zh.json5 里没有这些键:${missing.join(', ')}`).toEqual([])
  })
}
