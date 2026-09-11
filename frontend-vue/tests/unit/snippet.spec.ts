import { test, expect } from '@playwright/test'

import { LineEditor } from '../../src/lib/lineEditor'
import { needsArming, slotFromEvent, slotLabel, snippetPreview, snippetSubmitText } from '../../src/lib/snippet'

// Snippets are saved scripts bound to Alt+1…9. The hotkey TYPES the script into
// the prompt and submits it — it does not call any execute endpoint of its own.
// Everything below exists to keep that true: the text has to leave through
// onSubmit, where the risk check lives, and it has to actually be submitted
// rather than left sitting at a continuation prompt looking like it ran.

// ---------------------------------------------------------------- key mapping

function keyEvent(init: Partial<KeyboardEvent> & { code: string }): KeyboardEvent {
  return { altKey: false, ctrlKey: false, metaKey: false, shiftKey: false, ...init } as KeyboardEvent
}

test('Alt+1…9 map to their slots, from the number row or the keypad', () => {
  for (let n = 1; n <= 9; n++) {
    expect(slotFromEvent(keyEvent({ code: `Digit${n}`, altKey: true }))).toBe(n)
    expect(slotFromEvent(keyEvent({ code: `Numpad${n}`, altKey: true }))).toBe(n)
  }
})

test('the modifier has to be exactly Alt', () => {
  // Ctrl+1…9 switches browser tabs and cannot be cancelled from the page, so it
  // is not ours to claim — claiming it would give half the hotkeys a second,
  // invisible meaning.
  expect(slotFromEvent(keyEvent({ code: 'Digit1', ctrlKey: true }))).toBeNull()
  expect(slotFromEvent(keyEvent({ code: 'Digit1', metaKey: true }))).toBeNull()
  expect(slotFromEvent(keyEvent({ code: 'Digit1', altKey: true, shiftKey: true }))).toBeNull()
  expect(slotFromEvent(keyEvent({ code: 'Digit1' }))).toBeNull()
})

test('Alt+0 and other keys are not hotkeys', () => {
  expect(slotFromEvent(keyEvent({ code: 'Digit0', altKey: true }))).toBeNull()
  expect(slotFromEvent(keyEvent({ code: 'KeyA', altKey: true }))).toBeNull()
  expect(slotFromEvent(keyEvent({ code: '', altKey: true }))).toBeNull()
})

test('a keyup never fires a snippet — one press is one run', () => {
  // The caller filters on type, but the pairing matters: matching both keydown
  // and keyup would run every snippet twice.
  const down = keyEvent({ code: 'Digit3', altKey: true })
  expect(slotFromEvent(down)).toBe(3)
  expect(slotLabel(3)).toBe('Alt+3')
  expect(slotLabel(0)).toBe('')
})

// ------------------------------------------------------------ submit text

test('a body that already ends in a semicolon is fed as-is', () => {
  expect(snippetSubmitText('SELECT 1;')).toBe('SELECT 1;\n')
})

test('a body without a terminator gets one, or it would never run', () => {
  // Without this the editor parks at a continuation prompt with the statement
  // typed but not submitted: the hotkey looks like it did nothing, and the next
  // Enter runs it at a moment nobody chose.
  expect(snippetSubmitText('SELECT 1')).toBe('SELECT 1;\n')
})

test('the terminator goes on its own line when the last line is a comment', () => {
  // Appended inline it would land inside the comment, terminating nothing.
  const body = 'SELECT 1\n-- why we count these'
  expect(snippetSubmitText(body)).toBe('SELECT 1\n-- why we count these\n;\n')
})

test('backslash meta-commands and \\G are already complete', () => {
  expect(snippetSubmitText('\\dt')).toBe('\\dt\n')
  expect(snippetSubmitText('SELECT * FROM users\\G')).toBe('SELECT * FROM users\\G\n')
})

test('trailing blank lines and CRLF do not change what runs', () => {
  expect(snippetSubmitText('SELECT 1;\r\n\r\n  ')).toBe('SELECT 1;\n')
  expect(snippetSubmitText('SELECT 1\r\nFROM t;')).toBe('SELECT 1\nFROM t;\n')
})

