import { Fragment, lazy, Suspense, useEffect, useRef, useState, type ComponentType } from 'react'
import { createPortal } from 'react-dom'
import { Outlet } from 'react-router'
import clsx from 'clsx'
import { FolderKanban, ShieldHalf, ArrowLeftRight,
  LayoutDashboard, SquareTerminal, ClipboardCheck, Database, ShieldAlert, Layers, UsersRound, Rocket, GitBranch, SpellCheck,
  ScrollText, Settings, Activity as ActivityIcon, Languages, Bell, BellOff, BellRing, Play, ChevronRight, Hourglass,
  CircleCheck, CircleX, Clock, DatabaseZap, Upload, LogOut, Sun, Moon, ShieldCheck, SearchCode, GitCompare, Stethoscope,
  LockKeyhole, Ellipsis, Bot, Link2, Unlink2,
} from 'lucide-react'
import Watermark from '@/components/Watermark'
import BrandMark from '@/components/BrandMark'
import KeepAlive from '@/components/KeepAlive'
import { brandText, titleCount } from '@/lib/brand'
import { renderApText } from '@/lib/ruleText'
import { useAuthStore } from '@/stores/auth'
import { useUIStore } from '@/stores/ui'
import { playTodoChime, chimeMuted, setChimeMuted } from '@/lib/chime'
// 「哪几张该响」是一条容易在重构里悄悄丢掉的规则(别人提的单不响),所以它是一个被单测
// 钉住的纯函数,不写在这个组件里 —— 见 lib/todoChime.ts。
import { countMyExecuteTodos } from '@/lib/todoChime'
import { BUILTIN_ROLE_NAME, BUILTIN_ROLE_LAYER, builtinLabel } from '@/lib/builtinNames'
import api from '@/api'
import { localizeText } from '@/api/http'
import MfaModal from '@/components/modals/MfaModal'
import PasswordModal from '@/components/modals/PasswordModal'
import ChatBindModal from '@/components/modals/ChatBindModal'
import type { ApprovalTodoItem, ChatBinding, Notification, RuleRef } from '@/types'
import { useBrand } from '@/hooks/useBrand'
import { useEvent } from '@/hooks/useEvent'
import { useI18n } from '@/hooks/useI18n'
import { nav, useRoute } from '@/hooks/useNav'
import { router } from '@/router'
import type { RouteMeta } from '@/router/routes'
import { useAdminIdle } from './useAdminIdle'
import './AppLayout.scoped.css'

const TerminalView = lazy(() => import('@/views/TerminalView'))

type Icon = ComponentType<any>
interface NavItem { key: string; path: string; icon: Icon; label: string; always?: boolean; gate?: string; badge?: boolean; admin?: boolean; auditor?: boolean }

const navItems: NavItem[] = [
  // 总览始终可见(always):它是落地页,而且只显示调用者本来就取得到的东西。
  { key: 'dashboard', always: true, path: '/dashboard', icon: LayoutDashboard, label: 'navHome' },
  { key: 'terminal', path: '/terminal', icon: SquareTerminal, label: 'navTerm' },
  // export & uploads reuse the terminal permission (`gate`) rather than own menu keys.
  { key: 'export', gate: 'terminal', path: '/export', icon: DatabaseZap, label: 'navExport' },
  { key: 'uploads', gate: 'terminal', path: '/uploads', icon: Upload, label: 'navUploads' },
  { key: 'asyncjobs', gate: 'terminal', path: '/async-jobs', icon: Clock, label: 'navAsync' },
  // 发布 (CI/CD) — the release list and the flow editor share the pipeline menu.
  { key: 'releases', gate: 'pipeline', path: '/releases', icon: Rocket, label: 'navReleases' },
  // 规范审查 rides the terminal permission: it is a self-check tool first, and a
  // rule library second (editing it is admin-only server-side).
  { key: 'sqlreview', gate: 'terminal', path: '/sql-review', icon: SpellCheck, label: 'navSqlReview' },
  // 跨环境表结构对比:自己一个菜单键,默认给平台管理员与 DBA 负责人(见 backfillSchemaDiffMenu)。
  { key: 'schemadiff', path: '/schema-diff', icon: GitCompare, label: 'navSchemaDiff' },
  // AI 巡检报告:默认给平台管理员、DBA 负责人、DBA L2
  { key: 'inspection', path: '/inspection', icon: Stethoscope, label: 'navInspection' },
  { key: 'approve', path: '/approvals', icon: ClipboardCheck, label: 'navAppr', badge: true },
  { key: 'project', path: '/projects', icon: FolderKanban, label: 'navProject' },
  { key: 'execwindow', path: '/exec-windows', icon: Clock, label: 'navExecWindow' },
  { key: 'query', path: '/query', icon: SearchCode, label: 'navQuery' },
  // 我的操作记录:人人可见(always),只看自己的;全量审计在管理后台。能看全量审计的人(管理员 / 审计员 / DBA 负责人)看到的是所有人的,
  // 菜单与页头改叫「操作记录」(见 teamLog)。只开了数据查询的业务方也有这一项(2026-10-14 用户确认)。
  { key: 'mylog', always: true, path: '/audit', icon: ScrollText, label: 'navMyLog' },
]

