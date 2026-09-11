import { createBrowserRouter, Navigate } from 'react-router-dom'
import { requireMenu, rootLoader } from './guards'
import AppShell from '@/components/AppShell'
import RouteError from '@/components/common/RouteError'
import LoginPage from '@/pages/login'
import ApprovalsPage from '@/pages/approvals'
import Placeholder from '@/pages/Placeholder'

/**
 * data router:取数与守卫都在 loader 里,组件只负责渲染(前端文档 §03)。
 *
 * `menuKey` 不放在 meta 里,而是直接传给 `requireMenu` —— 守卫是这条路由唯一
 * 会用到它的地方,多一层间接只会让人多翻一次。
 */
export const router = createBrowserRouter([
  { path: '/login', element: <LoginPage /> },
  {
    path: '/',
    element: <AppShell />,
    ErrorBoundary: RouteError,
    children: [
      { index: true, loader: rootLoader, element: null },

      // ---- 用户前台 ----
      { path: 'terminal', loader: requireMenu('terminal'), element: <Placeholder titleKey="navTerminal" /> },
      { path: 'approvals', loader: requireMenu('approve'), element: <ApprovalsPage /> },
      { path: 'changes', loader: requireMenu('pipeline'), element: <Placeholder titleKey="navChanges" /> },
      { path: 'exec-windows', loader: requireMenu('execwindow'), element: <Placeholder titleKey="navExecWindows" /> },
      { path: 'async-jobs', loader: requireMenu('terminal'), element: <Placeholder titleKey="navAsyncJobs" /> },
      { path: 'export', loader: requireMenu('terminal'), element: <Placeholder titleKey="navExport" /> },
      { path: 'scripts', loader: requireMenu('terminal'), element: <Placeholder titleKey="navScripts" /> },
      { path: 'catalog', loader: requireMenu('terminal'), element: <Placeholder titleKey="navCatalog" /> },

      // ---- 管理后台(portal + role 双重校验,见 guards) ----
      { path: 'connections', loader: requireMenu('db'), element: <Placeholder titleKey="navConnections" /> },
      { path: 'sql-review', loader: requireMenu('sqlreview'), element: <Placeholder titleKey="navSqlReview" /> },
      { path: 'risk-rules', loader: requireMenu('rules'), element: <Placeholder titleKey="navRiskRules" /> },
      { path: 'permissions', loader: requireMenu('perms'), element: <Placeholder titleKey="navPermissions" /> },
      { path: 'users', loader: requireMenu('perms'), element: <Placeholder titleKey="navUsers" /> },
      { path: 'pipelines', loader: requireMenu('pipeline'), element: <Placeholder titleKey="navPipelines" /> },
      { path: 'audit', loader: requireMenu('audit'), element: <Placeholder titleKey="navAudit" /> },
      { path: 'settings', loader: requireMenu('settings'), element: <Placeholder titleKey="navSettings" /> },
    ],
  },
  { path: '*', element: <Navigate to="/" replace /> },
])