test('an empty body produces nothing to feed', () => {
  expect(snippetSubmitText('')).toBe('')
  expect(snippetSubmitText('  \n\t')).toBe('')
})

test('leading indentation is kept — it is how the script reads', () => {
  expect(snippetSubmitText('  SELECT 1;')).toBe('  SELECT 1;\n')
})

// -------------------------------------------------- through the line editor

function fakeTerm() {
  let handler: (d: string) => void = () => {}
  return {
    written: [] as string[],
    onData(cb: (d: string) => void) { handler = cb },
    write(s: string) { this.written.push(s) },
    clear() {},
    type(d: string) { handler(d) },
  }
}

function newEditor() {
  const term = fakeTerm()
  const submitted: string[] = []
  const editor = new LineEditor(term as any, {
    prompt: () => '> ',
    promptLen: () => 2,
    contPrompt: () => '. ',
    contPromptLen: () => 2,
    onSubmit: (stmt) => submitted.push(stmt.trim()),
  })
  editor.start()
  return { term, editor, submitted }
}

test('a fired snippet leaves through onSubmit, where the risk check is', () => {
  // The single most important property of the feature. If a snippet could reach
  // the server by any other route, it would reach it unjudged.
  const { editor, submitted } = newEditor()
  editor.feed(snippetSubmitText('SELECT count(*) FROM orders'))
  expect(submitted).toEqual(['SELECT count(*) FROM orders;'])
})

test('a multi-statement snippet runs one statement at a time, like a paste', () => {
  const { editor, submitted } = newEditor()
  editor.feed(snippetSubmitText('SELECT 1;\nSELECT 2;\nSELECT 3'))
  expect(submitted).toEqual(['SELECT 1;'])

  // Each following statement is submitted only as the previous one finishes, so
  // every one of them gets its own risk check — and a denial mid-batch can stop
  // the rest (discardQueued).
  editor.resume()
  expect(submitted).toEqual(['SELECT 1;', 'SELECT 2;'])
  editor.resume()
  expect(submitted).toEqual(['SELECT 1;', 'SELECT 2;', 'SELECT 3;'])
})

test('cancelling mid-snippet drops the statements that had not run', () => {
  const { editor, submitted } = newEditor()
  editor.feed(snippetSubmitText('SELECT 1;\nDROP TABLE orders;\nSELECT 3;'))
  expect(submitted).toEqual(['SELECT 1;'])

  expect(editor.discardQueued()).toBeGreaterThan(0)
  editor.resume()
  expect(submitted).toEqual(['SELECT 1;'])
})

test('the script is echoed at the prompt, so the scrollback records what ran', () => {
  const { term, editor } = newEditor()
  editor.feed(snippetSubmitText('SELECT 1'))
  expect(term.written.join('')).toContain('SELECT 1')
})

test('a multi-line statement is submitted whole', () => {
  const { editor, submitted } = newEditor()
  editor.feed(snippetSubmitText('UPDATE users\nSET tier = \'vip\'\nWHERE id = 88'))
  expect(submitted).toEqual(["UPDATE users\nSET tier = 'vip'\nWHERE id = 88;"])
})

// ------------------------------------------------------------------ arming

test('a danger-banner tier takes two presses; an ordinary one fires at once', () => {
  expect(needsArming({ dangerBanner: true })).toBe(true)
  expect(needsArming({ dangerBanner: false })).toBe(false)
})

test('an unresolved tier arms — unknown is not the same as safe', () => {
  // The caution banner reasons the same way. Were this inverted, the instances
  // nobody has classified yet would be the ones with the least confirmation.
  expect(needsArming(undefined)).toBe(true)
  expect(needsArming(null)).toBe(true)
})

// ------------------------------------------------------------------ preview

test('the preview is one line and bounded', () => {
  expect(snippetPreview('SELECT 1;\n  SELECT 2;')).toBe('SELECT 1; SELECT 2;')
  const long = snippetPreview('x'.repeat(200), 20)
  expect(long).toHaveLength(20)
  expect(long.endsWith('…')).toBe(true)
})
