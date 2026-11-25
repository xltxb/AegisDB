// AI 巡检报告(.scratch/inspection-report 工单 04)。
//
// 一个页面三种视图,由地址里的参数决定 —— 刷新、分享链接都回到同一处:
//   - 无参数:实例列表,每个实例一行,异常的在前;
//   - ?instance=ID:这个实例的报告历史;
//   - ?instance=ID&report=ID:报告查看器。
//
// 报告是外部系统生成的 HTML,放进 sandbox="allow-scripts" 的 iframe 里显示(地址是 .../view,不用 srcdoc:
// srcdoc 会继承控制台页面的 CSP,报告里的脚本就跑不了了):脚本能跑
// (内联图表照常画),但报告运行在"无来源"的隔离环境里,读不到平台的登录凭证和本地存储、
// 动不了父页面、不能跳走或弹窗。**不要**给它加 allow-same-origin。
import { useEffect, useState } from 'react'
import clsx from 'clsx'
import { Search, ChevronLeft, Download, CircleAlert, TriangleAlert, CircleCheck, CircleHelp } from 'lucide-react'
import api from '@/api'
import { useUIStore } from '@/stores/ui'
import { useEnvTierStore } from '@/stores/envtier'
import { saveBlob } from '@/lib/download'
import { useI18n } from '@/hooks/useI18n'
import { nav, useRoute } from '@/hooks/useNav'
import { useEvent } from '@/hooks/useEvent'
import { useStateRef } from '@/hooks/useStateRef'
import { useMountEffect, useUpdateEffect } from '@/hooks/effects'
import type { InspectionLatestRow, InspectionReport } from '@/types'
import './InspectionView.scoped.css'

const FILTERS = ['all', 'critical', 'warning', 'missing', 'ok'] as const
type Filter = (typeof FILTERS)[number]

// 状态:严重 > 警告 > 今天没收到 > 正常。今天没收到 = 从没收到过,或最新一份不是今天的。
type State = 'critical' | 'warning' | 'missing' | 'ok'
const rank: Record<State, number> = { critical: 0, warning: 1, missing: 2, ok: 3 }
const STATE_ICON = { critical: CircleAlert, warning: TriangleAlert, missing: CircleHelp, ok: CircleCheck }

const pageSize = 30
const scoreText = (s?: number | null) => (s === undefined || s === null ? '—' : String(s))

