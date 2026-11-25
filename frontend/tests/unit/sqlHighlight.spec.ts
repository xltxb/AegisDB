import { test, expect } from '@playwright/test'

import { tokenizeSql, highlightSqlHtml } from '../../src/lib/sqlHighlight'

const kinds = (s: string) => tokenizeSql(s).map((t) => `${t.kind}:${t.text}`)

// 分词结果钉住:2026-10-26 改成线性实现时,与旧实现在两万组随机输入上逐 token 比对过一致。
test('tokenizeSql:注释、字符串、数字、关键字、元命令', () => {
  expect(kinds("SELECT 'it''s', x1 -- c\n/* b */ 1.5e3")).toEqual([
    'kw:SELECT', 'plain: ', "str:'it''s'", 'plain:,', 'plain: ', 'plain:x1', 'plain: ', 'cmt:-- c', 'plain:\n',
    'cmt:/* b */', 'plain: ', 'num:1.5e3',
  ])
  expect(kinds("\\d t\n'a\\'b'")).toEqual(['meta:\\d t', 'plain:\n', "str:'a\\'b'"])
  expect(kinds('a1.2')).toEqual(['plain:a1', 'plain:.', 'num:2'])
  expect(kinds("'unterminated")).toEqual(["str:'unterminated"])
  expect(kinds('/*/ x')).toEqual(['cmt:/*/', 'plain: ', 'plain:x']) // 与旧实现一致:*/ 可与 /* 共用星号
})

// 旧实现每个 token 都切一次剩余子串(平方级),查询编辑器几千行就卡死,库树看大包源码也慢。
test('highlightSqlHtml:一万行在一秒多内画完(线性)', () => {
  const line = "SELECT a.id, b.name, 'it''s' AS s, 12.5 FROM t_user a JOIN t_order b ON a.id = b.uid -- c\n"
  const t0 = Date.now()
  highlightSqlHtml(line.repeat(10000))
  expect(Date.now() - t0).toBeLessThan(1500)
})
