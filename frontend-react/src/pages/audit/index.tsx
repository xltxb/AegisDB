import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useNavigate } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import clsx from 'clsx'
import {
  Calendar, ChevronLeft, ChevronRight, CircleCheck, CircleX, Download, Filter,
  FlaskConical, Hourglass, TriangleAlert, X,
} from 'lucide-react'
import { useAudit, useAuditExport } from '@/hooks/useAudit'
import { meQueryOptions } from '@/api/modules/auth'
import { Table, type Column } from '@/components/common/Table'
import { Badge, type BadgeTone } from '@/components/common/Badge'
import { Button } from '@/components/common/Button'
import { Modal } from '@/components/common/Modal'
import { Loading, ErrorState, Empty } from '@/components/common/States'
import type { AuditRow, AuditQuery } from '@/types'

/** 一页 100 条 —— 审计是往回翻着看的,页太小会把一段连续的操作切碎在两页上。 */
const PAGE_SIZE = 100

/**
 * 两个过滤器都是**点击循环**的按钮,不是下拉。
 *
 * 每个只有三四个取值,而且几乎总是在相邻两档之间来回切(全部↔高危、近 24h↔近 7 天)。
 * 下拉要「点开—移动—再点」三步,循环按钮一步就到位。
 */
const RISK_CYCLE = ['all', 'high', 'mid', 'low'] as const
const RISK_LABEL: Record<string, string> = {
  all: 'audRiskAll', high: 'audRiskHigh', mid: 'audRiskMid', low: 'audRiskLow',
}
/** 相对范围与它们要发给服务端的值,下标一一对应。 */
const RANGE_CYCLE = ['24h', '7d', '30d', ''] as const
const RANGE_LABEL = ['audT24h', 'audT7d', 'audT30d', 'audTAll']

const RESULT_META: Record<string, { key: string; cls: string; Icon: typeof CircleCheck }> = {
  pending: { key: 'rPending', cls: 's-wait', Icon: Hourglass },
  executed: { key: 'rExecuted', cls: 's-ok', Icon: CircleCheck },
  rejected: { key: 'rRejected', cls: 's-bad', Icon: CircleX },
  // 数据以文件形式离开了控制台。中性而非告警:没有任何东西出错,它只是一件以后要
  // 找得回来的事实。
  exported: { key: 'rExported', cls: 's-info', Icon: Download },
  // 这条命令**根本没碰过任何数据库** —— 它走的是模拟路径(仅开发/演示环境)。
  // 少了这一条它会落到下面的"已告警",而那和"已执行"一样不是真的。
  simulated: { key: 'rSimulated', cls: 's-muted', Icon: FlaskConical },
}
const RESULT_FALLBACK = { key: 'rWarn', cls: 's-wait', Icon: TriangleAlert }

const RISK_TONE: Record<string, BadgeTone> = { high: 'danger', mid: 'warning' }

