<script setup lang="ts">
import Watermark from '@/components/Watermark.vue'
import BrandMark from '@/components/BrandMark.vue'
import { brand, brandText, titleCount } from '@/lib/brand'
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { renderApText } from '@/lib/ruleText'
import { FolderKanban, ShieldHalf, ArrowLeftRight,
  LayoutDashboard, SquareTerminal, ClipboardCheck, Database, ShieldAlert, Layers, UsersRound, Rocket, GitBranch, SpellCheck,
  ScrollText, Settings, Activity, Languages, Bell, BellOff, BellRing, Play, ChevronRight, Hourglass,
  CircleCheck, CircleX, Clock, DatabaseZap, Upload, LogOut, Sun, Moon, ShieldCheck, SearchCode, GitCompare, Stethoscope,
  LockKeyhole, Ellipsis,
} from 'lucide-vue-next'
import { useAuthStore } from '@/stores/auth'
import { useUIStore } from '@/stores/ui'
import { playTodoChime, chimeMuted, setChimeMuted } from '@/lib/chime'
// 「哪几张该响」是一条容易在重构里悄悄丢掉的规则(别人提的单不响),所以它是一个被单测
// 钉住的纯函数,不写在这个组件里 —— 见 lib/todoChime.ts。
import { countMyExecuteTodos } from '@/lib/todoChime'
import { BUILTIN_ROLE_NAME, BUILTIN_ROLE_LAYER, builtinLabel } from '@/lib/builtinNames'
import api from '@/api'
import { localizeText } from '@/api/http'
import { CODE_OK, CODE_ADMIN_IP_BLOCKED } from '@/api/codes'
import { hasUnsavedChanges, clearUnsaved } from '@/lib/unsavedChanges'
import MfaModal from '@/components/modals/MfaModal.vue'
import PasswordModal from '@/components/modals/PasswordModal.vue'
import type { ApprovalTodoItem, Notification, RuleRef } from '@/types'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const ui = useUIStore()
const { t, te } = useI18n()
// 描述类单子(执行窗口)按读者语言显示;SQL 单子没有 commandRef,原样显示 command(lib/ruleText)。
const apCmd = (a: { command: string; commandRef?: RuleRef }) => renderApText(a.commandRef, a.command, { t: t as never, te: te as never })

