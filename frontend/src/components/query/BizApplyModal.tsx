// 在网关里申请数据查询权限(设置 bizQuery.applyMode = internal 时,issue #173)。
//
// 提交后服务端生成一张 type=grant 的审批单,走正常的审批链;批准即生成查询授权,没有需要手动
// 执行的命令。这里只收五样东西:哪个实例、哪个库、多少天、要不要导出、为什么 —— 上限、
// 实例是不是业务查询源、库在不在、有没有重复的待审批申请,都以服务端为准,报错原样显示。
//
// BizApplyList(我的申请)同时用在数据查询页没有授权时的拦截卡片里:申请交上去之后,人最想
// 知道的就是它走到哪一步了。
import { useState } from 'react'
import clsx from 'clsx'
import { KeyRound, X, Undo2 } from 'lucide-react'
import VButton from '@/components/common/VButton'
import api from '@/api'
import { useI18n } from '@/hooks/useI18n'
import type { QueryGrantRequest } from '@/types'
import { BizSourceDbFields, useBizSourceDb } from './BizSourceDbPicker'
import { GRANT_REQUEST_STATUS_LABEL } from '@/lib/grantRequestStatus'
import './BizApplyModal.scoped.css'

/** 天数输入框的初值:7 天,上限比 7 小就取上限。 */
export const defaultApplyDays = (max: number) => Math.max(1, Math.min(7, max || 7))

interface ListProps { items: QueryGrantRequest[]; busyId?: number; onWithdraw: (r: QueryGrantRequest) => void }

/** 我的申请(最近 20 张):状态、单号、实例 / 库、天数;待审批的可以撤回。 */
export function BizApplyList({ items, busyId, onWithdraw }: ListProps) {
  const { t } = useI18n()
  if (!items.length) return null
  return (
    <div className="ba-list" data-testid="q-apply-mine">
      <div className="ba-lh">{t('qaMine')}</div>
      {items.map((r) => (
        <div key={r.id} className="ba-row" data-testid="q-apply-item">
          <span className={clsx('ba-pill', r.status)}>{t(GRANT_REQUEST_STATUS_LABEL[r.status])}</span>
          <div className="ba-main">
            <div className="ba-tg" title={`${r.source} / ${r.database}`}>{r.source} / <b>{r.database}</b></div>
            <div className="ba-meta">
              {r.apNo} · {t('qaDaysN', { n: r.days })}{r.allowExport && <> · {t('qaExportTag')}</>}
            </div>
            {r.status === 'failed' && r.error && <div className="ba-err">{r.error}</div>}
          </div>
          {r.status === 'pending' && (
            <button className="ba-wd" disabled={busyId === r.id} title={t('qaWithdraw')} data-testid="q-apply-withdraw" onClick={() => onWithdraw(r)}>
              <Undo2 size={12} />{t('qaWithdraw')}
            </button>
          )}
        </div>
      ))}
    </div>
  )
}

interface Props {
  open: boolean
  maxGrantDays: number
  exportEnabled: boolean
  mine: QueryGrantRequest[]
  busyId?: number
  onWithdraw: (r: QueryGrantRequest) => void
  onSubmitted: (r: QueryGrantRequest) => void
  onClose: () => void
}

export default function BizApplyModal({ open, ...rest }: Props) {
  return open ? <Body {...rest} /> : null
}

function Body({ maxGrantDays, exportEnabled, mine, busyId, onWithdraw, onSubmitted, onClose }: Omit<Props, 'open'>) {
  const { t } = useI18n()
  const [days, setDays] = useState<number | ''>(defaultApplyDays(maxGrantDays))
  const [allowExport, setAllowExport] = useState(false)
  const [reason, setReason] = useState('')
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [done, setDone] = useState<QueryGrantRequest | null>(null)
  const picker = useBizSourceDb(setErr)
  const { sources, sourceId, database } = picker

  // 与服务端同样的几条;服务端仍会再判一次,这里只是让人少等一个来回。
  function check(): string {
    if (!sourceId) return t('qaNeedSource')
    if (!database) return t('qaNeedDb')
    if (days === '' || days < 1 || days > maxGrantDays) return t('qaBadDays', { max: maxGrantDays })
    if (!reason.trim()) return t('qaNeedReason')
    return ''
  }

  async function submit() {
    if (busy) return
    const bad = check()
    setErr(bad)
    if (bad) return
    setBusy(true)
    try {
      const r = await api.bizApply({ sourceId, database, days: Number(days), allowExport: exportEnabled && allowExport, reason: reason.trim() })
      setDone(r)
      onSubmitted(r)
    } catch (e: any) {
      setErr(e?.message || t('actionFailed'))
    } finally { setBusy(false) }
  }

  const noSources = sources !== null && !sources.length
  return (
    <div className="ba-overlay" data-testid="q-apply-modal">
      <div className="ba-mask" onClick={onClose} />
      <div className="modal">
        <div className="topbar" />
        <div className="pad">
          <div className="hdr">
            <div className="ic"><KeyRound size={20} color="var(--accent-text)" /></div>
            <div className="grow">
              <div className="title">{t('qaTitle')}</div>
              <div className="desc">{t('qaDesc')}</div>
            </div>
            <div className="x" onClick={onClose}><X size={18} /></div>
          </div>

          {done ? (
            <div className="ba-done" data-testid="q-apply-done">{t('qaSubmitted', { ap: done.apNo })}</div>
          ) : noSources ? (
            <div className="ba-none" data-testid="q-apply-nosrc">{t('qaNoSources')}</div>
          ) : (
            <>
              <BizSourceDbFields picker={picker} testid="q-apply" />
              <label className="fl">{t('qaDays')}</label>
              <div className="ba-days">
                <input
                  className="pin" type="number" min={1} max={maxGrantDays} value={days} data-testid="q-apply-days"
                  onChange={(e) => setDays(e.target.value === '' ? '' : Math.round(Number(e.target.value)))}
                />
                <span className="ba-hint">{t('qaDaysHint', { max: maxGrantDays })}</span>
              </div>
              {exportEnabled && (
                <label className="ba-chk">
                  <input type="checkbox" checked={allowExport} data-testid="q-apply-export" onChange={(e) => setAllowExport(e.target.checked)} />
                  <span>{t('qaExport')}</span><span className="ba-hint">{t('qaExportHint')}</span>
                </label>
              )}
              <label className="fl">{t('qaReason')}</label>
              <textarea className="pin ba-ta" maxLength={500} placeholder={t('qaReasonPh')} value={reason} data-testid="q-apply-reason" onChange={(e) => setReason(e.target.value)} />
            </>
          )}
          {err && <div className="err" data-testid="q-apply-err">{err}</div>}
          <BizApplyList items={mine} busyId={busyId} onWithdraw={onWithdraw} />
        </div>
        <div className="footer">
          <VButton variant="secondary" onClick={onClose}>{done || noSources ? t('btnClose') : t('btnCancel')}</VButton>
          {!done && !noSources && (
            <VButton variant="primary" disabled={busy || sources === null} data-testid="q-apply-submit" onClick={submit}>{busy ? t('qaSubmitting') : t('qaSubmit')}</VButton>
          )}
        </div>
      </div>
    </div>
  )
}
