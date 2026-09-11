import { createBrowserRouter, Navigate } from 'react-router-dom'
import { requireMenu, rootLoader } from './guards'
import AppShell from '@/components/AppShell'
import RouteError from '@/components/common/RouteError'
import LoginPage from '@/pages/login'
import ApprovalsPage from '@/pages/approvals'
import ExecWindowsPage from '@/pages/execWindows'
import AuditPage from '@/pages/audit'
import SettingsPage from '@/pages/settings'
import CatalogPage from '@/pages/catalog'
import ChangesPage from '@/pages/changes'
import PipelinesPage from '@/pages/pipelines'
import TerminalPage from '@/pages/terminal'
import AsyncJobsPage from '@/pages/asyncJobs'
import ExportPage from '@/pages/export'
import ScriptsPage from '@/pages/scripts'
import PermissionsPage from '@/pages/permissions'
import UsersPage from '@/pages/users'
import GovPage from '@/pages/gov'
import ConnectionsPage from '@/pages/connections'
import RiskRulesPage from '@/pages/riskRules'
import SqlReviewPage from '@/pages/sqlReview'

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
      { path: 'terminal', loader: requireMenu('terminal'), element: <TerminalPage /> },
      { path: 'approvals', loader: requireMenu('approve'), element: <ApprovalsPage /> },
      { path: 'changes', loader: requireMenu('pipeline'), element: <ChangesPage /> },
      { path: 'exec-windows', loader: requireMenu('execwindow'), element: <ExecWindowsPage /> },
      { path: 'async-jobs', loader: requireMenu('terminal'), element: <AsyncJobsPage /> },
      { path: 'export', loader: requireMenu('terminal'), element: <ExportPage /> },
      { path: 'scripts', loader: requireMenu('terminal'), element: <ScriptsPage /> },
      { path: 'catalog', loader: requireMenu('terminal'), element: <CatalogPage /> },

      // ---- 管理后台(portal + role 双重校验,见 guards) ----
      { path: 'connections', loader: requireMenu('db', { adminOnly: true }), element: <ConnectionsPage /> },
      { path: 'sql-review', loader: requireMenu('rules', { adminOnly: true }), element: <SqlReviewPage /> },
      { path: 'risk-rules', loader: requireMenu('rules', { adminOnly: true }), element: <RiskRulesPage /> },
      { path: 'gov', loader: requireMenu('settings', { adminOnly: true }), element: <GovPage /> },
      { path: 'permissions', loader: requireMenu('perms', { adminOnly: true }), element: <PermissionsPage /> },
      { path: 'users', loader: requireMenu('perms', { adminOnly: true }), element: <UsersPage /> },
      { path: 'pipelines', loader: requireMenu('pipeline', { adminOnly: true }), element: <PipelinesPage /> },
      { path: 'audit', loader: requireMenu('audit', { adminOnly: true }), element: <AuditPage /> },
      { path: 'settings', loader: requireMenu('settings', { adminOnly: true }), element: <SettingsPage /> },
    ],
  },
  { path: '*', element: <Navigate to="/" replace /> },
])
