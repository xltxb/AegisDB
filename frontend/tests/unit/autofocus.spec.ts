import { test, expect } from '@playwright/test'
import fs from 'node:fs'
import path from 'node:path'

// A dialog that pops up in response to something the user did — the PROD MFA
// step-up above all — is asking a question. The caret must already be in the
// answer box: the user just pressed Enter on a statement, their hands are on the
// keyboard, and the six digits they type next have to land somewhere. They were
// landing in the xterm canvas behind the dialog instead.
//
// Every one-time-code box in the app is, by definition, the thing the user is
// being asked for. There is no such box that should open unfocused, so this is
// checkable as a rule rather than one dialog at a time.
//
// React has no `v-autofocus` directive to look for; the equivalents are the
// `autoFocus` prop (React calls `.focus()` on mount) or an explicit `.focus()`
// on a ref — the latter is what a form needs when it must RE-focus after a
// rejected code, since the input never unmounts on a retry.
//
// 落地时抓到一个:`pages/changes/index.tsx` 的 MFA 输入框(`needMfa` 为真时才挂载的
// 那个)没有 autoFocus。

/** 认出「一次性验证码输入框」。 */
function isOneTimeCodeInput(tag: string): boolean {
  // 标准写法优先 —— 它同时告诉浏览器和密码管理器这是什么。
  if (tag.includes('one-time-code')) return true
  // 其次按它绑的是什么:`mfaCode` / `otpCode` / `id="mfa"`。
  // 刻意**不**按裸 `mfa` 匹配:设置页的 `mfaGrace` 是个分钟数,不是验证码框。
  return /\b(mfa|otp)Code\b/.test(tag) || /\b(id|name)="(mfa|otp)"/.test(tag)
}

function tsxFiles(dir: string): string[] {
  return fs.readdirSync(dir, { withFileTypes: true }).flatMap((e) => {
    const p = path.join(dir, e.name)
    if (e.isDirectory()) return tsxFiles(p)
    return e.name.endsWith('.tsx') ? [p] : []
  })
}

test('every one-time-code input opens focused', () => {
  const offenders: string[] = []
  for (const file of [...tsxFiles('src/pages'), ...tsxFiles('src/components')]) {
    const src = fs.readFileSync(file, 'utf8')
    for (const m of src.matchAll(/<input\b[^>]*?\/?>/gs)) {
      const tag = m[0]
      if (!isOneTimeCodeInput(tag)) continue
      if (/\bautoFocus\b/.test(tag)) continue
      // 另一条同样成立的路子:绑一个 ref,自己 focus()。重试时要重新聚焦的表单只能
      // 这么做 —— 输入框没卸载过,autoFocus 不会再触发。
      const ref = /\bref=\{(\w+)\}/.exec(tag)
      if (ref && src.includes(`${ref[1]}.current?.focus()`)) continue
      offenders.push(`${file} → ${tag.replace(/\s+/g, ' ').slice(0, 70)}…`)
    }
  }
  expect(offenders, 'add autoFocus to these inputs').toEqual([])
})

// 自检:上面那条在**一个验证码框都没认出来**时也是绿的,而那正是规则被悄悄绕过去的
// 样子(有人把 `mfaCode` 改名成别的)。所以钉住它确实看见了至少一个。
test('the rule actually sees the app’s one-time-code inputs', () => {
  const seen: string[] = []
  for (const file of [...tsxFiles('src/pages'), ...tsxFiles('src/components')]) {
    const src = fs.readFileSync(file, 'utf8')
    for (const m of src.matchAll(/<input\b[^>]*?\/?>/gs)) {
      if (isOneTimeCodeInput(m[0])) seen.push(file)
    }
  }
  expect(seen.length, '一个一次性验证码输入框都没认出来,这条规则等于没生效').toBeGreaterThan(1)
})
