import { useEffect, useRef, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import clsx from 'clsx'
import {
  CircleCheck, CircleX, Hourglass, Loader2, Plus, RotateCw, ShieldAlert, Terminal,
} from 'lucide-react'
import { useAsyncJob, useAsyncJobs, useExecAsync } from '@/hooks/useAsyncJobs'
import { useDbOptions } from '@/hooks/useDbOptions'
import { useTierOf } from '@/hooks/useTier'
import { connectionsQueryOptions } from '@/api/modules/connections'
import { isAsyncJobActive } from '@/api/modules/terminal'
import { CODE_OK } from '@/api/http'
import { Badge, type BadgeTone } from '@/components/common/Badge'
import { Button } from '@/components/common/Button'
import { Card, CardHead } from '@/components/common/Card'
import { Modal } from '@/components/common/Modal'
import { Empty, ErrorState, Loading } from '@/components/common/States'
import type { AsyncJob, Connection } from '@/types'

/** 后端给的时间是 ISO;这里只要"几月几号几点",不换算时区也不进 i18n。 */
const at = (s: string | null) => (s ? s.slice(5, 16).replace('T', ' ') : '—')

interface Draft {
  connectionId: number
  database: string
  sql: string
  reason: string
}

const EMPTY_DRAFT: Draft = { connectionId: 0, database: '', sql: '', reason: '' }

export default function AsyncJobsPage() {
  const { t } = useTranslation()
  const { data, isLoading, error, refetch } = useAsyncJobs()
  const { data: conns } = useQuery(connectionsQueryOptions())
  const tierOf = useTierOf()
  const [openId, setOpenId] = useState(0)
  const [draft, setDraft] = useState<Draft | null>(null)
  // 每次开窗都换一把 key,让表单重新挂一次 —— 否则"重填"填进去的参数会被上一次
  // 没提交完的输入盖住。
  const [formSeq, setFormSeq] = useState(0)

  const jobs = data ?? []
  const connByName = new Map<string, Connection>()
  for (const c of (conns ?? []) as Connection[]) connByName.set(c.name, c)

  function openForm(d: Draft) {
    setDraft(d)
    setFormSeq((n) => n + 1)
  }

  /**
   * 「重填」不是「重跑」。
   *
   * 一条后台任务跑的是提交那一刻的语句和那个库;凭旧参数再建一个,等于替人做了
   * 「这些参数现在还对吗」的判断 —— 而库还在不在、语句改没改过,只有人知道。所以
   * 参数填回表单,由人按下提交。
   */
  function rerun(j: AsyncJob) {
    openForm({
      connectionId: connByName.get(j.instance)?.id ?? 0,
      database: j.database,
      sql: j.sql,
      reason: j.reason,
    })
  }

  const polling = jobs.some(isAsyncJobActive)

  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1>{t('asyncTitle')}</h1>
          <p>{t('asyncSub')}</p>
        </div>
        <div className="grow">
          {polling && (
            <span className="aj-poll"><Loader2 size={13} className="spin" />{t('asyncPolling')}</span>
          )}
          <Button variant="primary" onClick={() => openForm(EMPTY_DRAFT)}>
            <Plus size={15} />{t('asyncNew')}
          </Button>
        </div>
      </header>

      {/* 这一句放在列表上面而不是提交弹窗里:提交前看到它才来得及,提交后再看到
          只是通知。 */}
      <div className="notice danger">
        <ShieldAlert size={15} />
        {t('asyncNoRollback')}
      </div>

      {isLoading && <Loading />}
      {error && <ErrorState error={error} retry={() => refetch()} />}

      {!isLoading && !error && (
        <div className="aj-grid">
          <div className="aj-list">
            {!jobs.length && (
              <Card><Empty hint={t('asyncEmpty')} /></Card>
            )}
            {jobs.map((j) => (
              <JobRow
                key={j.id}
                job={j}
                on={openId === j.id}
                tierLabel={tierOf(connByName.get(j.instance)?.env ?? '')?.displayName ?? ''}
                danger={!!tierOf(connByName.get(j.instance)?.env ?? '')?.dangerBanner}
                onOpen={() => setOpenId(openId === j.id ? 0 : j.id)}
                onRerun={() => rerun(j)}
              />
            ))}
          </div>

          <LogPanel id={openId} />
        </div>
      )}

      <NewJobModal
        key={formSeq}
        open={draft !== null}
        initial={draft ?? EMPTY_DRAFT}
        conns={(conns ?? []) as Connection[]}
        onClose={() => setDraft(null)}
        onDone={(jobId) => { setDraft(null); if (jobId) setOpenId(jobId) }}
      />
    </div>
  )
}

