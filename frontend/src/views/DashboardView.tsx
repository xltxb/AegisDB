// 总览 —— 登录后的落地页。
//
// 从前进来直接就是 Web 命令行:一个连着生产库、光标在等你敲字的提示符。那是这个
// 产品里唯一能改动真实数据的地方,把它当默认页,等于每次开工都先站到闸门里面,再
// 想起来自己本来是要去看审批的。落地页应该先让人知道**此刻有什么要管**,再由人
// 决定走进哪一扇门。
//
// 这一页不新增任何接口,也不新增任何权限面:每块数据都是用户本来就能自己去拉的那
// 一个接口,而那些接口各自带着菜单闸。所以卡片按 auth.menus 决定出不出现 —— 页面
// 上不会出现一个用户本来取不到的数字,漏了判断也只会得到 403 后的空卡片,而不是越权。
import { useState, type MouseEvent } from 'react'
import clsx from 'clsx'
import {
  SquareTerminal, ClipboardCheck, Database, Activity, CalendarClock,
  ScrollText, ArrowRight, Play, Rocket, RotateCw, CircleCheckBig, CircleX, CheckCheck, Undo2,
  Stethoscope, CircleAlert, TriangleAlert,
} from 'lucide-react'
import api from '@/api'
import { renderApText } from '@/lib/ruleText'
import { auditResult } from '@/lib/auditResult'
import { relTime as relTimeOf, absTime } from '@/lib/relTime'
import { awaitsExecution, humanGateOf } from '@/lib/pendingWork'
import { confirmAction } from '@/lib/confirm'
import { initialsOf } from '@/lib/initials'
import { useAuthStore } from '@/stores/auth'
import { useEnvTierStore } from '@/stores/envtier'
import { useInspectionStore } from '@/stores/inspection'
import { useUIStore } from '@/stores/ui'
import { useI18n } from '@/hooks/useI18n'
import { nav } from '@/hooks/useNav'
import { useMountEffect } from '@/hooks/effects'
import { useStateRef } from '@/hooks/useStateRef'
import type { Approval, AuditRow, Connection, ExecWindow, Release, RuleRef } from '@/types'
import './DashboardView.scoped.css'

/** 一块卡片拿回来的不是列表(接口抖了、字段缺了)就当空的,不该让整页挂掉。 */
const listOf = <T,>(v: T[] | null | undefined): T[] => (Array.isArray(v) ? v : [])

/**
 * 这条审计记的是不是一次对库的动作。
 *
 * 登录、登出这类会话事件也进同一条审计链,但它们不是"经网关执行的命令",而且比命令
 * 频繁得多。区分靠的是**实例为空**这个结构事实,不是去匹配 "login" 这个词 —— 命令
 * 文本是给人读的,会随文案改,而"没有目标实例"这件事不会。
 */
function isCommandRow(r: AuditRow) { return !!r.instance }

function go(path: string) { void nav.push(path) }
/** 行上的按钮:只做自己的事,不连带触发行的点击。 */
const stop = (fn: () => unknown) => (e: MouseEvent) => { e.stopPropagation(); void fn() }

