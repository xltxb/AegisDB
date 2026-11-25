// 管理总览:管理后台的首页,一页看全局(管理后台拆分,工单 06)。
//
// 四块:实例连通状态、待审批(最久没人处理的在前)、在线用户、最近的高危操作。全部来自
// 已有接口 —— 实例列表(管理员看得到全部,带上次巡检结论)、审批单列表、在线用户、全量审计 ——
// 没有另开聚合接口:这些接口各自已有平台管理员的校验,另开一个就多一处要守的门。
import { useState } from 'react'
import clsx from 'clsx'
import { Database, ClipboardCheck, ShieldAlert, RotateCw, CircleAlert, CircleCheck, CircleHelp, Wrench, TriangleAlert } from 'lucide-react'
import api from '@/api'
import OnlineUsersPanel from '@/components/perms/OnlineUsersPanel'
import { useMountEffect } from '@/hooks/effects'
import { useI18n } from '@/hooks/useI18n'
import { nav } from '@/hooks/useNav'
import { useEnvTierStore } from '@/stores/envtier'
import { useAuthStore } from '@/stores/auth'
import { useInspectionStore } from '@/stores/inspection'
import { relTime, absTime } from '@/lib/relTime'
import type { Approval, AuditRow, Connection } from '@/types'
import './AdminOverviewView.scoped.css'

// 实例状态:巡检失败 > 维护中 > 从没探过 > 正常。从没探过不等于正常 —— 不替它担保。
type Health = 'fail' | 'maint' | 'unknown' | 'ok'
function health(c: Connection): Health {
  if (c.lastCheckOk === false) return 'fail'
  if (c.status === 'maint') return 'maint'
  if (c.lastCheckOk === undefined || c.lastCheckOk === null) return 'unknown'
  return 'ok'
}
const rank: Record<Health, number> = { fail: 0, maint: 1, unknown: 2, ok: 3 }

function openInsp(connId: number, reportId?: number) {
  nav.push({ path: '/inspection', query: reportId ? { instance: String(connId), report: String(reportId) } : { instance: String(connId) } })
}

const clip = (s: string, n = 90) => (s && s.length > n ? s.slice(0, n) + '…' : s)