/** 状态的四种样子。进行中的两种额外出一条进度条。 */
function statusOf(status: string): { tone: BadgeTone; icon: ReactNode; key: string; bar: boolean } {
  switch (status) {
    case 'running':
      return { tone: 'accent', icon: <Loader2 size={12} className="spin" />, key: 'asyncStRunning', bar: true }
    case 'done':
      return { tone: 'success', icon: <CircleCheck size={12} />, key: 'asyncStDone', bar: false }
    case 'failed':
      return { tone: 'danger', icon: <CircleX size={12} />, key: 'asyncStFailed', bar: false }
    default:
      return { tone: 'warning', icon: <Hourglass size={12} />, key: 'asyncStPending', bar: true }
  }
}

function JobRow({
  job, on, tierLabel, danger, onOpen, onRerun,
}: {
  job: AsyncJob
  on: boolean
  tierLabel: string
  danger: boolean
  onOpen: () => void
  onRerun: () => void
}) {
  const { t } = useTranslation()
  const st = statusOf(job.status)

  return (
    <article className={clsx('aj-item', on && 'on')} onClick={onOpen}>
      <div className="aj-top">
        <Badge tone={st.tone} icon={st.icon}>{t(st.key)}</Badge>
        <span className="aj-no">#{job.id}</span>
        <span className="aj-inst">
          {job.instance}{job.database ? <span className="aj-db"> / {job.database}</span> : null}
        </span>
        {tierLabel && <Badge tone={danger ? 'danger' : 'neutral'}>{tierLabel}</Badge>}
        <span className="aj-when">{t('asyncStarted')} {at(job.startedAt ?? job.createdAt)}</span>
      </div>

      <div className="aj-sql">{job.sql}</div>
      {job.reason && <div className="aj-reason">{job.reason}</div>}

      {/*
        进度条是**不确定**的那种 —— 来回扫,不显示百分比。网关只回状态和日志,
        没有"跑到第几行"这个数;画一个 60% 出来,那 60% 是编的。
      */}
      {st.bar && <div className="aj-bar" role="progressbar" aria-busy="true"><span /></div>}

      <div className="aj-foot">
        {job.status === 'failed' && <span className="aj-err">{job.error || t('asyncStFailed')}</span>}
        {job.status === 'done' && <span className="aj-rows">{t('asyncRows', { n: job.rows })}</span>}
        <Button
          variant="ghost"
          title={t('asyncRerunTip')}
          onClick={(e) => { e.stopPropagation(); onRerun() }}
        >
          <RotateCw size={13} />{t('asyncRerun')}
        </Button>
      </div>
    </article>
  )
}

/**
 * 右栏:选中那条任务的日志流。
 *
 * 它自己拉详情而不是从列表里取:列表接口回的是一屏任务的概览,把每条的整份日志
 * 都带上,列表就随着任务变大 —— 而一次只看得到一条。
 */
