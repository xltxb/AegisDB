import { createRouter, createWebHashHistory } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import api from '@/api'
import { CODE_ADMIN_IP_BLOCKED } from '@/api/codes'
import { useUIStore } from '@/stores/ui'
import { i18n } from '@/locales'

const router = createRouter({
  history: createWebHashHistory(),
  routes: [
    { path: '/login', name: 'login', component: () => import('@/views/LoginView.vue'), meta: { public: true } },
    {
      path: '/',
      component: () => import('@/components/AppLayout.vue'),
      // 落地页是总览,不是 Web 命令行。终端是这个产品里唯一能改动真实数据的地方,
      // 把它当默认页等于每次开工都先站到闸门里面。
      redirect: '/dashboard',
      children: [
        // 总览不设菜单闸:它只显示调用者本来就取得到的东西(每个数据接口自己带闸),
        // 而一个没有落地页的登录态只会把人弹回登录页。`home` 见下方守卫。
        { path: 'dashboard', name: 'dashboard', component: () => import('@/views/DashboardView.vue'), meta: { menuKey: 'dashboard', home: true } },
        { path: 'terminal', name: 'terminal', component: () => import('@/views/TerminalView.vue'), meta: { menuKey: 'terminal' } },
        // export is a terminal-level data capability; gate on the terminal menu.
        { path: 'export', name: 'export', component: () => import('@/views/ExportView.vue'), meta: { menuKey: 'export', gate: 'terminal' } },
        { path: 'uploads', name: 'uploads', component: () => import('@/views/UploadView.vue'), meta: { menuKey: 'uploads', gate: 'terminal' } },
        { path: 'async-jobs', name: 'asyncjobs', component: () => import('@/views/AsyncExecView.vue'), meta: { menuKey: 'asyncjobs', gate: 'terminal' } },
        // 发布流程 (CI/CD). Both pages sit behind the `pipeline` menu: raising a
        // release ends in an execution, so it is granted to whoever may execute.
        // Editing the templates is admin-only on the server; the pages hide the
        // controls rather than guessing.
        { path: 'releases', name: 'releases', component: () => import('@/views/ReleasesView.vue'), meta: { menuKey: 'releases', gate: 'pipeline' } },
        // 规范审查 gates on `terminal`, not `rules`: the check is a self-service
        // tool for whoever writes the change, and a review nobody can run before
        // submitting is a review that only ever arrives as a rejection.
        { path: 'sql-review', name: 'sql-review', component: () => import('@/views/SqlReviewView.vue'), meta: { menuKey: 'sqlreview', gate: 'terminal' } },
        { path: 'approvals', name: 'approvals', component: () => import('@/views/ApprovalsView.vue'), meta: { menuKey: 'approve' } },
        // 项目自己一页,自己的菜单键。它从数据源配置页搬出来,因为两者的读者不是
        // 同一批人:配置实例的是 DBA,按项目跟进升级单的是业务线上的人。
        { path: 'projects', name: 'projects', component: () => import('@/views/ProjectsView.vue'), meta: { menuKey: 'project' } },
        // 执行窗口有了自己的菜单键。它从分层页搬出来,是因为两者性质不同:分层是
        // **规则**(管理员改),窗口是一次**申请**(要执行的人提,再由人签字)。
        { path: 'exec-windows', name: 'exec-windows', component: () => import('@/views/ExecWindowsView.vue'), meta: { menuKey: 'execwindow' } },
        // 业务方数据查询:能查哪些库只由查询授权决定(外部审批系统下发)。
        { path: 'query', name: 'query', component: () => import('@/views/QueryView.vue'), meta: { menuKey: 'query' } },
        { path: 'schema-diff', name: 'schemadiff', component: () => import('@/views/SchemaDiffView.vue'), meta: { menuKey: 'schemadiff' } },
        // AI 巡检报告(.scratch/inspection-report):菜单 inspection,后端另有角色校验
        { path: 'inspection', name: 'inspection', component: () => import('@/views/InspectionView.vue'), meta: { menuKey: 'inspection' } },
        // 「我的操作记录」:人人可进(open),只看自己的 —— 全量审计在管理后台 /admin/audit(工单 04)
        { path: 'audit', name: 'audit', component: () => import('@/views/AuditView.vue'), meta: { menuKey: 'mylog', mine: true, open: true } },

        // ---- 管理后台(/admin)----
        // 挂在同一个父路由下,而不是另起一个布局路由:切换前后台时布局不重新挂载,
        // keep-alive 的 Web 命令行(和它每个标签独占的数据库连接、没提交的事务)都还在。
        // portal: 'admin' 由下面的守卫把关 —— 只有平台管理员;auditor: true 的页审计员也能进。
        // 后端接口有同一口径的强制校验(工单 01),这里只是不让人走进一个必然 403 的页面。
        // /admin 落到哪页要看身份,而 redirect 函数在匹配时就跑了 —— 刷新直接打开 #/admin 时还没 fetchMe,
        // adminHome 是空的,管理员也被送回了用户后台(2026-10 审查)。改由下面的守卫在拿到身份之后决定。
        { path: 'admin', name: 'admin', component: { render: () => null } },
        // 管理总览:管理后台首页(工单 06),只给平台管理员;open = 不挂菜单闸
        { path: 'admin/overview', name: 'admin-overview', component: () => import('@/views/AdminOverviewView.vue'), meta: { menuKey: 'adminhome', portal: 'admin', open: true } },
        { path: 'admin/connections', name: 'connections', component: () => import('@/views/ConnectionsView.vue'), meta: { menuKey: 'db', portal: 'admin' } },
        { path: 'admin/risk-rules', name: 'risk-rules', component: () => import('@/views/RiskRulesView.vue'), meta: { menuKey: 'rules', portal: 'admin' } },
        { path: 'admin/env-tiers', name: 'env-tiers', component: () => import('@/views/EnvTiersView.vue'), meta: { menuKey: 'envtier', portal: 'admin' } },
        { path: 'admin/permissions', name: 'permissions', component: () => import('@/views/PermissionsView.vue'), meta: { menuKey: 'perms', portal: 'admin' } },
        { path: 'admin/audit', name: 'admin-audit', component: () => import('@/views/AuditView.vue'), meta: { menuKey: 'audit', portal: 'admin', auditor: true } },
        // 规范审查规则库的维护(用户后台的 /sql-review 只读 + 自查)
        { path: 'admin/sql-review', name: 'admin-sql-review', component: () => import('@/views/SqlReviewView.vue'), meta: { menuKey: 'sqlreview', gate: 'terminal', portal: 'admin' } },
        // 执行窗口全局管理:所有人的窗口,可停用(用户后台的 /exec-windows 只列自己的)
        { path: 'admin/exec-windows', name: 'admin-exec-windows', component: () => import('@/views/ExecWindowsView.vue'), meta: { menuKey: 'execwindow', portal: 'admin' } },
        { path: 'admin/pipelines', name: 'pipelines', component: () => import('@/views/PipelinesView.vue'), meta: { menuKey: 'pipelines', gate: 'pipeline', portal: 'admin' } },
        { path: 'admin/settings', name: 'settings', component: () => import('@/views/SettingsView.vue'), meta: { menuKey: 'settings', portal: 'admin' } },
        // 旧地址:书签、文档里的链接照样能打开
        { path: 'connections', redirect: (to) => ({ path: '/admin/connections', query: to.query }) },
        { path: 'risk-rules', redirect: (to) => ({ path: '/admin/risk-rules', query: to.query }) },
        { path: 'env-tiers', redirect: (to) => ({ path: '/admin/env-tiers', query: to.query }) },
        { path: 'permissions', redirect: (to) => ({ path: '/admin/permissions', query: to.query }) },
        { path: 'pipelines', redirect: (to) => ({ path: '/admin/pipelines', query: to.query }) },
        { path: 'settings', redirect: (to) => ({ path: '/admin/settings', query: to.query }) },
      ],
    },
    { path: '/:pathMatch(.*)*', redirect: '/dashboard' },
  ],
})

