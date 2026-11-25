/**
 * 审计日志。
 *
 * 这一页要回答的是同一句话的六个部分:**什么时候、谁、从哪台机器、对哪个库、做了什么、
 * 结果如何**。2026-09 的重构围着这句话重排:
 *
 *   · 筛选散在页头的一排控件收进一张卡,并且改成"改条件 → 按查询"。逐个即时生效在
 *     这一页是负担:排障时常常要同时定住时间段、实例和人,即时生效等于每改一格就打
 *     一次请求,而中间那几次结果没有人要看。
 *   · 一行里把"谁"和"从哪台机器"并成一格、把实例与库表并成一格 —— 它们本来就是一句
 *     话,拆成两列只会让眼睛在行内来回找。
 *   · 语句原文回到行上(等宽、单行截断、可复制),完整的、排过版的那份在右侧抽屉里。
 *     此前行上只有"动词 + 表名",而一条命令到底做了什么,动词和表名说不全。
 *
 * 颜色一律走语义令牌(--text-strong / --surface-card / --success-subtle …),不写死
 * 十六进制:这套令牌本身就是 Slate 那一档,而且深色主题整套换值 —— 写死的浅灰在深色
 * 下会变成看不见的字。
 */
import { useEffect, useRef, useState, type MouseEvent } from 'react'
import clsx from 'clsx'
import { sessionHeaders } from '@/lib/session'
import {
  Search, Calendar, CircleCheck, Hourglass, CircleX, TriangleAlert, Download, FlaskConical,
  ChevronLeft, ChevronRight, X, Copy, Check, RotateCcw, ScrollText, ShieldCheck, ShieldAlert, Layers, Undo2,
  type LucideIcon,
} from 'lucide-react'
import VButton from '@/components/common/VButton'
import VSelect from '@/components/common/VSelect'
import VMultiSelect from '@/components/common/VMultiSelect'
import VDateTime from '@/components/common/VDateTime'
import api from '@/api'
import { localizeText } from '@/api/http'
import { useAuthStore } from '@/stores/auth'
import { useUIStore } from '@/stores/ui'
import { extractTables, tablesLabel } from '@/lib/sqlTables'
import { formatSQL, looksLikeSQL } from '@/lib/sqlFormat'
import { copyText } from '@/lib/clipboard'
import { auditResult, type AuditTone } from '@/lib/auditResult'
import { useI18n } from '@/hooks/useI18n'
import { nav, useRoute } from '@/hooks/useNav'
import { useStateRef } from '@/hooks/useStateRef'
import { useMountEffect } from '@/hooks/effects'
import type { AuditRow, AuditQuery } from '@/types'
import './AuditView.scoped.css'

// draft 是人正在填的那一份,applied 是列表**此刻真的按着筛**的那一份。分开两份,是为了
// 让「查询」有确切的含义:按下之前,列表还是上一次的结果,而不是一个改到一半的条件拼
// 出来的中间态。两份不一致时在按钮旁说一句,免得改完忘了按。
type Draft = {
  risk: string
  events: string[]
  actions: string[]
  sources: string[]
  instance: string
  keyword: string
  range: string
  from: string
  to: string
}
const emptyDraft = (): Draft => ({
  risk: 'all', events: [], actions: [], sources: [], instance: '', keyword: '', range: '24h', from: '', to: '',
})

const pageSizes = [20, 50, 100, 200]
const riskKeys = ['all', 'high', 'mid', 'low']
const rangeKeys = ['1h', '24h', '7d', '30d', '']

// 入口来源(迁移 0063)。顺序与后端 model.AuditSources 一致,最后一项筛旧记录。
const SOURCES = ['query', 'terminal', 'script', 'export', 'approval', 'release', 'api', 'auth', 'admin', 'system'] as const

// 时间拆成两行:日期一行、时刻一行。170px 里塞一整个 "2026-09-16 14:32:07" 只能靠缩字号
// 或者听任它折行 —— 折出来的第二行从哪儿断是浏览器说了算,十几行下来参差不齐。
const pad = (n: number) => String(n).padStart(2, '0')
function fmtDate(s: string) {
  const d = new Date(s)
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
}
function fmtClock(s: string) {
  const d = new Date(s)
  return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
}

// 这一行"做了什么"的抬头,按风险着色 —— 一屏扫过去,红色的那几个就是要找的东西。
//
// 默认取命令的第一个词,但只认**像动词的那种**:纯字母/点/下划线,且不超过 20 个字符
// (admin.role.menus 这样的内部动作名要留得下)。
// 审计行里躺着的不都是 SQL,还有"开启执行窗口「发版班车 · 演示」"这类整句描述 —— 按空
// 白切出来的第一段是半句中文,塞进一枚徽标里既读不懂也撑破格子。认不出动词就退回说风险
// 等级:那一格总得有个东西,而"高危"至少是这一行真实的属性。
const VERB_RE = /^[A-Za-z][A-Za-z0-9_.]{0,19}$/
function riskCls(r: string) { return r === 'high' ? 'v-high' : r === 'mid' ? 'v-mid' : 'v-low' }

