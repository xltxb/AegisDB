import type { ReactNode } from 'react'
import clsx from 'clsx'

/** 卡片:1px --border-subtle 描边 · radius 14 · --surface-card 底(原型规格)。 */
export function Card({ className, children }: { className?: string; children: ReactNode }) {
  return <section className={clsx('c-card', className)}>{children}</section>
}

/** 卡头 padding:16px 20px。icon 是 32px 的方块图标位。 */
export function CardHead({
  icon, title, sub, actions,
}: { icon?: ReactNode; title: ReactNode; sub?: ReactNode; actions?: ReactNode }) {
  return (
    <header className="c-card-head">
      {icon && <span className="c-card-icon">{icon}</span>}
      <div className="c-card-titles">
        <div className="c-card-title">{title}</div>
        {sub && <div className="c-card-sub">{sub}</div>}
      </div>
      {actions && <div className="c-card-actions">{actions}</div>}
    </header>
  )
}

/** 卡内一行 padding:14px 20px,左标题右控件。 */
export function CardRow({
  title, hint, children,
}: { title: ReactNode; hint?: ReactNode; children?: ReactNode }) {
  return (
    <div className="c-card-row">
      <div className="c-row-titles">
        <div className="c-row-title">{title}</div>
        {hint && <div className="c-row-hint">{hint}</div>}
      </div>
      {children && <div className="c-row-ctl">{children}</div>}
    </div>
  )
}
