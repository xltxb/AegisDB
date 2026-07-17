import { createRouter, createWebHashHistory } from 'vue-router'
import { useAuthStore } from '@/stores/auth'

const router = createRouter({
  history: createWebHashHistory(),
  routes: [
    { path: '/login', name: 'login', component: () => import('@/views/LoginView.vue'), meta: { public: true } },
    {
      path: '/',
      component: () => import('@/components/AppLayout.vue'),
      redirect: '/terminal',
      children: [
        { path: 'terminal', name: 'terminal', component: () => import('@/views/TerminalView.vue'), meta: { menuKey: 'terminal' } },
        // export is a terminal-level data capability; gate on the terminal menu.
        { path: 'export', name: 'export', component: () => import('@/views/ExportView.vue'), meta: { menuKey: 'export', gate: 'terminal' } },
        { path: 'uploads', name: 'uploads', component: () => import('@/views/UploadView.vue'), meta: { menuKey: 'uploads', gate: 'terminal' } },
        { path: 'approvals', name: 'approvals', component: () => import('@/views/ApprovalsView.vue'), meta: { menuKey: 'approve' } },
        { path: 'connections', name: 'connections', component: () => import('@/views/ConnectionsView.vue'), meta: { menuKey: 'db' } },
        { path: 'risk-rules', name: 'risk-rules', component: () => import('@/views/RiskRulesView.vue'), meta: { menuKey: 'rules' } },
        { path: 'permissions', name: 'permissions', component: () => import('@/views/PermissionsView.vue'), meta: { menuKey: 'perms' } },
        { path: 'audit', name: 'audit', component: () => import('@/views/AuditView.vue'), meta: { menuKey: 'audit' } },
        { path: 'settings', name: 'settings', component: () => import('@/views/SettingsView.vue'), meta: { menuKey: 'settings' } },
      ],
    },
    { path: '/:pathMatch(.*)*', redirect: '/terminal' },
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
  // `gate` lets a route reuse another menu's permission (e.g. export ← terminal).
  const key = (to.meta.gate as string | undefined) ?? (to.meta.menuKey as string | undefined)
  if (key && !auth.menus[key]) {
    const dest = auth.firstVisibleRoute
    // No accessible route (empty menus) or the fallback is the very route we're
    // being denied → break the redirect loop by logging out (L12).
    if (!dest || dest === to.path) {
      auth.logout()
      return { name: 'login' }
    }
    return { path: dest }
  }
  return true
})

export default router
