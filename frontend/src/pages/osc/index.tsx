import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import clsx from 'clsx'
import {
  CircleCheck, CircleX, Loader2, OctagonX, Play, ShieldAlert, TriangleAlert,
} from 'lucide-react'
import { oscApi, oscJobsQueryOptions, oscStatusQueryOptions } from '@/api/modules/osc'
import { connectionsQueryOptions } from '@/api/modules/connections'
import { copyPercent, isLive, leftovers, startRefusal, type OscJob } from '@/lib/osc'
import { Badge, type BadgeTone } from '@/components/common/Badge'
import { Button } from '@/components/common/Button'
import { Card } from '@/components/common/Card'
import { Modal } from '@/components/common/Modal'
import { Empty, ErrorState, Loading } from '@/components/common/States'
import { useUIStore } from '@/stores/ui'
import type { Connection } from '@/types'

/**
 * MySQL 在线表结构变更(ADR 0011)。
 *
 * 这张页面做的事在生产库上留痕:建影子表、成块拷贝全表、订阅 binlog、原子改名。
 * 所以它的版面不是「发起在上、历史在下」,而是**先说清这套东西现在还做不到什么**,
 * 再给按钮。那段警告从接口取(`/osc/status` 的 caveats),不在前端硬编码一份 ——
 * 后端补上一件,页面上就少一行,不会出现两边各说各话。
 */

const at = (s: string | null) => (s ? s.slice(5, 16).replace('T', ' ') : '—')

interface Draft {
  connectionId: number
  schema: string
  table: string
  alter: string
}

const EMPTY_DRAFT: Draft = { connectionId: 0, schema: '', table: '', alter: '' }

export default function OscPage() {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const notify = useUIStore((s) => s.notify)
  const { data: status } = useQuery(oscStatusQueryOptions())
  const { data, isLoading, error, refetch } = useQuery(oscJobsQueryOptions())
  const { data: conns } = useQuery(connectionsQueryOptions())
  const [draft, setDraft] = useState<Draft | null>(null)

  const jobs = (data ?? []) as OscJob[]
  // 不再按 status 过滤 —— 一条死在半路的 copying 记录正是这里最该出现的东西。
  const stranded = leftovers(jobs)
  const refusal = startRefusal(status)

  const abort = useMutation({
    mutationFn: (id: number) => oscApi.abort(id),
    onSuccess: () => {
      notify(t('oscAbortSent'), 'info')
      qc.invalidateQueries({ queryKey: ['osc', 'jobs'] })
    },
    onError: (e: Error) => notify(e.message, 'error'),
  })

  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1>{t('oscTitle')}</h1>
          <p>{t('oscSub')}</p>
        </div>
        <div className="grow">
          <Button
            variant="primary"
            disabled={refusal !== null}
            title={refusal ? t(refusal) : undefined}
            onClick={() => setDraft(EMPTY_DRAFT)}
          >
            <Play size={15} />{t('oscStart')}
          </Button>
        </div>
      </header>

      {/*
        按钮旁边,不是文档里。
        要按下"开始迁移"的那个人不会去翻 ADR,而这套东西目前**没有从库延迟限流**——
        一次不会自己减速的全表拷贝,在主从架构下能把从库拖出可观的延迟。
      */}
      <div className="notice danger osc-caveats">
        <ShieldAlert size={15} />
        <div>
          <strong>{refusal ? t(refusal) : t('oscCaveatsOn')}</strong>
          <ul>
            {(status?.caveats ?? []).map((c) => <li key={c}>{c}</li>)}
          </ul>
        </div>
      </div>

      {/*
        残局:进程重启之后,库里可能还留着一张影子表,而没有任何人在推进那条任务。
        它排在历史列表**前面**,因为它是唯一需要人现在动手的东西。
      */}
      {stranded.length > 0 && (
        <Card className="osc-stranded">
          <div className="osc-stranded-head">
            <TriangleAlert size={16} />
            <div>
              <strong>{t('oscLeftoverTitle', { n: stranded.length })}</strong>
              <p>{t('oscLeftoverHint')}</p>
            </div>
          </div>
          <ul>
            {stranded.map((j) => (
              <li key={j.id}>
                <code>{j.schema}.{j.shadow || j.table}</code>
                <span>{t('oscLeftoverOf', { id: j.id, table: j.table })}</span>
                {j.err && <em>{j.err}</em>}
              </li>
            ))}
          </ul>
        </Card>
      )}

      {isLoading && <Loading />}
      {error && <ErrorState error={error} retry={() => refetch()} />}

      {!isLoading && !error && (
        <div className="osc-list">
          {!jobs.length && <Card><Empty hint={t('oscEmpty')} /></Card>}
          {jobs.map((j) => (
            <JobRow
              key={j.id}
              job={j}
              instance={(conns as Connection[] | undefined)?.find((c) => c.id === j.connectionId)?.name ?? `#${j.connectionId}`}
              onAbort={() => abort.mutate(j.id)}
              aborting={abort.isPending}
            />
          ))}
        </div>
      )}

      <StartModal
        open={draft !== null}
        conns={(conns ?? []) as Connection[]}
        onClose={() => setDraft(null)}
        onStarted={() => { setDraft(null); qc.invalidateQueries({ queryKey: ['osc', 'jobs'] }) }}
      />
    </div>
  )
}

