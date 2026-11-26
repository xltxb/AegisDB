import { useLayoutEffect, useRef, useState } from 'react'
import clsx from 'clsx'
import { renderApText, renderRule } from '@/lib/ruleText'
import OscJobPanel from '@/components/approvals/OscJobPanel'
import { Inbox, GitPullRequestArrow, CircleCheckBig, CircleX, X, Play, Search, RotateCw, ChevronLeft, ChevronRight, Undo2, Loader } from 'lucide-react'
import VSelect from '@/components/common/VSelect'
import VButton from '@/components/common/VButton'
import api from '@/api'
import { localizeText } from '@/api/http'
import { useMountEffect, useUpdateEffect } from '@/hooks/effects'
import { useEvent } from '@/hooks/useEvent'
import { useI18n } from '@/hooks/useI18n'
import { nav, useRoute } from '@/hooks/useNav'
import { useStateRef } from '@/hooks/useStateRef'
import { confirmAction } from '@/lib/confirm'
import { initialsOf } from '@/lib/initials'
import { extractTables, tablesLabel } from '@/lib/sqlTables'
import { useUIStore } from '@/stores/ui'
import { useAuthStore } from '@/stores/auth'
import type { Approval, RuleRef } from '@/types'
import './ApprovalsView.scoped.css'

const pageSizes = [20, 50, 100]

// 谁能决定这张单,由服务端说了算(service.DecideBlockFor)。这里**不再自己判
// 一遍**:此前前端只看审批链成员,而服务端还有"不能审自己发起的"这一条 ——
// 两份判断不一致的结果,就是一个亮着却点不动的按钮。
function canDecide(a: Approval) {
  return a.canDecide === true
}

// 行内状态比 a.status 多分了一档:approved 里"还没跑"和"跑过了"是两回事,而
// 前者往往正等着看这一列的人去处理。挤在一个"已通过"里,他就看不见了。
function rowState(a: Approval) {
  // 升级单和**窗口申请单**都不走手动执行:前者归流水线的执行阶段,后者批准即生效
  // (它的 Command 是一句描述,不是可执行语句)。把它们显示成"待执行",人会一直在
  // 等一个永远不该按的按钮。数据查询权限申请单(queryGrantReqId)同理:批准即生成授权。
  if (a.status === 'approved' && !a.executedAt && !a.releaseId && !a.windowId && !a.queryGrantReqId) return 'waitrun'
  // 最终状态以**库那边收没收下**为准。批准是人的决定,而这一列说的是那次下发的
  // 结果 —— 一条跑挂的 DROP 显示成"已通过",读的人会以为变更已经生效了。
  //
  // 这里不动 a.status:跑挂了不该把一张已批准的工单变回没批准,能不能执行、按状态
  // 筛选走的都是它。行内状态一直是这样合成出来的(waitrun 也是),多这一档而已。
  if (a.status === 'approved' && a.execStatus === 'failed') return 'failed'
  // 点下执行、结论还没落库:在库上跑着。不能显示成"已执行" —— 那等于替它说了一句跑完了。
  if (a.status === 'approved' && a.execStatus === 'running') return 'running'
  return a.status
}

function rowLabel(a: Approval) {
  switch (rowState(a)) {
    case 'pending': return 'apPending'
    case 'waitrun': return 'apStWaitRun'
    case 'failed': return 'apStFailed'
    case 'running': return 'apStRunning'
    // 历史工单没有 execStatus(这一列是后加的),它们的成败无从得知 —— 照旧只说
    // "已执行",不替它们编一个结果。
    case 'approved': return a.executedAt ? (a.execStatus === 'success' ? 'apResultOk' : 'apStRan') : 'apStDone'
    // 已失效 ≠ 已驳回。前者是超时没人管、被清扫自动作废的(见 sweepStaleApprovals),
    // 后者是有人看过并且说了不行。原先它们共用 default 分支,于是一张过期单在列表上
    // 显示成"已驳回" —— 而按状态筛"已驳回"又筛不到它,同一行的两处说法自相矛盾。
    case 'expired': return 'apStExpired'
    // 已撤回 ≠ 已驳回:前者是发起人自己收回的,没有人对它做过判断。共用一个标签
    // 的话,记录上就看不出到底有没有人拒绝过什么。
    case 'cancelled': return 'apStCancelled'
    // 用短标签,不是 apRejected 那一句。"已驳回 · 命令未执行 · 已通知发起人" 是一句
    // 话,塞进状态列会折成三四行,把那一行撑得比别人高一倍 —— 英文下尤其明显。
    // 那句话留在详情卡底部,那里有一整行宽度。
    default: return 'apStRejected'
  }
}

