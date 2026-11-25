import { test, expect } from '@playwright/test'

import { readZipTexts, decodeText } from '../../src/lib/zipRead'
import { makeBundle, resolveIn, checkIncludes, expandScript, normPath } from '../../src/lib/sqlplusScript'
import { newDefineState, parseSqlPlusCommand, parseStartArgs } from '../../src/lib/sqlplus'

// @ / @@ / START 脚本(sqlplus-compat 08)与 SPOOL / WHENEVER 的识别。

const ORA = 'Oracle 19c'

// ---------------------------------------------------------------- 现场拼一个 zip

async function deflateRaw(data: Uint8Array): Promise<Uint8Array> {
  const s = new Blob([data as BlobPart]).stream().pipeThrough(new CompressionStream('deflate-raw'))
  return new Uint8Array(await new Response(s).arrayBuffer())
}

/** name 可以是字符串(按 UTF-8,置标志位 11)或原始字节(不置标志位,模拟 Windows 中文系统打的 zip) */
async function makeZip(files: { name: string | Uint8Array; text: string; deflate?: boolean }[]): Promise<ArrayBuffer> {
  const enc = new TextEncoder()
  const locals: Uint8Array[] = []
  const cens: Uint8Array[] = []
  let off = 0
  for (const f of files) {
    const nameBytes = typeof f.name === 'string' ? enc.encode(f.name) : f.name
    const flags = typeof f.name === 'string' ? 0x800 : 0
    const raw = enc.encode(f.text)
    const data = f.deflate ? await deflateRaw(raw) : raw
    const loc = new Uint8Array(30 + nameBytes.length + data.length)
    const lv = new DataView(loc.buffer)
    lv.setUint32(0, 0x04034b50, true); lv.setUint16(6, flags, true); lv.setUint16(8, f.deflate ? 8 : 0, true)
    lv.setUint32(18, data.length, true); lv.setUint32(22, raw.length, true); lv.setUint16(26, nameBytes.length, true)
    loc.set(nameBytes, 30); loc.set(data, 30 + nameBytes.length)
    const cen = new Uint8Array(46 + nameBytes.length)
    const cv = new DataView(cen.buffer)
    cv.setUint32(0, 0x02014b50, true); cv.setUint16(8, flags, true); cv.setUint16(10, f.deflate ? 8 : 0, true)
    cv.setUint32(20, data.length, true); cv.setUint32(24, raw.length, true); cv.setUint16(28, nameBytes.length, true)
    cv.setUint32(42, off, true); cen.set(nameBytes, 46)
    locals.push(loc); cens.push(cen); off += loc.length
  }
  const cenSize = cens.reduce((a, c) => a + c.length, 0)
  const eocd = new Uint8Array(22)
  const ev = new DataView(eocd.buffer)
  ev.setUint32(0, 0x06054b50, true); ev.setUint16(8, files.length, true); ev.setUint16(10, files.length, true)
  ev.setUint32(12, cenSize, true); ev.setUint32(16, off, true)
  return new Blob([...locals, ...cens, eocd] as BlobPart[]).arrayBuffer()
}

test('读 zip:存储与 deflate 两种,目录跳过,GBK 文件名', async () => {
  const gbkName = new Uint8Array([0xd6, 0xd0, 0x2e, 0x73, 0x71, 0x6c]) // "中.sql" 的 GBK 字节
  const zip = await makeZip([
    { name: 'deploy/main.sql', text: '@@sub/b.sql\nSELECT 1 FROM dual;' },
    { name: 'deploy/sub/', text: '' },
    { name: 'deploy/sub/b.sql', text: 'INSERT INTO t VALUES (1);\n'.repeat(50), deflate: true },
    { name: gbkName, text: 'SELECT 2 FROM dual;' },
  ])
  const got = await readZipTexts(zip)
  expect(got.map((e) => e.name)).toEqual(['deploy/main.sql', 'deploy/sub/b.sql', '中.sql'])
  expect(got[1].text).toBe('INSERT INTO t VALUES (1);\n'.repeat(50))
  await expect(readZipTexts(new TextEncoder().encode('not a zip').buffer as ArrayBuffer)).rejects.toThrow()
})

test('脚本内容:去 BOM,不是 UTF-8 时按 GBK', () => {
  expect(decodeText(new Uint8Array([0xef, 0xbb, 0xbf, 0x41]))).toBe('A')
  expect(decodeText(new Uint8Array([0xd6, 0xd0, 0xce, 0xc4]))).toBe('中文')
})

// ---------------------------------------------------------------- 找文件、检查引用

const bundle = () => makeBundle([
  { name: 'Deploy/MAIN.sql', text: 'PROMPT main\n@@sub/b\n@c.sql x\nSELECT &1 FROM dual;' },
  { name: 'Deploy/sub/b.sql', text: 'PROMPT b\n@@d' },
  { name: 'Deploy/sub/d.sql', text: 'PROMPT d' },
  { name: 'Deploy/c.sql', text: 'PROMPT c &1' },
])

test('找文件:@@ 相对当前文件,@ 相对主脚本;补 .sql;不分大小写;按文件名兜底', () => {
  const b = bundle()
  expect(resolveIn(b, 'main.sql', '')).toBe('deploy/main.sql')
  expect(resolveIn(b, 'sub/b', 'deploy')).toBe('deploy/sub/b.sql')
  expect(resolveIn(b, 'd', 'deploy/sub')).toBe('deploy/sub/d.sql')
  expect(resolveIn(b, '../c.sql', 'deploy/sub')).toBe('deploy/c.sql')
  expect(resolveIn(b, 'nope', 'deploy')).toBeNull()
  expect(normPath('a\\b/./c/../d.SQL')).toBe('a/b/d.sql')
})