// 管理后台(/admin)的导航。admin:只有平台管理员;auditor:审计员也看得到(只有审计这一项)。
// 后端接口有同一口径的强制校验(工单 01),这里只决定显示。
const adminNavItems: NavItem[] = [
  { key: 'adminhome', admin: true, always: true, path: '/admin/overview', icon: LayoutDashboard, label: 'navAdminHome' },
  { key: 'db', admin: true, path: '/admin/connections', icon: Database, label: 'navDb' },
  { key: 'rules', admin: true, path: '/admin/risk-rules', icon: ShieldAlert, label: 'navRules' },
  { key: 'envtier', admin: true, path: '/admin/env-tiers', icon: Layers, label: 'navEnvTier' },
  { key: 'perms', admin: true, path: '/admin/permissions', icon: UsersRound, label: 'navPerms' },
  { key: 'audit', auditor: true, path: '/admin/audit', icon: ScrollText, label: 'navAuditFull' },
  { key: 'execwindow', admin: true, path: '/admin/exec-windows', icon: Clock, label: 'navExecWindowAll' },
  { key: 'sqlreview', admin: true, gate: 'terminal', path: '/admin/sql-review', icon: SpellCheck, label: 'navSqlRules' },
  { key: 'pipelines', admin: true, gate: 'pipeline', path: '/admin/pipelines', icon: GitBranch, label: 'navPipelines' },
  { key: 'settings', admin: true, path: '/admin/settings', icon: Settings, label: 'navSettings' },
]

const okTypes = ['approval-approved', 'export-done', 'release-done']
const badTypes = ['approval-rejected', 'export-failed', 'release-failed', 'exec-failed']
// 聊天账号绑上 / 解绑:谁能替我批单变了,和审批结果分得开。没列进 okTypes —— 不是本人绑的就不是好消息。
const chatNotif: Record<string, Icon> = { 'chat-bound': Link2, 'chat-unbound': Unlink2 }
const notifIcon = (t: string): Icon => chatNotif[t] ?? (okTypes.includes(t) ? CircleCheck : badTypes.includes(t) ? CircleX : Clock)
const notifCls = (t: string) => (okTypes.includes(t) ? 'ok' : badTypes.includes(t) ? 'bad' : 'warn')

// ---- 右上角"待办":此刻在等我动手的三类事 ----
//
// 它必须**显眼**:一张等着审的单子,发起人正坐在那儿等;一张批了没跑的单子,批准的
// 授权是有时效的。原先这里是一枚永远同一种颜色的"待审批 N",数的还是"我看得见的
// 待审批"(含我自己发起的) —— 它既不区分"要我动手"和"等别人动手",也不会在有事时
// 变个样子,于是没人看它。
//
// 现在:数只算服务端判过"此刻能动"的(见 auth.refreshTodo);有待办时整枚变成琥珀色
// 并带一圈脉冲;新到一条弹一次 toast;标签页标题前挂上数量,切到别的标签页也看得见。
// 点开是一张面板,每行直接跳到那张单的详情。
// 三类待办各自的图标、色与说法。用表不用三元:再加一类时改一处,而一串嵌套三元
// 会在第三类上变得没法读 —— 这一段正是这么长出来的。
const TODO_ICON: Record<string, Icon> = { decide: CircleCheck, execute: Play, confirm: Hourglass }
const TODO_CLS: Record<string, string> = { decide: 'warn', execute: 'ok', confirm: 'info' }
const TODO_LABEL: Record<string, string> = { decide: 'todoKindDecide', execute: 'todoKindExecute', confirm: 'todoKindConfirm' }
// 认不出的 kind 照常画出来,只是不带颜色与说法。会出现这种情况的是版本错位:页面开着
// 没刷新,服务端已经升到了认识第四类的版本 —— 那一行仍然要能点开,而不是一个空白格
// 加一串控制台告警。
const todoIcon = (k: string): Icon => TODO_ICON[k] || Clock
const todoLabel = (k: string) => TODO_LABEL[k] || 'todoTitle'

