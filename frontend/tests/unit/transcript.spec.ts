import { test, expect } from '@playwright/test'
import fs from 'fs'
import path from 'path'

import { MAX_ENTRIES, Transcript, UTF8_BOM, redactSecrets, stripAnsi } from '../../src/lib/transcript'

// A transcript is a file that leaves the building. Two of these tests exist
// because of that and not because of anything about formatting: credentials must
// be masked exactly as the audit log masks them, and a session too long to keep
// in full must SAY it was cut rather than quietly starting in the middle.

const AT = new Date(2026, 7, 7, 14, 2, 15) // 2026-08-07 14:02:15
const META = { instance: 'prod-hk-tongcha', database: 'orders', user: 'Lin Wei', exportedAt: AT }

// Mirrors backend/pkg/sqlutil/redact.go — the audit row and this file are handed
// the same command text, so masking one and not the other would be pointless.
test('credential literals are masked the way the audit log masks them', () => {
  expect(redactSecrets("CREATE USER a IDENTIFIED BY 'hunter2';"))
    .toBe("CREATE USER a IDENTIFIED BY '***';")
  expect(redactSecrets("ALTER USER a IDENTIFIED WITH mysql_native_password BY 'p@ss';"))
    .toBe("ALTER USER a IDENTIFIED WITH mysql_native_password BY '***';")
  expect(redactSecrets("SET PASSWORD FOR 'bob'@'%' = 'topsecret';"))
    .toBe("SET PASSWORD FOR 'bob'@'%' = '***';")
  expect(redactSecrets("CREATE ROLE r WITH ENCRYPTED PASSWORD 'swordfish';"))
    .toBe("CREATE ROLE r WITH ENCRYPTED PASSWORD '***';")
  expect(redactSecrets("SELECT PASSWORD('abc');")).toBe("SELECT PASSWORD('***');")
})

test('a statement with no credential in it is left alone', () => {
  const sql = "SELECT * FROM orders WHERE note = 'password reset requested';"
  expect(redactSecrets(sql)).toBe(sql)
})

// The recorded command goes through the same masking, so the secret never even
// reaches the buffer — not merely the rendered file.
test('the secret never enters the buffer, not just the rendered file', () => {
  const tr = new Transcript()
  tr.command("CREATE USER a IDENTIFIED BY 'hunter2';", AT)
  const out = tr.render(META)
  expect(out).not.toContain('hunter2')
  expect(out).toContain("IDENTIFIED BY '***'")
})

test('output is stripped of colour and cursor control', () => {
  expect(stripAnsi('\x1b[1;31mDROP\x1b[0m blocked')).toBe('DROP blocked')
  expect(stripAnsi('\x1b]0;title\x07rows')).toBe('rows')
  // Tabs survive — they are table content, not control.
  expect(stripAnsi('a\tb')).toBe('a\tb')
})

// An escape sequence arriving as DATA (a value stored in a table) must not steer
// whatever opens the file.
test('escape sequences inside a value cannot survive into the file', () => {
  const tr = new Transcript()
  tr.output('name: \x1b[2J\x1b[Hevil', AT)
  const out = tr.render(META)
  expect(out).not.toContain('\x1b')
  expect(out).toContain('name: evil')
})

test('commands and output are distinguishable and timestamped', () => {
  const tr = new Transcript()
  tr.command('SELECT 1;', AT)
  tr.output('1 row (0.01s)', AT)
  const out = tr.render(META)
  expect(out).toContain('[14:02:15] > SELECT 1;')
  expect(out).toContain('1 row (0.01s)')
  expect(out).toContain('# 实例: prod-hk-tongcha · 库: orders')
  expect(out).toContain('# 操作人: Lin Wei')
})

