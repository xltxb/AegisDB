import type { ReactNode } from 'react'
import type { LucideIcon } from 'lucide-react'
import clsx from 'clsx'

interface Props {
  icon: LucideIcon
  title: ReactNode
  sub?: ReactNode
  /** Optional next step (a button or link) under the text. */
  children?: ReactNode
  /** Tighter padding, for empty states inside a list or a narrow column. */
  compact?: boolean
  className?: string
  /** data-testid, and the parent's scoped-CSS attribute, land on the root. */
  [attr: `data-${string}`]: unknown
}

/**
 * The one empty state: an icon in a soft tile, a line saying what is (not) here,
 * and optionally a hint and an action. Pages used to print a lone grey sentence,
 * which read like a loading glitch rather than "nothing yet".
 */
export default function EmptyState({ icon: Icon, title, sub, children, compact, className, ...rest }: Props) {
  return (
    <div {...rest} className={clsx('v-empty', compact && 'v-empty--compact', className)}>
      <div className="v-empty-ic"><Icon size={compact ? 16 : 20} /></div>
      <div className="v-empty-title">{title}</div>
      {sub && <div className="v-empty-sub">{sub}</div>}
      {children}
    </div>
  )
}
