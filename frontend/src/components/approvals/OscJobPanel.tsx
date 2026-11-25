// 在线变更工单的进度与控制(.scratch/osc-product 工单 05)。
//
// 数据来自 GET /osc/jobs/:id —— 进度由后端每 3 秒从引擎落库,这里读库,刷新、换人看都一样。
// 进行中每 3 秒再取一次;结束了就停。控制按钮只给 canControl 的人(能访问实例、对这条 DDL 有
// 生产操作权限),中止前要确认:中止 = 删掉影子表,原表不动。
// 网关重启打断的任务是 interrupted(工单 07):影子表与进度都在,给「续跑」与「放弃」。
import { useMemo, useRef, useState, type HTMLAttributes } from 'react'
import clsx from 'clsx'
import { Pause, Play, OctagonX, Loader } from 'lucide-react'
import VButton from '@/components/common/VButton'
import api from '@/api'
import { localizeText } from '@/api/http'
import { useMountEffect, useUpdateEffect } from '@/hooks/effects'
import { useEvent } from '@/hooks/useEvent'
import { useI18n } from '@/hooks/useI18n'
import { useStateRef } from '@/hooks/useStateRef'
import { useUIStore } from '@/stores/ui'
import { confirmAction } from '@/lib/confirm'
import { oscItemText, humanSize, thousands, oscTime, oscDuration, type OSCItem } from '@/lib/oscText'
import type { OSCJobView } from '@/types'
import './OscJobPanel.scoped.css'

interface Props extends HTMLAttributes<HTMLDivElement> { jobId: number; onChanged?: () => void }