/** 行可能跨天(按绝对时间过滤或翻到很后面时),所以日期和时间都要显示。 */
function fmtTime(s: string): string {
  const d = new Date(s)
  if (Number.isNaN(d.getTime())) return s
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

/** 命令的第一个词就是它的动作 —— 按风险着色的正是这个词。 */
function keywordOf(cmd: string): string {
  return (cmd || '').trim().split(/\s+/)[0] ?? ''
}

export default function AuditPage() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const { data: me } = useQuery(meQueryOptions())
  const [riskIdx, setRiskIdx] = useState(0)
  const [rangeIdx, setRangeIdx] = useState(0)
  const [from, setFrom] = useState('')
  const [to, setTo] = useState('')
  const [page, setPage] = useState(1)
  const [detail, setDetail] = useState<AuditRow | null>(null)

  // 绝对起止时间一旦填了就压过相对范围(auditQS 也是这么拼的),所以相对范围那个
  // 按钮要显得"不生效",否则界面会同时声称两套时间条件。
  const useAbsolute = !!from || !!to
  const query: AuditQuery = {
    risk: RISK_CYCLE[riskIdx],
    range: RANGE_CYCLE[rangeIdx],
    from, to,
    page, pageSize: PAGE_SIZE,
  }
  const { data, isLoading, error, refetch } = useAudit(query)
  const exportCsv = useAuditExport()

  const rows = data?.items ?? []
  const total = data?.total ?? 0
  const pages = Math.max(1, Math.ceil(total / PAGE_SIZE))

  /**
   * 一条审计记录带着批准它的那张单,而"谁批的"正是看完这一行之后的下一个问题。
   * 只对**进得去审批页**的人开放这个跳转 —— 否则这一下会撞在守卫上,看着像坏了。
   */
  const canOpenApproval = !!me?.menus?.approve
  function openApproval(apNo: string) {
    if (apNo && canOpenApproval) navigate(`/approvals?ap=${encodeURIComponent(apNo)}`)
  }

  // 任何过滤条件变了都回到第一页:留在第 7 页看新条件下只有 2 页的结果,会得到一张
  // 空表,而那张空表说的是"没有记录",并不是真的。
  function cycleRisk() { setRiskIdx((i) => (i + 1) % RISK_CYCLE.length); setPage(1) }
  function cycleRange() {
    setRangeIdx((i) => (i + 1) % RANGE_CYCLE.length)
    setFrom(''); setTo('') // 不清掉绝对区间,换了预设也不会生效
    setPage(1)
  }
  function setAbsolute(which: 'from' | 'to', v: string) {
    if (which === 'from') setFrom(v); else setTo(v)
    setPage(1)
  }
  function clearAbsolute() { setFrom(''); setTo(''); setPage(1) }

  const columns: Column<AuditRow>[] = [
    {
      key: 'time', head: t('colTime'), width: '0.9fr', mono: true,
      cell: (r) => fmtTime(r.occurredAt),
    },
    {
      key: 'who', head: t('colWho'), width: '1fr',
      cell: (r) => r.actor,
    },
    {
      key: 'inst', head: t('colInstance'), width: '1.1fr', mono: true,
      cell: (r) => <>{r.instance}{r.database && <span className="aud-db"> / {r.database}</span>}</>,
    },
    {
      key: 'cmd', head: t('colCmd'), width: '2.2fr',
      cell: (r) => (
        <div className="aud-cmd" title={r.command}>
          <span className={clsx('aud-kw', `k-${r.risk}`)}>{keywordOf(r.command)}</span>
          <span className="aud-rest">{r.command.slice(keywordOf(r.command).length).trim()}</span>
        </div>
      ),
    },
    {
      key: 'risk', head: t('colRisk'), width: '0.8fr',
      cell: (r) => (
        <Badge tone={RISK_TONE[r.risk] ?? 'accent'}>{t(RISK_LABEL[r.risk] ?? 'audRiskLow')}</Badge>
      ),
    },
    {
      key: 'result', head: t('colResult'), width: '1fr',
      cell: (r) => {
        const m = RESULT_META[r.result] ?? RESULT_FALLBACK
        return <span className={clsx('aud-res', m.cls)}><m.Icon size={12} />{t(m.key)}</span>
      },
    },
    {
      key: 'ap', head: t('colAp'), width: '0.9fr',
      cell: (r) => (
        <span
          className={clsx('aud-ap', r.approvalNo && canOpenApproval && 'link', !r.approvalNo && 'none')}
          title={r.approvalNo && canOpenApproval ? t('audOpenTicket') : undefined}
          onClick={(e) => { e.stopPropagation(); openApproval(r.approvalNo) }}
        >
          {r.approvalNo ? `#${r.approvalNo}` : '—'}
        </span>
      ),
    },
  ]

  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1>{t('audTitle')}</h1>
          <p>{t('audSub')}</p>
        </div>
        <div className="grow">
          <Button onClick={cycleRisk} title={t('audRiskCycle')}>
            <Filter size={14} />{t(RISK_LABEL[RISK_CYCLE[riskIdx]])}
          </Button>
          <Button
            className={clsx(useAbsolute && 'is-muted')}
            onClick={cycleRange}
            title={useAbsolute ? t('audRangeOverridden') : t('audRangeCycle')}
          >
            <Calendar size={14} />{t(RANGE_LABEL[rangeIdx])}
          </Button>
          <div className="aud-range">
            <input
              type="datetime-local" aria-label={t('audFrom')}
              value={from} onChange={(e) => setAbsolute('from', e.target.value)}
            />
            <span className="dash">—</span>
            <input
              type="datetime-local" aria-label={t('audTo')}
              value={to} onChange={(e) => setAbsolute('to', e.target.value)}
            />
            {useAbsolute && (
              <button className="clr" title={t('audClearRange')} onClick={clearAbsolute}>
                <X size={14} />
              </button>
            )}
          </div>
          <Button
            variant="secondary"
            disabled={exportCsv.isPending}
            onClick={() => exportCsv.mutate(query)}
          >
            <Download size={14} />{exportCsv.isPending ? t('audExporting') : t('audExport')}
          </Button>
        </div>
      </header>

      {isLoading && <Loading />}
      {error && <ErrorState error={error} retry={() => refetch()} />}
      {!isLoading && !error && (
        <>
          <Table
            columns={columns}
            rows={rows}
            rowKey={(r) => r.id}
            empty={<Empty hint={t('audEmpty')} />}
            onRowClick={(r) => setDetail(r)}
          />
          <div className="page-foot">
            <span className="hint">{t('audFootHint')}</span>
            <span className="total">{t('audTotal', { n: total })}</span>
            <div className="pager">
              <button className="pg" disabled={page <= 1} onClick={() => setPage((p) => Math.max(1, p - 1))}>
                <ChevronLeft size={15} />
              </button>
              <span className="pgn">{t('audPageOf', { p: page, n: pages })}</span>
              <button className="pg" disabled={page >= pages} onClick={() => setPage((p) => Math.min(pages, p + 1))}>
                <ChevronRight size={15} />
              </button>
            </div>
          </div>
        </>
      )}

      <DetailModal
        row={detail}
        canOpenApproval={canOpenApproval}
        onClose={() => setDetail(null)}
        onOpenApproval={openApproval}
      />
    </div>
  )
}

