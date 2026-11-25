// 步骤列表底部的「+ 添加步骤」。
//
// 以前是一排硬邦邦的文字按钮贴在表单底下。这里改成一条分割线 + 居中的一个入口,点开
// 才出现按类型排开的图标卡片 —— 七种类型一字排开常驻在页面上,读的人每次都要把它们
// 全扫一遍;而加步骤是偶发动作,不该一直占着版面。
import { createElement, useRef, useState, type FocusEvent, type HTMLAttributes } from 'react'
import clsx from 'clsx'
import {
  Plus, ShieldCheck, ClipboardCheck, DatabaseBackup, Terminal, Zap, Search, Hourglass, Bell, type LucideIcon,
} from 'lucide-react'
import { useI18n } from '@/hooks/useI18n'
import type { StageType } from '@/types'
import './NodeToolbar.scoped.css'

// disabled:暂时不能加的类型 → 原因(悬停提示)。「自动执行」只在允许它的分层上能加,
// 卡片照样摆出来、置灰并说明为什么 —— 藏起来的话,没人知道还有这一种步骤。
interface Props extends HTMLAttributes<HTMLDivElement> {
  types: StageType[]
  disabled?: Partial<Record<StageType, string>>
  onAdd: (type: StageType) => void
}

const icon: Record<StageType, LucideIcon> = {
  review: ShieldCheck, approve: ClipboardCheck, backup: DatabaseBackup,
  execute: Terminal, auto_execute: Zap, verify: Search, manual: Hourglass, notify: Bell,
}

export default function NodeToolbar({ types, disabled, onAdd, className, ...rest }: Props) {
  const { t } = useI18n()
  const [open, setOpen] = useState(false)
  const root = useRef<HTMLDivElement>(null)

  function pick(ty: StageType) {
    onAdd(ty)
    setOpen(false)
  }
  // 焦点离开整个组件就收起 —— 与 VSelect 同一种关法,不另装全局点击监听。
  function onFocusOut(e: FocusEvent) {
    if (!root.current?.contains(e.relatedTarget as Node | null)) setOpen(false)
  }

  return (
    <div
      {...rest} ref={root} className={clsx('toolbar', className)} tabIndex={-1} onBlur={onFocusOut}
      onKeyDown={(e) => { if (e.key === 'Escape') setOpen(false) }}
    >
      <div className="rule" />
      <button type="button" className={clsx('add', open && 'open')} onClick={() => setOpen(!open)}>
        <Plus size={14} />{t('plAddStep')}
      </button>
      <div className="rule" />

      {open && (
        <div className="pop">
          <div className="pt">{t('plPickType')}</div>
          <div className="cards">
            {types.map((ty) => (
              <button
                key={ty} type="button" className="card" data-testid={'add-step-' + ty}
                disabled={!!disabled?.[ty]} title={disabled?.[ty] || undefined} onClick={() => pick(ty)}
              >
                <span className="ci">{createElement(icon[ty], { size: 15 })}</span>
                <span className="cn">{t('plType_' + ty)}</span>
              </button>
            ))}
          </div>
        </div>
      )}
    </div>
  )
}
