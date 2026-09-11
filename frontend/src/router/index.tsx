import { createBrowserRouter, Navigate } from 'react-router-dom'
import { requireMenu, rootLoader } from './guards'
import AppShell from '@/components/AppShell'
import RouteError from '@/components/common/RouteError'

/**
 * 把一个默认导出的页面模块接成 data router 认识的形状。
 *
 * 每个页面各自成包(前端文档 §10「Vite 按路由懒加载分包」)。全量静态导入时整个
 * 控制台是一个 1MB 的 JS —— 只想看一眼审批的人要先把终端、xterm、流水线编辑器
 * 全下载下来。
 */
const page = async (load: () => Promise<{ default: React.ComponentType }>) => ({
  Component: (await load()).default,
})

/**
 * data router:取数与守卫都在 loader 里,组件只负责渲染(前端文档 §03)。
 *
 * `menuKey` 不放在 meta 里,而是直接传给 `requireMenu` —— 守卫是这条路由唯一
 * 会用到它的地方,多一层间接只会让人多翻一次。
 */
export const router = createBrowserRouter([
  { path: '/login', lazy: () => page(() => import('@/pages/login')) },
  {
    path: '/',
    element: <AppShell />,
    ErrorBoundary: RouteError,
    children: [
      { index: true, loader: rootLoader, element: null },

      /*
       * 总览是落地页,**不设菜单闸**。
       *
       * 它只显示调用者本来就取得到的东西(每块数据自己带闸),而一个没有落地页的
       * 登录态只会把人弹回登录页。守卫仍然要求登录,以及"至少有一处能去"。
       */
      { path: 'dashboard', loader: requireMenu(), lazy: () => page(() => import('@/pages/dashboard')) },

      // ---- 用户前台 ----
      { path: 'terminal', loader: requireMenu('terminal'), lazy: () => page(() => import('@/pages/terminal')) },
      { path: 'approvals', loader: requireMenu('approve'), lazy: () => page(() => import('@/pages/approvals')) },
      { path: 'changes', loader: requireMenu('pipeline'), lazy: () => page(() => import('@/pages/changes')) },
      { path: 'exec-windows', loader: requireMenu('execwindow'), lazy: () => page(() => import('@/pages/execWindows')) },
      { path: 'async-jobs', loader: requireMenu('terminal'), lazy: () => page(() => import('@/pages/asyncJobs')) },
      { path: 'export', loader: requireMenu('terminal'), lazy: () => page(() => import('@/pages/export')) },
      { path: 'scripts', loader: requireMenu('terminal'), lazy: () => page(() => import('@/pages/scripts')) },
      { path: 'catalog', loader: requireMenu('terminal'), lazy: () => page(() => import('@/pages/catalog')) },

      // ---- 管理后台(portal + role 双重校验,见 guards) ----
      { path: 'connections', loader: requireMenu('db', { adminOnly: true }), lazy: () => page(() => import('@/pages/connections')) },
      { path: 'sql-review', loader: requireMenu('rules', { adminOnly: true }), lazy: () => page(() => import('@/pages/sqlReview')) },
      { path: 'risk-rules', loader: requireMenu('rules', { adminOnly: true }), lazy: () => page(() => import('@/pages/riskRules')) },
      { path: 'gov', loader: requireMenu('settings', { adminOnly: true }), lazy: () => page(() => import('@/pages/gov')) },
      { path: 'permissions', loader: requireMenu('perms', { adminOnly: true }), lazy: () => page(() => import('@/pages/permissions')) },
      { path: 'users', loader: requireMenu('perms', { adminOnly: true }), lazy: () => page(() => import('@/pages/users')) },
      { path: 'pipelines', loader: requireMenu('pipeline', { adminOnly: true }), lazy: () => page(() => import('@/pages/pipelines')) },
      { path: 'audit', loader: requireMenu('audit', { adminOnly: true }), lazy: () => page(() => import('@/pages/audit')) },
      { path: 'settings', loader: requireMenu('settings', { adminOnly: true }), lazy: () => page(() => import('@/pages/settings')) },
    ],
  },
  { path: '*', element: <Navigate to="/" replace /> },
])
