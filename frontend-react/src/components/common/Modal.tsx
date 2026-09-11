import { useEffect, type ReactNode } from 'react'
import { X } from 'lucide-react'

/**
 * 模态:radius 16 · modalIn .26s · 遮罩 rgba(4,6,12,.7)(原型规格)。
 *
 * 点遮罩关闭、Esc 关闭都做了 —— 一个只能靠右上角小叉关掉的弹窗,在人已经改了
 * 一半表单又想放弃时最让人烦躁。
 */
export function Modal({
  open, title, sub, width = 560, onClose, footer, children,
}: {
  open: boolean
  title: ReactNode
  sub?: ReactNode
  width?: number
  onClose: () => void
  footer?: ReactNode
  children: ReactNode
}) {
  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [open, onClose])

  if (!open) return null
  return (
    <div className="c-overlay" onMouseDown={(e) => { if (e.target === e.currentTarget) onClose() }}>
      <div className="c-modal" style={{ width }} role="dialog" aria-modal="true">
        <header className="c-modal-head">
          <div>
            <div className="c-modal-title">{title}</div>
            {sub && <div className="c-modal-sub">{sub}</div>}
          </div>
          <button className="c-modal-x" onClick={onClose} aria-label="close"><X size={16} /></button>
        </header>
        <div className="c-modal-body">{children}</div>
        {footer && <footer className="c-modal-foot">{footer}</footer>}
      </div>
    </div>
  )
}
