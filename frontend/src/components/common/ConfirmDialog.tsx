// 全局确认弹窗(lib/confirm.ts 的 confirmAction 驱动),替代浏览器原生 confirm。
// Enter 确认、Esc / 点遮罩取消;打开时焦点落在确认按钮上。
import { Fragment, useEffect, useRef } from 'react'
import clsx from 'clsx'
import { AlertTriangle, X } from 'lucide-react'
import VButton from '@/components/common/VButton'
import { confirmSegments, confirmState, settleConfirm } from '@/lib/confirm'
import { useI18n } from '@/hooks/useI18n'
import { useObservable } from '@/hooks/useObservable'
import './ConfirmDialog.scoped.css'

export default function ConfirmDialog() {
  const { t } = useI18n()
  const st = useObservable(confirmState)
  const okBox = useRef<HTMLSpanElement>(null)

  useEffect(() => {
    if (!st.open) return
    okBox.current?.querySelector('button')?.focus()
  }, [st.open])

  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if (!confirmState.value.open) return
      if (e.key === 'Escape') { e.preventDefault(); settleConfirm(false) }
      else if (e.key === 'Enter' && !e.isComposing) { e.preventDefault(); settleConfirm(true) }
    }
    window.addEventListener('keydown', onKey, true)
    return () => window.removeEventListener('keydown', onKey, true)
  }, [])

  if (!st.open) return null
  return (
    <div className="cf-overlay" data-testid="confirm-dialog">
      <div className="cf-mask" onClick={() => settleConfirm(false)} />
      <div className={clsx('cf-modal', st.danger && 'danger')} role="alertdialog" aria-modal="true">
        <div className="cf-top" />
        <div className="cf-pad">
          <div className="cf-hdr">
            <div className="cf-ic"><AlertTriangle size={20} /></div>
            <div className="cf-grow">
              <div className="cf-title">{st.title || t('confirmTitle')}</div>
              <div className="cf-msg" data-testid="confirm-message">
                {confirmSegments(st.message).map((s, i) => (
                  <Fragment key={i}>{s.code ? <code className="cf-code">{s.text}</code> : s.text}</Fragment>
                ))}
              </div>
            </div>
            <div className="cf-x" onClick={() => settleConfirm(false)}><X size={18} /></div>
          </div>
        </div>
        <div className="cf-footer">
          <VButton variant="secondary" data-testid="confirm-cancel" onClick={() => settleConfirm(false)}>{t('btnCancel')}</VButton>
          <span ref={okBox}>
            <VButton variant={st.danger ? 'danger' : 'primary'} data-testid="confirm-ok" onClick={() => settleConfirm(true)}>
              {st.okText || t('confirmOk')}
            </VButton>
          </span>
        </div>
      </div>
    </div>
  )
}