// 库与表并成一格:orders_db.t_pay_log。取不出表名时只显示库名,两个都没有给一个短横 ——
// 登录、配置变更这类行本来就不落在某张表上。
function targetOf(r: AuditRow) {
  // tablesLabel 取不到表名时给的是一个短横,不是空串 —— 那是给"表"那一格用的占位符,
  // 在这里会被拼进 "orders_db.—",所以先还原成空。
  const label = tablesLabel(extractTables(r.command || ''))
  const tables = label === '—' ? '' : label
  if (r.database && tables) return `${r.database}.${tables}`
  return r.database || tables || ''
}

const EVENT_TYPES = ['intercept', 'approve', 'exec', 'login']
const ACTION_TYPES = ['create', 'delete', 'update', 'read', 'other']

// 结果:成 / 败 / 悬而未决,各一个胶囊。已导出与"模拟(未执行)"都是中性的 —— 没有任何
// 东西出错,把它们画成告警是不实的。
// 同一个 pending 在"拦截"与"审批"两种事件上意思相反(等人审 / 已经审过),说法见 lib/auditResult。
const TONE_ICON: Record<AuditTone, LucideIcon> = {
  ok: CircleCheck, pass: ShieldCheck, wait: Hourglass, bad: CircleX, info: Download, mute: FlaskConical, warn: TriangleAlert,
}

const stop = (fn: () => void) => (e: MouseEvent) => { e.stopPropagation(); fn() }

