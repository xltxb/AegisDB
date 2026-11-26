import { useState } from 'react'
import clsx from 'clsx'
import { Loader, CircleCheck, CircleX, Hourglass, Play } from 'lucide-react'
import EmptyState from '@/components/common/EmptyState'
import VButton from '@/components/common/VButton'
import VSelect from '@/components/common/VSelect'
import api from '@/api'
import { CODE_OK, CODE_INTERCEPTED, localizeText, localizeLines } from '@/api/http'
import { useMountEffect } from '@/hooks/effects'
import { useEvent } from '@/hooks/useEvent'
import { useI18n } from '@/hooks/useI18n'
import { useStateRef } from '@/hooks/useStateRef'
import type { Connection, AsyncJob } from '@/types'
import './AsyncExecView.scoped.css'

const label = (c: Connection) => `${c.env}-${c.name}`
const isActive = (j: AsyncJob) => j.status === 'pending' || j.status === 'running'

function statusMeta(s: string) {
  if (s === 'running') return { icon: Loader, c: 'var(--accent-text)', spin: true }
  if (s === 'done') return { icon: CircleCheck, c: 'var(--success-text)', spin: false }
  if (s === 'failed') return { icon: CircleX, c: 'var(--danger-text)', spin: false }
  return { icon: Hourglass, c: 'var(--warning-text)', spin: false }
}
function fmt(s: string | null) { return s ? new Date(s).toLocaleString('en-GB') : '—' }

