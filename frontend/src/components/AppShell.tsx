import { NavLink, Outlet, useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import clsx from 'clsx'
import {
  LayoutDashboard, SquareTerminal, ClipboardCheck, GitPullRequestArrow, FileCode2, FileDown, ListChecks,
  BusFront, BookOpen, Database, ShieldAlert, ScanLine, VenetianMask, ShieldCheck,
  Workflow, UsersRound, ScrollText, Settings, SlidersHorizontal, Lock, LogOut,
  Activity, Hourglass, Languages, Bell, Search, Moon, Sun,
} from 'lucide-react'
import { meQueryOptions } from '@/api/modules/auth'
import { useAuthStore, isAdminOf, type Portal } from '@/stores/auth'
import { useUIStore } from '@/stores/ui'
import i18n from '@/locales'

interface NavItem {
  to: string
  /** 菜单闸的键,与后端 `menus` 对应。 */
  key: string
  /** rail 上那行 9px 的短标签。 */
  short: string
  /** hover 出来的全名。 */
  title: string
  icon: typeof Database
}

/** 用户前台 —— 运维 DBA 的日常(原型 v2 八项)。 */
const FRONT_NAV: NavItem[] = [
  { to: '/dashboard', key: '*', short: '总览', title: '总览', icon: LayoutDashboard },
  { to: '/terminal', key: 'terminal', short: '终端', title: '终端', icon: SquareTerminal },
  { to: '/approvals', key: 'approve', short: '申请', title: '我的申请', icon: ClipboardCheck },
  { to: '/changes', key: 'pipeline', short: '变更', title: '变更工单', icon: GitPullRequestArrow },
  { to: '/scripts', key: 'terminal', short: '脚本', title: '脚本库', icon: FileCode2 },
  { to: '/export', key: 'terminal', short: '导出', title: '数据导出', icon: FileDown },
  { to: '/async-jobs', key: 'terminal', short: '调度', title: '后台执行调度', icon: ListChecks },
  { to: '/exec-windows', key: 'execwindow', short: '班车', title: '执行窗口(班车)', icon: BusFront },
  { to: '/catalog', key: 'terminal', short: '资产', title: '数据资产', icon: BookOpen },
]

/** 管理后台 —— 平台管理员的配置面(原型 v2 九项)。 */
const BACK_NAV: NavItem[] = [
  { to: '/dashboard', key: '*', short: '总览', title: '总览', icon: LayoutDashboard },
  { to: '/connections', key: 'db', short: '配置', title: '数据库配置', icon: Database },
  { to: '/risk-rules', key: 'rules', short: '规则', title: '高危规则', icon: ShieldAlert },
  { to: '/sql-review', key: 'rules', short: '审查', title: 'SQL 审查规范', icon: ScanLine },
  { to: '/gov', key: 'settings', short: '治理', title: '数据治理', icon: VenetianMask },
  { to: '/permissions', key: 'perms', short: '权限', title: '角色与权限', icon: ShieldCheck },
  { to: '/pipelines', key: 'pipeline', short: '流程', title: '变更流程配置', icon: Workflow },
  { to: '/users', key: 'perms', short: '用户', title: '用户管理', icon: UsersRound },
  { to: '/audit', key: 'audit', short: '审计', title: '审计日志', icon: ScrollText },
  { to: '/settings', key: 'settings', short: '设置', title: '系统设置', icon: Settings },
]

export default function AppShell() {
  const { t } = useTranslation()
  const nav = useNavigate()
  const { data: me } = useQuery(meQueryOptions())
  const portal = useAuthStore((s) => s.portal)
  const setPortal = useAuthStore((s) => s.setPortal)
  const logout = useAuthStore((s) => s.logout)
  const theme = useUIStore((s) => s.theme)
  const setTheme = useUIStore((s) => s.setTheme)
  const lang = useUIStore((s) => s.lang)
  const setLang = useUIStore((s) => s.setLang)

  const canAdmin = isAdminOf(me ?? null)
  const isBackend = portal === 'backend' && canAdmin
  const menus = me?.menus ?? {}
  // key 为 '*' 的项不设菜单闸 —— 总览只显示调用者本来就取得到的东西,每块数据
  // 自己带闸,没有什么可收敛的。
  const items = (isBackend ? BACK_NAV : FRONT_NAV).filter((i) => i.key === '*' || menus[i.key])

  function toggleLang() {
    const next = lang === 'zh' ? 'en' : 'zh'
    setLang(next)
    i18n.changeLanguage(next)
  }

  function switchPortal(p: Portal) {
    if (p === 'backend' && !canAdmin) return
    setPortal(p)
    nav('/', { replace: true })
  }

  return (
    <div className="shell">
      <aside className="rail">
        <div className="rail-logo"><SquareTerminal size={20} /></div>

        {/*
          门户切换器在 rail 里,不在顶栏(原型 v2)。不是管理员时整块不渲染 ——
          摆一个点不动的入口,只会让人反复去点它。
        */}
        {canAdmin && (
          <div className="portal-sw">
            <button
              className={clsx('portal-sw-item', !isBackend && 'on')}
              title={t('portalFrontend')}
              onClick={() => switchPortal('frontend')}
            >
              <SquareTerminal size={15} />
            </button>
            <button
              className={clsx('portal-sw-item', isBackend && 'on')}
              title={t('portalBackend')}
              onClick={() => switchPortal('backend')}
            >
              <SlidersHorizontal size={15} />
            </button>
          </div>
        )}

        <nav className="rail-nav">
          {items.map((i) => (
            <NavLink
              key={i.to}
              to={i.to}
              title={i.title}
              className={({ isActive }) => clsx('rail-item', isActive && 'on')}
            >
              <i.icon size={21} />
              <span>{i.short}</span>
            </NavLink>
          ))}
        </nav>

        <div className="rail-foot">
          <button className="rail-item rail-logout" title={t('logout')}
                  onClick={() => { logout(); nav('/login', { replace: true }) }}>
            <LogOut size={19} />
          </button>
          <div className="rail-avatar">{me?.initials || '··'}</div>
        </div>
      </aside>

      <div className="main">
        <header className="top">
          <div className="top-head">
            <div className="top-title">{t('appTagline')}</div>
            <div className="top-sub">{me?.roleName}</div>
          </div>

          <div className="top-right">
            <button className="pill pill-search" title="⌘K">
              <Search size={14} />
              <kbd>⌘K</kbd>
            </button>
            <span className="pill pill-health">
              <Activity size={14} />
              {t('gwOnline')}
            </span>
            {!isBackend && (
              <span className="pill pill-pending">
                <Hourglass size={14} />
                {t('pendingN')}
              </span>
            )}
            <button className="iconbtn" onClick={toggleLang} title="zh / en"><Languages size={16} /></button>
            <button className="iconbtn" onClick={() => setTheme(theme === 'dark' ? 'light' : 'dark')}>
              {theme === 'dark' ? <Sun size={16} /> : <Moon size={16} />}
            </button>
            <button className="iconbtn bell" title={t('notifications')}><Bell size={17} /></button>
            <span className="lock-hint">{!canAdmin && <Lock size={11} />}</span>
          </div>
        </header>

        <main className="content"><Outlet /></main>
      </div>
    </div>
  )
}