/** 八个状态的样子。五个在途状态共用一个"转圈",区别在文案。 */
function toneOf(status: OscJob['status']): { tone: BadgeTone; key: string } {
  switch (status) {
    case 'done': return { tone: 'success', key: 'oscStDone' }
    case 'failed': return { tone: 'danger', key: 'oscStFailed' }
    case 'aborted': return { tone: 'warning', key: 'oscStAborted' }
    case 'preflight': return { tone: 'neutral', key: 'oscStPreflight' }
    case 'copying': return { tone: 'accent', key: 'oscStCopying' }
    case 'replaying': return { tone: 'accent', key: 'oscStReplaying' }
    case 'cutover': return { tone: 'accent', key: 'oscStCutover' }
    default: return { tone: 'neutral', key: 'oscStPending' }
  }
}

function JobRow({
  job, instance, onAbort, aborting,
}: {
  job: OscJob
  instance: string
  onAbort: () => void
  aborting: boolean
}) {
  const { t } = useTranslation()
  const st = toneOf(job.status)
  const live = isLive(job)
  const pct = copyPercent(job)

  return (
    <article className={clsx('osc-item', live && 'on')}>
      <div className="osc-top">
        <Badge
          tone={st.tone}
          icon={
            live ? <Loader2 size={12} className="spin" />
              : job.status === 'done' ? <CircleCheck size={12} />
                : <CircleX size={12} />
          }
        >
          {t(st.key)}
        </Badge>
        <span className="osc-no">#{job.id}</span>
        <span className="osc-tbl">{instance} / {job.schema}.{job.table}</span>
        <span className="osc-when">{at(job.createdAt)} · {job.createdBy}</span>
        {/*
          中止按钮跟着 running 走,不跟着 status 走。网关重启之后那条任务的状态还停在
          copying,但没有任何进程拿着它的取消钩子 —— 给出的按钮按下去只会回一句
          "任务不在运行中",而人会以为自己已经止住了它。
        */}
        {job.running && (
          <Button variant="ghost" disabled={aborting} onClick={onAbort}>
            <OctagonX size={13} />{t('oscAbort')}
          </Button>
        )}
      </div>

      <div className="osc-alter">ALTER TABLE {job.table} {job.alter}</div>

      {/*
        进度只在拷贝阶段有意义,而且**总行数是估算值**:它可能为 0(统计信息没更新),
        这时宁可写"未知"也不画一根看起来完成了的条 —— 一个停在 100% 的进度条会
        让人以为可以走了。
      */}
      {job.status === 'copying' && job.running && (
        <div className="osc-prog">
          {pct === null ? (
            <span className="osc-unknown">{t('oscCopiedUnknown', { n: job.copiedRows })}</span>
          ) : (
            <>
              <div className="osc-bar" role="progressbar" aria-valuenow={pct} aria-valuemin={0} aria-valuemax={100}>
                <span style={{ width: `${pct}%` }} />
              </div>
              <span>{t('oscCopied', { n: job.copiedRows, total: job.totalRows, pct })}</span>
            </>
          )}
        </div>
      )}

      {live && !job.running && <div className="osc-orphan">{t('oscOrphan')}</div>}
      {job.shadow && <div className="osc-shadow">{t('oscShadow', { name: job.shadow })}</div>}
      {job.err && <div className="osc-err">{job.err}</div>}
    </article>
  )
}