export default function OscJobPanel({ jobId, onChanged, className, ...rest }: Props) {
  const { t, te, locale } = useI18n()
  const [job, setJob, jobRef] = useStateRef<OSCJobView | null>(null)
  const [busy, setBusy] = useState(false)
  const timer = useRef<ReturnType<typeof setTimeout>>(undefined)
  const alive = useRef(true)

  // 限流原因:主库负载 / 从库延迟 / 从库延迟未知。原因是引擎的规范中文,中文界面直接带上
  function throttleText(): string {
    const r = job?.throttleReason
    if (!r) return t('oscThrottled')
    if (locale.startsWith('zh')) return t('oscThrottledBy', { reason: r })
    // 其它语言:原因有身份(迁移 0061 之后)就译出来,没有就只说"限流中"
    const s = localizeText(r, job?.throttleReasonRef)
    return s !== r ? t('oscThrottledBy', { reason: s }) : t('oscThrottled')
  }
  const rawWarnings = job?.warnings
  const warnings = useMemo<OSCItem[]>(() => {
    try { return (rawWarnings ? JSON.parse(rawWarnings) : []) ?? [] } catch { return [] }
  }, [rawWarnings])
  const running = job?.status === 'running'
  // 开始 / 结束时刻与用时。执行中的"已运行"随每 3 秒的轮询刷新
  const timesText = useMemo(() => {
    const j = job
    if (!j?.startedAt) return ''
    const start = new Date(j.startedAt).getTime()
    let s = t('oscStartedAt', { at: oscTime(j.startedAt) })
    if (j.finishedAt) s += t('oscEndedAt', { at: oscTime(j.finishedAt), dur: oscDuration(new Date(j.finishedAt).getTime() - start, t) })
    else if (j.status === 'running') s += t('oscRunningFor', { dur: oscDuration(Date.now() - start, t) })
    return s
  }, [job, t])
  function pct(j: OSCJobView): number {
    if (!j.rowsEstimated) return 0
    if (j.status === 'done') return 100
    return Math.min(100, Math.floor((j.rowsCopied * 100) / j.rowsEstimated))
  }

  const load = useEvent(async () => {
    clearTimeout(timer.current)
    try {
      const before = jobRef.current?.status
      setJob(await api.oscJob(jobId))
      // 结束的那一刻让工单列表也刷新一次(执行状态由后端回写到工单上)
      if (alive.current && before === 'running' && jobRef.current?.status !== 'running') onChanged?.()
    } catch { /* 拿不到就保持上一次的样子 */ }
    if (!alive.current) return
    if (jobRef.current?.status === 'running' || jobRef.current?.status === 'awaiting') timer.current = setTimeout(load, 3000)
  })
  useMountEffect(() => {
    void load()
    return () => { alive.current = false; clearTimeout(timer.current) }
  })
  useUpdateEffect(() => { void load() }, [jobId])

  const interrupted = job?.status === 'interrupted'

  async function control(action: 'pause' | 'resume' | 'abort' | 'continue') {
    if (action === 'abort' && !(await confirmAction(t(interrupted ? 'oscAbandonConfirm' : 'oscAbortConfirm')))) return
    setBusy(true)
    try {
      await api.oscControl(jobId, action)
      await load()
    } catch (e) {
      useUIStore.getState().notifyError(e, t('actionFailed'))
    } finally {
      setBusy(false)
    }
  }

  const phaseKey = (p?: string) => (p && te('oscPhase.' + p) ? 'oscPhase.' + p : '')

  if (!job) return null
  return (
    <div {...rest} className={clsx('oscp', className)} data-testid="osc-panel">
      <div className="ohead">
        <span className="otag">{t('oscTitle')}</span>
        <code className="ostmt">{job.statement}</code>
      </div>
      <div className="ometa">
        {job.database}.{job.table} · {t('oscSize', { rows: thousands(job.sizeRows), size: humanSize(job.sizeBytes) })}
      </div>
      {warnings.length > 0 && (
        <ul className="owarn">
          {warnings.map((w) => <li key={w.code}>⚠ {oscItemText(w, locale, t, te)}</li>)}
        </ul>
      )}

      {job.status !== 'awaiting' ? (
        <>
          <div className="oprog">
            <div className="obar"><div className={clsx('ofill', job.status)} style={{ width: pct(job) + '%' }} /></div>
            <span className="opct" data-testid="osc-pct">{pct(job)}%</span>
          </div>
          <div className="ostat" data-testid="osc-stat">
            {running && !job.paused && <Loader size={13} className="spin" />}
            <b>{phaseKey(job.phase) ? t(phaseKey(job.phase)) : job.phase || '—'}</b>
            <span>{t('oscCopied', { n: thousands(job.rowsCopied), total: thousands(job.rowsEstimated) })}</span>
            <span>{t('oscReplayed', { n: thousands(job.rowsReplayed) })}</span>
            {!!job.cutOverAttempts && <span>{t('oscCutOvers', { n: job.cutOverAttempts })}</span>}
            {job.paused && <span className="obadge">{t('oscPaused')}</span>}
            {interrupted && <span className="obadge warn" data-testid="osc-interrupted">{t('oscInterrupted')}</span>}
            {job.throttled && <span className="obadge warn" title={job.throttleReason || ''} data-testid="osc-throttle">{throttleText()}</span>}
          </div>
          {timesText && <div className="otimes" data-testid="osc-times">{timesText}</div>}
          {job.status === 'done' && job.oldTable && <div className="onote" data-testid="osc-old">{t('oscOldTable', { table: job.oldTable })}</div>}
          {(job.status === 'failed' || job.status === 'aborted') && job.error && <div className="oerr">{localizeText(job.error, job.errorRef)}</div>}
          {interrupted && <div className="onote" data-testid="osc-interrupted-hint">{locale.startsWith('zh') && job.error ? job.error : t('oscInterruptedHint')}</div>}
          {interrupted && job.canControl && (
            <div className="oacts">
              <VButton variant="primary" height="30px" disabled={busy} data-testid="osc-continue" onClick={() => control('continue')}><Play size={13} />{t('oscContinue')}</VButton>
              <VButton variant="danger" height="30px" disabled={busy} data-testid="osc-abandon" onClick={() => control('abort')}><OctagonX size={13} />{t('oscAbandon')}</VButton>
            </div>
          )}
          {running && job.canControl && (
            <div className="oacts">
              {!job.paused
                ? <VButton variant="secondary" height="30px" disabled={busy} data-testid="osc-pause" onClick={() => control('pause')}><Pause size={13} />{t('oscPause')}</VButton>
                : <VButton variant="secondary" height="30px" disabled={busy} data-testid="osc-resume" onClick={() => control('resume')}><Play size={13} />{t('oscResume')}</VButton>}
              <VButton variant="danger" height="30px" disabled={busy} data-testid="osc-abort" onClick={() => control('abort')}><OctagonX size={13} />{t('oscAbort')}</VButton>
            </div>
          )}
        </>
      ) : <div className="onote">{t('oscAwaiting')}</div>}
    </div>
  )
}