// Continuation lines are marked `.` and the statement's OWN indentation is kept
// — a formatted statement is easier to read back if it still looks formatted.
test('a multi-line statement keeps its shape', () => {
  const tr = new Transcript()
  tr.command('SELECT a,\n  b\nFROM t;', AT)
  const lines = tr.render(META).split('\n')

  expect(lines.some((l) => l.includes('> SELECT a,'))).toBe(true)
  const cont = lines.filter((l) => l.trimStart().startsWith('.'))
  expect(cont).toHaveLength(2)
  expect(cont[0]).toContain('.   b')   // marker + the author's own two spaces
  expect(cont[1]).toContain('. FROM t;')
  // Continuations line up under the command rather than restating the time.
  expect(cont[0].startsWith(' '.repeat('[14:02:15]'.length))).toBe(true)
})

// The one that matters most. A long session cannot be kept whole, and a file that
// silently begins in the middle is read as a complete record of the session.
test('an over-long session says it was cut, and keeps the most recent part', () => {
  const tr = new Transcript()
  for (let i = 0; i < MAX_ENTRIES + 50; i++) tr.output(`line-${i}`, AT)

  expect(tr.length).toBe(MAX_ENTRIES)
  expect(tr.droppedCount).toBe(50)

  const out = tr.render(META)
  expect(out).toContain('已丢弃最早的 50 条记录')
  expect(out).not.toContain('line-0\n')      // the dropped beginning is gone…
  expect(out).toContain(`line-${MAX_ENTRIES + 49}`) // …and the recent end is kept
})

test('a session within the cap makes no truncation claim', () => {
  const tr = new Transcript()
  tr.output('only line', AT)
  expect(tr.render(META)).not.toContain('已丢弃')
})

test('the filename is filesystem-safe and carries instance plus time', () => {
  const tr = new Transcript()
  expect(tr.filename(META)).toBe('vela-prod-hk-tongcha-20260807-140215.log')
  // An instance named with characters a filesystem would refuse.
  expect(tr.filename({ ...META, instance: '生产/订单 库:1' }))
    .toMatch(/^vela-[\w.-]*-20260807-140215\.log$/)
})

test('an empty session reports itself as empty so the control can be disabled', () => {
  const tr = new Transcript()
  expect(tr.isEmpty).toBe(true)
  tr.output('x', AT)
  expect(tr.isEmpty).toBe(false)
  tr.clear()
  expect(tr.isEmpty).toBe(true)
  expect(tr.droppedCount).toBe(0)
})

// The exported file opened as mojibake on a Chinese Windows.
//
// The bytes were always correct UTF-8 — the editor guessed. With no byte order
// mark, Notepad and most editors on a Chinese install fall back to the ANSI code
// page (GBK), and a transcript whose headings and output are almost entirely
// Chinese becomes unreadable with nothing in the file to fix.
test('the downloaded file starts with the UTF-8 byte order mark', () => {
  const tr = new Transcript()
  tr.command('SELECT 1;', AT)
  const file = tr.renderFile(META)
  expect(file.charCodeAt(0)).toBe(0xfeff)
  expect(file.startsWith(UTF8_BOM + '# Vela')).toBe(true)
})

test('the mark is added once, and only where the file is made', () => {
  // render() stays the plain text every other test compares against; a mark in
  // the middle of a file is data, not an encoding declaration.
  const tr = new Transcript()
  tr.command('SELECT 1;', AT)
  expect(tr.render(META).charCodeAt(0)).not.toBe(0xfeff)
  expect(tr.renderFile(META).split(UTF8_BOM)).toHaveLength(2)
})

test('Chinese content survives into the file unchanged', () => {
  const tr = new Transcript()
  tr.command("SELECT * FROM orders WHERE 状态 = '已支付';", AT)
  tr.output('· 目标实例处于维护态', AT)
  const file = tr.renderFile(META)
  expect(file).toContain("SELECT * FROM orders WHERE 状态 = '已支付';")
  expect(file).toContain('· 目标实例处于维护态')
  expect(file).toContain('# Vela 数据库网关 · 终端会话日志')
})