const navItems = [
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
const adminNavItems = [
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
const inAdmin = computed(() => route.meta.portal === 'admin' || route.path.startsWith('/admin'))

// 只开了数据查询页的业务方:总览对他没有内容(路由也会把他送回 /query),总览这类常驻项藏起来;
// 「我的操作记录」留着 —— 他查过什么,自己也该看得到(2026-10-14 用户确认)。
const visibleNav = computed(() => inAdmin.value
  ? adminNavItems.filter((n) => (n.admin ? auth.isAdmin : auth.isAdmin || auth.isAuditor) && ((n as any).always || auth.menus[(n as any).gate ?? n.key]))
  : navItems.filter((n) => ((n as any).always && (!auth.onlyQuery || n.key === 'mylog')) || auth.menus[(n as any).gate ?? n.key]))
// 能看全量审计的人(管理员 / 审计员 / DBA 负责人)在用户后台的记录页默认看所有人的(见 AuditView 的 team;
// 2026-10-26 回归排查 09:管理员 / 审计员从前在这里只看自己的)
const teamLog = computed(() => auth.seesAllAudit)
function navLabel(n: { key: string; label: string }) {
  return n.key === 'mylog' && teamLog.value ? 'navTeamLog' : n.label
}
// 切换前后台:到管理后台用 /admin(按身份落到能进的第一页),回用户后台落总览。
function switchPortal() { go(inAdmin.value ? '/dashboard' : '/admin') }
// 同一件事几秒内只提示一次:一个页面同时发出好几个管理接口请求(管理总览一次取五六样),每个都回
// 40303 / 40304,从前就弹五六个一模一样的提示(2026-10 审查)。
const lastAdminNotice: Record<string, number> = {}
function adminNoticeOnce(key: string, msg: string, kind: 'error' | 'info') {
  const now = Date.now()
  if (now - (lastAdminNotice[key] || 0) < 5000) return
  lastAdminNotice[key] = now
  ui.notify(msg, kind, 6000)
}
// 管理接口说"你的 IP 不在管理后台白名单内"(工单 08,api/http.ts 发出这个事件):提示,并退回用户后台。
function onAdminIpBlocked(e: Event) {
  adminNoticeOnce('ip', (e as CustomEvent<string>).detail || t('adminIpBlocked'), 'error')
  if (inAdmin.value) go('/dashboard')
}
window.addEventListener('vela:admin-ip-blocked', onAdminIpBlocked)

// ---- 管理后台的独立空闲超时(工单 09)----
// 停在管理后台无操作满 adminIdleMinutes(会话策略里读,默认 5)就退回用户后台,不登出 —— 终端标签与
// 事务都不受影响。人有操作时报心跳(节流),后端据此判断"这个管理会话还活着";后端那边超时了
// (页面被挂起、前端计时失效)会回 40304,同样退回。
//
// 心跳节流不能让后端看到的"最后一次操作"落后太多(2026-10 审查):从前每分钟最多一次、且节流掉的
// 操作不补报 —— 最后一分钟里的操作后端看不见,人明明 5 分钟内还在点,接口却回 40304;空闲时长设成
// 1 分钟时更是隔一会儿就被踢。现在间隔取 min(30 秒, 空闲时长的 1/4),被节流的那次在间隔到了补一拍
// (后端另留 30 秒余量)。滚动不冒泡,要在捕获阶段听,面板里的滚动才算;鼠标移动也算操作。
let adminIdleMs = 5 * 60 * 1000
let adminTimer: ReturnType<typeof setTimeout> | null = null
let lastBeat = 0
let lastAdminKick = 0
let trailingBeat: ReturnType<typeof setTimeout> | null = null
function adminIdleOut() {
  if (!inAdmin.value) return
  if (hasUnsavedChanges()) { lockAdmin(); return }
  adminNoticeOnce('idle', t('adminIdleBack'), 'info')
  go('/dashboard')
}
// 空闲到点时当前页有没保存的改动(2026-10-26 回归排查 10):不跳走 —— 跳走页面就销毁了,改到一半的
// 配置 / 权限跟着丢。改为遮罩锁住当前页(不可操作),重新进入管理后台(POST /admin/enter,和从用户
// 后台走进来是同一道门:角色 + 管理后台 IP 白名单)之后原地解锁,内容还在。没有改动的照旧弹回总览。
const adminLocked = ref(false)
const adminUnlocking = ref(false)
const adminLockMsg = ref('')
function lockAdmin() {
  if (adminLocked.value) return
  adminLocked.value = true
  adminLockMsg.value = ''
  if (adminTimer) clearTimeout(adminTimer)
  adminTimer = null
  if (trailingBeat) clearTimeout(trailingBeat)
  trailingBeat = null
  // 焦点留在表单里的话,键盘还能接着往被锁住的输入框里打字
  ;(document.activeElement as HTMLElement | null)?.blur?.()
}
async function unlockAdmin() {
  if (adminUnlocking.value) return
  adminUnlocking.value = true
  adminLockMsg.value = ''
  try {
    const res = await api.adminEnter()
    if (res.code === CODE_OK) {
      adminLocked.value = false
      lastBeat = Date.now()
      armAdminTimer()
    } else {
      adminLockMsg.value = res.code === CODE_ADMIN_IP_BLOCKED ? t('adminIpBlockedStay') : (res.msg || t('actionFailed'))
    }
  } catch {
    adminLockMsg.value = t('actionFailed')
  } finally {
    adminUnlocking.value = false
  }
}
// 锁屏里选"放弃修改":清掉登记,照旧回总览
function leaveAdminLocked() {
  clearUnsaved()
  adminLocked.value = false
  adminNoticeOnce('idle', t('adminIdleBack'), 'info')
  go('/dashboard')
}
function armAdminTimer() {
  if (adminTimer) clearTimeout(adminTimer)
  adminTimer = inAdmin.value ? setTimeout(adminIdleOut, adminIdleMs) : null
}
function sendBeat() {
  trailingBeat = null
  if (!inAdmin.value) return
  lastBeat = Date.now()
  api.adminHeartbeat().catch(() => {})
}
function kickAdmin() {
  if (!inAdmin.value || adminLocked.value) return // 锁着时的鼠标移动不算操作,也不报心跳
  const now = Date.now()
  if (now - lastAdminKick < 1000) return // mousemove 一秒能来几十次,一秒算一次足够
  lastAdminKick = now
  armAdminTimer()
  const wait = lastBeat + Math.min(30_000, adminIdleMs / 4) - now
  if (wait <= 0) sendBeat()
  else if (!trailingBeat) trailingBeat = setTimeout(sendBeat, wait) // 节流掉的这次到点补报
}
// 刚进管理后台时路由已经调过 enter,心跳从此刻算起
watch(inAdmin, (on) => { if (on) lastBeat = Date.now(); else adminLocked.value = false; armAdminTimer() }, { immediate: true })
const ADMIN_EVENTS = ['mousedown', 'mousemove', 'wheel', 'keydown', 'scroll', 'touchstart'] as const
ADMIN_EVENTS.forEach((e) => window.addEventListener(e, kickAdmin, { passive: true, capture: true }))
function onAdminIdle(e: Event) {
  if (!inAdmin.value) return
  // 后端先判了超时(页面被挂起、前端计时失效):有没保存的改动同样锁屏,不跳走
  if (adminLocked.value || hasUnsavedChanges()) { lockAdmin(); return }
  adminNoticeOnce('idle', (e as CustomEvent<string>).detail || t('adminIdleBack'), 'info')
  go('/dashboard')
}
window.addEventListener('vela:admin-idle', onAdminIdle)
const activeKey = computed(() => (route.meta.menuKey as string) || '')
// Titles are keyed off the MENU key, not the route name. The two differ for
// several routes (connections→db, approvals→approve, risk-rules→rules,
// permissions→perms), and vue-i18n renders a missing key as the key itself — so
// those four pages showed a raw "t_connections" where their title belongs.
const titleKey = computed(() => {
  const k = (route.meta.menuKey as string) || (route.name as string) || ''
  return k === 'mylog' && teamLog.value ? 'teamlog' : k
})
const pageTitle = computed(() => t(`t_${titleKey.value}` as any))
// Views may publish a data-driven subtitle via ui.pageSub; otherwise fall back to i18n default.
// 在这里渲染,而不是在各个视图里:t() 在 computed 里跟着 locale 走,所以切语言
// 这行字会跟着变。视图设的是 key + 参数(见 ui store 里 PageSub 的注释)。
const pageSub = computed(() => {
  const s = ui.pageSub
  if (!s) return t(`t_${titleKey.value}Sub` as any)
  return typeof s === 'string' ? s : t(s.key as any, (s.params || {}) as any)
})
const isDark = computed(() => ui.resolvedTheme() === 'dark')

// Reset the override on navigation so each view starts from its default.
watch(() => route.name, () => { ui.pageSub = '' })

function go(path: string) { router.push(path) }

// ---- notifications ----
const notifs = ref<Notification[]>([])
const notifUnread = ref(0)

// ---- live gateway status (real latency, not hard-coded) ----
const gwOnline = ref(true)
const gwP50 = ref<number | null>(null)
let gwTimer: ReturnType<typeof setInterval> | null = null
async function fetchGwStats() {
  try {
    const s = await api.gatewayStats()
    gwOnline.value = s.online
    gwP50.value = s.p50Ms
  } catch { gwOnline.value = false }
}
const notifOpen = ref(false)
let notifTimer: ReturnType<typeof setInterval> | null = null

const okTypes = ['approval-approved', 'export-done', 'release-done']
const badTypes = ['approval-rejected', 'export-failed', 'release-failed', 'exec-failed']
const notifIcon = (t: string) =>
  okTypes.includes(t) ? CircleCheck : badTypes.includes(t) ? CircleX : Clock
const notifCls = (t: string) =>
  okTypes.includes(t) ? 'ok' : badTypes.includes(t) ? 'bad' : 'warn'

// 已经弹过的通知 id。
//
// **首次拉取只记账、不弹窗**:否则每次刷新页面都会把历史通知重演一遍 —— 三天前
// 那次导出完成的提示,今天早上再弹一次,人很快就学会了无视所有弹窗。这里要的是
// "刚刚完成了",不是"曾经完成过"。
const toasted = new Set<number>()
let notifPrimed = false

// 审批出结果、异步导出跑完,人早就离开那个页面了 —— 不主动告诉他,他只能自己
// 想起来回去刷一下。铃铛上的红点不算"告诉":它要人先看见、再点开。
function toastNew(items: Notification[]) {
  for (const n of items) {
    if (toasted.has(n.id)) continue
    toasted.add(n.id)
    if (!notifPrimed || n.read) continue
    const title = localizeText(n.title, n.titleRef)
    const body = n.body ? `${title} — ${localizeText(n.body, n.bodyRef)}` : title
    ui.notify(body, badTypes.includes(n.type) ? 'error' : okTypes.includes(n.type) ? 'success' : 'info')
  }
}

async function fetchNotifs() {
  try {
    const r = await api.notifications()
    notifs.value = r.items
    notifUnread.value = r.unread
    toastNew(r.items)
    notifPrimed = true // 第一轮只是记下已有的,从第二轮起才算"新到的"
  } catch { /* ignore */ }
}

async function toggleNotif() {
  notifOpen.value = !notifOpen.value
  if (notifOpen.value) {
    await fetchNotifs()
    if (notifUnread.value > 0) {
      try { await api.markNotificationsRead() } catch { /* ignore */ }
      notifUnread.value = 0
      notifs.value = notifs.value.map((n) => ({ ...n, read: true }))
    }
  }
}

// ---- second-factor enrolment (available to every account) ----
const mfaOpen = ref(false)
const mfaBound = computed(() => !!auth.me?.mfaEnabled)
function openMfa() {
  userMenuOpen.value = false
  mfaOpen.value = true
}
async function onMfaChanged() {
  mfaOpen.value = false
  await auth.fetchMe() // refresh the bound state shown in the menu
}

// ---- 自助修改密码(黑盒 2026-10 工单 05)----
const pwOpen = ref(false)
function openPassword() {
  userMenuOpen.value = false
  pwOpen.value = true
}

// 窄屏(手机宽度)下顶栏放不下的次要按钮收进这个「更多」菜单:语言、深色、前后台切换、网关状态。
// 宽屏时它不显示(CSS 控制),按钮照常摆在顶栏上。
const moreOpen = ref(false)

function openApprovals() {
  notifOpen.value = false
  if (auth.menus.approve) router.push('/approvals')
}

// 有待办可言的三种菜单:审批(等我审批)、终端(等我执行)、发布(等我确认)。
// 一个都没有的人不显示这枚徽章 —— 他没有任何能处理的动作。
const canTodo = computed(() => !!(auth.menus.approve || auth.menus.terminal || auth.menus.pipeline))

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
const TODO_ICON: Record<string, any> = { decide: CircleCheck, execute: Play, confirm: Hourglass }
const TODO_CLS: Record<string, string> = { decide: 'warn', execute: 'ok', confirm: 'info' }
const TODO_LABEL: Record<string, string> = {
  decide: 'todoKindDecide', execute: 'todoKindExecute', confirm: 'todoKindConfirm',
}
// 认不出的 kind 照常画出来,只是不带颜色与说法。会出现这种情况的是版本错位:页面开着
// 没刷新,服务端已经升到了认识第四类的版本 —— 那一行仍然要能点开,而不是一个空白格
// 加一串控制台告警。
const todoIcon = (k: string) => TODO_ICON[k] || Clock
const todoLabel = (k: string) => TODO_LABEL[k] || 'todoTitle'
const todoOpen = ref(false)
let todoTimer: ReturnType<typeof setInterval> | null = null
let todoPrimed = false
async function pollTodo() {
  const before = auth.todoTotal
  await auth.refreshTodo()
  const after = auth.todoTotal
  // 首轮只记账:刷新页面不该把已有的待办再"新到"一遍。
  if (todoPrimed && after > before) ui.notify(t('todoNew', { n: after - before }), 'info', 6000)
  todoPrimed = true
}

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
  if (!auth.consumeJustLoggedIn()) return
  await auth.refreshTodo()
  const mine = countMyExecuteTodos(auth.todo.items, auth.me)
  if (mine <= 0) return
  playTodoChime(CHIME_TIMES)
  ui.notify(t('todoChimeToast', { n: mine }), 'info', 8000)
}
// 连响三下。一下容易被当成别的软件的提示音错过,三下是"有人在叫你"。
const CHIME_TIMES = 3


// 提示音的开关。存在这台浏览器上(localStorage),不是账号设置 —— 吵不吵是"我这台机器、
// 这个工位"的事,换台机器该重新决定。一个关不掉的提示音,第二天会被整个标签页静音掉。
const muted = ref(chimeMuted())
function toggleChime() {
  muted.value = !muted.value
  setChimeMuted(muted.value)
  if (!muted.value) playTodoChime() // 打开时响一声,让人知道它是什么动静
}
async function toggleTodo() {
  todoOpen.value = !todoOpen.value
  if (todoOpen.value) await auth.refreshTodo()
}
function openTodoItem(it: ApprovalTodoItem) {
  todoOpen.value = false
  // 待确认的是一张**发布单**,不是工单 —— 它的 id 是发布单 id,去发布页按 id 深链。
  if (it.kind === 'confirm') { router.push({ path: '/releases', query: { rel: String(it.id) } }); return }
  // 审批页按单号深链直接打开详情卡;没有审批菜单的人(只有终端)在总览页执行。
  if (auth.menus.approve) router.push({ path: '/approvals', query: { ap: it.apNo } })
  else router.push('/dashboard')
}
function openApprovalsFromTodo() {
  todoOpen.value = false
  router.push('/approvals')
}
// 标签页标题带上数量:人切去别的标签页干活时,"(2) DP DB GATEWAY" 是唯一还能看见的提示。
// 标题本身由 App.vue 写(品牌名 + 这个数),见 lib/brand.ts。
watch(() => auth.todoTotal, (n) => { titleCount.value = n }, { immediate: true })

onMounted(async () => {
  // 登录之后第一件事就是这一声 —— 排在拉通知、拉网关状态之前,别让它跟在一串接口后面。
  void chimeOnLogin()
  if (auth.menus.approve) {
    try {
      // The listing is paged, so the badge uses the server-side count rather than
      // counting a page — otherwise it would silently cap at the page size.
      const res = await api.approvals('all', 1, 1)
      auth.pendingCount = res.pending
    } catch { /* ignore */ }
  }
  await fetchNotifs()
  notifTimer = setInterval(fetchNotifs, 20000)
  if (canTodo.value) {
    await pollTodo()
    todoTimer = setInterval(pollTodo, 20000)
  }
  await fetchGwStats()
  gwTimer = setInterval(fetchGwStats, 5000)

  // idle auto-lock: 读会话策略。从前读的是 GET /settings,而它只给管理员 —— 非管理员一律按默认
  // 15 分钟锁定,设置里改的时长对他们不生效。会话策略人人可读,分钟数已按会话时长封顶(L6)。
  try {
    const p = await api.sessionPolicy()
    idleEnabled = p.idleLock
    idleMs = (p.idleMinutes || 15) * 60 * 1000
    adminIdleMs = (p.adminIdleMinutes || 5) * 60 * 1000
    armAdminTimer()
  } catch { idleEnabled = true }
  IDLE_EVENTS.forEach((e) => window.addEventListener(e, kickIdle, { passive: true }))
  kickIdle()
})

// ---- idle auto-lock (Session & Security · security.idleLock) ----
// Effective idle timeout (ms); overwritten from settings on mount (L6).
let idleMs = 15 * 60 * 1000
const IDLE_EVENTS = ['mousemove', 'mousedown', 'keydown', 'scroll', 'touchstart'] as const
let idleEnabled = true
let idleTimer: ReturnType<typeof setTimeout> | null = null
let lastKick = 0

function lockSession() {
  teardownIdle()
  auth.logout()
  router.push({ path: '/login', query: { locked: '1' } })
}

// ---- user menu / manual logout ----
const userMenuOpen = ref(false)
function logout() {
  userMenuOpen.value = false
  teardownIdle()
  auth.logout()
  router.push({ name: 'login' })
}
function kickIdle() {
  const now = Date.now()
  if (now - lastKick < 5000) return // throttle: reset at most every 5s
  lastKick = now
  if (idleTimer) clearTimeout(idleTimer)
  if (idleEnabled) idleTimer = setTimeout(lockSession, idleMs)
}
function teardownIdle() {
  if (idleTimer) clearTimeout(idleTimer)
  idleTimer = null
  IDLE_EVENTS.forEach((e) => window.removeEventListener(e, kickIdle))
}

onUnmounted(() => {
  window.removeEventListener('vela:admin-ip-blocked', onAdminIpBlocked)
  window.removeEventListener('vela:admin-idle', onAdminIdle)
  ADMIN_EVENTS.forEach((e) => window.removeEventListener(e, kickAdmin, { capture: true }))
  if (adminTimer) clearTimeout(adminTimer)
  if (trailingBeat) clearTimeout(trailingBeat)
  if (notifTimer) clearInterval(notifTimer)
  if (gwTimer) clearInterval(gwTimer)
  if (todoTimer) clearInterval(todoTimer)
  titleCount.value = 0
  teardownIdle()
})
</script>

<template>
  <div class="shell" :class="{ 'is-admin': inAdmin }" :inert="adminLocked && inAdmin">
    <!-- 水印铺在整个登录后的壳层上,包括弹窗 —— 它是给截图看的,见 Watermark.vue。 -->
    <Watermark />
    <!-- LEFT RAIL -->
    <aside class="rail">
      <div class="logo" :class="{ custom: !!brand.logo }" :title="brandText(ui.lang)" data-testid="rail-logo"><BrandMark :size="20" /></div>
      <nav class="rail-nav">
        <div
          v-for="n in visibleNav"
          :key="n.key"
          class="rail-item"
          :class="{ active: activeKey === n.key }"
          :title="$t(navLabel(n) as any)"
          @click="go(n.path)"
        >
          <component :is="n.icon" :size="21" />
          <span class="rl">{{ $t(navLabel(n) as any) }}</span>
          <span v-if="(n as any).badge && auth.pendingCount" class="dot">{{ auth.pendingCount }}</span>
        </div>
      </nav>
      <div class="rail-bottom">
        <div class="avatar" :title="$t('accountMenu')" @click="userMenuOpen = !userMenuOpen">{{ auth.me?.initials || 'LW' }}</div>
        <template v-if="userMenuOpen">
          <div class="um-mask" @click="userMenuOpen = false" />
          <div class="usermenu">
            <div class="um-head">
              <div class="um-ava">{{ auth.me?.initials || 'LW' }}</div>
              <div class="um-id">
                <div class="um-name">{{ auth.me?.name || '—' }}</div>
                <div class="um-mail">{{ auth.me?.email }}</div>
              </div>
            </div>
            <div class="um-role">{{ builtinLabel(BUILTIN_ROLE_NAME, auth.me?.roleName || '', t) }}<span v-if="auth.me?.layer"> · {{ builtinLabel(BUILTIN_ROLE_LAYER, auth.me.layer, t) }}</span></div>
            <div class="um-mfa" @click="openMfa">
              <ShieldCheck :size="15" />{{ $t('mfaSelfTitle') }}
              <span class="um-tag" :class="{ on: mfaBound }">{{ mfaBound ? $t('otpBound') : $t('otpUnboundState') }}</span>
            </div>
            <div class="um-mfa" data-testid="menu-password" @click="openPassword">
              <LockKeyhole :size="15" />{{ $t('pwTitle') }}
            </div>
            <div class="um-logout" @click="logout"><LogOut :size="15" />{{ $t('logout') }}</div>
          </div>
        </template>
      </div>
    </aside>

    <!-- MAIN -->
    <div class="main">
      <header class="topbar">
        <span v-if="inAdmin" class="portal-tag" data-testid="admin-portal-tag"><ShieldHalf :size="13" />{{ $t('adminPortal') }}</span>
        <div class="tb-title">{{ pageTitle }}</div>
        <div class="tb-sub">{{ pageSub }}</div>
        <div class="tb-right">
          <button v-if="auth.canEnterAdmin" class="pill portal-switch tb-sec" data-testid="portal-switch" @click="switchPortal">
            <ArrowLeftRight :size="14" />{{ inAdmin ? $t('userPortal') : $t('adminPortal') }}
          </button>
          <div class="pill tb-sec" :class="{ off: !gwOnline }">
            <Activity :size="14" :color="gwOnline ? '#2dcde6' : 'var(--danger-text)'" />
            {{ gwOnline ? $t('gwOnline') : $t('gwOffline') }}<span v-if="gwOnline && gwP50 !== null"> · p50 {{ gwP50 }}ms</span>
          </div>
          <div v-if="canTodo" class="todo">
            <button
              class="pill todo-btn"
              :class="{ hot: auth.todoTotal > 0, active: todoOpen }"
              data-testid="todo-pill"
              :title="$t('todoTip')"
              @click="toggleTodo"
            >
              <component :is="auth.todoTotal ? BellRing : ClipboardCheck" :size="14" />
              <span>{{ auth.todoTotal ? $t('todoTitle') : $t('todoNone') }}</span>
              <b v-if="auth.todoTotal" class="num">{{ auth.todoTotal > 99 ? '99+' : auth.todoTotal }}</b>
            </button>
            <template v-if="todoOpen">
              <div class="backdrop" @click="todoOpen = false" />
              <div class="panel todo-panel" data-testid="todo-panel">
                <div class="phead">
                  <span>
                    {{ $t('todoTitle') }}
                    <span class="pcount">
                      {{ $t('todoDecide') }} {{ auth.todo.decide }} ·
                      {{ $t('todoExecute') }} {{ auth.todo.execute }} ·
                      {{ $t('todoConfirm') }} {{ auth.todo.confirm }}
                    </span>
                  </span>
                  <span class="bellbtn" :title="muted ? $t('todoChimeOn') : $t('todoChimeOff')" @click.stop="toggleChime">
                    <component :is="muted ? BellOff : Bell" :size="14" />
                  </span>
                  <span v-if="auth.menus.approve" class="link" @click="openApprovalsFromTodo">{{ $t('todoViewAll') }}</span>
                </div>
                <div class="plist">
                  <div v-if="!auth.todo.items.length" class="pempty">{{ $t('todoEmpty') }}</div>
                  <div v-for="it in auth.todo.items" :key="it.kind + it.id" class="pitem titem" @click="openTodoItem(it)">
                    <component :is="todoIcon(it.kind)" :size="16" class="pic" :class="TODO_CLS[it.kind] || ''" />
                    <div class="pbody">
                      <div class="pt">
                        <span class="tkind" :class="it.kind">{{ $t(todoLabel(it.kind) as any) }}</span>
                        <span class="pref">{{ it.apNo }}</span>
                      </div>
                      <div class="pd">{{ it.initiator }} · {{ it.instance }}<span v-if="it.database"> / {{ it.database }}</span></div>
                      <!-- 停在哪一道闸上:阶段名由类型派生,按读者的语言渲染(见 plType_*) -->
                      <div class="pd tcmd">
                        <span v-if="it.stage" class="tstage">{{ $t(('plType_' + it.stage) as any) }}</span>{{ apCmd(it) }}
                      </div>
                    </div>
                    <ChevronRight :size="14" class="tgo" />
                  </div>
                </div>
              </div>
            </template>
          </div>
          <div class="pill click tb-sec" :title="$t('langSwitch')" @click="ui.toggleLang()">
            <Languages :size="14" />{{ $t('langLabel') }}
          </div>
          <div class="pill click tb-sec" :title="$t('themeToggle')" @click="ui.toggleTheme()">
            <component :is="isDark ? Sun : Moon" :size="14" />{{ isDark ? $t('themeLight') : $t('themeDark') }}
          </div>
          <div class="tb-more">
            <button class="bell" :class="{ active: moreOpen }" :title="$t('tbMore')" data-testid="topbar-more" @click="moreOpen = !moreOpen">
              <Ellipsis :size="18" />
            </button>
            <template v-if="moreOpen">
              <div class="backdrop" @click="moreOpen = false" />
              <div class="panel more-panel" data-testid="topbar-more-panel">
                <div class="mitem" :class="{ off: !gwOnline }">
                  <Activity :size="14" :color="gwOnline ? '#2dcde6' : 'var(--danger-text)'" />
                  {{ gwOnline ? $t('gwOnline') : $t('gwOffline') }}<span v-if="gwOnline && gwP50 !== null"> · p50 {{ gwP50 }}ms</span>
                </div>
                <div v-if="auth.canEnterAdmin" class="mitem click" @click="moreOpen = false; switchPortal()">
                  <ArrowLeftRight :size="14" />{{ inAdmin ? $t('userPortal') : $t('adminPortal') }}
                </div>
                <div class="mitem click" @click="ui.toggleLang()"><Languages :size="14" />{{ $t('langLabel') }}</div>
                <div class="mitem click" @click="ui.toggleTheme()">
                  <component :is="isDark ? Sun : Moon" :size="14" />{{ isDark ? $t('themeLight') : $t('themeDark') }}
                </div>
              </div>
            </template>
          </div>
          <div class="notif">
            <button class="bell" :class="{ active: notifOpen }" :title="$t('notifTitle')" @click="toggleNotif">
              <Bell :size="18" />
              <span v-if="notifUnread" class="nbadge">{{ notifUnread > 99 ? '99+' : notifUnread }}</span>
            </button>
            <template v-if="notifOpen">
              <div class="backdrop" @click="notifOpen = false" />
              <div class="panel">
                <div class="phead">
                  <span>{{ $t('notifTitle') }}</span>
                  <span class="link" @click="openApprovals">{{ $t('notifViewAppr') }}</span>
                </div>
                <div class="plist">
                  <div v-if="!notifs.length" class="pempty">{{ $t('notifEmpty') }}</div>
                  <div v-for="n in notifs" :key="n.id" class="pitem" :class="{ unread: !n.read }">
                    <component :is="notifIcon(n.type)" :size="16" class="pic" :class="notifCls(n.type)" />
                    <div class="pbody">
                      <div class="pt">{{ localizeText(n.title, n.titleRef) }}<span v-if="n.refNo" class="pref">{{ n.refNo }}</span></div>
                      <div class="pd">{{ localizeText(n.body, n.bodyRef) }}</div>
                    </div>
                  </div>
                </div>
              </div>
            </template>
          </div>
        </div>
      </header>
      <main class="content">
        <!-- keep the terminal (its tabs, sessions, sockets) alive across menu switches -->
        <!-- component 的 key 按路由名:审计、班车这类视图两个后台共用一个组件(/audit 与 /admin/audit),
             不带 key 时 Vue 复用同一个实例,切过去还是上一个后台的数据(2026-10 审查)。只换 query
             (系统设置的 ?tab=)不重建。注释不能放进 keep-alive 里:开发模式下它算一个子节点。 -->
        <router-view v-slot="{ Component }">
          <keep-alive include="TerminalView">
            <component :is="Component" :key="route.name" />
          </keep-alive>
        </router-view>
      </main>
    </div>
  </div>
  <Teleport to="body">
    <div v-if="adminLocked && inAdmin" class="admin-lock" role="dialog" aria-modal="true" data-testid="admin-idle-lock">
      <div class="admin-lock-card">
        <div class="admin-lock-ic"><LockKeyhole :size="20" /></div>
        <div class="admin-lock-t">{{ $t('adminLockTitle') }}</div>
        <div class="admin-lock-d">{{ $t('adminLockBody') }}</div>
        <div v-if="adminLockMsg" class="admin-lock-err" data-testid="admin-idle-lock-err">{{ adminLockMsg }}</div>
        <div class="admin-lock-acts">
          <button class="admin-lock-btn ghost" data-testid="admin-idle-lock-leave" @click="leaveAdminLocked">{{ $t('adminLockLeave') }}</button>
          <button class="admin-lock-btn primary" :disabled="adminUnlocking" data-testid="admin-idle-lock-resume" @click="unlockAdmin">{{ $t('adminLockResume') }}</button>
        </div>
      </div>
    </div>
  </Teleport>
  <MfaModal :open="mfaOpen" :enabled="mfaBound" @close="mfaOpen = false" @changed="onMfaChanged" />
  <PasswordModal :open="pwOpen" @close="pwOpen = false" />
</template>

<style scoped>
.shell {
  height: 100vh;
  display: flex;
  background: var(--surface-page);
  color: var(--text-body);
  overflow: hidden;
}
/* rail
   84px, not 74: the labels were 9px mono, which is below the size anything is
   readable at — they existed to satisfy the layout, not to be read. At 10.5px
   in the body face they are legible, and the extra 10px is what pays for it.
   The rail also sits on the CARD surface now, so the page's content area is
   the recessed plane and the chrome is the raised one, rather than the other
   way round. */
.rail {
  width: 84px;
  flex-shrink: 0;
  background: var(--surface-card);
  box-shadow: 1px 0 0 var(--border-subtle);
  display: flex;
  flex-direction: column;
  align-items: center;
  padding: 16px 0 14px;
  overflow: hidden;
  transition: width var(--dur-base) var(--ease-out), padding var(--dur-base) var(--ease-out);
}
.logo {
  width: 38px;
  height: 38px;
  border-radius: 11px;
  background: linear-gradient(135deg, var(--azure-500), var(--cyan-400));
  display: flex;
  align-items: center;
  justify-content: center;
  box-shadow: 0 6px 16px -6px rgba(59, 110, 246, 0.7);
}
/* 配置了 LOGO 图片:去掉默认图标的渐变底,图片自己说了算 */
.logo.custom { background: none; box-shadow: none; }
/* The nav scrolls, the logo and the account button do not.
   With 14 destinations the rail is taller than a 900px window: it used to just
   run past the bottom edge, taking the ACCOUNT BUTTON with it — the one control
   that holds logout and MFA enrolment. Scrolling the middle keeps both ends
   reachable at any window height. The scrollbar is hidden because the rail is
   84px wide and a 9px gutter would eat a tenth of it; the list still scrolls by
   wheel, trackpad and keyboard. */
.rail-nav {
  margin-top: 20px;
  display: flex;
  flex: 1 1 auto;
  min-height: 0;
  overflow-y: auto;
  flex-direction: column;
  gap: 2px;
  width: 100%;
  align-items: center;
  scrollbar-width: none;
}
.rail-nav::-webkit-scrollbar { display: none; }
.rail-item {
  position: relative;
  width: 68px;
  height: 54px;
  border-radius: 8px;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 5px;
  cursor: pointer;
  transition: var(--transition-colors);
  color: var(--text-muted);
  border: none;
}
.rail-item:hover { background: var(--surface-sunken); color: var(--text-body); }
.rail-item:active { transform: scale(0.97); transition: transform var(--dur-instant) var(--ease-out); }
.rail-item.active {
  background: var(--accent-subtle);
  color: var(--accent-text);
}
/* The brand gradient finally does a job: it marks WHERE YOU ARE. Until now the
   azure→cyan pair appeared once, on the logo, and never again. */
.rail-item.active::before {
  content: "";
  position: absolute;
  left: -8px;
  top: 15px;
  bottom: 15px;
  width: 2px;
  border-radius: 0 2px 2px 0;
  background: linear-gradient(180deg, var(--accent), var(--glow-accent));
}
.rl { font: 600 10.5px var(--font-body); letter-spacing: 0.01em; }
/* 紧凑宽度下侧栏收成纯图标:84 → 60px。标签隐掉,靠 title 提示。
   省下的 24px 在 1080 这一档正好是"表格要不要横滚"的差别。 */
@media (max-width: 1080px) {
  .rail { width: 60px; padding: 12px 0 10px; }
  .rail-item { width: 44px; height: 44px; gap: 0; }
  .rl { display: none; }
  .usermenu { left: 60px; }
}
.dot {
  position: absolute;
  top: 6px;
  right: 12px;
  min-width: 16px;
  height: 16px;
  padding: 0 4px;
  border-radius: 999px;
  background: var(--danger);
  color: #fff;
  font: 700 9.5px var(--font-mono);
  display: flex;
  align-items: center;
  justify-content: center;
  /* a ring in the rail surface so the badge reads as ON the icon, not beside it */
  box-shadow: 0 0 0 2px var(--surface-card);
}
.rail-bottom {
  margin-top: auto;
  flex-shrink: 0;
  padding-top: 12px;
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 14px;
}
.avatar {
  width: 34px;
  height: 34px;
  border-radius: 50%;
  background: linear-gradient(135deg, #5e83fb, #2dcde6);
  display: flex;
  align-items: center;
  justify-content: center;
  font: 600 12px var(--font-body);
  color: #fff;
  cursor: pointer;
  transition: box-shadow var(--dur-fast) var(--ease-out);
}
.avatar:hover { box-shadow: 0 0 0 3px var(--accent-subtle); }
.um-mask { position: fixed; inset: 0; z-index: 400; }
.um-mfa { display: flex; align-items: center; gap: 8px; padding: 10px 14px; cursor: pointer;
  font: 500 13px var(--font-body); color: var(--text-body); border-top: 1px solid var(--border-subtle); }
.um-mfa:hover { background: var(--surface-page); }
.um-tag { margin-left: auto; font: 600 10px var(--font-mono); color: var(--text-faint); }
.um-tag.on { color: var(--success-text); }
.usermenu {
  position: fixed; bottom: 18px; left: 84px; z-index: 401; width: 240px;
  background: var(--surface-overlay, var(--surface-card)); border: 1px solid var(--border-default);
  border-radius: 12px; box-shadow: var(--shadow-xl); padding: 12px;
}
.um-head { display: flex; align-items: center; gap: 10px; }
.um-ava { width: 34px; height: 34px; border-radius: 50%; flex-shrink: 0; background: linear-gradient(135deg, #5e83fb, #2dcde6); display: flex; align-items: center; justify-content: center; font: 600 12px var(--font-body); color: #fff; }
.um-id { min-width: 0; }
.um-name { font: 600 13px var(--font-display); color: var(--text-strong); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.um-mail { font: 500 11px var(--font-mono); color: var(--text-faint); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.um-role { margin: 10px 0; padding: 5px 9px; border-radius: 7px; background: var(--accent-subtle); color: var(--accent-text); font: 600 11px var(--font-mono); display: inline-block; }
.um-logout { display: flex; align-items: center; gap: 8px; padding: 9px 10px; border-radius: 8px; border: 1px solid var(--border-default); color: var(--danger-text); font: 600 12.5px var(--font-body); cursor: pointer; }
.um-logout:hover { background: var(--danger-subtle); border-color: rgba(240, 71, 62, 0.4); }
/* main */
.main { flex: 1; min-width: 0; display: flex; flex-direction: column; }
.topbar {
  display: flex;
  align-items: center;
  gap: 12px;
  height: 56px;
  flex-shrink: 0;
  padding: 0 var(--space-6);
  background: var(--topbar-bg);
  box-shadow: 0 1px 0 var(--border-subtle);
}
/* 管理后台:顶栏一个醒目的标记,侧边栏换深色 —— 一眼分得清现在是在哪个后台里改东西 */
.portal-tag { display: inline-flex; align-items: center; gap: 5px; height: 24px; padding: 0 9px; margin-right: 10px; border-radius: 6px;
  background: var(--warning-subtle); color: var(--warning-text); font: 700 11.5px var(--font-body); white-space: nowrap; }
.portal-switch { cursor: pointer; background: var(--accent-subtle); color: var(--accent-text); font: 600 12px var(--font-body); }
.portal-switch:hover { filter: brightness(0.97); }
.shell.is-admin .rail { background: var(--gray-900, #111827); }
.shell.is-admin .rail-item { color: rgba(255, 255, 255, 0.72); }
.shell.is-admin .rail-item:hover { color: #fff; background: rgba(255, 255, 255, 0.08); }
.shell.is-admin .rail-item.active { color: #fff; background: rgba(255, 255, 255, 0.14); }
.tb-title { font: 700 17px var(--font-display); color: var(--text-strong); letter-spacing: var(--tracking-tight); }
/* The subtitle is PROSE ("一次变更走完一条流水线"), not data — it was set in
   JetBrains Mono, which is why every page header read like a log line. Numbers
   inside it keep the mono face via .v-num. */
.tb-sub { font: 500 12.5px var(--font-body); color: var(--text-muted); }
.tb-right { margin-left: auto; display: flex; align-items: center; gap: 10px; }
/* 标题与胶囊一律不折行:窄下来时逐字竖排比截断难读得多(黑盒 2026-10 工单 05)。 */
.tb-title { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; min-width: 0; flex: 0 1 auto; }
.tb-sub { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; min-width: 0; flex: 0 1 auto; }
.tb-right { flex-shrink: 0; }
.tb-right .pill { white-space: nowrap; flex-shrink: 0; }
.tb-more { display: none; position: relative; }
.more-panel { width: 220px; max-height: none; padding: 6px 0; }
.mitem { display: flex; align-items: center; gap: 8px; padding: 9px 14px; font: 600 12px var(--font-body); color: var(--text-body); white-space: nowrap; }
.mitem.off { color: var(--danger-text); }
.mitem.click { cursor: pointer; }
.mitem.click:hover { background: var(--surface-sunken); }
/* 手机宽度:副标题让位,次要按钮(网关状态、前后台切换、语言、深色)收进「更多」,
   待办只留图标与数字。不追求完美的移动端,只保证不裁掉、不竖排。 */
@media (max-width: 760px) {
  .topbar { padding: 0 12px; gap: 8px; }
  .tb-sub { display: none; }
  .portal-tag { margin-right: 0; }
  .tb-right { gap: 6px; }
  .tb-sec { display: none !important; }
  .tb-more { display: flex; }
  .todo-btn > span { display: none; }
  .panel { width: min(340px, calc(100vw - 24px)); position: fixed; top: 52px; right: 12px; }
  .more-panel { width: 220px; }
}
/* Status pills are filled, not outlined: an outline on a light page competes
   with the card edges around it, and these are meant to be read at a glance. */
.pill {
  display: inline-flex;
  align-items: center;
  gap: 7px;
  height: 30px;
  padding: 0 12px;
  border: none;
  border-radius: var(--radius-full);
  background: var(--surface-sunken);
  font: 600 11.5px var(--font-body);
  color: var(--text-muted);
  transition: var(--transition-colors);
}
.pill .v-num, .pill .num { font-family: var(--font-mono); font-variant-numeric: tabular-nums; }
.pill.off { background: var(--danger-subtle); color: var(--danger-text); font-weight: 600; }
.pill.click { color: var(--text-body); cursor: pointer; font-weight: 600; gap: 5px; }
.pill.click:hover { background: var(--border-subtle); }

/* 待办。空闲时和旁边的 pill 一样安静;有事时整枚变琥珀色、加脉冲 —— 这是页面上唯一
   会自己动的东西,所以它必须只在真有事的时候动。 */
.todo { position: relative; display: flex; }
.todo-btn { cursor: pointer; color: var(--text-body); }
.todo-btn:hover, .todo-btn.active { background: var(--border-subtle); }
.todo-btn .num {
  display: inline-flex; align-items: center; justify-content: center;
  min-width: 18px; height: 18px; padding: 0 5px; border-radius: 999px;
  font: 700 11px var(--font-mono); font-variant-numeric: tabular-nums;
}
.todo-btn.hot {
  background: var(--warning); color: #1b1200; font-weight: 700;
  animation: todoPulse 1.8s ease-out infinite;
}
.todo-btn.hot:hover, .todo-btn.hot.active { background: var(--warning-600, var(--warning)); }
.todo-btn.hot .num { background: rgba(0, 0, 0, 0.16); }
@keyframes todoPulse {
  0% { box-shadow: 0 0 0 0 rgba(245, 165, 36, 0.6); }
  70% { box-shadow: 0 0 0 9px rgba(245, 165, 36, 0); }
  100% { box-shadow: 0 0 0 0 rgba(245, 165, 36, 0); }
}
.todo-panel { width: 420px; }
.pcount { margin-left: 8px; font: 500 11px var(--font-mono); color: var(--text-muted); }
.titem { cursor: pointer; align-items: flex-start; transition: background var(--dur-fast, 0.12s) ease; }
.titem:hover { background: var(--surface-sunken); }
.tkind { font: 600 10px var(--font-body); padding: 1px 6px; border-radius: 999px; white-space: nowrap; }
.tkind.decide { background: var(--warning-subtle); color: var(--warning-text); }
.tkind.execute { background: var(--success-subtle); color: var(--success-text); }
.tkind.confirm { background: var(--accent-subtle); color: var(--accent-text); }
.pic.info { color: var(--accent-text); }
.tstage { margin-right: 6px; padding: 1px 5px; border-radius: 4px; background: var(--accent-subtle); color: var(--accent-text); font: 600 10px var(--font-body); }
.tcmd { font: 400 11px/1.5 var(--font-mono); color: var(--text-body); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.tgo { flex-shrink: 0; align-self: center; color: var(--text-faint); }
.content { flex: 1; min-height: 0; display: flex; flex-direction: column; }
/* 管理后台空闲锁屏(有没保存的改动时):盖住整页,底下的内容原样留着 */
.admin-lock { position: fixed; inset: 0; z-index: 2000; display: flex; align-items: center; justify-content: center; padding: 16px;
  background: color-mix(in srgb, var(--surface-page) 55%, transparent); backdrop-filter: blur(3px); }
.admin-lock-card { width: 100%; max-width: 420px; padding: 24px; border-radius: 14px; background: var(--surface-card); border: 1px solid var(--border-subtle);
  box-shadow: var(--shadow-lg); text-align: center; }
.admin-lock-ic { width: 44px; height: 44px; margin: 0 auto 12px; border-radius: 12px; display: flex; align-items: center; justify-content: center;
  background: var(--warning-subtle); color: var(--warning-text); }
.admin-lock-t { font: 700 15px var(--font-body); color: var(--text-strong); margin-bottom: 6px; }
.admin-lock-d { font-size: 12.5px; line-height: 1.6; color: var(--text-muted); }
.admin-lock-err { margin-top: 10px; font-size: 12px; color: var(--danger-text); }
.admin-lock-acts { display: flex; gap: 10px; justify-content: center; margin-top: 18px; flex-wrap: wrap; }
.admin-lock-btn { height: 34px; padding: 0 16px; border-radius: 9px; font: 600 12.5px var(--font-body); cursor: pointer; border: 1px solid var(--border-subtle); }
.admin-lock-btn.ghost { background: transparent; color: var(--text-muted); }
.admin-lock-btn.primary { background: var(--accent); border-color: var(--accent); color: var(--text-inverse); }
.admin-lock-btn:disabled { opacity: .6; cursor: default; }

/* notifications */
.notif { position: relative; display: flex; }
.bell {
  position: relative; display: flex; align-items: center; justify-content: center;
  width: 32px; height: 32px; border-radius: 9px; border: 1px solid transparent;
  background: transparent; color: var(--text-muted); cursor: pointer; transition: all 0.15s ease;
}
.bell:hover { color: var(--text-body); background: var(--surface-sunken); }
.bell.active { color: var(--accent-text); background: var(--accent-subtle); border-color: var(--accent-subtle-border); }
.nbadge {
  position: absolute; top: -3px; right: -3px; min-width: 16px; height: 16px; padding: 0 4px;
  border-radius: 999px; background: var(--danger); color: #fff; font: 700 9px var(--font-mono);
  display: flex; align-items: center; justify-content: center; border: 2px solid var(--surface-page);
}
.backdrop { position: fixed; inset: 0; z-index: 40; }
.panel {
  position: absolute; top: 42px; right: 0; z-index: 41; width: 340px; max-height: 440px;
  display: flex; flex-direction: column; overflow: hidden;
  background: var(--surface-card); border: 1px solid var(--border-default); border-radius: 12px;
  box-shadow: 0 16px 40px -12px rgba(0, 0, 0, 0.55);
}
.phead {
  display: flex; align-items: center; gap: 10px; padding: 12px 14px;
  border-bottom: 1px solid var(--border-subtle); font: 600 13px var(--font-body); color: var(--text-strong);
}
.phead > span:first-child { flex: 1; min-width: 0; }
/* 提示音开关。静音时是空心的铃 —— 光靠颜色分不出开关状态,色弱的人看到的是同一个图标。 */
.bellbtn { display: grid; place-items: center; width: 26px; height: 26px; flex: none; border-radius: 7px; color: var(--text-faint); cursor: pointer; }
.bellbtn:hover { background: var(--surface-sunken); color: var(--accent-text); }
.phead .link { font: 600 11px var(--font-mono); color: var(--accent-text); cursor: pointer; }
.phead .link:hover { text-decoration: underline; }
.plist { overflow-y: auto; }
.pempty { padding: 28px 14px; text-align: center; font: 500 12px var(--font-mono); color: var(--text-faint); }
.pitem { display: flex; gap: 10px; padding: 11px 14px; border-bottom: 1px solid var(--border-subtle); }
.pitem.unread { background: var(--accent-subtle); }
.pic { flex-shrink: 0; margin-top: 1px; }
.pic.ok { color: var(--success); }
.pic.bad { color: var(--danger); }
.pic.warn { color: var(--warning); }
.pbody { min-width: 0; }
.pt { font: 600 12px var(--font-body); color: var(--text-strong); display: flex; align-items: center; gap: 7px; }
.pref { font: 600 10px var(--font-mono); color: #8facff; }
.pd { margin-top: 3px; font: 400 12px/1.5 var(--font-body); color: var(--text-muted); word-break: break-word; }
</style>