/**
 * 命令全文放在详情卡里,不放在行里。
 *
 * 一条被审计的命令常常是**整个脚本**。摊进表格就只剩两条路:截断(藏掉的往往正是
 * 要紧的那一段),或者把整行撑坏。行里给动作与目标,全文点开看。
 */
function DetailModal({
  row, canOpenApproval, onClose, onOpenApproval,
}: {
  row: AuditRow | null
  canOpenApproval: boolean
  onClose: () => void
  onOpenApproval: (apNo: string) => void
}) {
  const { t } = useTranslation()
  if (!row) return null
  const m = RESULT_META[row.result] ?? RESULT_FALLBACK
  return (
    <Modal
      open
      width={760}
      title={t('colCmd')}
      sub={`${fmtTime(row.occurredAt)} · ${row.actor}`}
      onClose={onClose}
    >
      <pre className="aud-full">{row.command}</pre>
      <div className="aud-grid">
        <div>
          <span className="ml">{t('colInstance')}</span>
          <div className="dv mono">{row.instance}{row.database ? ` / ${row.database}` : ''}</div>
        </div>
        <div>
          <span className="ml">{t('colRisk')}</span>
          <div className="dv">
            <Badge tone={RISK_TONE[row.risk] ?? 'accent'}>{t(RISK_LABEL[row.risk] ?? 'audRiskLow')}</Badge>
          </div>
        </div>
        <div>
          <span className="ml">{t('colResult')}</span>
          <div className="dv"><span className={clsx('aud-res', m.cls)}><m.Icon size={12} />{t(m.key)}</span></div>
        </div>
        {/* operator 只有在"批准的人不是执行的人"时才有值 —— 空着表示同一个人。 */}
        {row.operator && (
          <div><span className="ml">{t('colOperator')}</span><div className="dv">{row.operator}</div></div>
        )}
        {row.approvalNo && (
          <div>
            <span className="ml">{t('colAp')}</span>
            <div
              className={clsx('dv aud-ap', canOpenApproval && 'link')}
              onClick={() => onOpenApproval(row.approvalNo)}
            >#{row.approvalNo}</div>
          </div>
        )}
        {/* 链式哈希:这一行有没有被改过,靠它来验。 */}
        {row.hash && (
          <div><span className="ml">{t('colHash')}</span><div className="dv mono aud-hash">{row.hash}</div></div>
        )}
      </div>
    </Modal>
  )
}