export default function AuditView() {
  const { t } = useI18n()
  const auth = useAuthStore()
  // 用户后台的 /audit(route.meta.mine)没有审计链校验、导出与单据轨迹 —— 那些是管理后台全量审计
  // (/admin/audit)的事(管理后台拆分,工单 04)。列表里看到谁的记录:
  //   - 能看全量审计的人(管理员 / 审计员 / DBA 负责人,auth.seesAllAudit):默认所有人的,标题「操作记录」,
  //     另有「只看我的」切换(2026-10-14 用户确认 DBA 负责人;2026-10-26 回归排查 09 补上管理员 / 审计员 ——
  //     审计员从总览「审计 → 详情」点进来的就是这页,从前落到只有自己的空表);
  //   - 其他人:只有自己的,「我的操作记录」。
  // 用户后台这页不带 full:管理后台空闲 / 白名单是管理后台的事,不该让这页报 40304 把人赶走。
  const route = useRoute()
  const userPortal = route.meta.mine === true
  // DBA 负责人在用户后台:审计导出、链校验、单据轨迹都给(2026-10-26 讨论 18,后端 middleware.AuditSupervisor)。
  // 管理员 / 审计员这三样仍在管理后台用(后端对他们照旧套管理后台的白名单与空闲超时)。
  const ownerSupervises = userPortal && auth.isDBALead && !auth.canEnterAdmin
  const seesAll = userPortal && auth.seesAllAudit
  const [onlyMine, setOnlyMine, onlyMineRef] = useStateRef(false)
  const team = seesAll && !onlyMine
  const mine = userPortal && !team
  // 发请求那一刻的"只看我的":刚切完开关就要用,不等重渲染
  const mineNow = () => userPortal && !(useAuthStore.getState().seesAllAudit && !onlyMineRef.current)
  function toggleOnlyMine() {
    setOnlyMine((v) => !v)
    setPage(1)
    void load()
    void loadInstances()
  }

  // 审计行带着授权它的工单号,顺着点过去是很自然的下一问("谁批的?")。只对进得了审批
  // 页的人给这个入口 —— 否则这一下会落在守卫上,看起来像坏了。
  const canOpenApproval = !!auth.menus.approve
  function openApproval(apNo?: string) {
    if (!apNo || !canOpenApproval) return
    void nav.push({ name: 'approvals', query: { ap: apNo } })
  }

  // ---------------------------------------------------------------- 筛选
  const [draft, setDraft, draftRef] = useStateRef<Draft>(emptyDraft)
  const [applied, setApplied, appliedRef] = useStateRef<Draft>(emptyDraft)
  const patch = (p: Partial<Draft>) => setDraft((d) => ({ ...d, ...p }))
  const dirty = JSON.stringify(draft) !== JSON.stringify(applied)

  const [rows, setRows] = useState<AuditRow[]>([])
  // 按审批单合并:同一张单的操作(拦截 → 审批 → 执行)在列表里合成一条,显示最新的一步;
  // groups 是本页每张单共几条。点开详情看这张单的全部操作(时间线)。
  const [grouped, setGrouped, groupedRef] = useStateRef(true)
  const [groups, setGroups] = useState<Record<string, number>>({})
  const [total, setTotal] = useState(0)
  const [page, setPage, pageRef] = useStateRef(1)
  const [pageSize, setPageSize, pageSizeRef] = useStateRef(50)
  const pages = Math.max(1, Math.ceil(total / pageSize))
  // 挂载即加载,初值直接给 true:否则加载前的那一帧会闪一下空态
  const [loading, setLoading] = useState(true)
  // 筛过之后的空,和"这段时间本来就没有记录"要给不同的出口:前者该给一个清空条件的按钮。
  const filtered = !!(applied.keyword || applied.instance || applied.events.length || applied.actions.length
    || applied.sources.length || applied.risk !== 'all' || applied.from || applied.to)

  function queryOf(a: Draft): AuditQuery {
    return {
      risk: a.risk,
      event: a.events,
      action: a.actions,
      source: a.sources,
      instance: a.instance,
      q: a.keyword.trim(),
      // 绝对区间一旦填了,服务端就以它为准;相对区间这时不发,免得两套条件同时在场。
      range: a.from || a.to ? '' : a.range,
      from: a.from,
      to: a.to,
      group: groupedRef.current ? 'approval' : '',
      mine: mineNow(),
      full: !userPortal, // 管理后台的全量审计:白名单外 / 空闲超时就拒,不悄悄降级成自己的
      page: pageRef.current,
      pageSize: pageSizeRef.current,
    }
  }

  async function load() {
    setLoading(true)
    try {
      const res = await api.audit(queryOf(appliedRef.current))
      setRows(res.items ?? [])
      setGroups(res.groups || {})
      setTotal(res.total)
      setPage(res.page)
    } catch (e) {
      useUIStore.getState().notifyError(e, t('loadFailed'))
    } finally {
      setLoading(false)
    }
  }

  // 实例下拉的可选值:审计里**真实出现过**的那些。照着连接表列的话,下拉里会有他够不到
  // 的实例,而一个筛出来永远是空的选项,读起来像"这台机器上什么都没发生过"。
  const [instances, setInstances] = useState<string[]>([])
  async function loadInstances() {
    try { setInstances((await api.auditInstances(mineNow(), !userPortal)) ?? []) } catch { setInstances([]) }
  }

  useMountEffect(() => {
    void load()
    void loadInstances()
  })

  function apply() {
    setApplied(structuredClone(draftRef.current))
    setPage(1)
    void load()
  }
  // 合并开关:立即生效(它不是筛选条件,不用等「查询」),回到第一页 —— 条目数变了,原来的页码没有意义。
  function toggleGrouped() {
    setGrouped((v) => !v)
    setPage(1)
    void load()
  }
  function reset() {
    setDraft(emptyDraft())
    apply()
  }
  function goto(p: number) {
    const np = Math.min(pages, Math.max(1, p))
    if (np === pageRef.current) return
    setPage(np)
    void load()
  }
  function changePageSize(n: number) {
    setPageSize(n)
    setPage(1)
    void load()
  }

  // ---- 各个下拉的取值 ↔ 显示文案 ----
  // VSelect 认的是显示文案,所以单选的那两个要把标签映射回内部取值;多选的用 VMultiSelect,
  // 它直接收 { v, label },不必来回映射。
  const riskOpts = [t('allRisk'), t('highTag'), t('med'), t('low')]
  const riskLabel = riskOpts[riskKeys.indexOf(draft.risk)] ?? riskOpts[0]
  function setRiskLabel(l: string) {
    const i = riskOpts.indexOf(l)
    if (i >= 0) patch({ risk: riskKeys[i] })
  }

  const rangeOpts = [t('t1h'), t('t24h'), t('t7d'), t('t30d'), t('tAll')]
  const rangeLabel = rangeOpts[Math.max(0, rangeKeys.indexOf(draft.range))]
  function setRangeLabel(l: string) {
    const i = rangeOpts.indexOf(l)
    if (i < 0) return
    // 选了预设就清掉绝对区间 —— 否则服务端以绝对区间为准,预设看着选中了却不生效。
    patch({ range: rangeKeys[i], from: '', to: '' })
  }

  const instOpts = [t('auditAllInstances'), ...instances]
  const instLabel = draft.instance || t('auditAllInstances')
  const setInstLabel = (l: string) => patch({ instance: l === t('auditAllInstances') ? '' : l })

  const eventOpts = EVENT_TYPES.map((v) => ({ v, label: t('auditEv_' + v) }))
  const actionOpts = ACTION_TYPES.map((v) => ({ v, label: t('auditAct_' + v) }))
  const sourceOpts = [
    ...SOURCES.map((v) => ({ v, label: t('auditSrc_' + v) })),
    { v: 'unknown', label: t('auditSrc_unknown') },
  ]
  function srcMeta(s?: string) {
    // 空 = 0063 之前写下的行:如实说「未知 / 旧记录」,不按命令去猜它当时从哪个页面来。
    if (!s || !(SOURCES as readonly string[]).includes(s)) return { t: t('auditSrc_unknown'), cls: 'src-none' }
    return { t: t('auditSrc_' + s), cls: 'src-' + s }
  }

  // ---------------------------------------------------------------- 一行怎么画
  function riskText(r: string) { return r === 'high' ? t('highTag') : r === 'mid' ? t('med') : t('low') }
  function verbOf(cmd: string, risk: string) {
    const first = (cmd || '').trim().split(/\s+/)[0] || ''
    return VERB_RE.test(first) ? first.toUpperCase() : riskText(risk)
  }
  function evMeta(ev?: string) {
    // 空 = 迁移 0039 之前写下的行。如实一个短横,不替它猜 —— 在一张哈希链审计表上猜等于造证据。
    return ev && EVENT_TYPES.includes(ev) ? { t: t('auditEv_' + ev), cls: 'ev-' + ev } : { t: '—', cls: 'ev-none' }
  }
  function actMeta(a?: string) {
    // 0040 之前的行没有这一列,同上
    return a && ACTION_TYPES.includes(a) ? { t: t('auditAct_' + a), cls: 'act-' + a } : { t: '—', cls: 'act-none' }
  }
  function resBadge(r: { result: string; eventType?: string }) {
    const v = auditResult(r.result, r.eventType)
    const Icon = v.key === 'rCancelled' ? Undo2 : TONE_ICON[v.tone]
    return <span className={clsx('stbadge', 'st-' + v.tone)}><Icon size={11} />{t(v.key)}</span>
  }

  // ---------------------------------------------------------------- 复制 / 抽屉
  const [copiedId, setCopiedId, copiedIdRef] = useStateRef<number | string>('')
  async function copyCmd(r: AuditRow | null, key: number | string) {
    if (!r) return
    // 复制的永远是**原文**,不是抽屉里排过版的那份:排版只为看,复制出去的东西要能原样
    // 对得上审计行。
    if (await copyText(r.command)) {
      setCopiedId(key)
      setTimeout(() => { if (copiedIdRef.current === key) setCopiedId('') }, 1600)
    } else {
      useUIStore.getState().notify(t('objCopyFail'), 'error')
    }
  }

  const [detail, setDetail] = useState<AuditRow | null>(null)
  const [pretty, setPretty] = useState(true)
  // 这张审批单的全部操作(按时间先后)。点时间线上的一条,抽屉切到那一条,时间线不重取。
  const [trail, setTrail] = useState<AuditRow[]>([])
  const [trailFor, setTrailFor, trailForRef] = useStateRef('')
  function showRow(r: AuditRow) {
    setDetail(r)
    setPretty(looksLikeSQL(r.command)) // 不是 SQL 的行没有"格式化"可言,默认就给原文
  }
  async function openDetail(r: AuditRow) {
    showRow(r)
    setTrail([])
    setTrailFor(r.approvalNo || '')
    // 单据轨迹接口给管理员 / 审计员(在管理后台看)与 DBA 负责人(进不了管理后台,在用户后台看)
    if (!r.approvalNo || (userPortal && !ownerSupervises)) return
    try {
      const list = await api.auditTrail(r.approvalNo)
      if (trailForRef.current === r.approvalNo) setTrail(list ?? []) // 等的时候又点了别的行:作废
    } catch { /* 取不到就不显示时间线,详情本身照常 */ }
  }
  function closeDetail() { setDetail(null); setTrail([]); setTrailFor('') }
  const detailText = !detail ? '' : pretty ? formatSQL(detail.command) : detail.command
  // 抽屉开着时按 Esc 关掉,而不是让人回头去找那个叉。
  //
  // 监听**开着的整段时间**都挂着,关掉时摘下。不能图省事用 { once: true }:那是"下一次
  // 按键之后就摘",而不是"按到 Esc 之后就摘" —— 在抽屉里按一下方向键或者 Ctrl,Esc 就
  // 再也不管用了,而这种失灵没有任何迹象,人只会觉得这一下没按准。
  //
  // 组件被卸载时抽屉可能还开着(点了侧栏走人),监听得跟着走。
  const drawerOpen = !!detail
  useEffect(() => {
    if (!drawerOpen) return
    const onDrawerKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') { setDetail(null); setTrail([]); setTrailFor('') }
    }
    window.addEventListener('keydown', onDrawerKey)
    return () => window.removeEventListener('keydown', onDrawerKey)
  }, [drawerOpen, setTrailFor])

  // ---------------------------------------------------------------- 哈希链校验
  //
  // 这一页此前只是**展示**了每行的链式校验值,没有任何地方去验它 —— 那种状态下"不可篡改"
  // 是一句没人检查的话。这个按钮就是那另一半。
  //
  // 只对监督角色亮:校验要扫全表(链是一条),它天然越过了"没有监督角色只看得见自己"那条
  // 线,服务端也照这个口径挡(service.CanVerifyAuditChain)。前端跟着亮/不亮,免得出现一个
  // 按下去必然 403 的按钮。
  const canVerify = ownerSupervises || (!userPortal &&
    (auth.me?.roleCodes || []).some((c) => c === 'admin' || c === 'audit'))
  const [verifying, setVerifying] = useState(false)
  const verifyBusy = useRef(false)
  const [verdict, setVerdict] = useState<{ ok: boolean; text: string } | null>(null)
  async function verifyChain() {
    if (verifyBusy.current) return
    verifyBusy.current = true
    setVerifying(true)
    setVerdict(null)
    try {
      const { report, reasonText, reasonRef } = await api.verifyAudit()
      setVerdict(report.ok
        ? {
            ok: true,
            text: t('auditVerifyOK', { n: report.checked, total: report.total })
              + (report.rolloverRows > 0 ? t('auditVerifyRollover', { n: report.rolloverRows }) : ''),
          }
        : {
            ok: false,
            // 断在哪一行、是哪一种断法 —— 两者要人做的事不同(去看那一行原来是什么,还是
            // 去找那一行去哪儿了),所以理由原样从服务端带下来,前端不照着三个取值各写一份。
            text: t('auditVerifyBad', { id: report.badId, at: fmtDate(report.badAt) + ' ' + fmtClock(report.badAt), why: localizeText(reasonText, reasonRef) }),
          })
    } catch (e) {
      useUIStore.getState().notifyError(e, t('auditVerifyFailed'))
    } finally {
      verifyBusy.current = false
      setVerifying(false)
    }
  }

  // ---------------------------------------------------------------- 导出
  const [exported, setExported] = useState('')
  async function doExport() {
    try {
      // 导出跟着**列表此刻真的按着的**那份条件走(applied,不是 draft):人是看着这张列表
      // 按下导出的,给他一份改到一半的条件筛出来的东西就是另一回事了。
      const res = await fetch(api.auditExportUrl(queryOf(appliedRef.current)), { headers: sessionHeaders() })
      const blob = await res.blob()
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = 'audit_export.csv'
      a.click()
      URL.revokeObjectURL(url)
      setExported('✓ ' + t('auditExported', { n: total }))
      setTimeout(() => setExported(''), 2600)
    } catch (e) {
      useUIStore.getState().notifyError(e, t('exportFailed'))
    }
  }

  const pickedN = t('auditPickedN', { n: '{n}' })

  return (
    <div className="scy page">
      <div className="v-head">
        <div className="v-head-l">
          <div className="v-eyebrow">AUDIT LOG</div>
          <div className="v-h1">{team ? t('auditTeamTitle') : mine ? t('auditMineTitle') : t('m_audit')}</div>
          <div className="v-sub sub"><ScrollText size={12} />{team ? t('auditTeamSub') : mine ? t('auditMineSub') : t('auditSub')}</div>
        </div>
        {exported && <div className="v-head-acts"><span className="exp">{exported}</span></div>}
      </div>

      {/* 筛选栏:一行紧凑表单 + 右侧操作区 */}
      <div className="card fbar" data-testid="audit-fbar">
        <div className="fields">
          <div className="f">
            <span className="fl"><Calendar size={11} />{t('colTime')}</span>
            <VSelect value={rangeLabel} onChange={setRangeLabel} options={rangeOpts} height="36px" />
          </div>
          <div className="f wide">
            <span className="fl">{t('auditFrom')} — {t('auditTo')}</span>
            <div className="rangebox">
              <VDateTime value={draft.from} onChange={(from) => patch({ from })} placeholder={t('auditFrom')} />
              <span className="dash">—</span>
              <VDateTime value={draft.to} onChange={(to) => patch({ to })} placeholder={t('auditTo')} />
              {(draft.from || draft.to) && (
                // LucideProps 的类型里没有 title,但它照样落到 <svg> 上
                <X className="clr" size={13} {...{ title: t('auditClearRange') }} onClick={() => patch({ from: '', to: '' })} />
              )}
            </div>
          </div>
          <div className="f">
            <span className="fl">{t('mInst')}</span>
            <VSelect value={instLabel} onChange={setInstLabel} options={instOpts} searchable height="36px" />
          </div>
          <div className="f" data-testid="audit-event-filter">
            <span className="fl">{t('colEvent')}</span>
            <VMultiSelect
              value={draft.events} onChange={(events) => patch({ events })} options={eventOpts} height="36px"
              placeholder={t('auditAllEvents')} summary={pickedN}
            />
          </div>
          <div className="f" data-testid="audit-action-filter">
            <span className="fl">{t('colAction')}</span>
            <VMultiSelect
              value={draft.actions} onChange={(actions) => patch({ actions })} options={actionOpts} height="36px"
              placeholder={t('auditAllActions')} summary={pickedN}
            />
          </div>
          <div className="f" data-testid="audit-source-filter">
            <span className="fl">{t('colSource')}</span>
            <VMultiSelect
              value={draft.sources} onChange={(sources) => patch({ sources })} options={sourceOpts} height="36px"
              placeholder={t('auditAllSources')} summary={pickedN}
            />
          </div>
          <div className="f">
            <span className="fl">{t('colRisk')}</span>
            <VSelect value={riskLabel} onChange={setRiskLabel} options={riskOpts} height="36px" />
          </div>
        </div>
        {/* 第二行:搜索框占满左边,操作按钮靠右。搜索与按钮同一行,回车或点查询都在手边。 */}
        <div className="fbot">
          <label className="f grow" aria-label={t('colWho') + ' / IP'}>
            <div className="searchbox">
              <Search size={14} color="var(--text-faint)" />
              <span className="sk">{t('colWho')} / IP</span>
              {/* 回车即查询:填完一个搜索框的下一个动作是回车,不是去找按钮 */}
              <input
                value={draft.keyword} onChange={(e) => patch({ keyword: e.target.value })}
                placeholder={t('auditSearchPh')} spellCheck={false}
                onKeyUp={(e) => { if (e.key === 'Enter') apply() }}
              />
            </div>
          </label>
          {dirty && <span className="dirty"><i className="ddot" />{t('auditDirty')}</span>}
          <div className="fbtns">
            <VButton variant="primary" height="36px" data-testid="audit-search" onClick={apply}>{t('auditQuery')}</VButton>
            <button className="gbtn" onClick={reset}><RotateCcw size={13} />{t('auditReset')}</button>
            {canVerify && (
              <button className="gbtn" disabled={verifying} onClick={verifyChain}>
                <ShieldCheck size={13} />{verifying ? t('auditVerifying') : t('auditVerify')}
              </button>
            )}
            <button className={clsx('gbtn', grouped && 'on')} title={t('auditGroupToggleTip')} data-testid="audit-group-toggle" onClick={toggleGrouped}>
              <Layers size={13} />{t('auditGroupToggle')}
            </button>
            {seesAll && (
              <button className={clsx('gbtn', onlyMine && 'on')} data-testid="audit-mine-toggle" onClick={toggleOnlyMine}>
                <ScrollText size={13} />{t('auditOnlyMine')}
              </button>
            )}
            {(!userPortal || ownerSupervises) && (
              <button className="gbtn" data-testid="audit-export" onClick={doExport}><Download size={13} />{t('exportCsv')}</button>
            )}
          </div>
        </div>
      </div>

      {/* 校验结论。摆在筛选与表格之间,而不是弹一下就没 —— 它是一句要被人抄走的结论。 */}
      {verdict && (
        <div className={clsx('vbar', !verdict.ok && 'bad')}>
          {verdict.ok ? <ShieldCheck size={14} /> : <ShieldAlert size={14} />}
          <span>{verdict.text}</span>
          <X size={14} className="vx" onClick={() => setVerdict(null)} />
        </div>
      )}

      {/* 表格 */}
      <div className="card">
        <div className="thead">
          <span>{t('colTime')}</span>
          <span>{t('colWho')}</span>
          <span>{t('colSource')}</span>
          <span>{t('colTarget')}</span>
          <span>{t('colSql')}</span>
          <span>{t('colCategory')}</span>
          <span>{t('colStatus')}</span>
          <span className="right">{t('apActions')}</span>
        </div>

        {/* 加载态用骨架行,不是一个转圈:表格的形状先立住,内容再填进来,视线不必重新找位置 */}
        {loading ? [1, 2, 3, 4, 5, 6, 7, 8].map((n) => (
          <div key={'sk' + n} className="tr sk">
            <span><i className="bar w60" /><i className="bar w40" /></span>
            <span><i className="bar w70" /><i className="bar w50" /></span>
            <span><i className="bar w60" /></span>
            <span><i className="bar w80" /><i className="bar w60" /></span>
            <span><i className="bar w90" /></span>
            <span><i className="bar w50" /></span>
            <span><i className="bar w60" /></span>
            <span><i className="bar w40" /></span>
          </div>
        )) : (
          <>
            {rows.map((r) => {
              const src = srcMeta(r.source)
              const target = targetOf(r)
              const ev = evMeta(r.eventType)
              const act = actMeta(r.dataAction)
              return (
                <div key={r.id} className={clsx('tr', r.risk === 'high' && 'high')} onClick={() => openDetail(r)}>
                  {/* 时间:日期一行、时刻一行,固定 170px,永远不折 */}
                  <span className="tcell">
                    <span className="clock">{fmtClock(r.occurredAt)}</span>
                    <span className="date">{fmtDate(r.occurredAt)}</span>
                  </span>

                  {/* 谁 + 从哪台机器。它们是一句话的两半,拆成两列眼睛要在行内来回找 */}
                  <span className="who">
                    <span className="wname">{r.actor}</span>
                    <span className="wip" title={r.clientIp ? '' : t('auditNoIp')}>{r.clientIp || '—'}</span>
                  </span>

                  {/* 入口来源:同样一条 SELECT,从数据查询来还是从终端来,一眼分开 */}
                  <span className="srccell">
                    <span className={clsx('srcbadge', src.cls)} data-testid="audit-source">{src.t}</span>
                  </span>

                  {/* 实例(按分层着色的胶囊)+ 库.表 */}
                  <span className="tgt">
                    {/* 没有实例的行(登录、配置变更)不画空胶囊:一个里面只有短横的标签,看着
                        像"有这么一台实例、只是名字没取到"。 */}
                    {r.instance && <span className={clsx('envtag', r.tierCode || 'unknown')}>{r.instance}</span>}
                    {target && <span className="dbtbl">{target}</span>}
                    {!r.instance && !target && <span className="dbtbl">—</span>}
                  </span>

                  {/* 动词徽标 + 语句原文(等宽、单行截断)+ 复制 */}
                  <span className="sqlcell">
                    <span className={clsx('verb', riskCls(r.risk))} title={riskText(r.risk)}>{verbOf(r.command, r.risk)}</span>
                    <code className="sql" title={r.command}>{r.command}</code>
                    <button className="cp" title={t('auditCopySql')} onClick={stop(() => void copyCmd(r, r.id))}>
                      {copiedId === r.id ? <Check size={12} /> : <Copy size={12} />}
                    </button>
                  </span>

                  {/* 分类:这一步在流程里是什么 / 它动了数据没有。两件正交的事,叠在一格里 */}
                  <span className="cat">
                    <span className={clsx('evbadge', ev.cls)}>{ev.t}</span>
                    <span className={clsx('actbadge', act.cls)}>{act.t}</span>
                  </span>

                  {/* 结果 + 授权它的工单 */}
                  <span className="stcell">
                    {resBadge(r)}
                    {r.approvalNo && (
                      <span
                        className={clsx('ap', canOpenApproval && 'link')}
                        title={canOpenApproval ? t('apOpenTicket') : ''} onClick={stop(() => openApproval(r.approvalNo))}
                      >#{r.approvalNo}</span>
                    )}
                    {grouped && r.approvalNo && (groups[r.approvalNo] || 0) > 1 && (
                      <span className="gcount" title={t('auditGroupCountTip')} data-testid="audit-group-count">
                        {t('auditGroupCount', { n: groups[r.approvalNo] })}
                      </span>
                    )}
                  </span>

                  <span className="acts">
                    <button className="lnk" onClick={stop(() => void openDetail(r))}>{t('auditDetail')}</button>
                  </span>
                </div>
              )
            })}

            {/* 空态分两种:筛没了的出口是清空条件,而不是干等 */}
            {!rows.length && (
              <div className="empty">
                <div className="eic"><ScrollText size={22} color="var(--text-faint)" /></div>
                <div className="et">{filtered ? t('auditEmptyFiltered') : t('auditEmpty')}</div>
                {filtered && <button className="eclear" onClick={reset}>{t('auditClearFilters')}</button>}
              </div>
            )}
          </>
        )}

        <div className="pfoot">
          <span className="ptotal">{t('auditTotal', { n: total })}</span>
          <div className="pager">
            <span className="psize">
              {t('apPerPage')}{' '}
              <select value={pageSize} onChange={(e) => changePageSize(Number(e.target.value))}>
                {pageSizes.map((n) => <option key={n} value={n}>{n}</option>)}
              </select>
            </span>
            <button className="pg" disabled={page <= 1 || loading} onClick={() => goto(page - 1)}><ChevronLeft size={15} /></button>
            <span className="pgn">{t('auditPageOf', { p: page, n: pages })}</span>
            <button className="pg" disabled={page >= pages || loading} onClick={() => goto(page + 1)}><ChevronRight size={15} /></button>
          </div>
        </div>
      </div>

      {/* 详情抽屉:完整语句 + 这一行的全部上下文 */}
      {detail && (
        <div className="overlay">
          <div className="mask" onClick={closeDetail} />
          <aside className="drawer">
            <header className="dhead">
              <span className={clsx('verb', riskCls(detail.risk))}>{verbOf(detail.command, detail.risk)}</span>
              <span className="dtitle">{t('auditDetailTitle')}</span>
              <X size={18} className="dx" onClick={closeDetail} />
            </header>
            <div className="dbody scy">
              <div className="dsec">
                <div className="dlbl">
                  {t('colCmd')}{' '}
                  <span className="dtools">
                    {/* 只有像 SQL 的命令才给这个开关:登录、配置变更那类行排版只会更难读 */}
                    {looksLikeSQL(detail.command) && (
                      <span className="seg">
                        <button className={clsx(pretty && 'on')} onClick={() => setPretty(true)}>{t('auditSqlFormatted')}</button>
                        <button className={clsx(!pretty && 'on')} onClick={() => setPretty(false)}>{t('auditSqlRaw')}</button>
                      </span>
                    )}
                    <button className="cp2" onClick={() => copyCmd(detail, 'drawer')}>
                      {copiedId === 'drawer' ? <Check size={12} /> : <Copy size={12} />}
                      {' '}{copiedId === 'drawer' ? t('copied') : t('copy')}
                    </button>
                  </span>
                </div>
                <pre className="dcmd">{detailText}</pre>
              </div>

              <div className="dgrid">
                <div><span className="ml">{t('colTime')}</span><div className="dv mono">{fmtDate(detail.occurredAt)} {fmtClock(detail.occurredAt)}</div></div>
                <div><span className="ml">{t('colWho')}</span><div className="dv">{detail.actor}</div></div>
                <div>
                  <span className="ml">{t('colClientIp')}</span>
                  <div className={clsx('dv mono', !detail.clientIp && 'none')}>{detail.clientIp || t('auditNoIp')}</div>
                </div>
                <div><span className="ml">{t('mInst')}</span><div className="dv mono">{detail.instance || '—'}</div></div>
                <div><span className="ml">{t('colTables')}</span><div className="dv mono">{targetOf(detail) || '—'}</div></div>
                <div><span className="ml">{t('colRisk')}</span><div className="dv">{riskText(detail.risk)}</div></div>
                <div><span className="ml">{t('colEvent')}</span><div className="dv">{evMeta(detail.eventType).t}</div></div>
                <div><span className="ml">{t('colAction')}</span><div className="dv">{actMeta(detail.dataAction).t}</div></div>
                <div><span className="ml">{t('colSource')}</span><div className="dv" data-testid="audit-detail-source">{srcMeta(detail.source).t}</div></div>
                <div>
                  <span className="ml">{t('colResult')}</span>
                  <div className="dv">{resBadge(detail)}</div>
                </div>
                {detail.operator && <div><span className="ml">{t('colOperator')}</span><div className="dv">{detail.operator}</div></div>}
                {detail.approvalNo && (
                  <div>
                    <span className="ml">{t('colAp')}</span>
                    <div className={clsx('dv ap', canOpenApproval && 'link')} onClick={() => openApproval(detail.approvalNo)}>#{detail.approvalNo}</div>
                  </div>
                )}
              </div>

              {/* 这张审批单的全部操作:拦截 → 审批 → 执行,按时间先后。点一条看那一条 */}
              {trail.length > 0 && (
                <div className="dsec">
                  <div className="dlbl">{t('auditTrail', { ap: trailFor })}</div>
                  <ol className="trail">
                    {trail.map((x) => (
                      <li
                        key={x.id} className={clsx('titem', x.id === detail.id && 'on')}
                        data-testid="audit-trail-item" onClick={() => showRow(x)}
                      >
                        <span className="tdot" />
                        <span className="ttime mono">{fmtDate(x.occurredAt)} {fmtClock(x.occurredAt)}</span>
                        <span className={clsx('evbadge', evMeta(x.eventType).cls)}>{evMeta(x.eventType).t}</span>
                        {resBadge(x)}
                        <span className="twho">{x.operator || x.actor}</span>
                      </li>
                    ))}
                  </ol>
                </div>
              )}

              {/* 链式校验值:这一行"没被改过"的凭据本身 */}
              <div className="dsec">
                <div className="dlbl">{t('auditChainHash')}</div>
                <div className="hash">{detail.hash}</div>
              </div>
            </div>
          </aside>
        </div>
      )}
    </div>
  )
}