export default function DashboardView() {
  const { t, te } = useI18n()
  // 描述类单子(执行窗口)按读者语言显示;SQL 单子没有 commandRef,原样显示 command(lib/ruleText)。
  const apCmd = (a: { command: string; commandRef?: RuleRef }) => renderApText(a.commandRef, a.command, { t, te })
  const me = useAuthStore((s) => s.me)
  const menus = useAuthStore((s) => s.menus)
  const seesAllAudit = useAuthStore((s) => s.seesAllAudit)
  // 今日巡检的异常(.scratch/inspection-report):今天的报告结论是严重 / 警告的实例,严重在前。
  // 只对能看巡检报告的角色取(useInspectionStore 自己判);没有异常时总览上什么都不加。
  const inspRows = useInspectionStore((s) => s.rows)
  const inspToday = useInspectionStore((s) => s.today)
  const inspIssues = listOf(inspRows)
    .filter((r) => r.report && r.report.reportDate === inspToday && r.report.level !== 'ok')
    .sort((a, b) => (a.report!.level === 'critical' ? 0 : 1) - (b.report!.level === 'critical' ? 0 : 1) || (a.instance ?? '').localeCompare(b.instance ?? ''))
  const inspCritical = inspIssues.filter((r) => r.report!.level === 'critical').length
  function openInspReport(connId: number, reportId: number) {
    void nav.push({ path: '/inspection', query: { instance: String(connId), report: String(reportId) } })
  }
  const envtier = useEnvTierStore()
  const { notify, notifyError, setPageSub } = useUIStore.getState()

  const can = (key: string) => !!menus[key]

  const [conns, setConns, connsRef] = useStateRef<Connection[]>([])
  const [windows, setWindows] = useState<ExecWindow[]>([])
  const [approvals, setApprovals] = useState<Approval[]>([])
  // 待执行的两种单据分开取:它们的"还没做完"是两种状态,一个是通过待执行,一个是
  // 流水线停在人工闸上。
  const [toRun, setToRun] = useState<Approval[]>([])
  const [toRelease, setToRelease] = useState<Release[]>([])
  const [audit, setAudit] = useState<AuditRow[]>([])
  const [stats, setStats] = useState<{ online: boolean; p50Ms: number; p95Ms: number; intercepts: number } | null>(null)

  // ---- 派生 ----

  /**
   * 班车(执行窗口)在这一页上分三档,顺序就是它们要人做的事:
   *
   *   开着的   —— 此刻本该审批的中/高风险语句正在被直接放行。这是这个系统里唯一一种
   *              "门开着而没人站在门口"的状态,所以它排最前、用警示色。
   *   等审批的 —— 有人申请了一扇门,还没人签字。它是**待办**,不是状态。
   *   排着的   —— 已批准、还没到点。它回答"今晚/这周会不会有一段免审批时间"。
   *
   * `active` 由后端用与判定完全相同的逻辑算出(并且已经把"没批准"算进去了),
   * 前端不自己重算跨午夜和时区 —— 那两处写两遍迟早分叉,而分叉的表现是界面说开着、
   * 网关说没开。
   */
  // 生效中的窗口是所有人的(2026-10-14 用户确认):门开在哪个库上,在那台库上干活的人都该看得到。
  // 已批准、还没到点的排期同样是所有人的(2026-10-19 用户确认):今晚 / 这周哪个库会有一段免审批
  // 时间,要提前知道。只有待审批的申请仍只列自己的(windows)。
  const [openWindows, setLiveWindows] = useState<ExecWindow[]>([])
  const pendingWindows = windows.filter((w) => w.status === 'pending')
  /** 已批准、启用中、但此刻没开、也没开完 —— "接下来会开"的那些。筛选在服务端(scope=scheduled)。 */
  const [queuedWindows, setQueuedWindows] = useState<ExecWindow[]>([])

  /** 等我决定的单子 —— canDecide 也是服务端算好的,前端不再拼一遍那三个条件。 */
  const myTodo = approvals.filter((a) => a.status === 'pending' && a.canDecide)

  /**
   * 待执行:批了但还没跑的工单,加上停在人工闸上的升级单。
   *
   * 两半不会重叠,这是后端保证的:canExecuteApproved 明确拒绝属于升级单的工单
   * (ReleaseID > 0 —— "执行由发布流水线的执行阶段完成"),所以一次变更只会出现在
   * 它真正该被点的那一边。
   */
  const pendingRun = toRun.filter((a) => a.canExecute)
  const pendingRelease = toRelease.filter(awaitsExecution)
  const runCount = pendingRun.length + pendingRelease.length

  /** 实例按分层归堆,顺序跟着分层表走,这样生产永远排在最上面。 */
  function groupByTier() {
    const groups = new Map<string, { code: string; label: string; danger: boolean; conns: Connection[] }>()
    for (const c of conns) {
      const tier = envtier.tierOf(c.env)
      const code = tier?.code ?? ''
      if (!groups.has(code)) {
        groups.set(code, {
          code,
          label: code ? envtier.tierLabel(code, t) : t('dashTierUnknown'),
          danger: !!tier?.dangerBanner || !!tier?.requireMfa,
          conns: [],
        })
      }
      groups.get(code)!.conns.push(c)
    }
    // 分层表的顺序即展示顺序;认不出分层的实例排在最后,而不是被藏起来 —— 一台落在
    // 没有规则的分层上的实例,恰恰是最该被看见的那种。
    const order = envtier.tierCodes
    return [...groups.values()].sort((a, b) => {
      const ia = order.indexOf(a.code)
      const ib = order.indexOf(b.code)
      return (ia < 0 ? 99 : ia) - (ib < 0 ? 99 : ib)
    })
  }
  const byTier = groupByTier()

  const dangerConns = byTier.filter((g) => g.danger).reduce((n, g) => n + g.conns.length, 0)

  // ---- 载入 ----
  //
  // 全部并发,而且各自失败各自算:一块卡片取不到数据不该让整页空掉。落地页最没有资格
  // 因为某个接口抖了一下就变成一张错误页 —— 人是来看"有没有事"的。
  async function load() {
    const jobs: Promise<unknown>[] = [
      useEnvTierStore.getState().load().catch(() => {}),
      api.connections().then((r) => { setConns(listOf(r)) }).catch(() => {}),
      api.execWindows().then((r) => { setWindows(listOf(r)) }).catch(() => {}),
      api.activeExecWindows().then((r) => { setLiveWindows(listOf(r)) }).catch(() => {}),
      api.scheduledExecWindows().then((r) => { setQueuedWindows(listOf(r)) }).catch(() => {}),
      api.gatewayStats().then((r) => { setStats(r) }).catch(() => {}),
    ]
    if (can('approve')) {
      // chain 而不是 all:平台管理员的 all 是全量,取前 20 条再筛 canDecide 会漏掉自己的待办
      jobs.push(api.approvals('chain', 1, 20, 'pending').then((r) => { setApprovals(listOf(r.items)) }).catch(() => {}))
      // 已通过的单独取一遍,并且是服务端按状态筛的 —— 见 api.approvals 的注释:
      // 通过了的工单不会过期,自己筛会漏。
      jobs.push(api.approvals('chain', 1, 20, 'approved').then((r) => { setToRun(listOf(r.items)) }).catch(() => {}))
    }
    if (can('pipeline')) {
      jobs.push(api.releases('all', 'waiting', 1, 20).then((r) => { setToRelease(listOf(r.items)) }).catch(() => {}))
    }
    if (can('audit')) {
      // 取一大页再自己筛:审计里混着登录这类会话事件,而它们比命令频繁得多 —— 直接取
      // 前 8 条,这张卡片就成了一列 login,把它本该回答的"网关上刚刚发生了什么"挤没了。
      jobs.push(api.audit({ risk: '', page: 1, pageSize: 40 })
        .then((r) => { setAudit(r.items.filter(isCommandRow)) })
        .catch(() => {}))
    }
    jobs.push(useInspectionStore.getState().load(true))
    await Promise.all(jobs)
    setPageSub({ key: 'dashPageSub', params: { n: connsRef.current.length } })
  }
  useMountEffect(() => { void load() })

  // ---- 展示助手 ----

  /** 一句话的时间表。与「班车」页上的 whenLabel 同一个口径,只是更短。 */
  function windowWhen(w: ExecWindow) {
    const hm = (n: number) => `${String(Math.floor(n / 60) % 24).padStart(2, '0')}:${String(n % 60).padStart(2, '0')}`
    if (w.kind === 'once') {
      // 与其余页面同一套写法:'sv' 给出 ISO 样式,不随界面语言改变数字顺序。
      const f = (s?: string) => (s ? new Date(s).toLocaleString('sv').slice(5, 16) : '—')
      return `${f(w.startsAt)} → ${f(w.endsAt)}`
    }
    const days = w.weekdays?.trim()
      ? w.weekdays.split(',').map((n) => t(`dow${Number(n)}`)).join('')
      : t('ewEveryDay')
    return `${days} ${hm(w.startMin)}-${hm(w.endMin)}`
  }

  function windowScope(w: ExecWindow) {
    const c = conns.find((x) => x.id === w.connectionId)
    return (c?.name || '#' + w.connectionId) + ' / ' + (w.databases?.length ? w.databases : [w.database]).join(', ')
  }

  /** 别人申请的窗口标出申请人;自己的标"我的"。 */
  const windowOwner = (w: ExecWindow) => (w.createdBy === me?.id ? t('dashWindowMine') : (w.createdByName || t('dashWindowOther')))

  // ---- 行上的动作 ----
  //
  // 落地页上直接执行/审批,省掉的是"进另一页、找到同一行、再点一次"。但它省不掉的是
  // **看清楚要放行的是什么**:这一行里的命令是截断显示的,所以两个动作都先弹一次确认,
  // 而确认文案里带的是完整命令。能不能点仍然只看服务端算好的 canExecute / canDecide ——
  // 前端不自己拼那几个条件,否则按钮亮不亮和点下去放不放行迟早两套说法。
  const [busy, setBusy, busyRef] = useStateRef(false)

  async function runNow(a: Approval) {
    if (!a.canExecute || busyRef.current) return
    if (!(await confirmAction(t('dashRunConfirm', { no: a.apNo, cmd: apCmd(a) })))) return
    setBusy(true)
    try {
      await api.executeApproval(a.id)
      notify(t('dashRunDone', { no: a.apNo }), 'success')
      await load()
      void useAuthStore.getState().refreshTodo() // 右上角的"待办"立刻少一条
    } catch (e) { notifyError(e, t('actionFailed')) } finally { setBusy(false) }
  }

  // 放弃这次下发。与 runNow 成对:确认文案里同样带完整命令,因为行里是截断的。
  async function cancelNow(a: Approval) {
    if (busyRef.current) return
    if (!(await confirmAction(t('apCancelConfirm', { no: a.apNo })))) return
    setBusy(true)
    try {
      await api.cancelApproval(a.id)
      notify(t('apCancelled', { no: a.apNo }), 'success')
      await load()
    } catch (e) { notifyError(e, t('actionFailed')); await load() } finally { setBusy(false) }
  }

  async function decideNow(a: Approval, approve: boolean) {
    if (busyRef.current) return
    const verb = approve ? t('apApprove') : t('apReject')
    if (!(await confirmAction(t('apConfirm', { verb, cmd: apCmd(a) })))) return
    setBusy(true)
    try {
      if (approve) await api.approve(a.id)
      else await api.reject(a.id)
      await load()
    } catch (e) { notifyError(e, t('actionFailed')); await load() } finally { setBusy(false) }
  }

  /**
   * 相对时间("10 分钟前")。完整时刻放进 title —— 相对时间好扫,但要对齐日志、
   * 对齐别人的截图时,需要的是那个绝对时刻。
   */
  // 相对/绝对时间来自 lib/relTime —— 数据源页也用它,同一个年龄在两页上显示一致。
  const relTime = (s?: string | null) => relTimeOf(t, s)

  /** 审计结论的说法与色档,与审计页同一份(lib/auditResult):同一个 pending,审批事件上是"已通过"。 */
  function resultView(r: { result: string; eventType?: string }) {
    const v = auditResult(r.result, r.eventType)
    const cls = { ok: 'ok', pass: 'pass', wait: 'wait', bad: 'bad', info: 'neutral', mute: 'neutral', warn: 'wait' }[v.tone]
    return { t: t(v.key), cls }
  }

  return (
    <div className="scy page">
      {/* 页眉:标题与欢迎语在左,主操作在右。刷新是这一页真正需要的第二个动作 ——
          它上面每一块都是"此刻的状态",而人会盯着它等状态变。 */}
      <div className="v-head">
        <div className="v-head-l">
          <div className="v-eyebrow">OVERVIEW</div>
          <div className="v-h1">{t('t_dashboard')}</div>
          <div className="v-sub">{t('dashGreeting', { name: me?.name || '' })}</div>
        </div>
        {/* 两个按钮放进公共的 .v-head-acts:原先直接排在 flex 页眉里没有间距,贴在一起。 */}
        <div className="v-head-acts">
          <button className="ghostbtn" disabled={busy} title={t('dashRefresh')} onClick={() => load()}>
            <RotateCw size={14} className={clsx(busy && 'spin')} />{t('dashRefresh')}
          </button>
          {can('terminal') && (
            <button className="cta" onClick={() => go('/terminal')}>
              <SquareTerminal size={15} />{t('dashOpenTerminal')}
            </button>
          )}
        </div>
      </div>

      {/* KPI。每张牌:大号等宽数字 + 一行说明 + 右上角一块淡底图标。
          只有**要人动手**的两张(等我审批、待我执行)在大于 0 时变琥珀并加左侧竖条:
          实例数和延迟无论多少都不是待办,把它们一起点亮等于没有重点。 */}
      <div className="stats">
        {can('approve') && (
          <div className={clsx('stat click', myTodo.length > 0 && 'act')} onClick={() => go('/approvals')}>
            <div className="si"><ClipboardCheck size={15} /></div>
            <div className="sv">{myTodo.length}</div>
            <div className="sl">{t('dashStatTodo')}</div>
          </div>
        )}
        {(can('approve') || can('pipeline')) && (
          <div
            className={clsx('stat click', runCount > 0 && 'act')}
            onClick={() => go(pendingRun.length ? '/approvals' : '/releases')}
          >
            <div className="si"><Play size={15} /></div>
            <div className="sv">{runCount}</div>
            <div className="sl">{t('dashStatToRun')}</div>
          </div>
        )}
        {can('execwindow') && (
          <div className={clsx('stat click', openWindows.length > 0 && 'warn')} onClick={() => go('/exec-windows')}>
            <div className="si"><CalendarClock size={15} /></div>
            <div className="sv">{openWindows.length}</div>
            <div className="sl">{t('dashStatWindows')}</div>
          </div>
        )}
        {/* 巡检异常:今天有严重 / 警告的实例时才出现;有严重的标红 */}
        {inspIssues.length > 0 && (
          <div className={clsx('stat click', inspCritical ? 'bad' : 'warn')} data-testid="dash-insp-stat" onClick={() => go('/inspection')}>
            <div className="si"><Stethoscope size={15} /></div>
            <div className="sv">{inspIssues.length}</div>
            <div className="sl">{t('dashStatInsp', { c: inspCritical })}</div>
          </div>
        )}
        <div className="stat">
          <div className="si"><Database size={15} /></div>
          <div className="sv">{conns.length}</div>
          <div className="sl">{t('dashStatInstances', { n: dangerConns })}</div>
        </div>
        <div className="stat">
          <div className="si"><Activity size={15} /></div>
          {/* 标的是 p50(中位数)。接口只给 p50 / p95,写成 P90 是编一个没人算过的数。 */}
          <div className="sv">{typeof stats?.p50Ms === 'number' ? stats.p50Ms.toFixed(1) + 'ms' : '—'}</div>
          <div className="sl">{stats?.online ? t('dashStatGwOn') : t('dashStatGwOff')}</div>
        </div>
      </div>

      {/* 今日巡检发现异常:列出今天严重 / 警告的实例与最严重的一条问题,点一下打开那份报告 */}
      {inspIssues.length > 0 && (
        <div className={clsx('panel ipanel', inspCritical > 0 && 'danger')} data-testid="dash-insp">
          <div className="phead">
            {inspCritical > 0 ? <CircleAlert size={17} className="picon" /> : <TriangleAlert size={17} className="picon" />}
            <div className="grow">
              <div className="pt">{t('dashInspTitle')}</div>
              <div className="ps">{t('dashInspSub', { c: inspCritical, w: inspIssues.length - inspCritical })}</div>
            </div>
            <button className="ghostbtn sm" onClick={() => go('/inspection')}>{t('dashInspAll')}<ArrowRight size={13} /></button>
          </div>
          <div className="pbody">
            {inspIssues.slice(0, 6).map((r) => (
              <div key={r.connectionId} className="ir" data-testid="dash-insp-row" onClick={() => openInspReport(r.connectionId, r.report!.id)}>
                <span className={clsx('ilv', r.report!.level)}>{t('inspLevel_' + r.report!.level)}</span>
                <span className="iname">{r.instance}</span>
                <span className="ifind" title={r.report!.topFinding}>{r.report!.topFinding || ''}</span>
                <span className="icnt">{t('inspCounts', { c: r.report!.criticalCount, w: r.report!.warningCount, i: r.report!.infoCount })}</span>
              </div>
            ))}
            {inspIssues.length > 6 && <div className="imore" onClick={() => go('/inspection')}>{t('dashInspMore', { n: inspIssues.length - 6 })}</div>}
          </div>
        </div>
      )}

      {/* 当前执行窗口。这是这一页唯一的"门开着"状态,所以它是一整块面板,不是一条提示:
          窗口开着的这几个小时里,本来要审批的中/高风险语句会一条不落地直接下发。
          呼吸点表示"此刻正开着",它是这块面板存在的全部理由。 */}
      {openWindows.length > 0 && (
        <div className="panel">
          <div className="phead">
            <span className="pulse" />
            <div className="grow">
              <div className="pt">{t('dashWindowLive')}</div>
              <div className="ps">{t('dashWindowOpen', { n: openWindows.length })}</div>
            </div>
            <button className="ghostbtn sm" onClick={() => go('/exec-windows')}>{t('dashWindowManage')}<ArrowRight size={13} /></button>
          </div>
          {/* 每扇门两列:左边是"哪个库",右边是"到什么时候关"。两件事都堆在左边时,
              人要在一行里数着分隔点找那个时间。 */}
          <div className="pbody">
            {openWindows.map((w) => (
              <div key={w.id} className="pw">
                <div className="pwl">
                  <div className="pwn">{w.name}</div>
                  <div className="pwr">{w.reason || t('ewNoReason')}</div>
                </div>
                <div className="pwm"><span className="chip">{windowScope(w)}</span></div>
                <div className="pwm"><span className="chip">{windowWhen(w)}</span></div>
                {/* 别人申请的窗口标出申请人;自己的不标 */}
                <div className="pwm"><span className="chip" data-testid="dash-window-owner">{windowOwner(w)}</span></div>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* 待执行队列。批完了、还等着有人去按下那一下的单子;排在"等我审批"前面 ——
          审批是在等别人,这些是在等你。 */}
      {runCount > 0 && (
        <div className="card">
          <div className="chead">
            <div className="cic"><Play size={17} color="var(--accent-text)" /></div>
            <div className="grow"><div className="ct">{t('dashRunTitle')}</div><div className="cs">{t('dashRunSub')}</div></div>
            <span className="cnum">{runCount}</span>
          </div>
          <div className="rows">
            {pendingRun.map((a) => (
              <div key={'ap' + a.id} className="row">
                <div className="rleft">
                  <span className="apno" title={a.apNo}>{a.apNo}</span>
                  <span className={clsx('tag', envtier.tierOf(a.env)?.dangerBanner && 'high')}>{a.env}</span>
                  <span className="tag">{a.instance}</span>
                </div>
                {/* 命令用代码片显示,单行截断;完整命令在确认框里(那才是要看清的地方)。 */}
                <code className="code" title={apCmd(a)}>{apCmd(a)}</code>
                <div className="rright">
                  <span className="who" title={absTime(a.createdAt)}>{a.initiator} · {relTime(a.createdAt)}</span>
                  {a.canExecute && (
                    <button className="btn primary" disabled={busy} onClick={stop(() => runNow(a))}>
                      <Play size={12} />{t('apExecute')}
                    </button>
                  )}
                  {/* 批是批了,但可以不跑。撤回只对**没跑过**的单子亮(canCancel 由服务端算),
                      所以这一列上它和"执行"是一对:要么下发,要么放弃这次下发。 */}
                  {a.canCancel && (
                    <button className="btn ghost" disabled={busy} onClick={stop(() => cancelNow(a))}>
                      <Undo2 size={12} />{t('apCancel')}
                    </button>
                  )}
                  <button className="btn ghost" onClick={stop(() => go('/approvals'))}>{t('dashDetail')}</button>
                </div>
              </div>
            ))}
            {pendingRelease.map((r) => (
              <div key={'rel' + r.id} className="row">
                <div className="rleft">
                  <Rocket size={12} color="var(--text-faint)" />
                  <span className="apno">{r.relNo}</span>
                  <span className={clsx('tag', envtier.tierOf(r.env)?.dangerBanner && 'high')}>{r.env}</span>
                  <span className="tag">{r.instance} / {r.database}</span>
                </div>
                {/* 停在哪一个节点上要说出来:一张升级单可能停在执行闸,也可能停在流程里
                    配置的确认点,点进去要找的东西不一样。 */}
                <code className="code plain" title={r.title}>{r.title} · {t('dashRunStage', { stage: humanGateOf(r)?.name || '' })}</code>
                <div className="rright">
                  <button className="btn ghost" onClick={stop(() => go('/releases'))}>{t('dashDetail')}<ArrowRight size={12} /></button>
                </div>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* 班车:等审批的排在前面(它要人去做一件事),已批准还没到点的排后面。
          已经开着的不在这里重复 —— 上面那块面板已经用更重的方式说过了。 */}
      {/* 待审批的只给有窗口菜单的申请人自己看;已批准的排期人人可见(没有窗口菜单的人点了不跳转) */}
      {((can('execwindow') && pendingWindows.length > 0) || queuedWindows.length > 0) && (
        <div className="card" data-testid="dash-win-schedule">
          <div className="chead">
            <div className="cic"><CalendarClock size={17} color="var(--accent-text)" /></div>
            <div className="grow"><div className="ct">{t('dashWinTitle')}</div><div className="cs">{t('dashWinSub')}</div></div>
          </div>
          <div className="rows">
            {[...(can('execwindow') ? pendingWindows : []), ...queuedWindows].map((w) => (
              <div key={'w' + w.id} className={clsx('row', can('execwindow') && 'click')} data-testid="dash-win-row" onClick={() => { if (can('execwindow')) go('/exec-windows') }}>
                <div className="rleft">
                  <span className={clsx('wtag', w.status === 'pending' ? 'pend' : 'ok')}>
                    {w.status === 'pending' ? t('ewStPending') : t('dashWinQueued')}
                  </span>
                  <span className="rname">{w.name}</span>
                  {w.apNo && w.status === 'pending' && <span className="apno">{w.apNo}</span>}
                </div>
                <code className="code plain">{windowScope(w)}</code>
                <div className="rright">
                  <span className="chip">{windowWhen(w)}</span>
                  {/* 已批准的排期是所有人的:标出申请人,自己的标"我的"(与生效面板同一套说法) */}
                  {w.status !== 'pending' && <span className="chip" data-testid="dash-win-owner">{windowOwner(w)}</span>}
                </div>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* 下半区 5:7。左边是要你决定的事(短、动作重),右边是刚发生的事(长、只读)。 */}
      <div className="cols">
        {can('approve') && (
          <div className="card">
            <div className="chead">
              <div className="cic"><ClipboardCheck size={17} color="var(--accent-text)" /></div>
              <div className="grow"><div className="ct">{t('dashTodoTitle')}</div><div className="cs">{t('dashTodoSub')}</div></div>
              {myTodo.length > 0 && <span className="cnum">{myTodo.length}</span>}
            </div>
            <div className="rows">
              {myTodo.slice(0, 5).map((a) => (
                <div key={a.id} className="trow">
                  <div className="tmeta">
                    <span className="av">{initialsOf(a.initiator)}</span>
                    <div className="grow">
                      <div className="rleft">
                        <span className="apno">{a.apNo}</span>
                        <span className={clsx('tag', envtier.tierOf(a.env)?.dangerBanner && 'high')}>{a.env}</span>
                      </div>
                      <div className="who" title={absTime(a.createdAt)}>{a.initiator} · {relTime(a.createdAt)}</div>
                    </div>
                  </div>
                  <code className="code" title={apCmd(a)}>{apCmd(a)}</code>
                  {a.reason && <div className="why">{a.reason}</div>}
                  <div className="tacts">
                    <button className="btn ghost" disabled={busy} onClick={stop(() => decideNow(a, false))}>
                      <CircleX size={12} />{t('apReject')}
                    </button>
                    <button className="btn ok" disabled={busy} onClick={stop(() => decideNow(a, true))}>
                      <CircleCheckBig size={12} />{t('apApprove')}
                    </button>
                  </div>
                </div>
              ))}
              {/* 空态说的是"都处理完了",不是"没有数据"。这两句话对读的人意义完全不同。 */}
              {!myTodo.length && (
                <div className="blank">
                  <div className="bic"><CheckCheck size={20} /></div>
                  <div className="bt">{t('dashTodoEmpty')}</div>
                  <div className="bs">{t('dashTodoEmptySub')}</div>
                </div>
              )}
            </div>
          </div>
        )}

        {can('audit') && (
          <div className="card">
            <div className="chead">
              <div className="cic"><ScrollText size={17} color="var(--accent-text)" /></div>
              <div className="grow"><div className="ct">{t('dashAuditTitle')}</div><div className="cs">{seesAllAudit ? t('dashAuditSub') : t('dashAuditSubMine')}</div></div>
              <button className="ghostbtn sm" onClick={() => go('/audit')}>{t('dashDetail')}<ArrowRight size={13} /></button>
            </div>
            {/* 时间轴:一条竖线串起来,左边是人,右边是那一刻发生的事。 */}
            <div className="tl">
              {audit.slice(0, 7).map((r) => {
                const rv = resultView(r)
                return (
                  <div key={r.id} className="tli click" onClick={() => go('/audit')}>
                    <span className="av sm">{initialsOf(r.actor)}</span>
                    <div className="grow">
                      <div className="rleft">
                        <span className="rname">{r.actor}</span>
                        <span className="chip">{r.instance}</span>
                        {r.database && <span className="chip">{r.database}</span>}
                        <span className={clsx('cap', rv.cls)}>{rv.t}</span>
                        <span className="ago" title={absTime(r.occurredAt)}>{relTime(r.occurredAt)}</span>
                      </div>
                      <code className="code" title={r.command}>{r.command}</code>
                    </div>
                  </div>
                )
              })}
              {!audit.length && (
                <div className="blank">
                  <div className="bic"><ScrollText size={20} /></div>
                  <div className="bt">{t('dashAuditEmpty')}</div>
                </div>
              )}
            </div>
          </div>
        )}
      </div>

      <div className="card">
        <div className="chead">
          <div className="cic"><Database size={17} color="var(--accent-text)" /></div>
          <div className="grow"><div className="ct">{t('dashInstTitle')}</div><div className="cs">{t('dashInstSub')}</div></div>
        </div>
        <div className="rows">
          {byTier.map((g) => (
            <div
              key={g.code}
              className={clsx('row', can('terminal') && 'click')}
              onClick={() => { if (can('terminal')) go('/terminal') }}
            >
              <div className="rleft">
                <span className={clsx('d', envtier.dotFor(g.code))} />
                <span className="rname">{g.label}</span>
                {g.danger && <span className="tag high">{t('dashTierGated')}</span>}
              </div>
              <code className="code plain">{g.conns.map((c) => c.name).join(' · ')}</code>
              <div className="rright"><span className="cnum">{g.conns.length}</span></div>
            </div>
          ))}
          {!byTier.length && <div className="blank"><div className="bt">{t('dashInstEmpty')}</div></div>}
        </div>
      </div>
    </div>
  )
}
