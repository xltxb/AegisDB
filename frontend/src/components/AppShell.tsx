import { useEffect, useState } from 'react'
import { NavLink, Outlet, useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import clsx from 'clsx'
import {
  LayoutDashboard, SquareTerminal, ClipboardCheck, GitPullRequestArrow, FileCode2, FileDown, ListChecks,
  BusFront, BookOpen, Database, ShieldAlert, ScanLine, VenetianMask, ShieldCheck,
  Workflow, UsersRound, ScrollText, Settings, SlidersHorizontal, Lock,
  Activity, Hourglass, Languages, Search, Moon, Sun,
} from 'lucide-react'
import { meQueryOptions } from '@/api/modules/auth'
import { pendingApprovalsQueryOptions } from '@/api/modules/approvals'
import { terminalApi } from '@/api/modules/terminal'
import { useAuthStore, isAdminOf, type Portal } from '@/stores/auth'
import { useUIStore } from '@/stores/ui'
import { useIdleLock, useIdlePolicy } from '@/hooks/useIdleLock'
import NotificationBell from '@/components/shell/NotificationBell'
import AccountMenu from '@/components/shell/AccountMenu'
import CommandPalette from '@/components/shell/CommandPalette'
import i18n from '@/locales'

interface NavItem {
  to: string
  /** 菜单闸的键,与后端 `menus` 对应。 */
  key: string
  /** rail 上那行 9px 的短标签。 */
  short: string
  /** hover 出来的全名。 */
  title: string
  /** 文案键 —— 命令面板按它显示与匹配,跟着语言走。 */
  labelKey: string
  icon: typeof Database
  /** 顶上挂待审批计数的那一项。 */
  badge?: boolean
}

/** 用户前台 —— 运维 DBA 的日常(原型 v2 八项)。 */
const FRONT_NAV: NavItem[] = [
  { to: '/dashboard', key: '*', short: '总览', title: '总览', labelKey: 'navDashboard', icon: LayoutDashboard },
  { to: '/terminal', key: 'terminal', short: '终端', title: '终端', labelKey: 'navTerminal', icon: SquareTerminal },
  { to: '/approvals', key: 'approve', short: '申请', title: '我的申请', labelKey: 'navApprovals', icon: ClipboardCheck, badge: true },
  { to: '/changes', key: 'pipeline', short: '变更', title: '变更工单', labelKey: 'navChanges', icon: GitPullRequestArrow },
  { to: '/scripts', key: 'terminal', short: '脚本', title: '脚本库', labelKey: 'navScripts', icon: FileCode2 },
  { to: '/export', key: 'terminal', short: '导出', title: '数据导出', labelKey: 'navExport', icon: FileDown },
  { to: '/async-jobs', key: 'terminal', short: '调度', title: '后台执行调度', labelKey: 'navAsyncJobs', icon: ListChecks },
  { to: '/exec-windows', key: 'execwindow', short: '班车', title: '执行窗口(班车)', labelKey: 'navExecWindows', icon: BusFront },
  { to: '/catalog', key: 'terminal', short: '资产', title: '数据资产', labelKey: 'navCatalog', icon: BookOpen },
]

/** 管理后台 —— 平台管理员的配置面(原型 v2 九项)。 */
const BACK_NAV: NavItem[] = [
  { to: '/dashboard', key: '*', short: '总览', title: '总览', labelKey: 'navDashboard', icon: LayoutDashboard },
  { to: '/connections', key: 'db', short: '配置', title: '数据库配置', labelKey: 'navConnections', icon: Database },
  { to: '/risk-rules', key: 'rules', short: '规则', title: '高危规则', labelKey: 'navRiskRules', icon: ShieldAlert },
  { to: '/sql-review', key: 'rules', short: '审查', title: 'SQL 审查规范', labelKey: 'navSqlReview', icon: ScanLine },
  { to: '/gov', key: 'settings', short: '治理', title: '数据治理', labelKey: 'navGov', icon: VenetianMask },
  { to: '/permissions', key: 'perms', short: '权限', title: '角色与权限', labelKey: 'navPermissions', icon: ShieldCheck },
  { to: '/pipelines', key: 'pipeline', short: '流程', title: '变更流程配置', labelKey: 'navPipelines', icon: Workflow },
  { to: '/users', key: 'perms', short: '用户', title: '用户管理', labelKey: 'navUsers', icon: UsersRound },
  { to: '/audit', key: 'audit', short: '审计', title: '审计日志', labelKey: 'navAudit', icon: ScrollText },
  { to: '/settings', key: 'settings', short: '设置', title: '系统设置', labelKey: 'navSettings', icon: Settings },
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
  const notify = useUIStore((s) => s.notify)
  const [paletteOpen, setPaletteOpen] = useState(false)

  const canAdmin = isAdminOf(me ?? null)
  const isBackend = portal === 'backend' && canAdmin
  const menus = me?.menus ?? {}
  // key 为 '*' 的项不设菜单闸 —— 总览只显示调用者本来就取得到的东西,每块数据
  // 自己带闸,没有什么可收敛的。
  const items = (isBackend ? BACK_NAV : FRONT_NAV).filter((i) => i.key === '*' || menus[i.key])

  /**
   * 网关健康 5s 一拉。用的是总览页那块卡片同一个 queryKey —— 一份数据两处显示,
   * 顶栏说在线而卡片说离线这种事没有发生的余地。
   */
  const gw = useQuery({
    queryKey: ['gateway-stats'] as const,
    queryFn: terminalApi.gatewayStats,
    refetchInterval: 5_000,
    retry: false,
  })
  const gwOnline = !gw.error && gw.data?.online !== false

  /**
   * 待审批计数。顶栏那颗胶囊和 rail 上「申请」的红点读的是同一个查询 ——
   * 两处各拉一次,迟早会在某一秒里显示两个不同的数字。
   */
  const pending = useQuery({ ...pendingApprovalsQueryOptions(), enabled: !!menus.approve })
  const pendingN = pending.data ?? 0

  /**
   * 空闲自动锁定。
   *
   * 注销走的是正经的 logout(令牌代次 +1),不是只把本地清掉 —— 否则那枚令牌还能
   * 在别的地方继续用,这道锁就只锁住了这一块屏幕。
   */
  const idle = useIdlePolicy(!!menus.settings)
  useIdleLock(idle.enabled, idle.idleMs, () => {
    logout()
    notify(t('sessionLocked'), 'info')
    nav('/login', { replace: true, state: { locked: true } })
  })

  /** ⌘K / Ctrl+K 开面板。 */
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault()
        setPaletteOpen((v) => !v)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

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

  /**
   * 命令面板里选中一台实例之后去哪。
   *
   * 去的是这台实例**所在的那张清单**:此刻站在管理后台就去数据库配置页,否则去
   * 数据资产页。判的是 `isBackend` 而不是"是不是管理员" —— `/connections` 的守卫
   * 要求 portal 也在后台侧,以运维身份点进去会被原地弹走(见 router/guards.ts)。
   * 两页都够不到就不收实例候选:列出一个点了之后会被守卫拦下的东西,比不列出来
   * 更让人费解。
   *
   * 目标页不会替人选中那一行:没有哪个页面从 URL 读实例 id,硬塞一个 `?conn=` 只会
   * 是个不生效的参数。这一点是这次的已知缺口,补它要动到那两页本身。
   */
  const instanceTo = isBackend && menus.db ? '/connections' : menus.terminal ? '/catalog' : ''

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
              {i.badge && pendingN > 0 && (
                <span className="rail-dot">{pendingN > 99 ? '99+' : pendingN}</span>
              )}
            </NavLink>
          ))}
        </nav>

        <div className="rail-foot">
          <AccountMenu />
        </div>
      </aside>

      <div className="main">
        <header className="top">
          <div className="top-head">
            <div className="top-title">{t('appTagline')}</div>
            <div className="top-sub">{me?.roleName}</div>
          </div>

          <div className="top-right">
            <button className="pill pill-search" title={t('cmdkTitle')} onClick={() => setPaletteOpen(true)}>
              <Search size={14} />
              <kbd>⌘K</kbd>
            </button>
            <span className={clsx('pill pill-health', !gwOnline && 'off')}>
              <Activity size={14} />
              {gwOnline
                ? `${t('gwOnline')}${gw.data ? ` · p50 ${gw.data.p50Ms}ms` : ''}`
                : t('gwOffline')}
            </span>
            {/* 待审批只在运维前台出现:管理后台是配置面,那里没人在等着签字。 */}
            {!isBackend && !!menus.approve && (
              <span className="pill pill-pending">
                <Hourglass size={14} />
                {t('pendingN')} {pendingN}
              </span>
            )}
            <button className="iconbtn" onClick={toggleLang} title="zh / en"><Languages size={16} /></button>
            <button className="iconbtn" onClick={() => setTheme(theme === 'dark' ? 'light' : 'dark')}>
              {theme === 'dark' ? <Sun size={16} /> : <Moon size={16} />}
            </button>
            <NotificationBell />
            <span className="lock-hint">{!canAdmin && <Lock size={11} />}</span>
          </div>
        </header>

        <main className="content"><Outlet /></main>
      </div>

      <CommandPalette
        open={paletteOpen}
        onClose={() => setPaletteOpen(false)}
        pages={items.map((i) => ({ to: i.to, label: t(i.labelKey), icon: i.icon }))}
        instanceTo={instanceTo}
      />
    </div>
  )
}