export default function AdminOverviewView() {
  const { t } = useI18n()
  const envtier = useEnvTierStore()
  const canViewInspection = useAuthStore((s) => s.canViewInspection)
  // 今日巡检(.scratch/inspection-report 工单 05):今天严重 / 警告的实例、今天没收到报告的实例
  const insp = useInspectionStore()
  const inspRows = insp.rows ?? []
  const [showMissing, setShowMissing] = useState(false)
  const inspToday = inspRows.filter((r) => r.report && r.report.reportDate === insp.today)
  const inspIssues = inspToday
    .filter((r) => r.report!.level !== 'ok')
    .sort((a, b) => (a.report!.level === 'critical' ? 0 : 1) - (b.report!.level === 'critical' ? 0 : 1) || a.instance.localeCompare(b.instance))
  const inspMissing = inspRows.filter((r) => !r.report || r.report.reportDate !== insp.today)
  const inspCount = (lv: string) => inspToday.filter((r) => r.report!.level === lv).length
  const inspTier = (env: string) => (envtier.tierOf(env)?.code || env || '').toUpperCase()

  const [conns, setConns] = useState<Connection[]>([])
  const [checkOn, setCheckOn] = useState<boolean | null>(null) // null = 读不到设置
  const [pendingTotal, setPendingTotal] = useState(0)
  const [oldest, setOldest] = useState<Approval[]>([])
  const [highs, setHighs] = useState<AuditRow[]>([])
  const [busy, setBusy] = useState(false)

  const counts = { fail: 0, maint: 0, unknown: 0, ok: 0 }
  for (const c of conns) counts[health(c)]++

  // 按分层归堆(分层表的顺序,生产在上),每堆里异常的置顶
  const groups = new Map<string, { code: string; label: string; conns: Connection[] }>()
  for (const c of conns) {
    const code = envtier.tierOf(c.env)?.code ?? ''
    if (!groups.has(code)) groups.set(code, { code, label: code ? envtier.tierLabel(code, t) : t('dashTierUnknown'), conns: [] })
    groups.get(code)!.conns.push(c)
  }
  const order = envtier.tierCodes
  const byTier = [...groups.values()].sort((a, b) => {
    const ia = order.indexOf(a.code), ib = order.indexOf(b.code)
    return (ia < 0 ? 99 : ia) - (ib < 0 ? 99 : ib)
  })
  for (const g of byTier) g.conns.sort((a, b) => rank[health(a)] - rank[health(b)] || a.name.localeCompare(b.name))

  // 布局(2026-10 用户反馈「页面布局太奇怪」):从前三栏并排,实例一栏把 90 多个实例逐行列到底,
  // 旁边两栏只有几行、下面一大片空白,而真正要看的只有那两三个巡检失败的。现在:要处理的(异常
  // 实例)整行置顶、按严重程度排并标出分层;正常的默认收起成一个数字,要看再展开成多列网格。
  const [showOk, setShowOk] = useState(false)
  const abnormal = byTier
    .flatMap((g, ti) => g.conns.filter((c) => health(c) !== 'ok').map((c) => ({ c, tier: (g.code || '?').toUpperCase(), ti })))
    .sort((a, b) => rank[health(a.c)] - rank[health(b.c)] || a.ti - b.ti || a.c.name.localeCompare(b.c.name))
  const okByTier = byTier
    .map((g) => ({ ...g, conns: g.conns.filter((c) => health(c) === 'ok') }))
    .filter((g) => g.conns.length)

  async function loadConns() {
    try { await useEnvTierStore.getState().load() } catch { /* 分层读不到就全部归"未知分层",照样列出来 */ }
    try { setConns((await api.connections()) ?? []) } catch { setConns([]) }
    try {
      const s = await api.settings()
      setCheckOn(JSON.parse(s.settings?.['conn.check.enabled'] ?? 'false') === true)
    } catch { setCheckOn(null) }
  }

  // 待审批:列表按 id 倒序,最后一页就是最久的那几张。
  async function loadPending() {
    try {
      const head = await api.approvals('all', 1, 1, 'pending')
      setPendingTotal(head.total)
      if (!head.total) { setOldest([]); return }
      const size = 5
      const last = Math.max(1, Math.ceil(head.total / size))
      const page = await api.approvals('all', last, size, 'pending')
      let items = page.items
      if (items.length < size && last > 1) { // 最后一页不满一页:从前一页补齐
        const prev = await api.approvals('all', last - 1, size, 'pending')
        items = [...prev.items, ...items]
      }
      setOldest(items.slice().sort((a, b) => a.id - b.id).slice(0, size))
    } catch { setPendingTotal(0); setOldest([]) }
  }

  async function loadHighs() {
    try {
      const r = await api.audit({ risk: 'high', range: '7d', page: 1, pageSize: 8, full: true })
      setHighs(r.items ?? [])
    } catch { setHighs([]) }
  }

  async function load() {
    setBusy(true)
    await Promise.all([loadConns(), loadPending(), loadHighs(), useInspectionStore.getState().load(true)])
    setBusy(false)
  }
  useMountEffect(() => { void load() })

  const ago = (s?: string | null) => relTime(t, s)

  return (
    <div className="scy page">
      <div className="v-head">
        <div className="v-head-l">
          <div className="v-eyebrow">ADMIN</div>
          <div className="v-h1">{t('t_adminhome')}</div>
          <div className="v-sub">{t('t_adminhomeSub')}</div>
        </div>
        <button className="ghostbtn" disabled={busy} onClick={() => load()}><RotateCw size={14} />{t('dashRefresh')}</button>
      </div>

      <div className="stats">
        <div className={clsx('stat click', counts.fail && 'bad')} data-testid="ao-stat-conn" onClick={() => nav.push('/admin/connections')}>
          <div className="si"><Database size={15} /></div>
          <div className="sv">{counts.fail}<span className="of">{` / ${conns.length}`}</span></div>
          <div className="sl">{t('aoStatConnFail')}</div>
        </div>
        <div className={clsx('stat click', pendingTotal && 'act')} data-testid="ao-stat-pending" onClick={() => nav.push('/approvals')}>
          <div className="si"><ClipboardCheck size={15} /></div>
          <div className="sv">{pendingTotal}</div>
          <div className="sl">{t('aoStatPending')}</div>
        </div>
        <div className={clsx('stat click', highs.length && 'bad')} data-testid="ao-stat-high" onClick={() => nav.push('/admin/audit')}>
          <div className="si"><ShieldAlert size={15} /></div>
          <div className="sv">{`${highs.length}${highs.length >= 8 ? '+' : ''}`}</div>
          <div className="sl">{t('aoStatHigh')}</div>
        </div>
      </div>

      {/* 实例连通状态:整行。异常的置顶,正常的收起成一个数字 */}
      <div className="card conncard">
        <div className="chead">
          <div className="ct">{t('aoConnTitle')}</div>
          <div className="cs">
            {t('aoConnSummary', { fail: counts.fail, maint: counts.maint, unknown: counts.unknown, ok: counts.ok })}
          </div>
          {counts.ok > 0 && (
            <span className="link" data-testid="ao-toggle-ok" onClick={() => setShowOk((v) => !v)}>
              {showOk ? t('aoHideOk') : t('aoShowOk', { n: counts.ok })}
            </span>
          )}
        </div>
        {checkOn === false && <div className="note" data-testid="ao-check-off">{t('aoCheckOff')}</div>}
        {!conns.length ? <div className="empty">{t('aoNoConn')}</div>
          : !abnormal.length ? <div className="allok" data-testid="ao-all-ok"><CircleCheck size={15} />{t('aoAllOk', { n: conns.length })}</div>
            : null}
        {abnormal.length > 0 && (
          <div className="cgrid">
            {abnormal.map((x) => (
              <div key={x.c.id} className={clsx('crow', health(x.c))} data-testid="ao-conn">
                {health(x.c) === 'fail' ? <CircleAlert size={14} className="hi" />
                  : health(x.c) === 'maint' ? <Wrench size={14} className="hi" />
                    : <CircleHelp size={14} className="hi" />}
                <span className="cname">{x.c.name}</span>
                <span className="ctier">{x.tier}</span>
                <span className="ceng">{x.c.engine}</span>
                <span className="cst" title={x.c.lastCheckErr || absTime(x.c.lastCheckAt)}>
                  {t('aoHealth_' + health(x.c))}{x.c.lastCheckAt && ` · ${ago(x.c.lastCheckAt)}`}
                </span>
              </div>
            ))}
          </div>
        )}
        {showOk && okByTier.map((g) => (
          <div key={g.code} className="tgroup">
            <div className="tlabel">{g.label} <span className="tn">{g.conns.length}</span></div>
            <div className="cgrid">
              {g.conns.map((c) => (
                <div key={c.id} className="crow ok" data-testid="ao-conn">
                  <CircleCheck size={14} className="hi" />
                  <span className="cname">{c.name}</span>
                  <span className="ceng">{c.engine}</span>
                  <span className="cst" title={absTime(c.lastCheckAt)}>
                    {t('aoHealth_ok')}{c.lastCheckAt && ` · ${ago(c.lastCheckAt)}`}
                  </span>
                </div>
              ))}
            </div>
          </div>
        ))}
      </div>

      {/* 今日巡检:只有能看巡检报告的角色才有这一块 */}
      {canViewInspection && (
        <div className="card conncard" data-testid="ao-insp">
          <div className="chead">
            <div className="ct">{t('aoInspTitle')}</div>
            <div className="cs">{t('aoInspSub', { got: inspToday.length, total: inspRows.length, c: inspCount('critical'), w: inspCount('warning') })}</div>
            <span className="link" onClick={() => nav.push('/inspection')}>{t('aoInspViewAll')}</span>
          </div>
          {!inspRows.some((r) => r.report) ? <div className="empty">{t('aoInspNone')}</div> : (
            <>
              {!inspIssues.length && !inspMissing.length && <div className="allok" data-testid="ao-insp-all-ok"><CircleCheck size={15} />{t('aoInspAllOk', { n: inspRows.length })}</div>}
              {inspIssues.length > 0 && (
                <div className="cgrid">
                  {inspIssues.map((r) => (
                    <div key={r.connectionId} className={clsx('crow click', r.report!.level === 'critical' ? 'fail' : 'maint')} data-testid="ao-insp-row" onClick={() => openInsp(r.connectionId, r.report!.id)}>
                      {r.report!.level === 'critical' ? <CircleAlert size={14} className="hi" /> : <TriangleAlert size={14} className="hi" />}
                      <span className="cname">{r.instance}</span>
                      <span className="ctier">{inspTier(r.env)}</span>
                      <span className="ceng ifind" title={r.report!.topFinding}>{r.report!.topFinding || ''}</span>
                      <span className="cst">{t('inspCounts', { c: r.report!.criticalCount, w: r.report!.warningCount, i: r.report!.infoCount })}</span>
                    </div>
                  ))}
                </div>
              )}
              {inspMissing.length > 0 && (
                <div className="missing">
                  <span className="link" data-testid="ao-insp-missing" onClick={() => setShowMissing((v) => !v)}>
                    {showMissing ? t('aoInspMissingHide') : t('aoInspMissing', { n: inspMissing.length })}
                  </span>
                  {showMissing && (
                    <div className="mchips">
                      {inspMissing.map((r) => (
                        <span key={r.connectionId} className="mchip" data-testid="ao-insp-missing-item" onClick={() => openInsp(r.connectionId)}>{r.instance}</span>
                      ))}
                    </div>
                  )}
                </div>
              )}
            </>
          )}
        </div>
      )}

      <div className="grid2">
        {/* 待审批(最久的在前) */}
        <div className="card">
          <div className="chead">
            <div className="ct">{t('aoPendingTitle')}</div>
            <div className="cs">{t('aoPendingSub', { n: pendingTotal })}</div>
            <span className="link" onClick={() => nav.push('/approvals')}>{t('aoViewAll')}</span>
          </div>
          {!oldest.length && <div className="empty">{t('aoPendingNone')}</div>}
          {oldest.map((a) => (
            <div key={a.id} className="arow" data-testid="ao-pending">
              <div className="atop"><b className="mono">{a.apNo}</b><span className="ameta">{a.initiator} · {a.instance}{a.database && ` / ${a.database}`}</span><span className="aage">{ago(a.createdAt)}</span></div>
              <div className="acmd mono">{clip(a.command)}</div>
            </div>
          ))}
        </div>

        {/* 最近的高危操作 */}
        <div className="card">
          <div className="chead">
            <div className="ct">{t('aoHighTitle')}</div>
            <div className="cs">{t('aoHighSub')}</div>
            <span className="link" onClick={() => nav.push('/admin/audit')}>{t('aoViewAll')}</span>
          </div>
          {!highs.length && <div className="empty">{t('aoHighNone')}</div>}
          {highs.map((h) => (
            <div key={h.id} className="arow" data-testid="ao-high">
              <div className="atop"><b>{h.actor}</b><span className="ameta">{h.instance}{h.database && ` / ${h.database}`} · {h.result}</span><span className="aage">{ago(h.occurredAt)}</span></div>
              <div className="acmd mono">{clip(h.command)}</div>
            </div>
          ))}
        </div>
      </div>

      {/* 在线用户:从权限页移过来 */}
      <div className="card online">
        <div className="chead"><div className="ct">{t('aoOnlineTitle')}</div></div>
        <div className="opad"><OnlineUsersPanel /></div>
      </div>
    </div>
  )
}