function LogPanel({ id }: { id: number }) {
  const { t } = useTranslation()
  const { data: job, isLoading, error } = useAsyncJob(id)
  const preRef = useRef<HTMLPreElement>(null)

  // 日志是往下追加的,视线该停在最新一行。人手动往上翻时不抢回来。
  useEffect(() => {
    const el = preRef.current
    if (!el) return
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 40
    if (atBottom) el.scrollTop = el.scrollHeight
  }, [job?.log])

  return (
    <Card className="aj-logcard">
      <CardHead
        icon={<Terminal size={16} />}
        title={t('asyncLogTitle')}
        sub={job ? `#${job.id} · ${job.instance}` : t('asyncLogPick')}
      />
      <div className="aj-logbody">
        {!id && <Empty hint={t('asyncLogPick')} />}
        {!!id && isLoading && <Loading />}
        {!!id && error && <ErrorState error={error} />}
        {!!id && job && (
          <>
            <pre className="aj-log" ref={preRef}>{job.log || t('asyncLogEmpty')}</pre>
            {job.status === 'done' && (
              <div className="aj-logfoot ok">{t('asyncRows', { n: job.rows })}</div>
            )}
            {job.status === 'failed' && <div className="aj-logfoot bad">{job.error}</div>}
            {isAsyncJobActive(job) && (
              <div className="aj-logfoot run">
                <Loader2 size={12} className="spin" />{t('asyncRunningNow')}
              </div>
            )}
            {/* 网关没有中止后台任务的接口,与其放一个点不动的「取消」,不如说清楚。 */}
            <div className="aj-lognote">{t('asyncNoCancel')}</div>
          </>
        )}
      </div>
    </Card>
  )
}

function NewJobModal({
  open, initial, conns, onClose, onDone,
}: {
  open: boolean
  initial: Draft
  conns: Connection[]
  onClose: () => void
  onDone: (jobId: number) => void
}) {
  const { t } = useTranslation()
  const exec = useExecAsync()
  const [f, setF] = useState<Draft>(initial)
  const conn = conns.find((c) => c.id === f.connectionId)
  const db = useDbOptions(conn)
  const tierOf = useTierOf()
  const tier = tierOf(conn?.env ?? '')

  // 目标库为空 = 还没人动过它,这时跟着预选规则走;人一旦选过就以人的选择为准。
  const database = f.database || db.preferred

  return (
    <Modal
      open={open}
      title={t('asyncNew')}
      sub={t('asyncNewSub')}
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>{t('cancel')}</Button>
          <Button
            variant="primary"
            capability={`ddl:${tier?.code ?? ''}`}
            disabled={exec.isPending || !f.connectionId || !f.sql.trim()}
            onClick={() =>
              exec.mutate(
                { connectionId: f.connectionId, sql: f.sql.trim(), database, reason: f.reason.trim() },
                { onSuccess: (env) => { if (env.code === CODE_OK) onDone(env.data?.jobId ?? 0) } },
              )
            }
          >
            {t('asyncSubmit')}
          </Button>
        </>
      }
    >
      <div className="fld-2">
        <div className="fld">
          <label>{t('asyncInstance')}</label>
          <select
            value={f.connectionId}
            onChange={(e) => setF({ ...f, connectionId: Number(e.target.value), database: '' })}
          >
            <option value={0}>—</option>
            {conns.map((c) => <option key={c.id} value={c.id}>{c.env}-{c.name}</option>)}
          </select>
        </div>
        <div className="fld">
          <label>{t('asyncDatabase')}</label>
          {db.options.length ? (
            <select value={database} onChange={(e) => setF({ ...f, database: e.target.value })}>
              {db.options.map((d) => <option key={d} value={d}>{d}</option>)}
            </select>
          ) : (
            // 探查不到就退回手填,而不是留一个空下拉让人以为这台实例没有库。
            <input value={f.database} onChange={(e) => setF({ ...f, database: e.target.value })} />
          )}
        </div>
      </div>
      {!!f.connectionId && !db.loading && !db.options.length && db.error && (
        <div className="notice warn">{db.error}</div>
      )}
      {tier?.dangerBanner && conn && (
        <div className="notice danger">
          <ShieldAlert size={14} />{t('asyncProdWarn', { name: `${conn.env}-${conn.name}` })}
        </div>
      )}

      <div className="fld">
        <label>SQL</label>
        <textarea
          value={f.sql}
          spellCheck={false}
          onChange={(e) => setF({ ...f, sql: e.target.value })}
        />
      </div>
      <div className="fld">
        <label>{t('asyncReason')}</label>
        <input
          value={f.reason}
          placeholder={t('asyncReasonPh')}
          onChange={(e) => setF({ ...f, reason: e.target.value })}
        />
      </div>
    </Modal>
  )
}