// Mirrors backend/pkg/sqlutil/redact_test.go. The transcript is a file that
// leaves the building, so the browser copy of these patterns has to mask exactly
// what the Go copy masks — and it did not: the plugin name in
// IDENTIFIED WITH '<plugin>' BY '<secret>' may be QUOTED, which neither side
// handled, so the password went into the downloaded log in the clear.
test('a quoted authentication plugin does not hide the password from masking', () => {
  const secret = 'X8wr^J+iu3n!L9cL'
  const sql = `create user 'db_opt'@'%' IDENTIFIED WITH 'mysql_native_password' by '${secret}' password expire never`
  const out = redactSecrets(sql)

  expect(out).not.toContain(secret)
  expect(out).toContain("by '***'")
  // The plugin name is not a secret and identifies the auth method — keep it whole.
  expect(out).toContain("'mysql_native_password'")
})

// The mask must not land in the middle of the statement. `password` used to match
// the tail of `mysql_native_password`, producing output that carried a *** and
// read as redacted while the real password sat right beside it — which is worse
// than no mask, because it stops anyone looking twice.
test('a partial mask never stands in for a real one', () => {
  const secret = 'X8wr^J+iu3n!L9cL'
  const out = redactSecrets(`CREATE USER x IDENTIFIED WITH 'mysql_native_password' BY '${secret}'`)
  expect(out).toBe("CREATE USER x IDENTIFIED WITH 'mysql_native_password' BY '***'")
})

test('masking is idempotent, so a twice-masked line is not chewed up', () => {
  const once = redactSecrets(`CREATE USER a IDENTIFIED WITH 'mysql_native_password' BY 'hunter2'`)
  expect(redactSecrets(once)).toBe(once)
})

test('an identifier that merely ends in "password" is left alone', () => {
  const sql = 'SELECT mysql_native_password FROM t'
  expect(redactSecrets(sql)).toBe(sql)
})

// The whole point of the browser copy: the secret must not reach the file.
test('the exported session log carries no password', () => {
  const secret = 'X8wr^J+iu3n!L9cL'
  const tr = new Transcript()
  tr.command(`create user 'db_opt'@'%' IDENTIFIED WITH 'mysql_native_password' by '${secret}'`, AT)
  tr.output(`ERROR near: IDENTIFIED WITH 'mysql_native_password' by '${secret}'`, AT)
  expect(tr.renderFile(META)).not.toContain(secret)
})

// The SAME corpus the Go redactor is tested against
// (backend/pkg/sqlutil/testdata/credential_syntaxes.json).
//
// Two hand-maintained lists is how one side quietly stops covering a form: the
// Go copy protects the audit row and everything sent to the external approval
// service, this copy protects the downloaded session log, and both are shown the
// same command text. Reading one file means adding an engine covers both, or
// neither — never one.
const CORPUS = JSON.parse(
  fs.readFileSync(path.join('..', 'backend', 'pkg', 'sqlutil', 'testdata', 'credential_syntaxes.json'), 'utf8'),
) as {
  cases: { engine: string; name: string; sql: string; secrets: string[]; keeps: string[] }[]
  untouched: string[]
}

test('the shared corpus is actually loaded — an empty one would pass silently', () => {
  expect(CORPUS.cases.length).toBeGreaterThan(20)
  expect(CORPUS.untouched.length).toBeGreaterThan(5)
})

for (const c of CORPUS.cases) {
  test(`${c.engine} · ${c.name} — the credential does not reach the exported log`, () => {
    const out = redactSecrets(c.sql)
    for (const s of c.secrets) expect(out, `leaked ${s}`).not.toContain(s)
    expect(out, 'no mask produced').toContain("'***'")
    // A mask that eats the account, the host or the auth plugin has cost the
    // reader exactly what they needed to understand what was done.
    for (const k of c.keeps) expect(out, `lost ${k}`).toContain(k)
  })
}

test('statements naming no credential are left byte-for-byte alone', () => {
  for (const q of CORPUS.untouched) expect(redactSecrets(q)).toBe(q)
})
