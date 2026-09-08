import { createRouter, createWebHashHistory } from 'vue-router'
import { useAuthStore } from '@/stores/auth'

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
        { path: 'pipelines', name: 'pipelines', component: () => import('@/views/PipelinesView.vue'), meta: { menuKey: 'pipelines', gate: 'pipeline' } },
        // 规范审查 gates on `terminal`, not `rules`: the check is a self-service
        // tool for whoever writes the change, and a review nobody can run before
        // submitting is a review that only ever arrives as a rejection.
        { path: 'sql-review', name: 'sql-review', component: () => import('@/views/SqlReviewView.vue'), meta: { menuKey: 'sqlreview', gate: 'terminal' } },
        { path: 'approvals', name: 'approvals', component: () => import('@/views/ApprovalsView.vue'), meta: { menuKey: 'approve' } },
        // 项目自己一页,自己的菜单键。它从数据源配置页搬出来,因为两者的读者不是
        // 同一批人:配置实例的是 DBA,按项目跟进升级单的是业务线上的人。
        { path: 'projects', name: 'projects', component: () => import('@/views/ProjectsView.vue'), meta: { menuKey: 'project' } },
        { path: 'connections', name: 'connections', component: () => import('@/views/ConnectionsView.vue'), meta: { menuKey: 'db' } },
        { path: 'risk-rules', name: 'risk-rules', component: () => import('@/views/RiskRulesView.vue'), meta: { menuKey: 'rules' } },
        { path: 'env-tiers', name: 'env-tiers', component: () => import('@/views/EnvTiersView.vue'), meta: { menuKey: 'envtier' } },
        { path: 'permissions', name: 'permissions', component: () => import('@/views/PermissionsView.vue'), meta: { menuKey: 'perms' } },
        { path: 'audit', name: 'audit', component: () => import('@/views/AuditView.vue'), meta: { menuKey: 'audit' } },
        { path: 'settings', name: 'settings', component: () => import('@/views/SettingsView.vue'), meta: { menuKey: 'settings' } },
      ],
    },
    { path: '/:pathMatch(.*)*', redirect: '/dashboard' },
  ],
})

// Auth + menu-permission guard (frontend doc §3).
router.beforeEach(async (to) => {
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
    return true
  }
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
