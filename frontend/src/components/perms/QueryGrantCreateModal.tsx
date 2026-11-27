// 管理员直接开通一条查询授权(权限 · 查询授权 ·「新建授权」,issue #173)。
//
// 只给网关里已有的账号开(不自动建号);实例必须是业务查询源、库必须真在那台实例上 —— 这些都由
// 服务端判,报错原样显示。行数上限 / 超时填 0 表示跟全局默认走。立即生效,写入审计。
import { useState } from 'react'
import { KeyRound, X } from 'lucide-react'
import VButton from '@/components/common/VButton'
import api from '@/api'
import { useI18n } from '@/hooks/useI18n'
import type { BizQueryLimits, QueryGrant } from '@/types'
import { BizSourceDbFields, useBizSourceDb } from '@/components/query/BizSourceDbPicker'
import './QueryGrantCreateModal.scoped.css'

interface Props { open: boolean; limits: BizQueryLimits | null; onCreated: (g: QueryGrant) => void; onClose: () => void }

export default function QueryGrantCreateModal({ open, ...rest }: Props) {
  return open ? <Body {...rest} /> : null
}

const intOr = (v: string): number | '' => (v === '' ? '' : Math.max(0, Math.round(Number(v)) || 0))

function Body({ limits, onCreated, onClose }: Omit<Props, 'open'>) {
  const { t } = useI18n()
  const maxDays = limits?.maxGrantDays || 90
  const [email, setEmail] = useState('')
  const [days, setDays] = useState<number | ''>(Math.min(30, maxDays))
  const [maxRows, setMaxRows] = useState<number | ''>(0)
  const [timeoutSec, setTimeoutSec] = useState<number | ''>(0)
  const [allowExport, setAllowExport] = useState(false)
  const [reason, setReason] = useState('')
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const picker = useBizSourceDb(setErr)
  const { sources, sourceId, database } = picker

  function check(): string {
    if (!email.trim()) return t('qgNewNeedEmail')
    if (!sourceId) return t('qaNeedSource')
    if (!database) return t('qaNeedDb')
    if (days === '' || days < 1 || days > maxDays) return t('qaBadDays', { max: maxDays })
    return ''
  }

  async function submit() {
    if (busy) return
    const bad = check()
    setErr(bad)
    if (bad) return
    setBusy(true)
    try {
      const g = await api.createQueryGrant({
        email: email.trim(), sourceId, database, days: Number(days), maxRows: Number(maxRows) || 0, timeoutSec: Number(timeoutSec) || 0,
        allowExport: !!limits?.exportEnabled && allowExport, reason: reason.trim(),
      })
      onCreated(g)
    } catch (e: any) {
      setErr(e?.message || t('actionFailed'))
    } finally { setBusy(false) }
  }

  return (
    <div className="qgc-overlay" data-testid="qg-new-modal">
      <div className="qgc-mask" onClick={onClose} />
      <div className="modal">
        <div className="topbar" />
        <div className="pad">
          <div className="hdr">
            <div className="ic"><KeyRound size={20} color="var(--accent-text)" /></div>
            <div className="grow">
              <div className="title">{t('qgNewTitle')}</div>
              <div className="desc">{t('qgNewDesc')}</div>
            </div>
            <div className="x" onClick={onClose}><X size={18} /></div>
          </div>
          <label className="fl">{t('qgNewEmail')}</label>
          <input autoFocus className="pin" type="email" placeholder="name@example.com" value={email} data-testid="qg-new-email" onChange={(e) => setEmail(e.target.value)} />
          <BizSourceDbFields picker={picker} testid="qg-new" />
          <div className="qgc-grid">
            <div>
              <label className="fl">{t('qaDays')}</label>
              <input className="pin" type="number" min={1} max={maxDays} value={days} data-testid="qg-new-days" onChange={(e) => setDays(intOr(e.target.value))} />
              <div className="qgc-hint">{t('qaDaysHint', { max: maxDays })}</div>
            </div>
            <div>
              <label className="fl">{t('qgNewMaxRows')}</label>
              <input className="pin" type="number" min={0} value={maxRows} data-testid="qg-new-rows" onChange={(e) => setMaxRows(intOr(e.target.value))} />
              <div className="qgc-hint">{t('qgNewDefault', { n: limits?.defaultMaxRows ?? '—' })}</div>
            </div>
            <div>
              <label className="fl">{t('qgNewTimeout')}</label>
              <input className="pin" type="number" min={0} value={timeoutSec} data-testid="qg-new-timeout" onChange={(e) => setTimeoutSec(intOr(e.target.value))} />
              <div className="qgc-hint">{t('qgNewDefault', { n: limits?.defaultTimeoutSec ?? '—' })}</div>
            </div>
          </div>
          {limits?.exportEnabled && (
            <label className="qgc-chk">
              <input type="checkbox" checked={allowExport} data-testid="qg-new-export" onChange={(e) => setAllowExport(e.target.checked)} />
              <span>{t('qgNewExport')}</span>
            </label>
          )}
          <label className="fl">{t('qgNewReason')}</label>
          <textarea className="pin qgc-ta" maxLength={500} placeholder={t('qgNewReasonPh')} value={reason} data-testid="qg-new-reason" onChange={(e) => setReason(e.target.value)} />
          {err && <div className="err" data-testid="qg-new-err">{err}</div>}
        </div>
        <div className="footer">
          <VButton variant="secondary" onClick={onClose}>{t('btnCancel')}</VButton>
          <VButton variant="primary" disabled={busy || sources === null} data-testid="qg-new-submit" onClick={submit}>{busy ? t('qaSubmitting') : t('qgNewSubmit')}</VButton>
        </div>
      </div>
    </div>
  )
}
