import { useAuthStore } from '@/stores/auth'
import api from '@/api'
import { CODE_ADMIN_IP_BLOCKED } from '@/api/codes'
import { useUIStore } from '@/stores/ui'
import { i18n } from '@/locales'
import type { RouteMeta } from './routes'

/** 放行返回 true;要去别处返回那个路径;'stay' = 就地拦下(留在原页)。 */
export type GuardResult = true | 'stay' | string

// Auth + menu-permission guard (frontend doc §3).
// from:此刻所在页的元信息;还没有任何页(冷启动、刷新)时是 null。
export async function guard(to: RouteMeta, from: RouteMeta | null): Promise<GuardResult> {
  const auth = useAuthStore.getState
  if (!auth().token) return '/login'
  if (!auth().loaded) {
    try {
      await auth().fetchMe()
    } catch {
      auth().logout()
      return '/login'
    }
  }
  if (to.name === 'admin') return auth().adminHome || '/dashboard'
  // 总览没有菜单闸。它带着 menuKey 只是为了页眉标题和导航高亮,所以这里要在闸门
  // 之前放行 —— 否则 menus['dashboard'] 永远是 undefined,落地页会把每个人
  // 都弹回各自的第一个菜单,也就是又回到了 Web 命令行。
  //
  // 但一个**一个菜单都没有**的账户仍然该被弹回登录页(L12):让他停在一张空总览上,
  // 看着像登进来了,其实什么都做不了。
  if (to.home) {
    if (!auth().firstVisibleRoute) {
      auth().logout()
      return '/login'
    }
    // 只有数据查询权限的业务方:总览上没有一样东西是给他的,直接送到数据查询页。
    if (auth().onlyQuery) return '/query'
    return true
  }
  // 管理后台:只有平台管理员(auditor 页审计员也行)。进不去就回用户后台,不往登录页踢。
  if (to.portal === 'admin' && !(auth().isAdmin || (to.auditor && auth().isAuditor))) return '/dashboard'
  // 从别处走进管理后台:告诉后端"人来了",重新计时管理后台的空闲超时(工单 09)。在管理后台里
  // 来回切页不算 —— 那由人在页面上的操作报心跳。
  //
  // IP 不在管理后台白名单内:就地拦下并提示。从前这里不看返回,照样走进管理后台,提示靠布局收拦截器
  // 的事件 —— 刷新直接打开管理后台的页面时布局还没挂上,事件丢了,人停在一个所有接口都回 40303
  // 的页面上(2026-10 审查)。站内点进来的留在原页;冷启动没有原页,回用户后台。
  if (to.portal === 'admin' && from?.portal !== 'admin') {
    let code = 0
    try { code = (await api.adminEnter()).code } catch { /* 网络错误:放行,后面的接口自己会报 */ }
    if (code === CODE_ADMIN_IP_BLOCKED) {
      useUIStore.getState().notify(i18n.global.t('adminIpBlockedStay'), 'error', 6000)
      return from ? 'stay' : '/dashboard'
    }
  }
  // 不挂菜单闸的页(我的操作记录):登录了就能进,内容由后端按人收窄。
  if (to.open) return true
  // 审计员在管理后台只看审计:菜单闸对它的审计页放行(审计员的审计菜单本来就开着)。
  // `gate` lets a route reuse another menu's permission (e.g. export ← terminal).
  const key = to.gate ?? to.menuKey
  if (key && !auth().menus[key]) {
    // 被拒之后送去总览,而不是送进 Web 命令行 —— 那是这里唯一能改动真实数据的地方,
    // 不该是"你去不了那儿"的默认落点。
    //
    // firstVisibleRoute 在这里只回答一件事:这个人有没有任何一处能去。一处都没有
    // 就回登录页(L12)。原先那条"兜底恰好就是被拒的这一页"的死循环判断也不必了 ——
    // 总览是 home 路由,在上面就已经放行,永远不会走到这里。
    if (!auth().firstVisibleRoute) {
      auth().logout()
      return '/login'
    }
    return '/dashboard'
  }
  return true
}
