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

// A fixed 60-column cap made `SHOW GRANTS` unreadable: every row is a long,
// near-identical string that differs only in its tail, so all seventeen rendered
// as the same `…t_dws_bp_order_user_kind_…` on a terminal with room to spare.
// Given the terminal width, a single column must get the whole terminal.
test('a lone wide column uses the terminal width instead of a fixed cap', () => {
  const grants = [
    'GRANT SELECT ON `c66_dws_report`.`t_dws_bp_order_user_kind_daily`',
    'GRANT SELECT ON `c66_dws_report`.`t_dws_bp_order_user_kind_month`',
  ]
  const t = { columns: ['Grants for archery@%'], rows: grants.map((g) => [g]), numeric: [false] } as any
  const body = renderTable(t, 200).join('\n')
  for (const g of grants) expect(body).toContain(g)

  // Without a terminal width the caller gets the old fixed cap — still bounded.
  const capped = renderTable(t).join('\n')
  expect(capped).not.toContain(grants[0])
  expect(capped).toContain('…')
})

// Every rendered line must be exactly as wide as the frame, or the box art tears
// and a wrapped row shifts everything below it.
test('rows stay aligned and inside the terminal at any width', () => {
  const visibleWidth = (s: string) => {
    const plain = s.replace(/\x1b\[[0-9;]*m/g, '')
    let w = 0
    for (const ch of plain) {
      const cp = ch.codePointAt(0) || 0
      w += (cp >= 0x2e80 && cp <= 0xa4cf) || (cp >= 0xac00 && cp <= 0xd7a3) || (cp >= 0xff00 && cp <= 0xff60) ? 2 : 1
    }
    return w
  }
  const t = {
    columns: ['id', 'long_column_name_here', '名称'],
    rows: [
      ['1', 'x'.repeat(300), '张三'],
      ['22', 'short', '李四说了一段很长的话用来把这一列撑开'],
    ],
    numeric: [true, false, false],
  } as any
  for (const cols of [200, 120, 80, 40]) {
    const lines = renderTable(t, cols)
    const widths = new Set(lines.map(visibleWidth))
    expect(widths.size, `ragged frame at ${cols} cols`).toBe(1)
    expect([...widths][0], `overflowed ${cols} cols`).toBeLessThanOrEqual(cols)
  }
})

// The water-fill must not spend the terminal evenly: narrow columns settle at
// their natural width so the column that actually varies gets what is left.
test('space a narrow column does not need goes to the wide one', () => {
  const t = {
    columns: ['id', 'stmt'],
    rows: [['1', 'GRANT SELECT ON `db`.`' + 'a'.repeat(200) + '`']],
    numeric: [true, false],
  } as any
  const line = renderTable(t, 100)[3] // 0 top bar, 1 header, 2 divider, 3 first row
  const [, idCell, stmtCell] = line.replace(/\x1b\[[0-9;]*m/g, '').split('│')
  expect(idCell.trim()).toBe('1') // narrow column not padded out to an equal share
  expect(stmtCell.length).toBeGreaterThan(80) // …the rest went to the wide one
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

// A clipped cell ends in `…`, which says a value continues — not that the whole
// one is still reachable. The data IS all in hand; only the display was cut. The
// hint is what turns the ellipsis from a dead end into a pointer at \G.
test('a table that clipped values says so, and how many', () => {
  const t = {
    columns: ['id', 'body'],
    rows: [
      ['1', 'x'.repeat(300)],
      ['2', 'y'.repeat(300)],
      ['3', 'short'],
    ],
    numeric: [true, false],
  }
  const lines = renderTable(t, 60, (n) => `CUT:${n}`)
  const last = lines[lines.length - 1]
  expect(last).toBe('CUT:2') // the two long bodies, not the short one
})

test('a table that fits adds no hint at all', () => {
  const t = { columns: ['id'], rows: [['1'], ['2']], numeric: [true] }
  const lines = renderTable(t, 80, (n) => `CUT:${n}`)
  expect(lines.some((l) => l.startsWith('CUT:'))).toBe(false)
})

// Headers are excluded from the count: a clipped column NAME hides no data, and
// counting it would overstate what \G recovers.
test('a clipped header is not counted as hidden data', () => {
  const t = {
    columns: ['a_very_long_column_name_that_will_not_fit_anywhere'],
    rows: [['1']],
    numeric: [false],
  }
  const lines = renderTable(t, 20, (n) => `CUT:${n}`)
  expect(lines.some((l) => l.startsWith('CUT:'))).toBe(false)
})

// Callers that pass no hint (and the existing ones did not) must be unaffected.
test('without a hint callback the table is unchanged', () => {
  const t = { columns: ['body'], rows: [['z'.repeat(300)]], numeric: [false] }
  const lines = renderTable(t, 40)
  // The bottom border is the last line (it carries an ANSI colour prefix, so
  // match on content rather than position within the string).
  expect(lines[lines.length - 1]).toContain('└')
})
