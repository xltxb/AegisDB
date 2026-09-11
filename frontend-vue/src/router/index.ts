import { createRouter, createWebHashHistory } from 'vue-router'
import { installGuards } from './guards'

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
        { path: 'terminal', name: 'terminal', component: () => import('@/views/terminal/TerminalView.vue'), meta: { menuKey: 'terminal' } },
        // export is a terminal-level data capability; gate on the terminal menu.
        { path: 'export', name: 'export', component: () => import('@/views/terminal/ExportView.vue'), meta: { menuKey: 'export', gate: 'terminal' } },
        { path: 'uploads', name: 'uploads', component: () => import('@/views/terminal/UploadView.vue'), meta: { menuKey: 'uploads', gate: 'terminal' } },
        { path: 'async-jobs', name: 'asyncjobs', component: () => import('@/views/terminal/AsyncExecView.vue'), meta: { menuKey: 'asyncjobs', gate: 'terminal' } },
        // 发布流程 (CI/CD). Both pages sit behind the `pipeline` menu: raising a
        // release ends in an execution, so it is granted to whoever may execute.
        // Editing the templates is admin-only on the server; the pages hide the
        // controls rather than guessing.
        { path: 'releases', name: 'releases', component: () => import('@/views/pipeline/ReleasesView.vue'), meta: { menuKey: 'releases', gate: 'pipeline' } },
        { path: 'pipelines', name: 'pipelines', component: () => import('@/views/pipeline/PipelinesView.vue'), meta: { menuKey: 'pipelines', gate: 'pipeline' } },
        // 规范审查 gates on `terminal`, not `rules`: the check is a self-service
        // tool for whoever writes the change, and a review nobody can run before
        // submitting is a review that only ever arrives as a rejection.
        { path: 'sql-review', name: 'sql-review', component: () => import('@/views/terminal/SqlReviewView.vue'), meta: { menuKey: 'sqlreview', gate: 'terminal' } },
        { path: 'approvals', name: 'approvals', component: () => import('@/views/approvals/ApprovalsView.vue'), meta: { menuKey: 'approve' } },
        // 项目自己一页,自己的菜单键。它从数据源配置页搬出来,因为两者的读者不是
        // 同一批人:配置实例的是 DBA,按项目跟进升级单的是业务线上的人。
        { path: 'projects', name: 'projects', component: () => import('@/views/connections/ProjectsView.vue'), meta: { menuKey: 'project' } },
        { path: 'connections', name: 'connections', component: () => import('@/views/connections/ConnectionsView.vue'), meta: { menuKey: 'db' } },
        { path: 'risk-rules', name: 'risk-rules', component: () => import('@/views/riskRules/RiskRulesView.vue'), meta: { menuKey: 'rules' } },
        { path: 'env-tiers', name: 'env-tiers', component: () => import('@/views/riskRules/EnvTiersView.vue'), meta: { menuKey: 'envtier' } },
        // 执行窗口有了自己的菜单键。它从分层页搬出来,是因为两者性质不同:分层是
        // **规则**(管理员改),窗口是一次**申请**(要执行的人提,再由人签字)。
        { path: 'exec-windows', name: 'exec-windows', component: () => import('@/views/approvals/ExecWindowsView.vue'), meta: { menuKey: 'execwindow' } },
        { path: 'permissions', name: 'permissions', component: () => import('@/views/permissions/PermissionsView.vue'), meta: { menuKey: 'perms' } },
        { path: 'audit', name: 'audit', component: () => import('@/views/audit/AuditView.vue'), meta: { menuKey: 'audit' } },
        { path: 'settings', name: 'settings', component: () => import('@/views/settings/SettingsView.vue'), meta: { menuKey: 'settings' } },
      ],
    },
    { path: '/:pathMatch(.*)*', redirect: '/dashboard' },
  ],
})

installGuards(router)

export default router