// Auth + menu-permission guard (frontend doc §3).
router.beforeEach(async (to, from) => {
  const auth = useAuthStore()
  if (to.meta.public) return true
  if (!auth.token) return { name: 'login' }
  if (!auth.loaded) {
    try {
      await auth.fetchMe()
    } catch {
      auth.logout()
      return { name: 'login' }
    }
  }
  if (to.name === 'admin') return auth.adminHome || { path: '/dashboard' }
  // 总览没有菜单闸。它带着 menuKey 只是为了页眉标题和导航高亮,所以这里要在闸门
  // 之前放行 —— 否则 auth.menus['dashboard'] 永远是 undefined,落地页会把每个人
  // 都弹回各自的第一个菜单,也就是又回到了 Web 命令行。
  //
  // 但一个**一个菜单都没有**的账户仍然该被弹回登录页(L12):让他停在一张空总览上,
  // 看着像登进来了,其实什么都做不了。
  if (to.meta.home) {
    if (!auth.firstVisibleRoute) {
      auth.logout()
      return { name: 'login' }
    }
    // 只有数据查询权限的业务方:总览上没有一样东西是给他的,直接送到数据查询页。
    if (auth.onlyQuery) return { path: '/query' }
    return true
  }
  // 管理后台:只有平台管理员(auditor 页审计员也行)。进不去就回用户后台,不往登录页踢。
  if (to.meta.portal === 'admin' && !(auth.isAdmin || (to.meta.auditor && auth.isAuditor))) {
    return { path: '/dashboard' }
  }
  // 从别处走进管理后台:告诉后端"人来了",重新计时管理后台的空闲超时(工单 09)。在管理后台里
  // 来回切页不算 —— 那由人在页面上的操作报心跳。
  //
  // IP 不在管理后台白名单内:就地拦下并提示。从前这里不看返回,照样走进管理后台,提示靠布局收拦截器
  // 的事件 —— 刷新直接打开管理后台的页面时布局还没挂上,事件丢了,人停在一个所有接口都回 40303
  // 的页面上(2026-10 审查)。站内点进来的留在原页;冷启动没有原页,回用户后台。
  if (to.meta.portal === 'admin' && from.meta.portal !== 'admin') {
    let code = 0
    try { code = (await api.adminEnter()).code } catch { /* 网络错误:放行,后面的接口自己会报 */ }
    if (code === CODE_ADMIN_IP_BLOCKED) {
      useUIStore().notify(i18n.global.t('adminIpBlockedStay'), 'error', 6000)
      return from.matched.length ? false : { path: '/dashboard' }
    }
  }
  // 不挂菜单闸的页(我的操作记录):登录了就能进,内容由后端按人收窄。
  if (to.meta.open) return true
  // 审计员在管理后台只看审计:菜单闸对它的审计页放行(审计员的审计菜单本来就开着)。
  // `gate` lets a route reuse another menu's permission (e.g. export ← terminal).
  const key = (to.meta.gate as string | undefined) ?? (to.meta.menuKey as string | undefined)
  if (key && !auth.menus[key]) {
    // 被拒之后送去总览,而不是送进 Web 命令行 —— 那是这里唯一能改动真实数据的地方,
    // 不该是"你去不了那儿"的默认落点。
    //
    // firstVisibleRoute 在这里只回答一件事:这个人有没有任何一处能去。一处都没有
    // 就回登录页(L12)。原先那条"兜底恰好就是被拒的这一页"的死循环判断也不必了 ——
    // 总览是 home 路由,在上面就已经放行,永远不会走到这里。
    if (!auth.firstVisibleRoute) {
      auth.logout()
      return { name: 'login' }
    }
    return { path: '/dashboard' }
  }
  return true
})

export default router