// 结果那一栏的抬头。历史工单没有 execStatus(这一列是后加的),它们的成败无从得知 ——
// 只说"已执行",不替它们编一个结果。
function execHeadKey(a: Approval) {
  if (a.execStatus === 'running') return 'apResultRunning'
  if (a.execStatus === 'failed') return 'apResultFailed'
  if (a.execStatus === 'success') return 'apResultOk'
  return 'apResultUnknown'
}

const shortTime = (s: string) =>
  new Date(s).toLocaleString('zh', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' })
// 列表里的两列时间共用一种写法(MM-DD hh:mm),否则同一行上两个时间戳格式不同,读的人
// 得先分辨格式才能比先后。
const listTime = (s: string) => new Date(s).toLocaleString('sv').slice(5, 16)

// The row answers "which data does this touch"; the command itself is in the
// detail card. A command can be a whole script, so putting it in the row means
// truncating it — and the truncated part is often the part that matters.
function tablesOf(a: Approval) { return tablesLabel(extractTables(a.command)) }

function riskMeta(a: Approval) {
  return a.riskLevel === 'high'
    ? { t: 'highP1', bg: 'var(--danger-subtle)', c: 'var(--danger-text)' }
    : { t: 'privP2', bg: 'var(--warning-subtle)', c: 'var(--warning-text)' }
}
// approval chain from real step data: initiator → approvers
function chainText(a: Approval) {
  const names = (a.steps || []).map((s) => s.approver).filter(Boolean)
  return [a.initiator, ...names].join(' → ')
}
// 在聊天卡片上决定的:写出渠道。只看这两种结局 —— 通过之后又撤回的单也留着当初的渠道,但撤回不是在聊天里做的。
const CHAT_NAMES: Record<string, string> = { lark: 'Lark', telegram: 'Telegram' }
const chatVia = (a: Approval) =>
  a.status === 'approved' || a.status === 'rejected' ? CHAT_NAMES[a.decidedVia ?? ''] ?? '' : ''
const envText = (a: Approval) => (a.env || '').toUpperCase()

export default function ApprovalsView() {
  const route = useRoute()
  const { t, te } = useI18n()
  // 描述类单子(执行窗口)按读者语言显示;SQL 单子没有 commandRef,原样显示 command(lib/ruleText)。
  const apCmd = (a: { command: string; commandRef?: RuleRef }) => renderApText(a.commandRef, a.command, { t, te })

  // The row shows a one-line summary; the full command, reason, chain and result
  // live in a detail card. A command can be a whole migration script, so putting
  // it in the row would either truncate it or wreck the list.
  const [detail, setDetail, detailRef] = useStateRef<Approval | null>(null)
  function openDetail(a: Approval) { setDetail(a) }

  // 脚本工单的全文。工单的 command 只是摘要(前 40 行 + 语句清单):一条建表语句就几十行,
  // 摘要装不下后面的 DDL,而审批人要为每一条签字。按需拉取,换一张单就作废 —— 包括还没回来的
  // 请求:旧响应不能盖在新单上,那会让审批人照着别人的 SQL 批(issue #101)。
  const [fullScript, setFullScript] = useState<string | null>(null)
  const [scriptErr, setScriptErr] = useState('')
  const [scriptLoading, setScriptLoading] = useState(false)
  const [scriptOf, setScriptOf] = useState<number | undefined>()
  if (scriptOf !== detail?.id) { setScriptOf(detail?.id); setFullScript(null); setScriptErr(''); setScriptLoading(false) }
  const scriptSeq = useRef(0)
  useLayoutEffect(() => { scriptSeq.current++ }, [detail?.id])
  async function toggleFullScript() {
    if (fullScript !== null) { setFullScript(null); return }
    const id = detailRef.current?.id
    if (!id) return
    const seq = ++scriptSeq.current
    // 序号在换单提交后才变,detailRef 在换单的同一刻就变,两个都要对得上
    const stale = () => seq !== scriptSeq.current || detailRef.current?.id !== id
    setScriptLoading(true)
    setScriptErr('')
    try {
      const { content } = await api.approvalScript(id)
      if (!stale()) setFullScript(content)
    } catch (e: any) {
      if (!stale()) setScriptErr(e?.message || '')
    } finally {
      if (!stale()) setScriptLoading(false)
    }
  }
  function closeDetail() {
    setDetail(null)
    // Drop the deep-link parameter so a later reload doesn't reopen the card.
    if (route.query.ap) void nav.replace({ name: 'approvals', query: {} })
  }
  const [scope, setScopeState, scopeRef] = useStateRef<'mine' | 'all'>('all')
  const [list, setList, listRef] = useStateRef<Approval[]>([])
  const [total, setTotal] = useState(0)
  const [page, setPage, pageRef] = useStateRef(1)
  const [pageSize, setPageSizeState, pageSizeRef] = useStateRef(20)
  const pages = Math.max(1, Math.ceil(total / pageSize))

  // 搜索与状态筛选都在**服务端**做(见 api.approvals 的注释)。列表是分页的,只筛
  // 当前页的搜索框会对一张躺在第三页的工单回答"没有" —— 而人会据此认为它不存在。
  const [q, setQ] = useState('')
  // 真正拿去搜的词:输入法组合途中的半截拼音只显示在框里,不拿去搜(与 v-model 一致)。
  const qRef = useRef('')
  const setQuery = (v: string) => { qRef.current = v; setQ(v) }
  const [statusFilter, setStatusFilter, statusFilterRef] = useStateRef('')
  // 状态筛选用的是服务端**真实存在**的四个状态。界面上还有一个"待执行",但那是由
  // status + executedAt 合成出来的显示态,库里没有这一列 —— 拿它当筛选条件,分页
  // 的总数就会和筛出来的行对不上。
  const statusOpts = [
    { v: '', label: t('apFilterAll') },
    { v: 'pending', label: t('apPending') },
    { v: 'approved', label: t('apStDone') },
    { v: 'rejected', label: t('apStRejected') },
    { v: 'expired', label: t('apStExpired') },
    // cancelled 是这一版新增的真实状态(发起人撤回)。不列进来的话,撤回过的单子在
    // "全部"里看得见、却没有任何一个筛选能单独找出来 —— 而"我上周撤了哪几张"正是
    // 事后最常问的一句。
    { v: 'cancelled', label: t('apStCancelled') },
  ]
  const statusLabel = statusOpts.find((o) => o.v === statusFilter)?.label || ''
  function pickStatus(l: string) {
    const hit = statusOpts.find((o) => o.label === l)
    if (hit) { setStatusFilter(hit.v); setPage(1); void load() }
  }
  const statusLabels = statusOpts.map((o) => o.label)

  // 工单**类型**。四类混在一张列表里,而它们要人做的事完全不同:命令单批完要发起人自己
  // 去执行,升级单归流水线,窗口单批准即生效,导出单由后台任务跑。更要紧的是后三类的
  // "命令"其实是一句描述,连表名都解析不出来 —— 不说类型,那一行看起来只是一张内容古怪
  // 的命令单。
  //
  // 和状态一样在**服务端**筛:列表是分页的,只筛当前页会对一张躺在第三页的工单回答"没有"。
  const [typeFilter, setTypeFilter, typeFilterRef] = useStateRef('')
  const typeOpts = [
    { v: '', label: t('apTypeAll') },
    { v: 'cmd', label: t('apType_cmd') },
    { v: 'release', label: t('apType_release') },
    { v: 'window', label: t('apType_window') },
    { v: 'export', label: t('apType_export') },
    { v: 'grant', label: t('apType_grant') },
  ]
  const typeLabel = typeOpts.find((o) => o.v === typeFilter)?.label || ''
  function pickType(l: string) {
    const hit = typeOpts.find((o) => o.label === l)
    if (hit) { setTypeFilter(hit.v); setPage(1); void load() }
  }
  const typeLabels = typeOpts.map((o) => o.label)

  // 输入时不要每敲一个字就打一次请求 —— 但也不能等失焦,那样人会以为搜索没反应。
  const searchTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  function onSearch() {
    if (searchTimer.current) clearTimeout(searchTimer.current)
    searchTimer.current = setTimeout(() => { setPage(1); void load() }, 300)
  }
  function clearSearch() { setQuery(''); setPage(1); void load() }

  const [refreshing, setRefreshing] = useState(false)
  async function refresh() {
    setRefreshing(true)
    try { await load() } finally { setRefreshing(false) }
  }
  function setPageSize(n: number) { setPageSizeState(n); setPage(1); void load() }

  async function load() {
    // M14: 加载失败以 toast 呈现，避免静默失败
    try {
      const res = await api.approvals(scopeRef.current, pageRef.current, pageSizeRef.current, statusFilterRef.current, qRef.current.trim(), typeFilterRef.current)
      setList(res.items ?? [])
      setTotal(res.total)
      // Server-side count: the badge must not be limited to the current page.
      useAuthStore.getState().setPendingCount(res.pending)
      // 刚批完 / 跑完的那一条要立刻从右上角的"待办"里消失,不等布局的下一轮轮询。
      void useAuthStore.getState().refreshTodo()
      const d = detailRef.current
      if (d) setDetail(listRef.current.find((x) => x.id === d.id) || d)
      settleWatched()
    } catch (e) {
      useUIStore.getState().notifyError(e, t('actionFailed'))
    }
    schedulePoll()
  }

  // ---- 执行中 ----
  //
  // 执行状态以工单上落库的为准(running → success / failed),不以"请求回来了没有"为准:一条
  // 跑得久的语句,请求可能先断(代理超时、页面刷新),命令却还在库上跑。所以页上有执行中的单
  // 就隔几秒刷新一次,直到它有结论;自己点的那几张,出结论时再弹一次结果。
  const [watchedExec] = useState(() => new Set<number>())
  const pollTimer = useRef<ReturnType<typeof setTimeout>>(undefined)
  function schedulePoll() {
    clearTimeout(pollTimer.current)
    if (listRef.current.some((a) => a.execStatus === 'running') || watchedExec.size) pollTimer.current = setTimeout(load, 3000)
  }
  function settleWatched() {
    for (const id of [...watchedExec]) {
      const a = listRef.current.find((x) => x.id === id)
      if (!a || a.execStatus === 'running' || !a.executedAt) continue
      watchedExec.delete(id)
      if (a.execStatus === 'failed') useUIStore.getState().notify(t('apExecuteFailed'), 'error')
      else useUIStore.getState().notify(t('apExecuteDone'), 'success')
    }
  }
  useMountEffect(() => {
    void load().then(() => openFromQuery())
    return () => clearTimeout(pollTimer.current)
  })
  // 已经在这一页上时,从右上角的"待办"面板点另一张单 —— 变的只有 ?ap=,组件不会重新
  // 挂载,得盯着它。
  useUpdateEffect(() => { if (route.query.ap) void openFromQuery() }, [route.query.ap])

  // The audit log links a ticket by number (?ap=AP-1234). Such a ticket may not be
  // in the current tab — "mine" only lists what awaits this user — so widen to all
  // before giving up, and say so plainly if it still cannot be found rather than
  // leaving the click looking broken.
  const openFromQuery = useEvent(async () => {
    const ap = String(route.query.ap || '')
    if (!ap) return
    // Ask the server for this one ticket. Searching the loaded page would only
    // work while it happened to be on it — with paging that is mostly false, and
    // the link would appear broken exactly as it did before paging existed.
    try {
      const hit = await api.approvalByNo(ap)
      if (hit) openDetail(hit)
      else useUIStore.getState().notify(t('apNotFound', { ap }), 'info')
    } catch {
      useUIStore.getState().notify(t('apNotFound', { ap }), 'info')
    }
  })

  async function setScope(s: 'mine' | 'all') {
    setScopeState(s)
    setPage(1) // a scope change invalidates the current position
    await load()
  }

  async function goto(p: number) {
    if (p < 1 || p > pages || p === pageRef.current) return
    setPage(p)
    await load()
  }

  async function decide(a: Approval, approve: boolean) {
    if (!canDecide(a)) return
    // B3: 两个方向都不可逆(通过会解锁一次真实执行,驳回会关掉这张单)——二次确认防误点
    const verb = approve ? t('apApprove') : t('apReject')
    if (!(await confirmAction(t('apConfirm', { verb, cmd: apCmd(a) })))) return
    // M14: 审批/驳回失败以 toast 呈现。api 层经 ok() 把业务级拒绝抛成 Error(msg),
    // 所以服务端说的理由会原样出现在 toast 上 —— 此前这里只 catch 抛出的异常,而
    // 拒绝是正常返回的信封,于是点了没反应,也没人说得出为什么。
    try {
      if (approve) await api.approve(a.id)
      else await api.reject(a.id)
      await load()
      // Reflect the new state in the open card instead of leaving a stale one.
      if (detailRef.current) setDetail(listRef.current.find((x) => x.id === a.id) || null)
    } catch (e) {
      useUIStore.getState().notifyError(e, t('actionFailed'))
      // 被拒的常见原因之一是"已被别人处理过",刷一次比让人对着过期卡片发呆好。
      await load()
    }
  }

  // 执行是**发起人**的动作,不是审批的副作用。
  //
  // 审批人按下的是"我同意",不是"现在就跑" —— 业务低峰、应用是否已停、备份是否
  // 就绪,只有发起人知道。所以这里是一个独立的按钮,而且它只对该去按的人亮:
  // canExecute 由服务端算(service.CanExecuteApproved),前端照着它走,免得出现
  // 一个亮着却点不动的按钮。
  const [running, setRunning, runningRef] = useStateRef(false)
  /**
   * 撤回自己发起的待审批工单。
   *
   * 和 decide() 分开写,因为它不是一次审批决定 —— 界面上也不该让它长得像。确认文案
   * 里写清"撤回后这条命令不会被执行",因为发起人此刻要判断的正是这件事。
   */
  async function cancelAp(a: Approval) {
    if (!(await confirmAction(t('apCancelConfirm', { no: a.apNo })))) return
    setRunning(true)
    try {
      await api.cancelApproval(a.id)
      useUIStore.getState().notify(t('apCancelled', { no: a.apNo }), 'success')
      await load()
      if (detailRef.current) setDetail(listRef.current.find((x) => x.id === a.id) || null)
    } catch (e) {
      // 服务端说的理由原样出现在 toast 上 —— "去驳回它"与"已被处理过"要人做的事不同。
      useUIStore.getState().notifyError(e, t('actionFailed'))
      await load()
    } finally { setRunning(false) }
  }

  async function runApproved(a: Approval) {
    if (a.canExecute !== true || runningRef.current) return
    // 这一下是真的落到库上,而且只有一次机会 —— 确认文案要把这两点都说出来。
    if (!(await confirmAction(t('apExecuteConfirm', { cmd: apCmd(a) })))) return
    setRunning(true)
    const id = a.id
    watchedExec.add(id)
    // 请求要等命令跑完才回。先刷新一次,让这张单马上显示成"执行中",而不是一直停在"待执行"。
    const peek = setTimeout(() => { void load() }, 800)
    try {
      await api.executeApproval(id)
      // 结果提示按刚落库的那个结论走(settleWatched),和列表上显示的是同一个来源 —— 目标库
      // 拒绝这条语句时,接口仍然是一次成功的调用,不能因此弹"执行完成"。
      await load()
    } catch (e: any) {
      // 业务拒绝(ok() 抛出的,带信封里的 code)是结论;axios 自己的错误 —— 没等到响应,或者
      // 代理回了 5xx —— 只说明请求断了,命令可能已经在跑
      if (e?.isAxiosError && (!e.response || e.response.status >= 500)) {
        // 请求本身断了(超时、网关代理切断),命令未必没跑:接着按工单上的状态等结论
        useUIStore.getState().notify(t('apExecuteStillRunning'), 'info')
      } else {
        // 服务端的拒绝理由原样透出:等审批、找发起人、看发布单、重新提交 —— 四种
        // 拒绝要人做的事完全不同。
        watchedExec.delete(id)
        useUIStore.getState().notifyError(e, t('actionFailed'))
      }
      await load()
    } finally {
      clearTimeout(peek)
      setRunning(false)
    }
  }

  return (
    <div className="scy page">
      <div className="v-head">
        <div className="v-head-l">
          <div className="v-eyebrow">APPROVALS</div>
          <div className="v-h1">{t('t_approve')}</div>
          <div className="v-sub hint" data-testid="appr-sub"><Inbox size={12} />{t('apprSubTail')}</div>
        </div>
      </div>
      <div className="card">
        {/* 卡片头:标题 + 一句弱化的说明 + 右侧控制区(搜索/状态/范围/刷新) */}
        <div className="chead">
          <div className="ctrls">
            <div className="searchbox">
              <Search size={14} color="var(--text-faint)" />
              <input
                value={q} placeholder={t('apSearchPh')} spellCheck={false}
                onChange={(e) => {
                  setQ(e.target.value)
                  if (!(e.nativeEvent as InputEvent).isComposing) qRef.current = e.target.value
                  onSearch()
                }}
                onCompositionEnd={(e) => { setQuery(e.currentTarget.value); onSearch() }}
              />
              {q && <button className="sclear" title={t('apSearchClear')} onClick={clearSearch}><X size={13} /></button>}
            </div>
            <VSelect value={statusLabel} onChange={pickStatus} options={statusLabels} height="32px" className="stsel" />
            <VSelect value={typeLabel} onChange={pickType} options={typeLabels} height="32px" className="stsel" />
            <div className="tabs">
              <div className={clsx('tab', scope === 'mine' && 'active')} onClick={() => setScope('mine')}>{t('mineAppr')}</div>
              <div className={clsx('tab', scope === 'all' && 'active')} onClick={() => setScope('all')}>{t('allAppr')}</div>
            </div>
            <button className="refresh" disabled={refreshing} title={t('btnRefresh')} onClick={refresh}>
              <RotateCw size={14} className={clsx(refreshing && 'spin')} />
            </button>
          </div>
        </div>

        <div className="table">
          <div className="thead">
            <span>{t('apNo')}</span><span>{t('apCommand')}</span>
            <span>{t('apInitiator')}</span><span className="right">{t('apTime')}</span><span className="right">{t('apExecTime')}</span>
            <span>{t('apStatus')}</span><span className="acth">{t('apActions')}</span>
          </div>
          {list.map((a) => (
            <div key={a.id} className="tr" onClick={() => openDetail(a)}>
              {/* 单号与优先级同格:它们回答的是同一件事 —— "这是哪一张单,有多要紧" */}
              <span className="idcell">
                <span className="mono apno click" title={t('apDetail')}>#{a.apNo}</span>
                <span className={clsx('badge', a.riskLevel === 'high' ? 'p1' : 'p2')}>{t(riskMeta(a).t)}</span>
                {/* 类型标签只画给**不是命令单**的那三类。命令单占了绝大多数,给每一行都挂
                    一枚"命令单"是纯噪音;而升级/窗口/导出这三类的命令栏本来就是一句描述、
                    解析不出表名(那一格显示的是一个短横),标签补的正是那一格缺的信息。
                    类型由服务端推好带下来(a.type),前端不照着三个外键再推一遍。 */}
                {a.type && a.type !== 'cmd' && <span className={clsx('tybadge', a.type)}>{t('apType_' + a.type)}</span>}
              </span>
              {/* 目标表用代码块;取不出表名时给一个居中的弱灰短横,而不是留空 */}
              <span className="tgtcell">
                <span className="tbl" title={apCmd(a)}>{tablesOf(a) ? <code>{tablesOf(a)}</code> : <span className="none">—</span>}</span>
                <span className="inst"><span className={clsx('envtag', a.tierCode || 'unknown')}>{envText(a)}</span>{a.instance}{a.database && <> / {a.database}</>}</span>
              </span>
              <span className="who"><span className="ava">{initialsOf(a.initiator)}</span>{a.initiator}</span>
              <span className="timecell">{listTime(a.createdAt)}</span>
              {/* 执行时间。批准只是解锁了一次执行,真正落到库上往往要等业务低峰 —— 事后
                  对时间线要看的是这一列。判据和详情卡一样只认 executedAt:没跑过的(等审批、
                  待执行、被驳回/撤回)给一个短横,不拿提交时间或审批时间充数。 */}
              <span className="timecell">{a.executedAt ? listTime(a.executedAt) : <span className="none">—</span>}</span>
              <span className="stcell">
                <span className={clsx('stbadge', rowState(a))} title={a.status === 'rejected' && a.rejectReason ? a.rejectReason : undefined}>
                  <span className="sdot" /><span>{t(rowLabel(a))}</span>
                </span>
              </span>
              {/* 操作列里只放**操作**。这里原先还有一个"详情"按钮,而它做的事和点这一
                  整行一模一样(.tr 自己就带 onClick) —— 一个纯冗余的按钮,
                  却把这一列撑宽了三分之一。看一眼不是一次操作,它不该占这里的位置。
                  进详情的入口是整行,以及左边那个单号。

                  代办项直接在行上决定,不必先开详情卡:待审的单子通常是一眼就能判的,
                  而为每一张都开一次卡再关掉,是这一页最常做也最没必要的一次点击。
                  判断谁能决定仍然只看服务端算好的 canDecide,确认弹窗与失败提示与卡片里
                  那一对按钮共用同一个 decide()。 */}
              <span className="acts">
                {/* 撤回排在最左、样式最轻:它是发起人的退路,不是这一列的主操作。
                    条件只看 canCancel,不再自己判 status —— 服务端已经把"等审批的"和
                    "已批准但没跑的"两种都算进去了,前端再判一遍就是第二套会跑偏的规则。 */}
                {a.canCancel && (
                  <button
                    className="rowbtn cxl" disabled={running}
                    title={t('apCancel')} onClick={(e) => { e.stopPropagation(); void cancelAp(a) }}
                  >
                    <Undo2 size={13} />{t('apCancel')}
                  </button>
                )}
                {a.status === 'pending' && canDecide(a) ? (
                  <>
                    <button className="rowbtn rej" title={t('apReject')} onClick={(e) => { e.stopPropagation(); void decide(a, false) }}>
                      <CircleX size={13} />{t('apReject')}
                    </button>
                    <button className="rowbtn ok" title={t('apApprove')} onClick={(e) => { e.stopPropagation(); void decide(a, true) }}>
                      <CircleCheckBig size={13} />{t('apApprove')}
                    </button>
                  </>
                ) : a.canExecute ? (
                  /* 执行同样放到行上。它和审批不会同时出现:一张单要么在等人决定,要么已经
                     批了在等人跑 —— canExecute 由服务端算(service.CanExecuteApproved),
                     所以这个按钮只对**此刻真的能跑它**的人亮,而不是"看起来能点"。
                     能跑它的不再只有发起人:够得到那台实例、能力矩阵也放行的同事都可以
                     接手(ADR 0010 的 2026-09-09 修订)。 */
                  <button
                    className="rowbtn run" disabled={running}
                    title={t('apExecute')} onClick={(e) => { e.stopPropagation(); void runApproved(a) }}
                  >
                    <Play size={13} />{t('apExecute')}
                  </button>
                ) : null}
              </span>
            </div>
          ))}
          {/* 空态要分清"确实没有"和"筛没了":后者的出口是清掉条件,不是等着 */}
          {!list.length && (
            <div className="empty">
              <div className="eic"><Inbox size={22} color="var(--text-faint)" /></div>
              <div className="et">{(q || statusFilter) ? t('apEmptyFiltered') : t('apEmpty')}</div>
              {(q || statusFilter) && (
                <button className="eclear" onClick={() => { setQuery(''); setStatusFilter(''); setPage(1); void load() }}>{t('apClearFilters')}</button>
              )}
            </div>
          )}
        </div>

        {/* 分页收进卡片内部,靠一条分割线和表格分开 */}
        <div className="pfootbar">
          <span className="ptotal">{t('apTotal', { n: total })}</span>
          <div className="pager">
            <span className="psize">
              {t('apPerPage')}{' '}
              <select value={pageSize} onChange={(e) => setPageSize(Number(e.target.value))}>
                {pageSizes.map((n) => <option key={n} value={n}>{n}</option>)}
              </select>
            </span>
            <button className="pg" disabled={page <= 1} onClick={() => goto(page - 1)}><ChevronLeft size={15} /></button>
            <span className="pgn">{t('auditPageOf', { p: page, n: pages })}</span>
            <button className="pg" disabled={page >= pages} onClick={() => goto(page + 1)}><ChevronRight size={15} /></button>
          </div>
        </div>
      </div>

      {/* detail card: the full command and everything needed to decide on it */}
      {detail && (
        <div className="overlay">
          <div className="mask" onClick={closeDetail} />
          <div className="dcard">
            <div className="dhead">
              <span className="badge" style={{ background: riskMeta(detail).bg, color: riskMeta(detail).c }}>{t(riskMeta(detail).t)}</span>
              <span className="dtitle">{detail.keyword} {t('apprCardTail')}</span>
              <span className="mono apno">#{detail.apNo}</span>
              <X size={18} className="dx" onClick={closeDetail} />
            </div>
            <div className="dbody">
              <div className="dlbl">{t('apCommand')}</div>
              {fullScript !== null
                ? <pre className="dcmd full" data-testid="ap-script-content">{fullScript}</pre>
                : <pre className="dcmd">{apCmd(detail)}</pre>}
              {!!detail.scriptUploadId && (
                <div className="fullbar">
                  <button className="linkbtn" data-testid="ap-full-script" disabled={scriptLoading} onClick={toggleFullScript}>
                    {scriptLoading ? t('apScriptLoading') : fullScript !== null ? t('apHideFullScript') : t('apViewFullScript')}
                  </button>
                  {scriptErr && <span className="scripterr" data-testid="ap-script-error">{scriptErr}</span>}
                </div>
              )}
              <div className="dgrid">
                <div><span className="ml">{t('apInitiator')}</span><div className="who"><span className="ava">{initialsOf(detail.initiator)}</span>{detail.initiator}</div></div>
                <div><span className="ml">{t('apTarget')}</span><div className="tgt">{envText(detail)} · {detail.instance}{detail.database && <> · {detail.database}</>}</div></div>
                {/* The tier the command was JUDGED under, snapshotted when the ticket
                    was raised. Shown separately from the environment because the two
                    can diverge later, and this is the one that explains the verdict.
                    Blank on tickets predating tiers — reported as unknown rather than
                    resolved from today's binding, which would be a guess about the
                    past presented as a fact. */}
                <div><span className="ml">{t('apTier')}</span><div className="tgt">{detail.tierCode ? detail.tierCode.toUpperCase() : t('apTierUnknown')}</div></div>
              </div>
              <div className="ml">{t('apReason')}</div><div className="rt">{detail.reason || '—'}</div>
              {/* 要审批人特别留意的一句话:生产大表上、在线变更做不了的 DDL 会直接执行,可能长时间锁表 */}
              {(detail.notice || detail.noticeRef) && <div className="apnotice" data-testid="ap-notice">⚠ {renderRule(detail.noticeRef, detail.notice || '', { t, te })}</div>}
              {/* 在线变更单:进度与控制(审批前也显示变更内容、数据量与前置检查提示) */}
              {!!detail.oscJobId && <OscJobPanel jobId={detail.oscJobId} onChanged={load} />}
              <div className="ml">{t('apChain')}</div>
              <div className="chain"><GitPullRequestArrow size={14} color="#8facff" />{chainText(detail)}{chatVia(detail) && <span className="via" data-testid="ap-decided-via">{t('apDecidedVia', { channel: chatVia(detail) })}</span>}</div>
              {detail.status === 'rejected' && detail.rejectReason && (
                <><div className="ml">{t('apRejectReason')}</div><div className="rt rejreason" data-testid="ap-reject-reason">{detail.rejectReason}</div></>
              )}
              {/* 执行结果。判据是 executedAt,不是"result 有没有内容":
                  一次失败可能什么输出都没留下,而那恰恰是最需要看到"它跑过、并且挂了"
                  的时候;反过来,一张刚批准还没跑的工单,result 里躺着的是审批时写下的
                  占位文字("· 已批准,等待发起人执行")—— 拿它当"有结果"会让详情页对着
                  一条还没下发的命令说"已执行"。跑没跑过,只有 executedAt 说了算。 */}
              {detail.executedAt && (
                <>
                  <div className="ml">{t('apResult')}</div>
                  <div className={clsx('rhead', detail.execStatus || 'unknown')}>
                    {detail.execStatus === 'failed' ? <CircleX size={14} />
                      : detail.execStatus === 'success' ? <CircleCheckBig size={14} />
                        : detail.execStatus === 'running' ? <Loader size={14} className="spin" /> : null}
                    <span>{t(execHeadKey(detail))}</span>
                    <span className="rmeta">{shortTime(detail.executedAt)}</span>
                    {/* resultRows < 0:数据库没报影响行数(Oracle PL/SQL 块),不显示一个错的 0 */}
                    {detail.execStatus === 'success' && <span className="rmeta" data-testid="ap-result-rows">{detail.resultRows < 0 ? t('apResultRowsUnknown') : t('apResultRows', { n: detail.resultRows })}</span>}
                  </div>
                  {/* 执行中:result 里还是审批时写下的"等待发起人执行",不拿它当输出 */}
                  {detail.execStatus === 'running' ? <div className="rt">{t('apResultRunningHint')}</div>
                    : detail.result ? <pre className={clsx('dcmd out', detail.execStatus === 'failed' && 'bad')}>{localizeText(detail.result, detail.resultRef)}</pre>
                      : <div className="rt">{t('apResultNoLog')}</div>}
                </>
              )}
            </div>
            <div className="dfoot">
              {detail.status === 'pending' && canDecide(detail) ? (
                <>
                  <VButton variant="danger" height="38px" onClick={() => decide(detail, false)}>{t('apReject')}</VButton>
                  <VButton variant="primary" height="38px" onClick={() => decide(detail, true)}>{t('apApprove')}</VButton>
                </>
              ) : detail.status === 'pending' ? (
                <span className="waitc">{localizeText(detail.blockReason, detail.blockReasonRef) || t('apWaitChain')}</span>
              ) : detail.status === 'approved' && detail.canExecute ? (
                /* 通过之后命令还没跑。这一格要说清它现在停在哪一步:等我执行 /
                   已经执行过了 / 归流水线执行 / 等的是别人。 */
                <>
                  <span className="waitc grow">{t('apExecuteMine')}</span>
                  <VButton variant="primary" height="38px" disabled={running} onClick={() => runApproved(detail)}>{t('apExecute')}</VButton>
                </>
              ) : detail.status === 'approved' && detail.execStatus === 'running' && detail.executedAt ? (
                /* 跑挂了也是"执行过了":一次批准只换一次执行,失败不退回重来(命令到底
                   跑没跑是不确定的,见 service.ExecuteApproved 的占位注释)。所以这里说的
                   是结果,不是一个可以再点一次的按钮。 */
                <span className="waitc">{t('apExecRunningSince', { at: shortTime(detail.executedAt) })}</span>
              ) : detail.status === 'approved' && detail.executedAt ? (
                <span className={clsx('waitc', detail.execStatus === 'failed' && 'bad')}>
                  {detail.execStatus === 'failed'
                    ? t('apExecutedFailed', { at: shortTime(detail.executedAt) })
                    : t('apExecuted', { at: shortTime(detail.executedAt) })}
                </span>
              ) : detail.status === 'approved' && detail.releaseId ? <span className="waitc">{t('apExecuteByPipeline')}</span>
                : detail.status === 'approved' && detail.windowId ? <span className="waitc">{t('apWindowLive')}</span>
                  : detail.status === 'approved' && detail.queryGrantReqId ? <span className="waitc" data-testid="ap-grant-live">{t('apGrantLive')}</span>
                  : <span className="waitc">{detail.status === 'approved' ? t('apDone') : t('apRejected')}</span>}
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
