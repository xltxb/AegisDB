import { redirect, type LoaderFunction } from 'react-router-dom'
import { queryClient } from '@/api/queryClient'
import { meQueryOptions } from '@/api/modules/auth'
import { useAuthStore, isAdminOf, type Portal } from '@/stores/auth'
import type { Me } from '@/types'

/**
 * 后台身份要求由路由**显式声明**,不再从菜单键反推。
 *
 * 反推过一次,错了:`pipeline` 这个键同时给 `/changes`(运维前台的变更工单)和
 * `/pipelines`(管理后台的流程配置)用,于是「这个键属于后台」这条规则把非管理员
 * 挡在了他本该能进的前台页面外面。菜单键管的是"看不看得见",入口身份管的是
 * "哪一侧",两件事不能共用一个判断。
 */
export interface GuardOpts {
  /** 需要以管理后台身份登录,且角色并集里含 admin。 */
  adminOnly?: boolean
}

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
export function requireMenu(key?: string, opts: GuardOpts = {}): LoaderFunction {
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
    if (opts.adminOnly && (portal !== 'backend' || !isAdminOf(me))) throw redirect(home)
    if (!key) return null
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
  // 有任何一处能去就落到总览;一处都没有的账户弹回登录页(否则他会停在一张
  // 什么卡片都渲染不出来的空总览上,看着像登进来了,其实什么都做不了)。
  throw redirect(firstVisibleRoute(me) ? '/dashboard' : '/login')
}
