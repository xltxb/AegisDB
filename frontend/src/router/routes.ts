import type { ComponentType } from 'react'

/** 路由元信息(route.handle)。 */
export interface RouteMeta {
  name: string
  menuKey?: string
  /** 复用另一个菜单的权限(如 export ← terminal)。 */
  gate?: string
  portal?: 'admin'
  /** 管理后台里审计员也能进的页。 */
  auditor?: boolean
  /** 落地页:不设菜单闸。 */
  home?: boolean
  /** 登录了就能进,不挂菜单闸。 */
  open?: boolean
  mine?: boolean
}

export interface AppRoute extends RouteMeta {
  path: string
  view: () => Promise<{ default: ComponentType }>
}

export const appRoutes: AppRoute[] = [
  // 总览不设菜单闸:它只显示调用者本来就取得到的东西(每个数据接口自己带闸),
  // 而一个没有落地页的登录态只会把人弹回登录页。`home` 见 guard。
  { path: 'dashboard', name: 'dashboard', view: () => import('@/views/DashboardView'), menuKey: 'dashboard', home: true },
  // Web 命令行由 AppLayout 常驻渲染(见 KeepAliveTerminal),这条路由自己不渲染东西。
  { path: 'terminal', name: 'terminal', view: async () => ({ default: () => null }), menuKey: 'terminal' },
  // export is a terminal-level data capability; gate on the terminal menu.
  { path: 'export', name: 'export', view: () => import('@/views/ExportView'), menuKey: 'export', gate: 'terminal' },
  { path: 'uploads', name: 'uploads', view: () => import('@/views/UploadView'), menuKey: 'uploads', gate: 'terminal' },
  { path: 'async-jobs', name: 'asyncjobs', view: () => import('@/views/AsyncExecView'), menuKey: 'asyncjobs', gate: 'terminal' },
  // 发布流程 (CI/CD). Both pages sit behind the `pipeline` menu: raising a
  // release ends in an execution, so it is granted to whoever may execute.
  // Editing the templates is admin-only on the server; the pages hide the
  // controls rather than guessing.
  { path: 'releases', name: 'releases', view: () => import('@/views/ReleasesView'), menuKey: 'releases', gate: 'pipeline' },
  // 规范审查 gates on `terminal`, not `rules`: the check is a self-service
  // tool for whoever writes the change, and a review nobody can run before
  // submitting is a review that only ever arrives as a rejection.
  { path: 'sql-review', name: 'sql-review', view: () => import('@/views/SqlReviewView'), menuKey: 'sqlreview', gate: 'terminal' },
  { path: 'approvals', name: 'approvals', view: () => import('@/views/ApprovalsView'), menuKey: 'approve' },
  // 项目自己一页,自己的菜单键。它从数据源配置页搬出来,因为两者的读者不是
  // 同一批人:配置实例的是 DBA,按项目跟进升级单的是业务线上的人。
  { path: 'projects', name: 'projects', view: () => import('@/views/ProjectsView'), menuKey: 'project' },
  // 执行窗口有了自己的菜单键。它从分层页搬出来,是因为两者性质不同:分层是
  // **规则**(管理员改),窗口是一次**申请**(要执行的人提,再由人签字)。
  { path: 'exec-windows', name: 'exec-windows', view: () => import('@/views/ExecWindowsView'), menuKey: 'execwindow' },
  // 业务方数据查询:能查哪些库只由查询授权决定(外部审批系统下发)。
  { path: 'query', name: 'query', view: () => import('@/views/QueryView'), menuKey: 'query' },
  { path: 'schema-diff', name: 'schemadiff', view: () => import('@/views/SchemaDiffView'), menuKey: 'schemadiff' },
  // AI 巡检报告(.scratch/inspection-report):菜单 inspection,后端另有角色校验
  { path: 'inspection', name: 'inspection', view: () => import('@/views/InspectionView'), menuKey: 'inspection' },
  // 「我的操作记录」:人人可进(open),只看自己的 —— 全量审计在管理后台 /admin/audit(工单 04)
  { path: 'audit', name: 'audit', view: () => import('@/views/AuditView'), menuKey: 'mylog', mine: true, open: true },

  // ---- 管理后台(/admin)----
  // 挂在同一个父路由下,而不是另起一个布局路由:切换前后台时布局不重新挂载,
  // 常驻的 Web 命令行(和它每个标签独占的数据库连接、没提交的事务)都还在。
  // portal: 'admin' 由 guard 把关 —— 只有平台管理员;auditor: true 的页审计员也能进。
  // 后端接口有同一口径的强制校验(工单 01),这里只是不让人走进一个必然 403 的页面。
  // /admin 落到哪页要看身份,由 guard 在拿到身份之后决定(刷新直接打开 #/admin 时还没 fetchMe)。
  { path: 'admin', name: 'admin', view: async () => ({ default: () => null }) },
  // 管理总览:管理后台首页(工单 06),只给平台管理员;open = 不挂菜单闸
  { path: 'admin/overview', name: 'admin-overview', view: () => import('@/views/AdminOverviewView'), menuKey: 'adminhome', portal: 'admin', open: true },
  { path: 'admin/connections', name: 'connections', view: () => import('@/views/ConnectionsView'), menuKey: 'db', portal: 'admin' },
  { path: 'admin/risk-rules', name: 'risk-rules', view: () => import('@/views/RiskRulesView'), menuKey: 'rules', portal: 'admin' },
  { path: 'admin/env-tiers', name: 'env-tiers', view: () => import('@/views/EnvTiersView'), menuKey: 'envtier', portal: 'admin' },
  { path: 'admin/permissions', name: 'permissions', view: () => import('@/views/PermissionsView'), menuKey: 'perms', portal: 'admin' },
  { path: 'admin/audit', name: 'admin-audit', view: () => import('@/views/AuditView'), menuKey: 'audit', portal: 'admin', auditor: true },
  // 规范审查规则库的维护(用户后台的 /sql-review 只读 + 自查)
  { path: 'admin/sql-review', name: 'admin-sql-review', view: () => import('@/views/SqlReviewView'), menuKey: 'sqlreview', gate: 'terminal', portal: 'admin' },
  // 执行窗口全局管理:所有人的窗口,可停用(用户后台的 /exec-windows 只列自己的)
  { path: 'admin/exec-windows', name: 'admin-exec-windows', view: () => import('@/views/ExecWindowsView'), menuKey: 'execwindow', portal: 'admin' },
  { path: 'admin/pipelines', name: 'pipelines', view: () => import('@/views/PipelinesView'), menuKey: 'pipelines', gate: 'pipeline', portal: 'admin' },
  { path: 'admin/settings', name: 'settings', view: () => import('@/views/SettingsView'), menuKey: 'settings', portal: 'admin' },
]

// 旧地址:书签、文档里的链接照样能打开(query 一并带过去)
export const legacyAdminPaths = ['connections', 'risk-rules', 'env-tiers', 'permissions', 'pipelines', 'settings']
