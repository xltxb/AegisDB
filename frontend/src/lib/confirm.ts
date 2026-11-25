// confirmAction gates a destructive/high-risk operation behind an explicit
// user confirmation (M15).
//
// 2026-10-29:从浏览器原生 confirm 换成样式化的弹窗(ConfirmDialog.vue,挂在 App.vue 根上)。原生框
// 顶着"aegisdb.xxx.com 显示"、字挤在一起,命令也看不清;换了之后它返回 Promise,调用处一律
// `if (!(await confirmAction(msg))) return`。
import { reactive } from 'vue'

export interface ConfirmOptions {
  title?: string
  okText?: string
  danger?: boolean
}

export const confirmState = reactive({ open: false, message: '', title: '', okText: '', danger: false })

let pending: ((ok: boolean) => void) | null = null

export function confirmAction(message: string, opts: ConfirmOptions = {}): Promise<boolean> {
  if (typeof window === 'undefined') return Promise.resolve(true)
  pending?.(false) // 上一个还没答就又来一个:旧的按取消处理
  Object.assign(confirmState, { open: true, message, title: opts.title ?? '', okText: opts.okText ?? '', danger: !!opts.danger })
  return new Promise((resolve) => { pending = resolve })
}

export function settleConfirm(ok: boolean) {
  const r = pending
  pending = null
  confirmState.open = false
  r?.(ok)
}

// confirmSegments 把文案里「…」包着的部分(命令、名字)切出来,弹窗里按等宽高亮显示。
export function confirmSegments(message: string): { text: string; code: boolean }[] {
  const out: { text: string; code: boolean }[] = []
  const re = /「([^」]*)」/g
  let last = 0
  for (let m = re.exec(message); m; m = re.exec(message)) {
    if (m.index > last) out.push({ text: message.slice(last, m.index), code: false })
    out.push({ text: m[1], code: true })
    last = m.index + m[0].length
  }
  if (last < message.length) out.push({ text: message.slice(last), code: false })
  return out
}
