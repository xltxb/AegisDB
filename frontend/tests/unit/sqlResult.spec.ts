import { test, expect } from '@playwright/test'

import { renderTable, renderVertical } from '../../src/lib/sqlResult'

// EF3: cell values come from the target database, so anyone who can write a row
// (a username, a comment, a log message) controls them. The rendered lines are
// handed straight to term.write(), which interprets ANSI/OSC escapes — so an
// escape stored in a row can repaint the scrollback the DBA is reading: erase
// the red "operating on PROD" banner, forge a green success line, or fake a
// prompt. A DSR query (\x1b[6n) is worse still: xterm answers it on the input
// channel, and the reply lands in the SQL buffer the user is typing.
//
// Only the colouring this module generates itself may reach the terminal; every
// control character arriving in DATA has to be neutralised.
const escapes = [
  '\x1b[2K\x1b[1A', // erase line + cursor up: repaints previous output
  '\x1b[6n', // device status report: provokes a reply into the input buffer
  '\x1b]0;pwn\x07', // OSC: sets the window title
  '\x07', // bell
  '\x00', // NUL
]

function assertNoControlChars(lines: string[], planted: string) {
  const body = lines.join('\n')
  // Strip the SGR colour codes this module adds deliberately, then nothing that
  // can move the cursor or address the terminal may remain.
  const withoutOwnColours = body.replace(/\x1b\[[0-9;]*m/g, '')
  expect(withoutOwnColours, `planted ${JSON.stringify(planted)}`).not.toMatch(/[\x00-\x08\x0b-\x1f\x7f-\x9f]/)
}

test('renderTable neutralises escape sequences stored in cell values', () => {
  for (const evil of escapes) {
    const lines = renderTable({
      columns: ['name'],
      rows: [[evil + 'bob']],
      numeric: [false],
    } as any)
    assertNoControlChars(lines, evil)
  }
})

test('renderTable neutralises escape sequences stored in column names', () => {
  for (const evil of escapes) {
    const lines = renderTable({
      columns: [evil + 'name'],
      rows: [['bob']],
      numeric: [false],
    } as any)
    assertNoControlChars(lines, evil)
  }
})

test('renderVertical neutralises escape sequences stored in cell values', () => {
  for (const evil of escapes) {
    assertNoControlChars(renderVertical(['name'], [[evil + 'bob']]), evil)
  }
})

test('ordinary text, including CJK and emoji, still renders', () => {
  const lines = renderTable({
    columns: ['名称'],
    rows: [['张三 🎉']],
    numeric: [false],
  } as any)
  expect(lines.join('\n')).toContain('张三 🎉')
})

// Vertical (\G) display exists for values a horizontal table cannot show — a
// SHOW CREATE TABLE body, a JSON blob. Their real newlines are the point, so
// sanitising must remove control characters WITHOUT flattening the value into
// one line; xterm needs \r\n to return to column 0.
test('renderVertical keeps genuine newlines in a multi-line value', () => {
  const ddl = 'CREATE TABLE t (\n  id INT,\n  name TEXT\n)'
  const lines = renderVertical(['ddl'], [[ddl]])
  const body = lines.join('\n')
  expect(body).toContain('id INT')
  expect(body).toContain('name TEXT')
  // Each embedded newline must be a CRLF so the next line starts at column 0.
  expect(body).toMatch(/\r\n/)
  // …and an escape smuggled alongside them is still removed.
  const evil = renderVertical(['ddl'], [['a\n\x1b[2Kb']]).join('\n')
  expect(evil).not.toContain('\x1b[2K')
  expect(evil).toContain('b')
})
