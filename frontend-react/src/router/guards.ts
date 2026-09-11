import { redirect, type LoaderFunction } from 'react-router-dom'
import { queryClient } from '@/api/queryClient'
import { meQueryOptions } from '@/api/modules/auth'
import { useAuthStore, isAdminOf, type Portal } from '@/stores/auth'
import type { Me } from '@/types'

/** 后台路由的菜单键 —— 这些页面除了菜单开关,还要求以管理后台身份登录。 */
const ADMIN_KEYS = new Set([
  'db', 'sqlreview', 'rules', 'perms', 'users', 'pipeline', 'audit', 'settings', 'envtier',
])

/**
 * 落地候选表,**按入口分开**。
 *
 * 两张表而不是一张:选了管理后台却被送进 Web 命令行,等于把人从他刚选的那扇门里
 * 推了出去。运维前台同理 —— 落地页必须落在他选的那一侧。
 */
const FRONT_ORDER: { key: string; path: string }[] = [
  { key: 'terminal', path: '/terminal' },
  { key: 'approve', path: '/approvals' },
  { key: 'pipeline', path: '/changes' },
  { key: 'execwindow', path: '/exec-windows' },
]
const BACK_ORDER: { key: string; path: string }[] = [
  { key: 'db', path: '/connections' },
  { key: 'rules', path: '/risk-rules' },
  { key: 'perms', path: '/permissions' },
  { key: 'audit', path: '/audit' },
  { key: 'settings', path: '/settings' },
]

export function firstVisibleRoute(me: Me | null, portal?: Portal): string {
  const menus = me?.menus ?? {}
  const p = portal ?? useAuthStore.getState().portal
  const primary = p === 'backend' && isAdminOf(me) ? BACK_ORDER : FRONT_ORDER
  // 选中那一侧一个都进不去时,才退到另一侧 —— 总比把人弹回登录页强。
  const fallback = primary === BACK_ORDER ? FRONT_ORDER : BACK_ORDER
  return (
    primary.find((r) => menus[r.key])?.path ??
    fallback.find((r) => menus[r.key])?.path ??
    ''
  )
}

/**
 * 守卫写在 loader 里而不是组件内(前端文档 §03)。
 *
 * 组件里判断意味着先渲染再跳转,受限页面会闪一下 —— 而那一下里它已经把数据请求
 * 发出去了。loader 在渲染之前跑,抛 redirect 就到此为止。
 *
 * 后台路由按 **portal + role 双重校验**:只隐藏入口不算门禁,以运维身份登录时
 * 直接拒绝。这仍然只是界面表达,服务端对每个请求独立重判。
 */
export function requireMenu(key?: string): LoaderFunction {
  return async () => {
    const { token, portal } = useAuthStore.getState()
    if (!token) throw redirect('/login')

    let me: Me
    try {
      me = await queryClient.ensureQueryData(meQueryOptions())
    } catch {
      // 令牌过期或被吊销:清掉本地会话,回登录页,别停在一个看着像登进来的空壳上。
      useAuthStore.getState().clearSession()
      throw redirect('/login')
    }
    // 守卫是同步读 store 的,把身份写回去供侧栏等处直接取用。
    useAuthStore.setState({ me })

    const home = firstVisibleRoute(me)
    // 一个菜单都没有的账户弹回登录页,而不是让他停在空白控制台上。
    if (!home) {
      useAuthStore.getState().clearSession()
      throw redirect('/login')
    }
    if (!key) return null

    if (ADMIN_KEYS.has(key) && (portal !== 'backend' || !isAdminOf(me))) throw redirect(home)
    if (!me.menus[key]) throw redirect(home)
    return null
  }
}

/** 落地页:把人送到他第一个能看的地方。 */
export const rootLoader: LoaderFunction = async () => {
  const { token } = useAuthStore.getState()
  if (!token) throw redirect('/login')
  const me = await queryClient.ensureQueryData(meQueryOptions()).catch(() => null)
  if (!me) {
    useAuthStore.getState().clearSession()
    throw redirect('/login')
  }
  useAuthStore.setState({ me })
  const home = firstVisibleRoute(me)
  throw redirect(home || '/login')
}