export default function InspectionView() {
  const { t } = useI18n()
  const route = useRoute()
  const envtier = useEnvTierStore()

  const instanceId = Number(route.query.instance) || 0
  const reportId = Number(route.query.report) || 0

  // ---- 实例列表 ----
  const [rows, setRows] = useState<InspectionLatestRow[]>([])
  const [today, setToday] = useState('')
  const [loaded, setLoaded] = useState(false)
  const [kw, setKw] = useState('')
  const [filter, setFilter] = useState<Filter>('all')

  function state(r: InspectionLatestRow): State {
    if (r.report && r.report.reportDate === today) {
      if (r.report.level === 'critical') return 'critical'
      if (r.report.level === 'warning') return 'warning'
      return 'ok'
    }
    return 'missing'
  }
  const tierCode = (env: string) => (envtier.tierOf(env)?.code || env || '').toUpperCase()

  const counts: Record<State, number> = { critical: 0, warning: 0, missing: 0, ok: 0 }
  for (const r of rows) counts[state(r)]++
  const received = rows.length - counts.missing
  const q = kw.trim().toLowerCase()
  const shown = rows
    .filter((r) => (filter === 'all' || state(r) === filter) && (!q || r.instance.toLowerCase().includes(q)))
    .sort((a, b) => rank[state(a)] - rank[state(b)] || a.instance.localeCompare(b.instance))

  async function loadLatest() {
    try {
      await useEnvTierStore.getState().load().catch(() => {})
      const r = await api.inspectionLatest()
      setRows(r.items ?? [])
      setToday(r.today)
    } catch (e) { useUIStore.getState().notifyError(e, t('loadFailed')) } finally { setLoaded(true) }
  }

  // ---- 报告历史 ----
  const [history, setHistory] = useState<InspectionReport[]>([])
  const [historyTotal, setHistoryTotal] = useState(0)
  const [page, setPage, pageRef] = useStateRef(1)
  const currentRow = rows.find((r) => r.connectionId === instanceId) || null
  const loadHistory = useEvent(async () => {
    if (!instanceId) return
    try {
      const r = await api.inspectionHistory(instanceId, pageRef.current, pageSize)
      setHistory(r.items ?? [])
      setHistoryTotal(r.total)
    } catch (e) { useUIStore.getState().notifyError(e, t('loadFailed')) }
  })

  // ---- 查看器 ----
  const [report, setReport] = useState<InspectionReport | null>(null)
  const [showAllFindings, setShowAllFindings] = useState(false)
  const order: Record<string, number> = { critical: 0, warning: 1, info: 2 }
  const allFindings = (report?.findings || []).slice().sort((a, b) => (order[a.severity] ?? 9) - (order[b.severity] ?? 9))
  const findings = showAllFindings ? allFindings : allFindings.slice(0, 5)
  const loadReport = useEvent(async () => {
    setReport(null); setShowAllFindings(false)
    if (!reportId) return
    try { setReport(await api.inspectionReport(reportId)) } catch (e) { useUIStore.getState().notifyError(e, t('loadFailed')) }
  })
  async function download() {
    if (!report) return
    let html: string
    try { html = await api.inspectionHtml(report.id) } catch (e) { useUIStore.getState().notifyError(e, t('loadFailed')); return }
    saveBlob(new Blob([html], { type: 'text/html;charset=utf-8' }), `${report.instanceName}-${report.reportDate}.html`)
  }

  function openInstance(id: number) { void nav.push({ query: { instance: String(id) } }) }
  function openReport(id: number) { void nav.push({ query: { instance: String(instanceId), report: String(id) } }) }
  function back() {
    if (reportId) void nav.push({ query: { instance: String(instanceId) } })
    else void nav.push({ query: {} })
  }

  useMountEffect(() => { void loadLatest() })
  useEffect(() => { setPage(1); void loadHistory() }, [instanceId, setPage, loadHistory])
  useEffect(() => { void loadReport() }, [reportId, loadReport])
  useUpdateEffect(() => { void loadHistory() }, [page])

  const levelText = (l?: string) => t('inspLevel_' + (l || 'none'))

  return (
    <div className="scy page">
      <div className="v-head">
        <div className="v-head-l">
          <div className="v-eyebrow">INSPECTION</div>
          <div className="v-h1">{t('t_inspection')}</div>
          <div className="v-sub" data-testid="insp-summary">
            {t('inspSummary', { got: received, total: rows.length, c: counts.critical, w: counts.warning })}
          </div>
        </div>
      </div>

      {reportId ? (
        // 查看器
        <>
          <button className="backlink" onClick={back}><ChevronLeft size={14} />{t('inspBackHistory')}</button>
          {report && (
            <div className="card viewer" data-testid="insp-viewer">
              <div className="vhead">
                <div className="grow">
                  <div className="vt">{report.instanceName} · {report.reportDate}</div>
                  <div className="vs">
                    <span className={clsx('lv', report.level)}>{levelText(report.level)}</span>
                    <span>{t('inspScore')} {scoreText(report.score)}</span>
                    <span>{t('inspCounts', { c: report.criticalCount, w: report.warningCount, i: report.infoCount })}</span>
                  </div>
                </div>
                <button className="ghostbtn" data-testid="insp-download" onClick={download}><Download size={14} />{t('inspDownload')}</button>
              </div>
              {findings.length > 0 && (
                <div className="findings">
                  {findings.map((f, i) => (
                    <div key={i} className={clsx('frow', f.severity)}>
                      <span className={clsx('lv', f.severity === 'info' ? 'ok' : f.severity)}>{t('inspSev_' + f.severity)}</span>
                      {f.category && <span className="fcat">{f.category}</span>}
                      <span className="ftitle">{f.title}</span>
                      {f.object && <span className="fobj mono">{f.object}</span>}
                    </div>
                  ))}
                  {(report.findings || []).length > 5 && (
                    <button className="morebtn" onClick={() => setShowAllFindings(!showAllFindings)}>
                      {showAllFindings ? t('inspFindingsLess') : t('inspFindingsAll', { n: (report.findings || []).length })}
                    </button>
                  )}
                </div>
              )}
              {/* 沙箱:只给 allow-scripts。不要加 allow-same-origin / allow-top-navigation / allow-popups(见文件头)。 */}
              <iframe className="frame" sandbox="allow-scripts" src={api.inspectionViewUrl(report.id)} title={report.instanceName} data-testid="insp-frame" />
            </div>
          )}
        </>
      ) : instanceId ? (
        // 报告历史
        <>
          <button className="backlink" onClick={back}><ChevronLeft size={14} />{t('inspBackList')}</button>
          <div className="card">
            <div className="chead">
              <div className="ct">{currentRow?.instance || ''}</div>
              <div className="cs">{t('inspHistorySub', { n: historyTotal })}</div>
            </div>
            {!history.length && <div className="empty">{t('inspHistoryNone')}</div>}
            {history.map((h) => (
              <div key={h.id} className="hrow" data-testid="insp-history" onClick={() => openReport(h.id)}>
                <span className="hdate mono">{h.reportDate}</span>
                <span className={clsx('lv', h.level)}>{levelText(h.level)}</span>
                <span className="hscore">{t('inspScore')} {scoreText(h.score)}</span>
                <span className="hcnt">{t('inspCounts', { c: h.criticalCount, w: h.warningCount, i: h.infoCount })}</span>
              </div>
            ))}
            {historyTotal > pageSize && (
              <div className="pager">
                <button className="ghostbtn" disabled={page <= 1} onClick={() => setPage((p) => p - 1)}>{t('inspPrevPage')}</button>
                <span>{page} / {Math.ceil(historyTotal / pageSize)}</span>
                <button className="ghostbtn" disabled={page * pageSize >= historyTotal} onClick={() => setPage((p) => p + 1)}>{t('inspNextPage')}</button>
              </div>
            )}
          </div>
        </>
      ) : (
        // 实例列表
        <>
          <div className="toolbar">
            <div className="search"><Search size={14} /><input value={kw} onChange={(e) => setKw(e.target.value)} placeholder={t('inspSearchPh')} data-testid="insp-search" /></div>
            <div className="chips">
              {FILTERS.map((f) => (
                <button key={f} className={clsx('chip', filter === f && 'on')} data-testid={'insp-filter-' + f} onClick={() => setFilter(f)}>
                  {t('inspFilter_' + f)}{f !== 'all' && counts[f]}
                </button>
              ))}
            </div>
          </div>
          <div className="card">
            {loaded && !rows.length ? <div className="empty">{t('inspNoInstances')}</div>
              : loaded && rows.every((r) => !r.report) ? <div className="empty">{t('inspNoReportsYet')}</div> : null}
            {shown.map((r) => {
              const st = state(r)
              const Icon = STATE_ICON[st]
              return (
                <div key={r.connectionId} className={clsx('irow', st)} data-testid="insp-row" onClick={() => openInstance(r.connectionId)}>
                  <Icon size={15} className="hi" />
                  <span className="iname">{r.instance}</span>
                  <span className="itier">{tierCode(r.env)}</span>
                  <span className="ieng">{r.engine}</span>
                  <span className="idate mono">{r.report ? r.report.reportDate : '—'}</span>
                  <span className={clsx('lv', st === 'missing' ? 'missing' : r.report?.level)}>{st === 'missing' ? t('inspMissing') : levelText(r.report?.level)}</span>
                  <span className="iscore">{scoreText(r.report?.score)}</span>
                  <span className="icnt">{r.report ? t('inspCounts', { c: r.report.criticalCount, w: r.report.warningCount, i: r.report.infoCount }) : ''}</span>
                </div>
              )
            })}
          </div>
        </>
      )}
    </div>
  )
}