function StartModal({
  open, conns, onClose, onStarted,
}: {
  open: boolean
  conns: Connection[]
  onClose: () => void
  onStarted: () => void
}) {
  const { t } = useTranslation()
  const notify = useUIStore((s) => s.notify)
  const [f, setF] = useState<Draft>(EMPTY_DRAFT)
  // 前置检查的阻塞项:它们是这次变更**不能**用在线方式做的理由,要一次全列出来 ——
  // 一条条试出来,在生产上就是一个个变更窗口。
  const [blockers, setBlockers] = useState<string[]>([])

  const start = useMutation({
    mutationFn: () => oscApi.start({
      connectionId: f.connectionId, schema: f.schema.trim(),
      table: f.table.trim(), alter: f.alter.trim(),
    }),
    onSuccess: () => { setF(EMPTY_DRAFT); setBlockers([]); notify(t('oscStarted'), 'ok'); onStarted() },
    onError: (e: Error & { data?: { blockers?: string[] } }) => {
      const bs = e.data?.blockers ?? []
      setBlockers(bs)
      if (!bs.length) notify(e.message, 'error')
    },
  })

  // MySQL 之外的引擎根本不该出现在选项里:影子表 + binlog 回放是 MySQL 专有的做法,
  // 让人选中一台 PostgreSQL 再被后端拒绝,是把本可以不犯的错留给他去犯。
  const mysqls = conns.filter((c) => /mysql|mariadb/i.test(c.engine))

  return (
    <Modal
      open={open}
      title={t('oscStart')}
      sub={t('oscStartSub')}
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>{t('cancel')}</Button>
          <Button
            variant="primary"
            disabled={start.isPending || !f.connectionId || !f.schema.trim() || !f.table.trim() || !f.alter.trim()}
            onClick={() => start.mutate()}
          >
            {start.isPending ? <Loader2 size={14} className="spin" /> : <Play size={14} />}
            {t('oscStartGo')}
          </Button>
        </>
      }
    >
      <div className="fld-2">
        <div className="fld">
          <label>{t('oscInstance')}</label>
          <select
            value={f.connectionId}
            onChange={(e) => setF({ ...f, connectionId: Number(e.target.value) })}
          >
            <option value={0}>—</option>
            {mysqls.map((c) => <option key={c.id} value={c.id}>{c.env}-{c.name}</option>)}
          </select>
        </div>
        <div className="fld">
          <label>{t('oscSchema')}</label>
          <input value={f.schema} onChange={(e) => setF({ ...f, schema: e.target.value })} />
        </div>
      </div>
      {!mysqls.length && <div className="notice warn">{t('oscNoMysql')}</div>}

      <div className="fld">
        <label>{t('oscTable')}</label>
        <input value={f.table} onChange={(e) => setF({ ...f, table: e.target.value })} />
      </div>
      <div className="fld">
        <label>{t('oscAlter')}</label>
        <input
          value={f.alter}
          spellCheck={false}
          placeholder="ADD INDEX idx_memo (memo)"
          onChange={(e) => setF({ ...f, alter: e.target.value })}
        />
        <small>{t('oscAlterHint')}</small>
      </div>

      {blockers.length > 0 && (
        <div className="notice danger">
          <strong>{t('oscBlocked')}</strong>
          <ul>{blockers.map((b) => <li key={b}>{b}</li>)}</ul>
        </div>
      )}
    </Modal>
  )
}