export default function AsyncExecView() {
  const { t } = useI18n()
  const [conns, setConns, connsRef] = useStateRef<Connection[]>([])
  const [connId, setConnId] = useState(0)
  const [db, setDb] = useState('')
  const [dbOptions, setDbOptions] = useState<string[]>([])
  const [sql, setSql] = useState('CALL your_long_running_proc()')
  const [reason, setReason] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const [jobs, setJobs, jobsRef] = useStateRef<AsyncJob[]>([])
  const [openId, setOpenId, openIdRef] = useStateRef(0)

  const connLabel = conns.map(label)
  const cur = conns.find((x) => x.id === connId)
  const selectedLabel = cur ? label(cur) : ''
  function pickLabel(l: string) {
    const c = conns.find((x) => label(x) === l)
    if (c) { setConnId(c.id); void loadDbs(c.id) }
  }

  const loadJobs = useEvent(async () => {
    try {
      const l = await api.asyncJobs()
      setJobs(Array.isArray(l) ? l : [])
    } catch { /* ignore */ }
  })
  // Refresh the open job's log/status while it runs.
  const refreshOpen = useEvent(async () => {
    if (!openIdRef.current) return
    try {
      const j = await api.asyncJob(openIdRef.current)
      setJobs((l) => (l.some((x) => x.id === j.id) ? l.map((x) => (x.id === j.id ? j : x)) : [j, ...l]))
    } catch { /* ignore */ }
  })

  async function loadDbs(id: number) {
    setDb(''); setDbOptions([])
    if (!id) return
    try {
      const sc = await api.connectionSchema(id)
      const opts = sc.databases.map((d) => d.name)
      setDbOptions(opts)
      const c = connsRef.current.find((x) => x.id === id)
      if (c?.database && opts.includes(c.database)) setDb(c.database)
      else if (opts.length) setDb(opts[0])
    } catch { /* leave default */ }
  }

  useMountEffect(() => {
    let gone = false
    let timer: ReturnType<typeof setInterval> | null = null
    void (async () => {
      try {
        const l = await api.connections()
        setConns(Array.isArray(l) ? l : [])
        const first = connsRef.current[0]
        if (first) { setConnId(first.id); await loadDbs(first.id) }
      } catch { /* ignore */ }
      await loadJobs()
      if (gone) return
      timer = setInterval(() => { if (jobsRef.current.some(isActive)) { void loadJobs(); void refreshOpen() } }, 2000)
    })()
    return () => { gone = true; if (timer) clearInterval(timer) }
  })

  async function submit() {
    const q = sql.trim()
    if (!connId) { setErr(t('pickInstance')); return }
    if (!q) { setErr(t('enterSql')); return }
    setBusy(true); setErr('')
    try {
      const env = await api.execAsync(connId, q, db, reason.trim())
      if (env.code === CODE_OK && env.data?.jobId) {
        await loadJobs(); setOpenId(env.data.jobId); return
      }
      if (env.code === CODE_INTERCEPTED) {
        setErr(t('interceptedNeedAppr') + ' ' + (env.data?.approvalNo || ''))
        return
      }
      // 受理了但没排队(实例维护态):服务端给一句终端式提示。从前落到下面,显示的是信封的 "ok"。
      if (env.code === CODE_OK && env.data?.output) {
        setErr(localizeText(env.data.output, env.data.outputRef).replace(/^\s*·\s*/, ''))
        return
      }
      setErr(env.msg || t('submitFailed'))
    } catch (e: any) { setErr(e?.message || t('submitFailed')) } finally { setBusy(false) }
  }

  return (
    <div className="scy page">
      <div className="v-head">
        <div className="v-head-l">
          <div className="v-eyebrow">ASYNC EXECUTION</div>
          <div className="v-h1">{t('t_asyncjobs')}</div>
          <div className="v-sub">{t('asyncSub')}</div>
        </div>
      </div>

      <div className="grid">
        {/* submit form */}
        <div className="card form">
          <div className="fl">{t('mInst')}</div>
          <VSelect value={selectedLabel} onChange={pickLabel} options={connLabel} />
          <div className="fl">{t('fDatabase')}</div>
          {dbOptions.length > 0
            ? <VSelect value={db} onChange={setDb} options={dbOptions} />
            : <input value={db} onChange={(e) => setDb(e.target.value)} className="in" placeholder={t('fDatabase')} />}
          <div className="fl">SQL</div>
          <textarea value={sql} onChange={(e) => setSql(e.target.value)} className="sqlbox" spellCheck={false}></textarea>
          <div className="fl">{t('reason')}</div>
          <input value={reason} onChange={(e) => setReason(e.target.value)} className="in" placeholder={t('asyncReasonPh')} />
          <div className="acts">
            {err && <span className="err">{err}</span>}
            <VButton variant="primary" height="38px" disabled={busy} onClick={submit}><Play size={15} />{t('asyncSubmit')}</VButton>
          </div>
          <div className="hint">{t('asyncHint')}</div>
        </div>

        {/* job list + log */}
        <div className="card jobs">
          <div className="jhead">{t('asyncJobs')}</div>
          {!jobs.length && <EmptyState compact icon={Hourglass} title={t('asyncEmpty')} />}
          {jobs.map((j) => {
            const m = statusMeta(j.status)
            return (
              <div
                key={j.id} className={clsx('job', openId === j.id && 'open')}
                onClick={() => { setOpenId(openId === j.id ? 0 : j.id); void refreshOpen() }}
              >
                <div className="jrow">
                  <m.icon size={15} className={clsx(m.spin && 'spin')} color={m.c} />
                  <span className="jno">#{j.id}</span>
                  <span className="jinst mono">{j.instance}{j.database && <span className="dbtag"> / {j.database}</span>}</span>
                  <span className="jsql mono">{j.sql}</span>
                  <span className="jtime mono">{fmt(j.createdAt)}</span>
                </div>
                {openId === j.id && (
                  <div className="log">
                    <pre className="logpre">{localizeLines(j.log, j.logRefs) || '· …'}</pre>
                    {j.status === 'done' ? <div className="logfoot ok">✓ {t('asyncDone', { n: j.rows })}</div>
                      : j.status === 'failed' ? <div className="logfoot err">✕ {localizeText(j.error, j.errorRef)}</div>
                        : <div className="logfoot run"><Loader size={12} className="spin" />{t('asyncRunning')}</div>}
                  </div>
                )}
              </div>
            )
          })}
        </div>
      </div>
    </div>
  )
}