// 连响三下。一下容易被当成别的软件的提示音错过,三下是"有人在叫你"。
const CHIME_TIMES = 3

const IDLE_EVENTS = ['mousemove', 'mousedown', 'keydown', 'scroll', 'touchstart'] as const

const go = (path: string) => { void nav.push(path) }

export default function AppLayout() {
  const { t, te } = useI18n()
  const route = useRoute()
  const auth = useAuthStore()
  const lang = useUIStore((s) => s.lang)
  const pageSubState = useUIStore((s) => s.pageSub)
  const isDark = useUIStore((s) => s.resolved) === 'dark'
  const toggleLang = useUIStore((s) => s.toggleLang)
  const toggleTheme = useUIStore((s) => s.toggleTheme)
  const brand = useBrand()

  const inAdmin = route.meta.portal === 'admin' || route.path.startsWith('/admin')
  const idle = useAdminIdle(inAdmin)

  // 描述类单子(执行窗口)按读者语言显示;SQL 单子没有 commandRef,原样显示 command(lib/ruleText)。
  const apCmd = (a: { command: string; commandRef?: RuleRef }) => renderApText(a.commandRef, a.command, { t: t as never, te: te as never })

  // 只开了数据查询页的业务方:总览对他没有内容(路由也会把他送回 /query),总览这类常驻项藏起来;
  // 「我的操作记录」留着 —— 他查过什么,自己也该看得到(2026-10-14 用户确认)。
  const visibleNav = inAdmin
    ? adminNavItems.filter((n) => (n.admin ? auth.isAdmin : auth.isAdmin || auth.isAuditor) && (n.always || auth.menus[n.gate ?? n.key]))
    : navItems.filter((n) => (n.always && (!auth.onlyQuery || n.key === 'mylog')) || auth.menus[n.gate ?? n.key])
  // 能看全量审计的人(管理员 / 审计员 / DBA 负责人)在用户后台的记录页默认看所有人的(见 AuditView 的 team;
  // 2026-10-26 回归排查 09:管理员 / 审计员从前在这里只看自己的)
  const teamLog = auth.seesAllAudit
  const navLabel = (n: NavItem) => (n.key === 'mylog' && teamLog ? 'navTeamLog' : n.label)
  // 切换前后台:到管理后台用 /admin(按身份落到能进的第一页),回用户后台落总览。
  const switchPortal = () => go(inAdmin ? '/dashboard' : '/admin')

  const activeKey = route.meta.menuKey || ''
  // Titles are keyed off the MENU key, not the route name. The two differ for
  // several routes (connections→db, approvals→approve, risk-rules→rules,
  // permissions→perms), and i18n renders a missing key as the key itself — so
  // those four pages showed a raw "t_connections" where their title belongs.
  const menuOrName = route.meta.menuKey || route.name || ''
  const titleKey = menuOrName === 'mylog' && teamLog ? 'teamlog' : menuOrName
  // Views may publish a data-driven subtitle via ui.pageSub; otherwise fall back to i18n default.
  // 在这里渲染,而不是在各个视图里:切语言这行字会跟着变。视图设的是 key + 参数(见 ui store 里 PageSub 的注释)。
  const pageSub = !pageSubState ? t(`t_${titleKey}Sub`)
    : typeof pageSubState === 'string' ? pageSubState : t(pageSubState.key, pageSubState.params || {})

  // 副标题的覆盖值只属于设它的那一页:路由一换就清掉。放在 router.subscribe 里(新页渲染之前),
  // 不放 effect —— 子视图的 effect 先于布局的跑,放 effect 会把新页刚设的副标题抹掉。
  useEffect(() => {
    let last = route.name
    return router.subscribe((st) => {
      const name = (st.matches.at(-1)?.route.handle as RouteMeta | undefined)?.name ?? ''
      if (name === last) return
      last = name
      useUIStore.getState().setPageSub('')
    })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // ---- notifications ----
  const [notifs, setNotifs] = useState<Notification[]>([])
  const [notifUnread, setNotifUnread] = useState(0)
  const [notifOpen, setNotifOpen] = useState(false)
  // 已经弹过的通知 id。
  //
  // **首次拉取只记账、不弹窗**:否则每次刷新页面都会把历史通知重演一遍 —— 三天前
  // 那次导出完成的提示,今天早上再弹一次,人很快就学会了无视所有弹窗。这里要的是
  // "刚刚完成了",不是"曾经完成过"。
  const toasted = useRef(new Set<number>())
  const notifPrimed = useRef(false)

  // 审批出结果、异步导出跑完,人早就离开那个页面了 —— 不主动告诉他,他只能自己
  // 想起来回去刷一下。铃铛上的红点不算"告诉":它要人先看见、再点开。
  function toastNew(items: Notification[]) {
    for (const n of items) {
      if (toasted.current.has(n.id)) continue
      toasted.current.add(n.id)
      if (!notifPrimed.current || n.read) continue
      const title = localizeText(n.title, n.titleRef)
      const body = n.body ? `${title} — ${localizeText(n.body, n.bodyRef)}` : title
      useUIStore.getState().notify(body, badTypes.includes(n.type) ? 'error' : okTypes.includes(n.type) ? 'success' : 'info')
    }
  }
  const fetchNotifs = useEvent(async (): Promise<number> => {
    try {
      const r = await api.notifications()
      setNotifs(r.items ?? [])
      setNotifUnread(r.unread)
      toastNew(r.items)
      notifPrimed.current = true // 第一轮只是记下已有的,从第二轮起才算"新到的"
      return r.unread
    } catch { return notifUnread }
  })
  async function toggleNotif() {
    const open = !notifOpen
    setNotifOpen(open)
    if (!open) return
    const unread = await fetchNotifs()
    if (unread > 0) {
      try { await api.markNotificationsRead() } catch { /* ignore */ }
      setNotifUnread(0)
      setNotifs((l) => l.map((n) => ({ ...n, read: true })))
    }
  }
  function openApprovals() {
    setNotifOpen(false)
    if (auth.menus.approve) go('/approvals')
  }

  // ---- live gateway status (real latency, not hard-coded) ----
  const [gwOnline, setGwOnline] = useState(true)
  const [gwP50, setGwP50] = useState<number | null>(null)
  const fetchGwStats = useEvent(async () => {
    try {
      const s = await api.gatewayStats()
      setGwOnline(s.online)
      setGwP50(s.p50Ms)
    } catch { setGwOnline(false) }
  })

  // ---- second-factor enrolment (available to every account) ----
  const [mfaOpen, setMfaOpen] = useState(false)
  const mfaBound = !!auth.me?.mfaEnabled
  async function onMfaChanged() {
    setMfaOpen(false)
    await auth.fetchMe() // refresh the bound state shown in the menu
  }
  // ---- 自助修改密码(黑盒 2026-10 工单 05)----
  const [pwOpen, setPwOpen] = useState(false)
  // ---- user menu / manual logout ----
  const [userMenuOpen, setUserMenuOpen] = useState(false)
  // ---- 审批机器人绑定。打开菜单时才问一次有没有开着的渠道;没有、或读不到,就不显示这一项 ----
  const [chatBindings, setChatBindings] = useState<ChatBinding[]>([])
  const [chatOpen, setChatOpen] = useState(false)
  const chatSeq = useRef(0) // 菜单与弹窗都来问:只认最后一次的答复
  const loadChat = useEvent(async () => {
    const mine = ++chatSeq.current
    const list = await api.chatBindings()
    if (mine === chatSeq.current) setChatBindings(list ?? [])
  })
  // 解绑成了就当场改过来,不指望接下来那次重读一定成功;在路上的旧答复作废。
  function onChatUnbound(channel: string) {
    chatSeq.current++
    setChatBindings((l) => l.map((b) => (b.channel === channel ? { ...b, bound: false, boundAt: undefined } : b)))
  }
  function toggleUserMenu() {
    setUserMenuOpen(!userMenuOpen)
    if (userMenuOpen) return
    setChatBindings([]) // 不拿上一次打开时的答复先顶着
    loadChat().catch(() => { /* 读不到就不显示 */ })
  }

  // 窄屏(手机宽度)下顶栏放不下的次要按钮收进这个「更多」菜单:语言、深色、前后台切换、网关状态。
  // 宽屏时它不显示(CSS 控制),按钮照常摆在顶栏上。
  const [moreOpen, setMoreOpen] = useState(false)

  // 有待办可言的三种菜单:审批(等我审批)、终端(等我执行)、发布(等我确认)。
  // 一个都没有的人不显示这枚徽章 —— 他没有任何能处理的动作。
  const canTodo = !!(auth.menus.approve || auth.menus.terminal || auth.menus.pipeline)
  const [todoOpen, setTodoOpen] = useState(false)
  const todoPrimed = useRef(false)
  const pollTodo = useEvent(async () => {
    const before = useAuthStore.getState().todoTotal
    await useAuthStore.getState().refreshTodo()
    const after = useAuthStore.getState().todoTotal
    // 首轮只记账:刷新页面不该把已有的待办再"新到"一遍。
    if (todoPrimed.current && after > before) useUIStore.getState().notify(t('todoNew', { n: after - before }), 'info', 6000)
    todoPrimed.current = true
  })

  // 登录之后那一声。
  //
  // **只对发起人响。** 执行权早就放开给"够得到那台实例的人"了(ADR 0010 的 2026-09-09
  // 修订),所以 todo.execute 里躺着的可能是别人提的单 —— 那些是"你可以顺手帮着跑",不是
  // "你的东西卡在这儿"。值班的人一登录就被自己没提过的单子叫一通,几天之后这声音就只剩
  // 噪音了。谁提的,见 isMine()。
  //
  // 只认 execute 这一类:发起人那张单已经被人批了,授权是有时效的,而他不点那一下就什么
  // 都不会发生。decide(等我审)已经有琥珀色徽章、脉冲和标签页计数在提醒。
  //
  // 它**自己去拉一次待办**,不搭轮询的便车:轮询挂在 canTodo 这个菜单条件下,而"我提的单
  // 批了没"跟我有没有审批菜单没有关系。登录之后要立刻知道,不该等下一轮。
  async function chimeOnLogin() {
    const a = useAuthStore.getState
    if (!a().consumeJustLoggedIn()) return
    await a().refreshTodo()
    const mine = countMyExecuteTodos(a().todo.items, a().me)
    if (mine <= 0) return
    playTodoChime(CHIME_TIMES)
    useUIStore.getState().notify(t('todoChimeToast', { n: mine }), 'info', 8000)
  }

  // 提示音的开关。存在这台浏览器上(localStorage),不是账号设置 —— 吵不吵是"我这台机器、
  // 这个工位"的事,换台机器该重新决定。一个关不掉的提示音,第二天会被整个标签页静音掉。
  const [muted, setMuted] = useState(chimeMuted)
  function toggleChime() {
    const m = !muted
    setMuted(m)
    setChimeMuted(m)
    if (!m) playTodoChime() // 打开时响一声,让人知道它是什么动静
  }
  async function toggleTodo() {
    setTodoOpen(!todoOpen)
    if (!todoOpen) await auth.refreshTodo()
  }
  function openTodoItem(it: ApprovalTodoItem) {
    setTodoOpen(false)
    // 待确认的是一张**发布单**,不是工单 —— 它的 id 是发布单 id,去发布页按 id 深链。
    if (it.kind === 'confirm') { void nav.push({ path: '/releases', query: { rel: String(it.id) } }); return }
    // 审批页按单号深链直接打开详情卡;没有审批菜单的人(只有终端)在总览页执行。
    if (auth.menus.approve) void nav.push({ path: '/approvals', query: { ap: it.apNo } })
    else go('/dashboard')
  }
  // 标签页标题带上数量:人切去别的标签页干活时,"(2) AegisDB" 是唯一还能看见的提示。
  // 标题本身由 App.tsx 写(品牌名 + 这个数),见 lib/brand.ts。
  useEffect(() => { titleCount.value = auth.todoTotal }, [auth.todoTotal])

  // ---- idle auto-lock (Session & Security · security.idleLock) ----
  const lockRef = useRef({ ms: 15 * 60 * 1000, enabled: true, timer: null as ReturnType<typeof setTimeout> | null, lastKick: 0 })
  const kickIdle = useEvent(() => {
    const l = lockRef.current
    const now = Date.now()
    if (now - l.lastKick < 5000) return // throttle: reset at most every 5s
    l.lastKick = now
    if (l.timer) clearTimeout(l.timer)
    if (l.enabled) l.timer = setTimeout(lockSession, l.ms)
  })
  function teardownIdle() {
    const l = lockRef.current
    if (l.timer) clearTimeout(l.timer)
    l.timer = null
    IDLE_EVENTS.forEach((e) => window.removeEventListener(e, kickIdle))
  }
  function lockSession() {
    teardownIdle()
    useAuthStore.getState().logout()
    void nav.push({ path: '/login', query: { locked: '1' } })
  }
  function logout() {
    setUserMenuOpen(false)
    teardownIdle()
    auth.logout()
    go('/login')
  }

  useEffect(() => {
    let gone = false
    const timers: ReturnType<typeof setInterval>[] = []
    const every = (fn: () => void, ms: number) => { if (!gone) timers.push(setInterval(fn, ms)) }
    void (async () => {
      // 登录之后第一件事就是这一声 —— 排在拉通知、拉网关状态之前,别让它跟在一串接口后面。
      void chimeOnLogin()
      if (useAuthStore.getState().menus.approve) {
        try {
          // The listing is paged, so the badge uses the server-side count rather than
          // counting a page — otherwise it would silently cap at the page size.
          const res = await api.approvals('all', 1, 1)
          useAuthStore.getState().setPendingCount(res.pending)
        } catch { /* ignore */ }
      }
      if (gone) return
      await fetchNotifs()
      every(fetchNotifs, 20000)
      if (gone) return
      if (canTodo) {
        await pollTodo()
        every(pollTodo, 20000)
      }
      if (gone) return
      await fetchGwStats()
      every(fetchGwStats, 5000)
      if (gone) return

      // idle auto-lock: 读会话策略。从前读的是 GET /settings,而它只给管理员 —— 非管理员一律按默认
      // 15 分钟锁定,设置里改的时长对他们不生效。会话策略人人可读,分钟数已按会话时长封顶(L6)。
      try {
        const p = await api.sessionPolicy()
        if (gone) return
        lockRef.current.enabled = p.idleLock
        lockRef.current.ms = (p.idleMinutes || 15) * 60 * 1000
        idle.setIdleMs((p.adminIdleMinutes || 5) * 60 * 1000)
      } catch { lockRef.current.enabled = true }
      if (gone) return
      IDLE_EVENTS.forEach((e) => window.addEventListener(e, kickIdle, { passive: true }))
      kickIdle()
    })()
    return () => {
      gone = true
      timers.forEach(clearInterval)
      titleCount.value = 0
      teardownIdle()
    }
    // 布局挂载一次、卸载一次
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const gw = (
    <>
      <ActivityIcon size={14} color={gwOnline ? '#2dcde6' : 'var(--danger-text)'} />
      {' '}{gwOnline ? t('gwOnline') : t('gwOffline')}{gwOnline && gwP50 !== null && <span> · p50 {gwP50}ms</span>}
    </>
  )
  const themeLabel = <>{isDark ? <Sun size={14} /> : <Moon size={14} />}{isDark ? t('themeLight') : t('themeDark')}</>
  const portalLabel = <><ArrowLeftRight size={14} />{inAdmin ? t('userPortal') : t('adminPortal')}</>

  return (
    <>
      <div className={clsx('shell', inAdmin && 'is-admin')} inert={idle.locked}>
        {/* 水印铺在整个登录后的壳层上,包括弹窗 —— 它是给截图看的,见 Watermark.tsx。 */}
        <Watermark />
        {/* LEFT RAIL */}
        <aside className="rail">
          <div className={clsx('logo', brand.logo && 'custom')} title={brandText(lang, undefined, brand)} data-testid="rail-logo"><BrandMark size={20} /></div>
          <nav className="rail-nav">
            {visibleNav.map((n) => (
              <div key={n.key} className={clsx('rail-item', activeKey === n.key && 'active')} title={t(navLabel(n))} onClick={() => go(n.path)}>
                <n.icon size={21} />
                <span className="rl">{t(navLabel(n))}</span>
                {n.badge && !!auth.pendingCount && <span className="dot">{auth.pendingCount}</span>}
              </div>
            ))}
          </nav>
          <div className="rail-bottom">
            <div className="avatar" title={t('accountMenu')} onClick={toggleUserMenu}>{auth.me?.initials || 'LW'}</div>
            {userMenuOpen && (
              <>
                <div className="um-mask" onClick={() => setUserMenuOpen(false)} />
                <div className="usermenu">
                  <div className="um-head">
                    <div className="um-ava">{auth.me?.initials || 'LW'}</div>
                    <div className="um-id">
                      <div className="um-name">{auth.me?.name || '—'}</div>
                      <div className="um-mail">{auth.me?.email}</div>
                    </div>
                  </div>
                  <div className="um-role">{builtinLabel(BUILTIN_ROLE_NAME, auth.me?.roleName || '', t)}{auth.me?.layer && <span> · {builtinLabel(BUILTIN_ROLE_LAYER, auth.me.layer, t)}</span>}</div>
                  <div className="um-mfa" onClick={() => { setUserMenuOpen(false); setMfaOpen(true) }}>
                    <ShieldCheck size={15} />{t('mfaSelfTitle')}
                    <span className={clsx('um-tag', mfaBound && 'on')}>{mfaBound ? t('otpBound') : t('otpUnboundState')}</span>
                  </div>
                  <div className="um-mfa" data-testid="menu-password" onClick={() => { setUserMenuOpen(false); setPwOpen(true) }}>
                    <LockKeyhole size={15} />{t('pwTitle')}
                  </div>
                  {!!chatBindings.length && (
                    <div className="um-mfa" data-testid="menu-chat" onClick={() => { setUserMenuOpen(false); setChatOpen(true) }}>
                      <Bot size={15} />{t('chatBindTitle')}
                      <span className={clsx('um-tag', chatBindings.some((b) => b.bound) && 'on')}>{chatBindings.some((b) => b.bound) ? t('otpBound') : t('otpUnboundState')}</span>
                    </div>
                  )}
                  <div className="um-logout" onClick={logout}><LogOut size={15} />{t('logout')}</div>
                </div>
              </>
            )}
          </div>
        </aside>

        {/* MAIN */}
        <div className="main">
          <header className="topbar">
            {inAdmin && <span className="portal-tag" data-testid="admin-portal-tag"><ShieldHalf size={13} />{t('adminPortal')}</span>}
            <div className="tb-title">{t(`t_${titleKey}`)}</div>
            <div className="tb-sub">{pageSub}</div>
            <div className="tb-right">
              {auth.canEnterAdmin && <button className="pill portal-switch tb-sec" data-testid="portal-switch" onClick={switchPortal}>{portalLabel}</button>}
              <div className={clsx('pill tb-sec', !gwOnline && 'off')}>{gw}</div>
              {canTodo && (
                <div className="todo">
                  <button
                    className={clsx('pill todo-btn', auth.todoTotal > 0 && 'hot', todoOpen && 'active')}
                    data-testid="todo-pill" title={t('todoTip')} onClick={toggleTodo}
                  >
                    {auth.todoTotal ? <BellRing size={14} /> : <ClipboardCheck size={14} />}
                    <span>{auth.todoTotal ? t('todoTitle') : t('todoNone')}</span>
                    {!!auth.todoTotal && <b className="num">{auth.todoTotal > 99 ? '99+' : auth.todoTotal}</b>}
                  </button>
                  {todoOpen && (
                    <>
                      <div className="backdrop" onClick={() => setTodoOpen(false)} />
                      <div className="panel todo-panel" data-testid="todo-panel">
                        <div className="phead">
                          <span>
                            {t('todoTitle')}{' '}
                            <span className="pcount">
                              {t('todoDecide')} {auth.todo.decide} · {t('todoExecute')} {auth.todo.execute} · {t('todoConfirm')} {auth.todo.confirm}
                            </span>
                          </span>
                          <span className="bellbtn" title={muted ? t('todoChimeOn') : t('todoChimeOff')} onClick={(e) => { e.stopPropagation(); toggleChime() }}>
                            {muted ? <BellOff size={14} /> : <Bell size={14} />}
                          </span>
                          {auth.menus.approve && <span className="link" onClick={() => { setTodoOpen(false); go('/approvals') }}>{t('todoViewAll')}</span>}
                        </div>
                        <div className="plist">
                          {!auth.todo.items.length && <div className="pempty">{t('todoEmpty')}</div>}
                          {auth.todo.items.map((it) => {
                            const Ic = todoIcon(it.kind)
                            return (
                              <div key={it.kind + it.id} className="pitem titem" onClick={() => openTodoItem(it)}>
                                <Ic size={16} className={clsx('pic', TODO_CLS[it.kind])} />
                                <div className="pbody">
                                  <div className="pt">
                                    <span className={clsx('tkind', it.kind)}>{t(todoLabel(it.kind))}</span>
                                    <span className="pref">{it.apNo}</span>
                                  </div>
                                  <div className="pd">{it.initiator} · {it.instance}{it.database && <span> / {it.database}</span>}</div>
                                  {/* 停在哪一道闸上:阶段名由类型派生,按读者的语言渲染(见 plType_*) */}
                                  <div className="pd tcmd">
                                    {it.stage && <span className="tstage">{t('plType_' + it.stage)}</span>}{apCmd(it)}
                                  </div>
                                </div>
                                <ChevronRight size={14} className="tgo" />
                              </div>
                            )
                          })}
                        </div>
                      </div>
                    </>
                  )}
                </div>
              )}
              <div className="pill click tb-sec" title={t('langSwitch')} onClick={toggleLang}><Languages size={14} />{t('langLabel')}</div>
              <div className="pill click tb-sec" title={t('themeToggle')} onClick={toggleTheme}>{themeLabel}</div>
              <div className="tb-more">
                <button className={clsx('bell', moreOpen && 'active')} title={t('tbMore')} data-testid="topbar-more" onClick={() => setMoreOpen(!moreOpen)}>
                  <Ellipsis size={18} />
                </button>
                {moreOpen && (
                  <>
                    <div className="backdrop" onClick={() => setMoreOpen(false)} />
                    <div className="panel more-panel" data-testid="topbar-more-panel">
                      <div className={clsx('mitem', !gwOnline && 'off')}>{gw}</div>
                      {auth.canEnterAdmin && <div className="mitem click" onClick={() => { setMoreOpen(false); switchPortal() }}>{portalLabel}</div>}
                      <div className="mitem click" onClick={toggleLang}><Languages size={14} />{t('langLabel')}</div>
                      <div className="mitem click" onClick={toggleTheme}>{themeLabel}</div>
                    </div>
                  </>
                )}
              </div>
              <div className="notif">
                <button className={clsx('bell', notifOpen && 'active')} title={t('notifTitle')} onClick={toggleNotif}>
                  <Bell size={18} />
                  {!!notifUnread && <span className="nbadge">{notifUnread > 99 ? '99+' : notifUnread}</span>}
                </button>
                {notifOpen && (
                  <>
                    <div className="backdrop" onClick={() => setNotifOpen(false)} />
                    <div className="panel">
                      <div className="phead">
                        <span>{t('notifTitle')}</span>
                        <span className="link" onClick={openApprovals}>{t('notifViewAppr')}</span>
                      </div>
                      <div className="plist">
                        {!notifs.length && <div className="pempty">{t('notifEmpty')}</div>}
                        {notifs.map((n) => {
                          const Ic = notifIcon(n.type)
                          return (
                            <div key={n.id} className={clsx('pitem', !n.read && 'unread')}>
                              <Ic size={16} className={clsx('pic', notifCls(n.type))} />
                              <div className="pbody">
                                <div className="pt">{localizeText(n.title, n.titleRef)}{n.refNo && <span className="pref">{n.refNo}</span>}</div>
                                <div className="pd">{localizeText(n.body, n.bodyRef)}</div>
                              </div>
                            </div>
                          )
                        })}
                      </div>
                    </div>
                  </>
                )}
              </div>
            </div>
          </header>
          <main className="content">
            {/* keep the terminal (its tabs, sessions, sockets) alive across menu switches */}
            <KeepAlive active={route.name === 'terminal'}>
              <Suspense fallback={null}><TerminalView /></Suspense>
            </KeepAlive>
            {/* key 按路由名:审计、班车这类视图两个后台共用一个组件(/audit 与 /admin/audit),
                不带 key 时 React 复用同一个实例,切过去还是上一个后台的数据(2026-10 审查)。只换 query
                (系统设置的 ?tab=)不重建。 */}
            <Fragment key={route.name}><Outlet /></Fragment>
          </main>
        </div>
      </div>
      {idle.locked && createPortal(
        <div className="admin-lock" role="dialog" aria-modal="true" data-testid="admin-idle-lock">
          <div className="admin-lock-card">
            <div className="admin-lock-ic"><LockKeyhole size={20} /></div>
            <div className="admin-lock-t">{t('adminLockTitle')}</div>
            <div className="admin-lock-d">{t('adminLockBody')}</div>
            {idle.lockMsg && <div className="admin-lock-err" data-testid="admin-idle-lock-err">{idle.lockMsg}</div>}
            <div className="admin-lock-acts">
              <button className="admin-lock-btn ghost" data-testid="admin-idle-lock-leave" onClick={idle.leave}>{t('adminLockLeave')}</button>
              <button className="admin-lock-btn primary" disabled={idle.unlocking} data-testid="admin-idle-lock-resume" onClick={idle.unlock}>{t('adminLockResume')}</button>
            </div>
          </div>
        </div>,
        document.body,
      )}
      <MfaModal open={mfaOpen} enabled={mfaBound} onClose={() => setMfaOpen(false)} onChanged={onMfaChanged} />
      <PasswordModal open={pwOpen} onClose={() => setPwOpen(false)} />
      <ChatBindModal open={chatOpen} bindings={chatBindings} reload={loadChat} onUnbound={onChatUnbound} onClose={() => setChatOpen(false)} />
    </>
  )
}
