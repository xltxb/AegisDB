import { NavLink, Outlet, useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import clsx from 'clsx'
import {
  TerminalSquare, ClipboardCheck, GitPullRequest, Clock, Cpu, Download, FileCode2,
  Library, Database, ScrollText, ShieldAlert, Users, UserCog, Workflow, FileSearch,
  Settings, LogOut, Moon, Sun, Languages,
} from 'lucide-react'
import { meQueryOptions } from '@/api/modules/auth'
import { useAuthStore, isAdminOf } from '@/stores/auth'
import { useUIStore } from '@/stores/ui'
import i18n from '@/locales'

interface NavItem { to: string; key: string; labelKey: string; icon: typeof Database }

/** 用户前台 —— 运维 DBA 的日常。 */
const FRONT_NAV: NavItem[] = [
  { to: '/terminal', key: 'terminal', labelKey: 'navTerminal', icon: TerminalSquare },
  { to: '/approvals', key: 'approve', labelKey: 'navApprovals', icon: ClipboardCheck },
  { to: '/changes', key: 'pipeline', labelKey: 'navChanges', icon: GitPullRequest },
  { to: '/exec-windows', key: 'execwindow', labelKey: 'navExecWindows', icon: Clock },
  { to: '/async-jobs', key: 'terminal', labelKey: 'navAsyncJobs', icon: Cpu },
  { to: '/export', key: 'terminal', labelKey: 'navExport', icon: Download },
  { to: '/scripts', key: 'terminal', labelKey: 'navScripts', icon: FileCode2 },
  { to: '/catalog', key: 'terminal', labelKey: 'navCatalog', icon: Library },
]

/** 管理后台 —— 平台管理员的配置面。 */
const BACK_NAV: NavItem[] = [
  { to: '/connections', key: 'db', labelKey: 'navConnections', icon: Database },
  { to: '/sql-review', key: 'sqlreview', labelKey: 'navSqlReview', icon: ScrollText },
  { to: '/risk-rules', key: 'rules', labelKey: 'navRiskRules', icon: ShieldAlert },
  { to: '/permissions', key: 'perms', labelKey: 'navPermissions', icon: Users },
  { to: '/users', key: 'perms', labelKey: 'navUsers', icon: UserCog },
  { to: '/pipelines', key: 'pipeline', labelKey: 'navPipelines', icon: Workflow },
  { to: '/audit', key: 'audit', labelKey: 'navAudit', icon: FileSearch },
  { to: '/settings', key: 'settings', labelKey: 'navSettings', icon: Settings },
]

export default function AppShell() {
  const { t } = useTranslation()
  const nav = useNavigate()
  const { data: me } = useQuery(meQueryOptions())
  const portal = useAuthStore((s) => s.portal)
  const logout = useAuthStore((s) => s.logout)
  const theme = useUIStore((s) => s.theme)
  const setTheme = useUIStore((s) => s.setTheme)
  const lang = useUIStore((s) => s.lang)
  const setLang = useUIStore((s) => s.setLang)

  // 侧栏按 menus 渲染 —— 后台那组还要求以管理后台身份登录且角色含 admin,
  // 与 guards 里那道门是同一条规则,免得看得见却进不去。
  const menus = me?.menus ?? {}
  const items = (portal === 'backend' && isAdminOf(me ?? null) ? BACK_NAV : FRONT_NAV)
    .filter((i) => menus[i.key])

  function toggleLang() {
    const next = lang === 'zh' ? 'en' : 'zh'
    setLang(next)
    i18n.changeLanguage(next)
  }

  return (
    <div className="shell">
      <aside className="side">
        <div className="side-mark">A</div>
        <nav>
          {items.map((i) => (
            <NavLink key={i.to} to={i.to} className={({ isActive }) => clsx('side-item', isActive && 'on')}>
              <i.icon size={18} />
              <span>{t(i.labelKey)}</span>
            </NavLink>
          ))}
        </nav>
      </aside>

      <div className="main">
        <header className="top">
          <div className="top-title">{t('appTagline')}</div>
          <div className="top-right">
            <button className="iconbtn" onClick={toggleLang} title="zh / en">
              <Languages size={16} />
            </button>
            <button className="iconbtn" onClick={() => setTheme(theme === 'dark' ? 'light' : 'dark')}>
              {theme === 'dark' ? <Sun size={16} /> : <Moon size={16} />}
            </button>
            <span className="who">{me?.name}</span>
            <button className="iconbtn" title={t('logout')} onClick={() => { logout(); nav('/login', { replace: true }) }}>
              <LogOut size={16} />
            </button>
          </div>
        </header>
        <main className="content">
          <Outlet />
        </main>
      </div>
    </div>
  )
}
