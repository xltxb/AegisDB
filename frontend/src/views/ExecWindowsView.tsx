// 执行窗口(「班车」)—— 申请 → 审批 → 生效。
//
// 它从「分层」页里搬出来,有了自己的菜单键。原因不是那一页太挤,而是这两件事的
// 性质变了:分层是**规则**(这个环境按什么规矩办),而窗口是一次**申请**(请让我
// 在这段时间里,对这个库,不用逐条等人批)。规则由管理员改,申请由要执行的人提 ——
// 读者和权限都不是同一批。
//
// 提交之后窗口停在待审批,判定层一行都不放行(闸门在后端 ExecWindowsFor 的 status
// 条件上,不在这一页)。通过之后它才按自己的时间表开合。
import { useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { Link } from 'react-router'
import clsx from 'clsx'
import { Clock, Plus, Pencil, Trash2, X, ShieldCheck, Hourglass, Ban, Undo2, CalendarX, ScrollText, PowerOff } from 'lucide-react'
import VButton from '@/components/common/VButton'
import VSelect from '@/components/common/VSelect'
import VTagSelect from '@/components/common/VTagSelect'
import VSwitch from '@/components/common/VSwitch'
import VDateTime from '@/components/common/VDateTime'
import api from '@/api'
import { confirmAction } from '@/lib/confirm'
import { envError } from '@/api/http'
import { useUIStore } from '@/stores/ui'
import { useAuthStore } from '@/stores/auth'
import { parseDbList } from '@/lib/dbList'
import { useI18n } from '@/hooks/useI18n'
import { useRoute } from '@/hooks/useNav'
import { useMountEffect } from '@/hooks/effects'
import { useStateRef } from '@/hooks/useStateRef'
import type { Connection, ExecWindow, WindowStatements } from '@/types'
import './ExecWindowsView.scoped.css'

// 一张窗口可以勾同一台实例上的多个库(2026-10-22),上限与后端 MaxWindowDatabases 一致
const MAX_WINDOW_DBS = 20

const labelOf = (c: Connection) => `${c.env}-${c.name}`

// 一张窗口勾的库,老数据只有 database 一个
const dbsOf = (w: ExecWindow) => (w.databases?.length ? w.databases : [w.database])
const dbsLabel = (w: ExecWindow) => dbsOf(w).join(', ')

const hmToMin = (hm: string) => {
  const [h, m] = (hm || '0:0').split(':').map((x) => Number(x) || 0)
  return h * 60 + m
}
const minToHM = (n: number) => `${String(Math.floor(n / 60) % 24).padStart(2, '0')}:${String(n % 60).padStart(2, '0')}`

// 班车的发车日。空串 = 每天(后端的约定,见 validWeekdays),1..7 对应周一到周日。
function daysOf(w: ExecWindow): boolean[] {
  const spec = (w.weekdays || '').trim()
  if (!spec) return [true, true, true, true, true, true, true]
  const set = new Set(spec.split(',').map((x) => Number(x.trim())))
  return [1, 2, 3, 4, 5, 6, 7].map((n) => set.has(n))
}

/**
 * 一行的状态。五种,顺序就是它们的优先级:
 *
 *   待审批 / 已驳回 —— 还没签字,或者签的是"不行"。这两种下面**没有**开不开的问题,
 *                     所以要先说,不能让一张待审批的窗口显示成"未到时间"。
 *   已停用          —— 签过字,但运维把它关了。
 *   进行中          —— 签过字、也启用了,而且此刻在时间表内。
 *   已到期 / 未到点  —— 都不在时间表内,但这两句话方向相反:一个是"再也不会开了",
 *                     另一个是"再等等就到了"。原先只有后者,于是一个上周就结束的
 *                     一次性窗口会永远显示成"未到点"。
 *
 * 到期与否由后端算(w.expired),和 active 同一个理由:时区与跨午夜写两遍会分叉。
 */
function stateOf(w: ExecWindow) {
  if (w.status === 'pending') return { cls: 'pending', text: 'ewStPending', icon: Hourglass }
  if (w.status === 'rejected') return { cls: 'rejected', text: 'ewStRejected', icon: Ban }
  // 撤回 ≠ 驳回:没有人看过并拒绝它,是申请人自己收回的。对判定层两者一样(都不是
  // approved),对读这一行的人不一样,所以用中性色而不是红色。
  if (w.status === 'cancelled') return { cls: 'off', text: 'ewStCancelled', icon: Undo2 }
  if (!w.enabled) return { cls: 'off', text: 'ewDisabled', icon: Ban }
  if (w.active) return { cls: 'open', text: 'ewOpen', icon: ShieldCheck }
  // 已到期用中性灰,不用红:它不是出了问题,只是这班车开完了。
  if (w.expired) return { cls: 'off', text: 'ewExpired', icon: CalendarX }
  return { cls: 'closed', text: 'ewClosed', icon: Clock }
}

const fmtTime = (s: string) => {
  const d = new Date(s)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

export default function ExecWindowsView() {
  const { t } = useI18n()
  const ui = useUIStore.getState() // 只用动作,不读状态
  const me = useAuthStore((s) => s.me)
  const seesAllAudit = useAuthStore((s) => s.seesAllAudit)
  const menus = useAuthStore((s) => s.menus)
  const isAdmin = me?.roleCodes?.includes('admin') ?? (me?.roleCode === 'admin')

  const [conns, setConns, connsRef] = useStateRef<Connection[]>([])
  const [windows, setWindows, windowsRef] = useStateRef<ExecWindow[]>([])
  const [busy, setBusy] = useState(false)
  const [winForm, setWinForm] = useState(false)

  // 星期缩写:1=周一 … 7=周日,与 ISO-8601 一致,也是 daysToIso 用的那套编号。
  const dayLabels = [1, 2, 3, 4, 5, 6, 7].map((n) => t(`dow${n}`))
  const kindOptions = [t('ewKindRecurring'), t('ewKindOnce')]
  const connOptions = conns.map(labelOf)
  const connLabel = (id: number) => {
    const c = conns.find((x) => x.id === id)
    return c ? labelOf(c) : `#${id}`
  }

  const blankWindow = () => ({
    id: 0, name: '', reason: '', databases: [] as string[], enabled: true,
    connLabel: connOptions[0] || '', kindLabel: t('ewKindRecurring'),
    startsAt: '', endsAt: '', startHM: '02:00', endHM: '04:00',
    timezone: 'Asia/Shanghai', notAfter: '', days: [true, true, true, true, true, true, true],
  })
  const [wf, setWf, wfRef] = useStateRef(blankWindow)
  const patchWf = (fields: Partial<typeof wf>) => setWf((f) => ({ ...f, ...fields }))

  // ---- 目标库下拉 ----
  //
  // 库名过去是手打的,而打错一个字的后果不是报错,是**窗口永远不开** —— 判定按
  // (实例, 库) 精确匹配,拼写不上就是查无此行,而查无此行等于没有窗口。人却以为
  // 配好了,直到那天夜里发现每条语句还在等审批。
  //
  // 所以改成从实例真实的库列表里选。探查失败时不静默留空:那时给回手工输入,并把
  // 失败原因说出来 —— 连不上目标库是一个要人去处理的事实,不是一个空下拉框。
  const [dbOptions, setDbOptions, dbOptionsRef] = useStateRef<string[]>([])
  /** 实例上真实列出来的库(dbOptions 里还可能有补进去的旧库,见 loadDbOptions)。 */
  const [dbListed, setDbListed] = useState<string[]>([])
  const dbSeq = useRef(0)
  const [dbLoading, setDbLoading] = useState(false)
  const [dbErr, setDbErr] = useState('')
  /** 探查完成过一次(用来区分"还没查"和"查完了但一个库都没有")。 */
  const [dbProbed, setDbProbed] = useState(false)
  // 从 ref 取:填表后紧接着就要用
  const selectedConn = () => connsRef.current.find((c) => labelOf(c) === wfRef.current.connLabel) || null

  // 列不出库时手填:逗号、空格或换行分隔
  const [dbText, setDbText, dbTextRef] = useStateRef('')
  function changeDbText(v: string) {
    if (v === dbTextRef.current) return
    setDbText(v)
    if (!dbOptionsRef.current.length) patchWf({ databases: parseDbList(v) })
  }

  async function loadDbOptions(keep: string[] = []) {
    // 请求编号:连着换实例(或关掉再开另一张窗口)时只认最后一次,迟到的响应不能盖掉它
    const seq = ++dbSeq.current
    const c = selectedConn()
    setDbOptions([])
    setDbListed([])
    setDbErr('')
    setDbProbed(false)
    setDbLoading(!!c)
    if (!c) return
    let listed: string[] = []
    let err = ''
    try {
      const r = await api.connectionSchema(c.id)
      err = r.error || ''
      listed = (r.databases || []).map((d) => d.name)
    } catch (e: any) {
      err = e?.message || String(e)
    }
    if (seq !== dbSeq.current) return
    setDbErr(err)
    setDbLoading(false)
    setDbProbed(true)
    setDbListed(listed)
    // 编辑一张老窗口时,它的库可能已经不在列表里(改过名、或者现在连不上)。
    // 把它补回选项里,而不是悄悄换成第一个 —— 那会把这次编辑变成一次改库。
    const opts = listed.length ? [...keep.filter((k) => !listed.includes(k)), ...listed] : []
    setDbOptions(opts)
    if (opts.length) {
      // 换了实例:上一台的库在这一台上多半不存在,只留这一台上有的;一个都不剩就先勾第一个
      const left = wfRef.current.databases.filter((d) => opts.includes(d))
      patchWf({ databases: !left.length && opts[0] ? [opts[0]] : left })
    } else {
      changeDbText(wfRef.current.databases.join(', '))
    }
  }
  // 换实例就换库列表:上一台实例的库名在这一台上多半不存在。
  // 只由人换实例触发。从前盯着 connLabel 变化,打开编辑表单填表也算"换实例",于是紧跟着又
  // 不带 keep 地加载一次,把刚补回来的旧库冲掉(issue #103)。
  function changeInstance(label: string) {
    if (label === wfRef.current.connLabel) return
    patchWf({ connLabel: label })
    void loadDbOptions()
  }

  /**
   * 目标库对不上这台实例时的警告。
   *
   * 这是这个功能最容易悄悄配错的地方,而且**错了不会报错** —— 判定按 (实例, 库)
   * 精确匹配,对不上就是查无此行,而查无此行等于没有窗口。人以为配好了,直到那天
   * 夜里发现每条语句还在等审批。
   *
   * 真实发生过一次:窗口建在 hk-orders/db_orders 上,而 db_orders 其实是 hk-billing
   * 的库;hk-orders 探查不出任何库,于是那个下拉退回成了输入框,一句提示也没有。
   *
   * 两种情况分开说,因为要人做的事不同:
   *   列不出库  —— 仿真实例或没配凭据。库名只能手填,提醒他确认这个库属于这台实例。
   *   列得出但没有这一个 —— 几乎可以肯定是选错了实例,或者库名写错了。
   */
  const dbWarn = (() => {
    if (!winForm || dbLoading || !dbProbed || dbErr) return ''
    if (!dbOptions.length) return t('ewDbNoList')
    const missing = wf.databases.filter((d) => !dbListed.includes(d))
    if (missing.length) return t('ewDbNotFound', { db: missing.join(', ') })
    return ''
  })()

  // whenLabel 只是显示。是否"进行中"由后端算(w.active),前端不重算 —— 跨午夜和时区
  // 写两遍迟早分叉,而分叉的表现是界面与网关各说各话。
  function whenLabel(w: ExecWindow): string {
    if (w.kind === 'once') {
      const f = (s?: string) => (s ? new Date(s).toLocaleString('sv').slice(0, 16) : '—')
      return `${f(w.startsAt)} → ${f(w.endsAt)}`
    }
    const weekdays = w.weekdays ?? ''
    const days = weekdays.trim()
      ? weekdays.split(',').map((n) => dayLabels[Number(n) - 1] || n).join('')
      : t('ewEveryDay')
    const cross = w.endMin <= w.startMin ? ' (+1d)' : ''
    return `${days} ${minToHM(w.startMin)}-${minToHM(w.endMin)}${cross} ${w.timezone}`
  }

  /** 只有申请人自己或管理员能改/撤(后端也同样判,这里只是不摆一个必被拒的按钮)。 */
  const canManage = (w: ExecWindow) => isAdmin || w.createdBy === (me?.id ?? -1)
  // 管理后台的全局管理页(/admin/exec-windows,工单 07):列所有人的窗口、带申请人,可停用别人的。
  // 用户后台的同一页只列自己申请的(后端按申请人过滤)。
  const route = useRoute()
  const adminMode = route.meta.portal === 'admin'
  // 停用只对还会放行的窗口有意义:已批准、开着开关、没到期
  const canDisable = (w: ExecWindow) => canManage(w) && w.enabled && !w.expired && w.status === 'approved'

  // ---- 回看:这扇门开着的时候跑了什么 ----
  //
  // 免审批的门开过之后没有人签过字,这段时间里的审计行是唯一的凭据。它此前只能到审计页
  // 去拼:记住是哪台实例的哪个库、几点到几点,再手动按时间筛;周期班车还要一周周地翻。
  // 所以这个入口开在窗口自己这一行上。
  //
  // 列表里有两种行,界面必须把它们分开:**这扇门放行的**(本来要审批,badge 标出来)和
  // 恰好在窗口期里跑的(低风险语句,开不开窗都一样)。混为一谈会让人把一扇门的责任看得
  // 比实际大。
  const [stmtFor, setStmtFor] = useState<ExecWindow | null>(null)
  const [stmts, setStmts] = useState<WindowStatements | null>(null)
  const [stmtLoading, setStmtLoading] = useState(false)
  const [stmtErr, setStmtErr] = useState('')

  async function openStatements(w: ExecWindow) {
    setStmtFor(w)
    setStmts(null)
    setStmtErr('')
    setStmtLoading(true)
    try {
      setStmts(await api.execWindowStatements(w.id))
    } catch (e: any) {
      setStmtErr(e?.message || String(e))
    } finally {
      setStmtLoading(false)
    }
  }

  const riskMeta = (r: string) =>
    r === 'high' ? { t: t('highTag'), bg: 'var(--danger-subtle)', c: 'var(--danger-text)' }
      : r === 'mid' ? { t: t('med'), bg: 'var(--warning-subtle)', c: 'var(--warning-text)' }
        : { t: t('low'), bg: 'var(--accent-subtle)', c: 'var(--accent-text)' }
  // 结果照实说:窗口期里跑失败的、跑的是仿真的,都不是"已执行"。
  const resMeta = (s: string) =>
    s === 'executed' ? { t: t('rExecuted'), c: 'var(--success-text)' }
      : s === 'simulated' ? { t: t('rSimulated'), c: 'var(--text-muted)' }
        : { t: t('rWarn'), c: 'var(--warning-text)' }

  // 「全部窗口」页签(2026-10-26 回归排查 08):有回看权限的人(后端 canReviewWindow:监督角色,或有班车菜单)
  // 在用户后台复核别人的班车放行过什么,含已过期的。只读 —— 这一页签里不摆改 / 停用 / 删,
  // 别人的窗口管理在管理后台。列哪些由后端 scope=reviewable 决定(与回看接口同一口径)。
  const canReview = seesAllAudit || !!menus.execwindow
  const [view, setViewState, viewRef] = useStateRef<'mine' | 'all'>('mine')
  const isAllView = () => !adminMode && viewRef.current === 'all'
  const allView = !adminMode && view === 'all'
  const showApplicant = adminMode || allView
  // 只读页签:动作只剩回看
  const editable = !allView
  function setView(v: 'mine' | 'all') {
    if (viewRef.current === v) return
    setViewState(v)
    setWindows([])
    void loadWindows()
  }

  async function loadWindows() {
    const want = isAllView()
    try {
      const ws = want ? await api.execWindowsReviewable() : await api.execWindows(adminMode)
      if (want === isAllView()) setWindows(ws ?? []) // 等的时候切了页签:作废
    } catch { if (want === isAllView()) setWindows([]) }
  }

  function openWindowForm(w?: ExecWindow) {
    if (w) {
      const local = (s?: string) => (s ? new Date(s).toLocaleString('sv').slice(0, 16).replace(' ', 'T') : '')
      const weekdays = w.weekdays ?? ''
      const days = weekdays.trim()
        ? dayLabels.map((_, i) => weekdays.split(',').includes(String(i + 1)))
        : [true, true, true, true, true, true, true]
      setWf({
        id: w.id, name: w.name, reason: w.reason, databases: [...dbsOf(w)], enabled: w.enabled,
        connLabel: connLabel(w.connectionId),
        kindLabel: w.kind === 'once' ? t('ewKindOnce') : t('ewKindRecurring'),
        startsAt: local(w.startsAt), endsAt: local(w.endsAt),
        startHM: minToHM(w.startMin), endHM: minToHM(w.endMin),
        timezone: w.timezone || 'Asia/Shanghai', notAfter: local(w.notAfter), days,
      })
    } else {
      setWf(blankWindow())
    }
    setWinForm(true)
    void loadDbOptions(w ? dbsOf(w) : [])
  }

  async function saveWindow() {
    const f = wfRef.current
    const conn = selectedConn()
    if (!conn) { ui.notifyError(new Error(t('ewPickInstance')), t('actionFailed')); return }
    const once = f.kindLabel === kindOptions[1]
    const iso = (v: string) => (v ? new Date(v).toISOString() : undefined)
    const body: Record<string, unknown> = {
      name: f.name.trim(), enabled: f.enabled, connectionId: conn.id,
      // databases 是全部勾选的库;database 是第一个,给只认单库的老后端
      databases: f.databases, database: f.databases[0] || '', reason: f.reason.trim(),
      kind: once ? 'once' : 'recurring',
    }
    if (once) {
      body.startsAt = iso(f.startsAt)
      body.endsAt = iso(f.endsAt)
    } else {
      body.timezone = f.timezone.trim()
      body.startMin = hmToMin(f.startHM)
      body.endMin = hmToMin(f.endHM)
      // 全选等于"每天",送空串 —— 与后端 weekdayAllowed 的约定一致。
      body.weekdays = f.days.every(Boolean) ? '' : f.days.map((on, i) => (on ? i + 1 : 0)).filter(Boolean).join(',')
      body.notAfter = iso(f.notAfter)
    }
    setBusy(true)
    try {
      const env = f.id ? await api.updateExecWindow(f.id, body) : await api.createExecWindow(body)
      if (env.code !== 0) { ui.notifyError(envError(env), t('actionFailed')); return }
      setWinForm(false)
      await loadWindows()
      // 说清"提交了,还没生效" —— 一句"已保存"会让人以为今晚可以直接跑了。
      ui.notify(t(f.id ? 'ewResubmitted' : 'ewSubmitted', { ap: env.data?.apNo || '' }), 'success', 5000)
    } catch (e) { ui.notifyError(e, t('actionFailed')) } finally { setBusy(false) }
  }

  async function disableWindow(w: ExecWindow) {
    if (!(await confirmAction(t('ewDisableConfirm', { name: w.name })))) return
    setBusy(true)
    try {
      const env = await api.disableExecWindow(w.id)
      if (env.code !== 0) { ui.notifyError(envError(env), t('actionFailed')); return }
      await loadWindows()
    } catch (e) { ui.notifyError(e, t('actionFailed')) } finally { setBusy(false) }
  }

  async function removeWindow(w: ExecWindow) {
    if (!(await confirmAction(t('ewDelConfirm', { name: w.name })))) return
    setBusy(true)
    try {
      const env = await api.deleteExecWindow(w.id)
      if (env.code !== 0) { ui.notifyError(envError(env), t('actionFailed')); return }
      await loadWindows()
    } catch (e) { ui.notifyError(e, t('actionFailed')) } finally { setBusy(false) }
  }

  useMountEffect(() => {
    void (async () => {
      try { setConns((await api.connections()) ?? []) } catch { /* 实例下拉降级为空 */ }
      await loadWindows()
      ui.setPageSub({ key: 'ewPageSub', params: { n: windowsRef.current.length } })
    })()
  })

  const items = stmts?.items ?? []

  return (
    <div className="scy page">
      <div className="v-head">
        <div className="v-head-l">
          <div className="v-eyebrow">EXECUTION WINDOWS</div>
          <div className="v-sub">{t('ewSub')}</div>
        </div>
        {!adminMode && <VButton variant="primary" onClick={() => openWindowForm()}><Plus size={15} />{t('ewApply')}</VButton>}
      </div>

      {/* 这一页最容易被误解的一句话:提交不等于生效。放在表格上面,不放在提交后的
          toast 里 —— toast 会消失,而这条约束一直成立。 */}
      <div className="banner">
        <ShieldCheck size={16} />
        <span>{t('ewApprovalNote')}</span>
      </div>

      {!adminMode && canReview && (
        <div className="segtabs" data-testid="ew-tabs">
          <button className={clsx('segtab', view === 'mine' && 'on')} data-testid="ew-tab-mine" onClick={() => setView('mine')}>{t('ewTabMine')}</button>
          <button className={clsx('segtab', view === 'all' && 'on')} data-testid="ew-tab-all" onClick={() => setView('all')}>{t('ewTabAll')}</button>
        </div>
      )}
      {allView && <div className="allnote" data-testid="ew-all-note">{t('ewAllNote')}</div>}

      <div className="card">
        <div className="scx">
          <div className="wgrid">
            <div className="th">
              <span>{t('ewColName')}</span><span>{t('ewColScope')}</span>
              <span>{t('ewColWhen')}</span><span>{t('ewColDays')}</span>
              <span>{t('ewColState')}</span><span className="ctr">{t('apActions')}</span>
            </div>
            {!windows.length && <div className="tr empty">{allView ? t('ewAllEmpty') : t('ewEmpty')}</div>}
            {windows.map((w) => {
              const st = stateOf(w)
              return (
                <div key={w.id} className="tr">
                  <div>
                    <div className="cn">{w.name}</div>
                    <div className="cl2">{w.reason || t('ewNoReason')}</div>
                    {showApplicant && <div className="cl2" data-testid="ew-applicant">{t('ewApplicant', { name: w.createdByName || '#' + w.createdBy })}</div>}
                    {/* 单号是这扇门"凭什么开着"的凭据,列在名字下面,点得进审批页去看链路 */}
                    {w.apNo && <Link className="apno" to={`/approvals?q=${w.apNo}`}>{w.apNo}</Link>}
                  </div>
                  <div className="mono mute" data-testid="ew-scope">{connLabel(w.connectionId)} / {dbsLabel(w)}</div>
                  <div className="mono mute">{whenLabel(w)}</div>
                  {/* 发车日用圆点徽章:七个字比一串 "1,3,5" 好认。一次性窗口没有"每周
                      哪几天"这回事,留空。 */}
                  <div className="daycell">
                    {w.kind === 'recurring'
                      ? daysOf(w).map((on, i) => <span key={i} className={clsx('daydot', on && 'on')}>{dayLabels[i]}</span>)
                      : <span className="dash">—</span>}
                  </div>
                  <div>
                    <span className={clsx('wst', st.cls)}>
                      <st.icon size={12} />{t(st.text)}
                    </span>
                  </div>
                  <div className="acts">
                    {/* 回看对所有人开:能看见这扇门,就该能看见它放行过什么 —— 连同是
                        谁跑的、原文是什么。后端不按人收窄。 */}
                    <button className="iconbtn" title={t('ewStmtOpen')} data-testid="ew-review" onClick={() => openStatements(w)}><ScrollText size={13} /></button>
                    {editable && (
                      <>
                        {canManage(w) && <button className="iconbtn" disabled={busy} title={t('ewEdit')} data-testid="ew-edit" onClick={() => openWindowForm(w)}><Pencil size={13} /></button>}
                        {canDisable(w) && <button className="iconbtn danger" disabled={busy} title={t('ewDisable')} data-testid="ew-disable" onClick={() => disableWindow(w)}><PowerOff size={13} /></button>}
                        {canManage(w) && <button className="iconbtn danger" disabled={busy} title={t('etDelete')} data-testid="ew-delete" onClick={() => removeWindow(w)}><Trash2 size={13} /></button>}
                      </>
                    )}
                  </div>
                </div>
              )
            })}
          </div>
        </div>
      </div>

      {/* 居中弹窗,不再是右侧抽屉。抽屉宽 560px、贴着屏幕右缘,而这张表单里有日期
          选择器 —— 面板从贴边的字段上弹出来,一半会落到窗口外面去。 */}
      {winForm && createPortal(
        <div className="ovl" onClick={(e) => { if (e.target === e.currentTarget) setWinForm(false) }}>
          <div className="modal">
            <div className="dhead">
              <div className="dic"><Clock size={16} color="var(--accent-text)" /></div>
              <div className="grow">
                <div className="dt">{wf.id ? t('ewEdit') : t('ewApply')}</div>
                <div className="ds">{wf.id ? t('ewEditNote') : t('ewApprovalNote')}</div>
              </div>
              <span className="x" onClick={() => setWinForm(false)}><X size={17} /></span>
            </div>

            <div className="dbody scy">
              <div className="frow">
                <div><div className="fl">{t('ewColName')}</div><input value={wf.name ?? ''} onChange={(e) => patchWf({ name: e.target.value })} placeholder={t('ewNamePh')} /></div>
                <div><div className="fl">{t('ewFReason')}</div><input value={wf.reason ?? ''} onChange={(e) => patchWf({ reason: e.target.value })} placeholder={t('ewReasonPh')} /></div>
              </div>
              <div className="frow">
                <div><div className="fl">{t('ewFInstance')}</div><VSelect value={wf.connLabel} onChange={changeInstance} options={connOptions} searchable data-testid="ew-instance" /></div>
                <div>
                  <div className="fl">{t('ewFDatabase')}</div>
                  {/* 库名从实例真实的库列表里选:打错一个字的后果不是报错,是窗口
                      永远不开(判定按 (实例, 库) 精确匹配)。 */}
                  {/* 同一台实例上可以勾多个库,每个勾到的库在窗口期内都免审批 */}
                  {dbOptions.length
                    ? (
                      <VTagSelect
                        value={wf.databases} onChange={(v) => patchWf({ databases: v })} options={dbOptions} searchable max={MAX_WINDOW_DBS}
                        placeholder={t('ewDbPickPh')} data-testid="ew-database"
                      />
                    )
                    : <input value={dbText} onChange={(e) => changeDbText(e.target.value)} placeholder={dbLoading ? t('schemaLoading') : t('ewDbPh')} data-testid="ew-database-text" />}
                  {/* 对不上不会报错,只会让窗口永远不开 —— 所以要在申请时就说出来。 */}
                  {dbErr ? <div className="dberr">{dbErr}</div>
                    : dbWarn ? <div className="dbwarn">{dbWarn}</div> : null}
                </div>
              </div>
              <div className="frow">
                <div><div className="fl">{t('ewFKind')}</div><VSelect value={wf.kindLabel} onChange={(v) => patchWf({ kindLabel: v })} options={kindOptions} /></div>
                <div><div className="fl">{t('ewFEnabled')}</div><label className="chk"><VSwitch value={wf.enabled} onChange={(v) => patchWf({ enabled: v })} />{t('ewEnabledHint')}</label></div>
              </div>

              {wf.kindLabel === kindOptions[1] ? (
                // 一次性
                <>
                  <div className="frow">
                    <div><div className="fl">{t('ewFFrom')}</div><VDateTime value={wf.startsAt} onChange={(v) => patchWf({ startsAt: v })} /></div>
                    <div><div className="fl">{t('ewFTo')}</div><VDateTime value={wf.endsAt} onChange={(v) => patchWf({ endsAt: v })} /></div>
                  </div>
                  <div className="note">{t('ewOnceNote')}</div>
                </>
              ) : (
                // 周期班车
                <>
                  <div className="frow">
                    <div><div className="fl">{t('ewFStart')}</div><input value={wf.startHM} onChange={(e) => patchWf({ startHM: e.target.value })} type="time" /></div>
                    <div><div className="fl">{t('ewFEnd')}</div><input value={wf.endHM} onChange={(e) => patchWf({ endHM: e.target.value })} type="time" /></div>
                  </div>
                  <div className="frow">
                    <div><div className="fl">{t('ewFTz')}</div><input value={wf.timezone} onChange={(e) => patchWf({ timezone: e.target.value })} placeholder="Asia/Shanghai" /></div>
                    <div><div className="fl">{t('ewFNotAfter')}</div><VDateTime value={wf.notAfter} onChange={(v) => patchWf({ notAfter: v })} /></div>
                  </div>
                  <div className="fl">{t('ewFDays')}</div>
                  <div className="days">
                    {dayLabels.map((d, i) => (
                      <label key={i} className={clsx('day', wf.days[i] && 'on')}>
                        <input
                          checked={wf.days[i]} type="checkbox"
                          onChange={(e) => { const on = e.target.checked; setWf((f) => ({ ...f, days: f.days.map((x, k) => (k === i ? on : x)) })) }}
                        />{d}
                      </label>
                    ))}
                  </div>
                  <div className="note">{t('ewRecurNote')}</div>
                </>
              )}
            </div>

            <div className="dfoot">
              <VButton variant="secondary" height="36px" onClick={() => setWinForm(false)}>{t('btnCancel')}</VButton>
              <VButton variant="primary" height="36px" disabled={busy || !wf.name || !wf.databases.length} onClick={saveWindow}>
                {t('ewSubmit')}
              </VButton>
            </div>
          </div>
        </div>, document.body)}

      {/* 回看:窗口期里下发过的语句 */}
      {stmtFor && createPortal(
        <div className="ovl" onClick={(e) => { if (e.target === e.currentTarget) setStmtFor(null) }}>
          <div className="modal stmtmodal">
            <div className="dhead">
              <div className="dic"><ScrollText size={16} color="var(--accent-text)" /></div>
              <div className="grow">
                <div className="dt">{t('ewStmtTitle')} · {stmtFor.name}</div>
                <div className="ds mono">{connLabel(stmtFor.connectionId)} / {dbsLabel(stmtFor)} · {whenLabel(stmtFor)}</div>
              </div>
              <span className="x" onClick={() => setStmtFor(null)}><X size={17} /></span>
            </div>

            <div className="dbody scy">
              {stmtLoading ? <div className="stmtinfo">{t('ewStmtLoading')}</div>
                : stmtErr ? <div className="dberr">{stmtErr}</div>
                  : stmts ? (
                    <>
                      <div className="stmtsum">
                        {t('ewStmtSummary', { n: items.length, m: stmts.byWindow })}
                      </div>
                      {/* 被截断的列表看起来和"就这么多"一模一样,所以必须说出来。 */}
                      {stmts.truncated && <div className="dbwarn">{t('ewStmtTruncated')}</div>}
                      {!items.length && <div className="stmtinfo">{t('ewStmtEmpty')}</div>}
                      {items.map((it) => (
                        <div key={it.auditId} className="stmt">
                          <div className="smeta">
                            <span className="mono mute">{fmtTime(it.occurredAt)}</span>
                            <span className="who">{it.actor}</span>
                            <span className="rbadge" style={{ background: riskMeta(it.risk).bg, color: riskMeta(it.risk).c }}>{riskMeta(it.risk).t}</span>
                            <span style={{ color: resMeta(it.result).c }}>{resMeta(it.result).t}</span>
                            {/* 这一条是这扇门放行的(本来要审批)。没有这枚徽章的是恰好在窗口期
                                里跑的普通语句,开不开窗都一样。 */}
                            {it.byWindow && <span className="bywin"><ShieldCheck size={11} />{t('ewStmtByWindow')}</span>}
                            {it.approvalNo && <Link className="apno" to={`/approvals?q=${it.approvalNo}`}>{it.approvalNo}</Link>}
                          </div>
                          <pre className="sql">{it.command}</pre>
                        </div>
                      ))}
                      {!!items.length && <div className="note">{t('ewStmtNote')}</div>}
                    </>
                  ) : null}
            </div>

            <div className="dfoot">
              <VButton variant="secondary" height="36px" onClick={() => setStmtFor(null)}>{t('scClose')}</VButton>
            </div>
          </div>
        </div>, document.body)}
    </div>
  )
}
