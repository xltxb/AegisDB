import { test, expect } from '@playwright/test'

import { analyzeCompletion, complete, longestCommonPrefix, type CompletionSource } from '../../src/lib/sqlComplete'

// 终端 Tab 补全的判断:光标处该补什么(关键字 / 库 / 表 / 列 / 元命令)、别名指向哪张表、
// 补到哪里(唯一候选补全,多个候选补到公共前缀)、大小写跟着人。

test('光标处该补什么', () => {
  const kind = (s: string) => analyzeCompletion(s).kind
  expect(kind('\\d')).toBe('meta')
  expect(kind('SELECT * FROM ord')).toBe('table')
  expect(kind('select * from orders o join cus')).toBe('table')
  expect(kind('SELECT * FROM a, b')).toBe('table')
  expect(kind('UPDATE ord')).toBe('table')
  expect(kind('INSERT INTO ord')).toBe('table')
  expect(kind('DESC ord')).toBe('table')
  expect(kind('\\d ord')).toBe('table')
  expect(kind('USE sh')).toBe('database')
  expect(kind('\\c sh')).toBe('database')
  expect(kind('SELECT o.')).toBe('column')
  expect(kind('SELECT * FROM shop.')).toBe('qualified')
  expect(kind('SELECT na')).toBe('any')
  expect(kind('SELECT * FROM orders WHERE amo')).toBe('any')
  // 字符串 / 注释里不补
  expect(kind("SELECT * FROM t WHERE a = 'ord")).toBe('none')
  expect(kind('SELECT 1 -- ord')).toBe('none')
})

test('前缀与限定名', () => {
  expect(analyzeCompletion('SELECT o.cust')).toMatchObject({ kind: 'column', qualifier: 'o', prefix: 'cust' })
  expect(analyzeCompletion('SELECT * FROM `shop`.ord')).toMatchObject({ kind: 'qualified', qualifier: 'shop', prefix: 'ord' })
  expect(analyzeCompletion('SELECT * FROM 订')).toMatchObject({ kind: 'table', prefix: '订' })
})

test('别名解析:FROM / JOIN / 逗号列表 / AS,关键字不算别名', () => {
  const a = analyzeCompletion('SELECT * FROM orders o JOIN shop.customers AS c ON o.cid = c.id, items i WHERE x.').aliases
  expect(a).toEqual({ o: 'orders', c: 'shop.customers', i: 'items' })
  expect(analyzeCompletion('SELECT * FROM orders WHERE ').aliases).toEqual({})
  expect(analyzeCompletion('SELECT * FROM orders WHERE id = 1 AND orders.').tables).toContain('orders')
})

test('最长公共前缀(不分大小写比较,取第一个候选的写法)', () => {
  expect(longestCommonPrefix(['customer_id', 'customer_name'])).toBe('customer_')
  expect(longestCommonPrefix(['Orders', 'ORDER_ITEMS'])).toBe('Order')
  expect(longestCommonPrefix(['a'])).toBe('a')
  expect(longestCommonPrefix([])).toBe('')
})

const SRC: CompletionSource = {
  keywords: ['SELECT', 'FROM', 'WHERE', 'ORDER', 'OR', 'ON'],
  metas: ['\\?', '\\conns', '\\c', '\\clear', '\\conninfo', '\\dt', '\\d'],
  databases: ['shop', 'shipping', 'hr'],
  quote: '`',
  tables: async (q) => (q === 'hr' ? ['staff'] : ['orders', 'order_items', 'customers', '订单']),
  columns: async (t) => (t === 'orders' ? ['id', 'customer_id', 'customer_name', 'amount'] : t === 'shop.customers' ? ['id', 'name'] : []),
}

test('唯一候选:补全;关键字跟着人的大小写', async () => {
  expect(await complete('SELECT * FROM cus', SRC)).toMatchObject({ replace: 3, text: 'customers', candidates: ['customers'] })
  expect(await complete('sel', SRC)).toMatchObject({ replace: 3, text: 'select ' })
  expect(await complete('SEL', SRC)).toMatchObject({ replace: 3, text: 'SELECT ' })
  expect(await complete('USE hr', SRC)).toMatchObject({ text: 'hr' })
  expect(await complete('SELECT * FROM hr.st', SRC)).toMatchObject({ replace: 2, text: 'staff' })
  expect(await complete('\\conni', SRC)).toMatchObject({ text: '\\conninfo ' })
})