test('检查引用:缺文件、循环、正常', () => {
  expect(checkIncludes(bundle(), 'deploy/main.sql')).toEqual({ missing: [], cycle: null, tooDeep: false })
  const miss = makeBundle([{ name: 'm.sql', text: '@@a.sql\n@b' }, { name: 'a.sql', text: '' }])
  expect(checkIncludes(miss, 'm.sql').missing).toEqual(['b.sql'])
  const loop = makeBundle([{ name: 'a.sql', text: '@@b' }, { name: 'b.sql', text: '@@a' }])
  expect(checkIncludes(loop, 'a.sql').cycle).toEqual(['a.sql', 'b.sql', 'a.sql'])
})

test('展开:被引用的文件就地替换,位置参数逐层生效', async () => {
  const st = newDefineState()
  const ex = await expandScript(bundle(), 'deploy/main.sql', ['ename'], st, async () => { throw new Error('不该问') }, ORA)
  expect(ex.cancelled).toBe(false)
  // @c.sql x 把 &1 改成了 x(SQL*Plus 的位置参数是全局的),回到主脚本时 &1 已经是 x
  expect(ex.text).toBe('PROMPT main\nPROMPT b\nPROMPT d\nPROMPT c x\nSELECT x FROM dual;')
})

test('展开:未定义的变量问人;取消就整份不跑;替换后才知道的文件名找不到要报出来', async () => {
  const b = makeBundle([{ name: 'm.sql', text: 'SELECT &col FROM dual;\n@@&sub' }, { name: 'x.sql', text: 'PROMPT x' }])
  const asked: string[] = []
  const ex = await expandScript(b, 'm.sql', [], newDefineState(), async (n) => { asked.push(n); return n === 'col' ? 'ename' : 'x' }, ORA)
  expect(ex.text).toBe('SELECT ename FROM dual;\nPROMPT x')
  expect(asked).toEqual(['col', 'sub'])
  expect((await expandScript(b, 'm.sql', [], newDefineState(), async () => null, ORA)).cancelled).toBe(true)
  const gone = await expandScript(b, 'm.sql', [], newDefineState(), async (n) => (n === 'col' ? 'a' : 'nope'), ORA)
  expect(gone.missing).toBe('nope.sql')
})

// ---------------------------------------------------------------- 识别

test('@ / @@ / START / SPOOL / WHENEVER 的识别', () => {
  expect(parseSqlPlusCommand('@deploy.sql 1 "two words"', ORA)).toEqual({ kind: 'start', nested: false, file: 'deploy.sql', args: ['1', 'two words'] })
  expect(parseSqlPlusCommand('@@sub/b', ORA)).toEqual({ kind: 'start', nested: true, file: 'sub/b', args: [] })
  expect(parseSqlPlusCommand('sta deploy', ORA)).toEqual({ kind: 'start', nested: false, file: 'deploy', args: [] })
  expect(parseSqlPlusCommand('startup', ORA)?.kind).toBe('refuse') // 不是 START
  expect(parseSqlPlusCommand('@', ORA)).toBeNull()
  expect(parseSqlPlusCommand('spool out.log', ORA)).toEqual({ kind: 'spool', arg: 'out.log' })
  expect(parseSqlPlusCommand('spo off', ORA)).toEqual({ kind: 'spool', arg: 'off' })
  expect(parseSqlPlusCommand('whenever sqlerror exit failure', ORA)).toEqual({ kind: 'whenever', arg: 'sqlerror exit failure' })
  expect(parseStartArgs("x.sql 'it''s'")).toEqual({ file: 'x.sql', args: ["it's"] })
  // 其他引擎
  expect(parseSqlPlusCommand('@x.sql', 'MySQL 8.0')).toBeNull()
})

// ---------------------------------------------------------------- 绑定变量(sqlplus-compat 06)

test('VARIABLE 解析、重新声明、语句里用到了哪些', async () => {
  const { parseVariable, declareBind, bindsUsedIn } = await import('../../src/lib/sqlplus')
  expect(parseVariable('n NUMBER')).toEqual({ name: 'N', type: 'NUMBER', value: '', null: true })
  expect(parseVariable("s varchar2 ( 30 ) = 'it''s'")).toEqual({ name: 'S', type: 'VARCHAR2(30)', value: "it's", null: false })
  expect(parseVariable('rc REFCURSOR')).toBeNull()
  expect(parseVariable('d DATE')).toBeNull()
  const a = declareBind([{ name: 'N', type: 'NUMBER', value: '5', null: false }], { name: 'S', type: 'CLOB', value: '', null: true })
  const b = declareBind(a, { name: 'N', type: 'VARCHAR2(10)', value: '', null: true })
  expect(b.map((x) => `${x.name}:${x.type}:${x.null}`)).toEqual(['S:CLOB:true', 'N:VARCHAR2(10):true'])
  expect(bindsUsedIn("BEGIN :n := 1; :S := ':x'; END; -- :q", [...b, { name: 'Q', type: 'NUMBER', value: '', null: true }]).map((x) => x.name)).toEqual(['S', 'N'])
  expect(parseSqlPlusCommand('var n number', ORA)).toEqual({ kind: 'variable', arg: 'n number' })
  expect(parseSqlPlusCommand('print n s', ORA)).toEqual({ kind: 'print', names: ['n', 's'] })
})
