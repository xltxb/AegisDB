// 查询授权总表:谁、哪个实例的哪个库、上限、有效期、来源(外部系统 / 审批单 / 审批人)。
//
// 授权由外部审批系统下发,这里不新建、不编辑 —— 只看,以及在离职、误授、安全事件时由管理员
// 立即撤销(撤销原因写进审计)。
import { useState } from 'react'
import clsx from 'clsx'
import { Search, Ban } from 'lucide-react'
import api from '@/api'
import { useUIStore } from '@/stores/ui'
import { useI18n } from '@/hooks/useI18n'
import { useEvent } from '@/hooks/useEvent'
import { useMountEffect, useUpdateEffect } from '@/hooks/effects'
import type { QueryGrant } from '@/types'
import './QueryGrantsPanel.scoped.css'

const STATUSES = ['', 'active', 'pending', 'expired', 'revoked']

const day = (s: string) => {
  const d = new Date(s)
  return isNaN(d.getTime()) ? s : d.toLocaleDateString()
}

export default function QueryGrantsPanel({ isAdmin }: { isAdmin: boolean }) {
  const { t } = useI18n()
  const [items, setItems] = useState<QueryGrant[]>([])
  // 挂载即加载,初值直接给 true:否则加载前的那一帧会闪一下"没有授权"
  const [loading, setLoading] = useState(true)
  const [email, setEmail] = useState('')
  const [dept, setDept] = useState('')
  const [status, setStatus] = useState('active')

  const load = useEvent(async () => {
    setLoading(true)
    try {
      const r = await api.queryGrants({ email: email.trim(), dept: dept.trim(), status })
      setItems(r.items ?? [])
    } catch (e) {
      useUIStore.getState().notifyError(e, t('loadFailed'))
    } finally {
      setLoading(false)
    }
  })
  useMountEffect(() => { void load() })
  useUpdateEffect(() => { void load() }, [status])
  useUpdateEffect(() => {
    const timer = setTimeout(load, 300)
    return () => clearTimeout(timer)
  }, [email, dept])

  async function revoke(g: QueryGrant) {
    if (!isAdmin || g.status === 'revoked' || g.status === 'expired') return
    const reason = window.prompt(t('qgRevokePrompt', { email: g.email, target: `${g.source} / ${g.database}` }))
    if (reason === null) return
    const ui = useUIStore.getState()
    try {
      await api.revokeQueryGrant(g.id, reason.trim())
      ui.notify(t('qgRevoked'), 'success')
      await load()
    } catch (e) {
      ui.notifyError(e, t('actionFailed'))
    }
  }

  return (
    <div className="qgp" data-testid="grants-panel">
      <div className="qgf">
        <div className="qgsearch"><Search size={13} /><input value={email} onChange={(e) => setEmail(e.target.value)} placeholder={t('qgFilterEmail')} data-testid="qg-email" /></div>
        <div className="qgsearch"><input value={dept} onChange={(e) => setDept(e.target.value)} placeholder={t('qgFilterDept')} data-testid="qg-dept" /></div>
        <select value={status} onChange={(e) => setStatus(e.target.value)} className="qgsel" data-testid="qg-status">
          {STATUSES.map((s) => <option key={s} value={s}>{s ? t('qgSt_' + s) : t('qgAll')}</option>)}
        </select>
      </div>
      <div className="qgt">
        <div className="qgh"><span>{t('qgColUser')}</span><span>{t('qgColTarget')}</span><span>{t('qgColLimit')}</span><span>{t('qgColPeriod')}</span><span>{t('qgColSource')}</span><span>{t('qgColStatus')}</span></div>
        {!loading && !items.length && <div className="qgempty">{t('qgEmpty')}</div>}
        {items.map((g) => (
          <div key={g.id} className="qgr" data-testid="qg-row">
            <div className="cell"><div className="b nm">{g.userName || g.email}</div><div className="s nm">{g.email} · {g.dept || '—'}</div></div>
            <div className="cell"><div className="b nm mono">{g.database}</div><div className="s nm mono">{g.source}</div></div>
            <div className="cell s">{g.effectiveMaxRows} · {g.effectiveTimeoutSec}s{g.allowExport && <span className="exp">{t('qgExport')}</span>}</div>
            <div className="cell s">{day(g.startsAt)} ~ {day(g.endsAt)}</div>
            <div className="cell"><div className="s nm">{g.client || '—'} · {g.externalId}</div><div className="s nm">{g.approver || '—'}</div></div>
            <div className="cell st">
              <span className={clsx('pill', g.status)}>{t('qgSt_' + g.status)}</span>
              {isAdmin && (g.status === 'active' || g.status === 'pending') && (
                <button className="rv" title={t('qgRevoke')} data-testid="qg-revoke" onClick={() => revoke(g)}><Ban size={13} /></button>
              )}
            </div>
          </div>
        ))}
      </div>
    </div>
  )
}
