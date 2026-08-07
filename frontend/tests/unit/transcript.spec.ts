import { test, expect } from '@playwright/test'

import { MAX_ENTRIES, Transcript, redactSecrets, stripAnsi } from '../../src/lib/transcript'

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