test('多个候选:补到公共前缀,把候选交给调用方列出', async () => {
  const r = await complete('SELECT * FROM ord', SRC)
  expect(r).toMatchObject({ replace: 3, text: 'order', candidates: ['order_items', 'orders'] })
  const c = await complete('SELECT o.customer FROM orders o', SRC, 'SELECT o.customer'.length)
  expect(c).toMatchObject({ replace: 8, text: 'customer_', candidates: ['customer_id', 'customer_name'] })
})

test('列:按别名、按表名、按语句里出现的表', async () => {
  expect(await complete('SELECT o.am FROM orders o', SRC, 'SELECT o.am'.length)).toMatchObject({ text: 'amount' })
  expect(await complete('SELECT c.na FROM shop.customers AS c', SRC, 'SELECT c.na'.length)).toMatchObject({ text: 'name' })
  expect(await complete('SELECT orders.am', SRC)).toMatchObject({ text: 'amount' })
  // 没带限定名:关键字 + 语句里出现的表的列
  expect(await complete('SELECT id FROM orders WHERE amo', SRC)).toMatchObject({ text: 'amount' })
})

test('没有候选 / 空前缀的通配位置:不补', async () => {
  expect(await complete('SELECT * FROM zzz', SRC)).toBeNull()
  expect(await complete('SELECT ', SRC)).toBeNull()
  expect(await complete("SELECT 'ord", SRC)).toBeNull()
})

test('空前缀的表位置:列出全部表', async () => {
  const r = await complete('SELECT * FROM ', SRC)
  expect(r?.candidates.length).toBe(4)
  expect(r?.text).toBe('')
})

test('需要引号的名字加上引号', async () => {
  const src = { ...SRC, tables: async () => ['order detail'] }
  expect(await complete('SELECT * FROM ord', src)).toMatchObject({ text: '`order detail`' })
})

// 2026-10-27 数据查询页:敲名字中间 / 后面的一段也能补(包含匹配)。只在 contains: true 时生效,
// 终端不传 —— 它靠"补到公共前缀"工作,包含匹配下公共前缀不成立。
test('包含匹配:默认关,终端照旧只认开头', async () => {
  expect(await complete('SELECT * FROM item', SRC)).toBeNull()
  expect(await complete('SELECT * FROM orders WHERE name', SRC)).toBeNull()
})

test('包含匹配:开头匹配的排前面,包含的接在后面;两个字起才包含匹配', async () => {
  const C: CompletionSource = { ...SRC, contains: true }
  expect(await complete('SELECT * FROM item', C)).toMatchObject({ replace: 4, text: 'order_items', candidates: ['order_items'] })
  expect(await complete('SELECT * FROM orders WHERE name', C)).toMatchObject({ replace: 4, candidates: ['customer_name'] })
  // 开头匹配的(customer_id、customers)按字母排前,只是包含的 old_cust 接在后面
  expect((await complete('SELECT * FROM orders WHERE cust', { ...C, columns: async () => ['customer_id', 'old_cust'] }))?.candidates)
    .toEqual(['customer_id', 'customers', 'old_cust'])
  // 多个候选不再补公共前缀(它们不一定以敲的这段开头):原样留着,交给弹框选
  expect(await complete('SELECT * FROM er', C)).toMatchObject({ replace: 2, text: 'er', candidates: ['customers', 'order_items', 'orders'] })
  // 一个字只认开头,免得一敲就弹一大片
  expect(await complete('SELECT * FROM s', C)).toBeNull()
})

test('包含匹配:关键字仍只认开头', async () => {
  const C: CompletionSource = { ...SRC, contains: true }
  // WHERE / ORDER 都含 re,但关键字不做包含匹配
  expect(await complete('SELECT * FROM orders WHERE re', C)).toBeNull()
  expect(await complete('sel', C)).toMatchObject({ replace: 3, text: 'select ' })
})

test('包含匹配:敲的词本身已是完整的关键字 / 名字时不追加(否则 AND、ON、id 敲完回车会被弹框吃掉)', async () => {
  const C: CompletionSource = { ...SRC, keywords: [...SRC.keywords, 'AND'], contains: true,
    columns: async (t) => (t === 'orders' ? ['id', 'customer_id', 'brand', 'amount'] : []) }
  // AND 含在 brand 里、ON 含在 amount 里:仍只有关键字本身
  expect((await complete('SELECT * FROM orders WHERE x = 1 AND', C))?.candidates).toEqual(['AND'])
  expect((await complete('SELECT * FROM orders o JOIN b ON', C))?.candidates).toEqual(['ON'])
  // id 本身是列:不追加 customer_id
  expect((await complete('SELECT * FROM orders WHERE id', C))?.candidates).toEqual(['id'])
  // 不是完整名字的照常包含匹配
  expect((await complete('SELECT * FROM orders WHERE rand', C))?.candidates).toEqual(['brand'])
})
