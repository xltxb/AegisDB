import { useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import {
  Check, CircleCheck, CircleX, Copy, Download, Eye, EyeOff, Hourglass, KeyRound, Loader2,
  Plus, RotateCw, ShieldAlert, Trash2,
} from 'lucide-react'
import { useCreateExport, useDownloadExport, useExportConfig, useExportJobs } from '@/hooks/useExport'
import { useDbOptions } from '@/hooks/useDbOptions'
import { useTierOf } from '@/hooks/useTier'
import { connectionsQueryOptions } from '@/api/modules/connections'
import { isExportJobActive } from '@/api/modules/terminal'
import { CODE_OK } from '@/api/http'
import { copyText } from '@/lib/clipboard'
import { Badge, type BadgeTone } from '@/components/common/Badge'
import { Button } from '@/components/common/Button'
import { Card } from '@/components/common/Card'
import { Modal } from '@/components/common/Modal'
import { Empty, ErrorState, Loading } from '@/components/common/States'
import type { Connection, ExportJob } from '@/types'

const at = (s: string | null) => (s ? s.slice(5, 16).replace('T', ' ') : '—')
const size = (n: number) =>
  n < 1024 ? `${n} B` : n < 1048576 ? `${(n / 1024).toFixed(1)} KB` : `${(n / 1048576).toFixed(2)} MB`

/** 六种状态,原型强调的是其中三种:执行中 / 等审批 / 完成。 */
function statusOf(s: string): { tone: BadgeTone; icon: ReactNode; key: string } {
  switch (s) {
    case 'running':
      return { tone: 'accent', icon: <Loader2 size={12} className="spin" />, key: 'exportStRunning' }
    case 'awaiting':
      return { tone: 'warning', icon: <Hourglass size={12} />, key: 'exportStAwaiting' }
    case 'done':
      return { tone: 'success', icon: <CircleCheck size={12} />, key: 'exportStDone' }
    case 'failed':
      return { tone: 'danger', icon: <CircleX size={12} />, key: 'exportStFailed' }
    case 'expired':
      return { tone: 'neutral', icon: <Trash2 size={12} />, key: 'exportStExpired' }
    default:
      return { tone: 'warning', icon: <Hourglass size={12} />, key: 'exportStPending' }
  }
}

interface Draft {
  connectionId: number
  database: string
  name: string
  sql: string
  includeSensitive: boolean
}

const EMPTY_DRAFT: Draft = { connectionId: 0, database: '', name: '', sql: '', includeSensitive: false }

export default function ExportPage() {
  const { t } = useTranslation()
  const { data, isLoading, error, refetch } = useExportJobs()
  const { data: cfg } = useExportConfig()
  const { data: conns } = useQuery(connectionsQueryOptions())
  const [draft, setDraft] = useState<Draft | null>(null)
  const [formSeq, setFormSeq] = useState(0)

  const jobs = data ?? []

  function openForm(d: Draft) {
    setDraft(d)
    setFormSeq((n) => n + 1)
  }

  /** 失败/过期的出口是「重填」:凭旧参数再建一个,该由人确认一次。 */
  function reuse(j: ExportJob) {
    const c = ((conns ?? []) as Connection[]).find((x) => x.name === j.instance)
    openForm({
      connectionId: c?.id ?? 0,
      database: j.database,
      name: j.name,
      sql: j.sql,
      includeSensitive: !!j.includeSensitive,
    })
  }

  return (
    <div className="page page-narrow">
      <header className="page-head">
        <div>
          <h1>{t('exportTitle')}</h1>
          <p>{t('exportSub')}</p>
        </div>
        <div className="grow">
          {jobs.some(isExportJobActive) && (
            <span className="aj-poll"><Loader2 size={13} className="spin" />{t('exportPolling')}</span>
          )}
          <Button variant="primary" onClick={() => openForm(EMPTY_DRAFT)}>
            <Plus size={15} />{t('exportNew')}
          </Button>
        </div>
      </header>

      {isLoading && <Loading />}
      {error && <ErrorState error={error} retry={() => refetch()} />}

      {!isLoading && !error && (
        <div className="ex-col">
          {!jobs.length && <Card><Empty hint={t('exportEmpty')} /></Card>}
          {jobs.map((j) => (
            <JobCard key={j.id} job={j} retentionDays={cfg?.retentionDays ?? 0} onReuse={() => reuse(j)} />
          ))}
        </div>
      )}

      {/* 保留期写在列表脚下而不是帮助文档里:导出完了才知道"三天后文件没了",
          那三天就已经被当成永久的了。取不到设置就什么都不说,不编一个默认值。 */}
      {cfg && (
        <p className="ex-retention">
          {cfg.retentionDays > 0
            ? t('exportRetention', { d: cfg.retentionDays })
            : t('exportRetentionForever')}
        </p>
      )}

      <NewExportModal
        key={formSeq}
        open={draft !== null}
        initial={draft ?? EMPTY_DRAFT}
        conns={(conns ?? []) as Connection[]}
        onClose={() => setDraft(null)}
      />
    </div>
  )
}

function JobCard({
  job, retentionDays, onReuse,
}: { job: ExportJob; retentionDays: number; onReuse: () => void }) {
  const { t } = useTranslation()
  const st = statusOf(job.status)
  const download = useDownloadExport()
  const [shown, setShown] = useState(false)
  const [copied, setCopied] = useState(false)

  // 口令只回显这一次,所以这里尤其不能静默失败:走 lib/clipboard 才有非安全上下文
  // (局域网 IP 打开)下的 execCommand 兜底,而且它如实返回成没成 —— 没成就不给
  // "已复制"的绿勾,口令还显示在旁边,人至少知道要自己选中。
  async function copyPw() {
    if (!(await copyText(job.password))) return
    setCopied(true)
    setTimeout(() => setCopied(false), 1600)
  }

  return (
    <Card className="ex-card">
      <div className="ex-head">
        <Badge tone={st.tone} icon={st.icon}>{t(st.key)}</Badge>
        <span className="ex-name">{job.name || `#${job.id}`}</span>
        <span className="ex-inst">
          {job.instance}{job.database ? <span className="ex-db"> / {job.database}</span> : null}
        </span>
        <span className="ex-when">{at(job.createdAt)}</span>
      </div>

      <div className="ex-sql">{job.sql}</div>

      {/* 执行中:不确定进度条 —— 网关只回状态,没有"跑了百分之几"这个数。 */}
      {isExportJobActive(job) && <div className="aj-bar" role="progressbar" aria-busy="true"><span /></div>}

      {job.status === 'awaiting' && (
        <div className="notice warn">
          <Hourglass size={14} />
          {t('exportAwaitingHint')}{job.apNo ? ` · ${job.apNo}` : ''}
        </div>
      )}

      {(job.status === 'done' || job.status === 'expired') && (
        <div className="ex-stat">
          {t('exportRowsN', { n: job.rows })} · {size(job.bytes)} · {job.parts} {t('exportParts')}
        </div>
      )}

      {job.status === 'done' && (
        <div className="ex-pw">
          <KeyRound size={13} />
          <span className="ex-pwl">{t('exportPassword')}</span>
          {/* 默认打码:密码不该在 2 秒一次的轮询里一直躺在屏幕上。 */}
          <code className="ex-pwv">{shown ? job.password : '••••••••••'}</code>
          <button
            className="iconbtn"
            title={t(shown ? 'exportHidePw' : 'exportShowPw')}
            onClick={() => setShown(!shown)}
          >
            {shown ? <EyeOff size={14} /> : <Eye size={14} />}
          </button>
          <button className="iconbtn" title={t('copy')} onClick={copyPw}>
            {copied ? <Check size={14} /> : <Copy size={14} />}
          </button>
          <span className="ex-once">{t('exportPwOnce')}</span>
        </div>
      )}

      {job.status === 'expired' && (
        <div className="ex-gone">
          <Trash2 size={13} />{t('exportExpiredHint', { d: retentionDays })}
        </div>
      )}

      {job.status === 'failed' && <div className="ex-err">{job.error || t('exportStFailed')}</div>}

      <div className="ex-ops">
        {job.status === 'done' && (
          <Button variant="secondary" disabled={download.isPending} onClick={() => download.mutate(job)}>
            <Download size={13} />
            {t('exportDownload')}{job.parts > 1 ? ` (${job.parts})` : ''}
          </Button>
        )}
        {(job.status === 'failed' || job.status === 'expired') && (
          <Button variant="ghost" onClick={onReuse}><RotateCw size={13} />{t('exportReuse')}</Button>
        )}
      </div>
    </Card>
  )
}

function NewExportModal({
  open, initial, conns, onClose,
}: { open: boolean; initial: Draft; conns: Connection[]; onClose: () => void }) {
  const { t } = useTranslation()
  const create = useCreateExport()
  const [f, setF] = useState<Draft>(initial)
  // 人主动选过目标库之后就不再跟预选规则走。'' 在这里是"实例默认库",是一个真实
  // 选项,所以要单独记一个"动过没有",不能拿空串当未选。
  const [dbTouched, setDbTouched] = useState(false)
  const conn = conns.find((c) => c.id === f.connectionId)
  const db = useDbOptions(conn)
  const tierOf = useTierOf()
  const tier = tierOf(conn?.env ?? '')

  const database = dbTouched ? f.database : (f.database || db.preferred)

  return (
    <Modal
      open={open}
      title={t('exportNew')}
      sub={t('exportNewSub')}
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>{t('cancel')}</Button>
          {/* 导出是一次读,所以看 select 能力;分层取自选中的实例。 */}
          <Button
            variant="primary"
            capability={`select:${tier?.code ?? ''}`}
            disabled={create.isPending || !f.connectionId || !f.sql.trim()}
            onClick={() =>
              create.mutate(
                {
                  connectionId: f.connectionId,
                  sql: f.sql.trim(),
                  name: f.name.trim(),
                  database,
                  includeSensitive: f.includeSensitive,
                },
                { onSuccess: (env) => { if (env.code === CODE_OK) onClose() } },
              )
            }
          >
            {t('exportSubmit')}
          </Button>
        </>
      }
    >
      <div className="fld-2">
        <div className="fld">
          <label>{t('exportConn')}</label>
          <select
            value={f.connectionId}
            onChange={(e) => {
              setF({ ...f, connectionId: Number(e.target.value), database: '' })
              setDbTouched(false)
            }}
          >
            <option value={0}>—</option>
            {conns.map((c) => <option key={c.id} value={c.id}>{c.env}-{c.name}</option>)}
          </select>
        </div>
        <div className="fld">
          <label>{t('exportDb')}</label>
          {db.options.length ? (
            <select
              value={database}
              onChange={(e) => { setF({ ...f, database: e.target.value }); setDbTouched(true) }}
            >
              <option value="">{t('exportDbDefault')}</option>
              {db.options.map((d) => <option key={d} value={d}>{d}</option>)}
            </select>
          ) : (
            <input
              value={f.database}
              onChange={(e) => { setF({ ...f, database: e.target.value }); setDbTouched(true) }}
            />
          )}
        </div>
      </div>

      {/*
        红色警告的判据是**分层的属性位**,不是环境叫不叫 prod。规则挂在分层上,环境
        只决定实例归属 —— 第二个生产集群照样该出警告,而把 prod 环境改挂到 dev 分层
        之后就不该出。
      */}
      {tier?.dangerBanner && conn && (
        <div className="notice danger">
          <ShieldAlert size={14} />{t('exportProdWarn', { name: `${conn.env}-${conn.name}` })}
        </div>
      )}

      <div className="fld">
        <label>{t('exportName')}</label>
        <input
          value={f.name}
          placeholder={t('exportNamePh')}
          onChange={(e) => setF({ ...f, name: e.target.value })}
        />
      </div>
      <div className="fld">
        <label>{t('exportSql')}</label>
        <textarea value={f.sql} spellCheck={false} onChange={(e) => setF({ ...f, sql: e.target.value })} />
      </div>

      {/* 打码是常态,放开才需要理由 —— 所以默认不勾,并且勾上之后立刻说清代价。 */}
      <label className="ex-chk">
        <input
          type="checkbox"
          checked={f.includeSensitive}
          onChange={(e) => setF({ ...f, includeSensitive: e.target.checked })}
        />
        {t('exportSensitive')}
      </label>
      {f.includeSensitive && (
        <div className="notice warn"><ShieldAlert size={14} />{t('exportSensitiveHint')}</div>
      )}
    </Modal>
  )
}
