import clsx from 'clsx'
import type { ReactNode } from 'react'

export type BadgeTone = 'neutral' | 'accent' | 'success' | 'warning' | 'danger'

/**
 * 状态徽标:22px 高 · 999px 圆角 · 600 11px mono(原型规格)。
 *
 * 配色走 tone 而不是让调用方传色值 —— 状态的颜色是产品语义,散在各页各写一遍
 * 就会出现同一个"已通过"在两页不同颜色。
 */
export function Badge({
  tone = 'neutral', icon, children,
}: { tone?: BadgeTone; icon?: ReactNode; children: ReactNode }) {
  return (
    <span className={clsx('c-badge', `t-${tone}`)}>
      {icon}
      {children}
    </span>
  )
}

/** 后端把状态给成自由字符串,这里集中映射成色调,避免每页各判一次。 */
export function toneOfStatus(status: string): BadgeTone {
  switch (status) {
    case 'approved': case 'done': case 'active': case 'online': case 'succeeded':
      return 'success'
    case 'rejected': case 'failed': case 'expired': case 'error':
      return 'danger'
    case 'pending': case 'awaiting': case 'waiting': case 'running': case 'maint':
      return 'warning'
    default:
      return 'neutral'
  }
}
