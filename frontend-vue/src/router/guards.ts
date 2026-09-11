import type { Router } from 'vue-router'
import { useAuthStore } from '@/stores/auth'

/**
 * 登录态 + 菜单权限守卫(前端开发文档 §03)。
 *
 * 从路由表里拆出来,是因为这两件事的变更理由不同:路由表因"加了一个页面"而改,
 * 守卫因"权限规则变了"而改。它们挤在一个文件里时,每次加页面都要从几十行注释
 * 中间找到守卫在哪。
 */
export function installGuards(router: Router) {
  router.beforeEach(async (to) => {
    const auth = useAuthStore()
    if (to.meta.public) return true
    if (!auth.token) return { name: 'login' }
    if (!auth.loaded) {
      try {
        await auth.fetchMe()
      } catch {
        auth.logout()
        return { name: 'login' }
      }
    }
    // 总览没有菜单闸。它带着 menuKey 只是为了页眉标题和导航高亮,所以这里要在闸门
    // 之前放行 —— 否则 auth.menus['dashboard'] 永远是 undefined,落地页会把每个人
    // 都弹回各自的第一个菜单,也就是又回到了 Web 命令行。
    //
    // 但一个**一个菜单都没有**的账户仍然该被弹回登录页(L12):让他停在一张空总览上,
    // 看着像登进来了,其实什么都做不了。
    if (to.meta.home) {
      if (!auth.firstVisibleRoute) {
        auth.logout()
        return { name: 'login' }
      }
      return true
    }
    // `gate` lets a route reuse another menu's permission (e.g. export ← terminal).
    const key = (to.meta.gate as string | undefined) ?? (to.meta.menuKey as string | undefined)
    if (key && !auth.menus[key]) {
      // 被拒之后送去总览,而不是送进 Web 命令行 —— 那是这里唯一能改动真实数据的地方,
      // 不该是"你去不了那儿"的默认落点。
      //
      // firstVisibleRoute 在这里只回答一件事:这个人有没有任何一处能去。一处都没有
      // 就回登录页(L12)。原先那条"兜底恰好就是被拒的这一页"的死循环判断也不必了 ——
      // 总览是 home 路由,在上面就已经放行,永远不会走到这里。
      if (!auth.firstVisibleRoute) {
        auth.logout()
        return { name: 'login' }
      }
      return { path: '/dashboard' }
    }
    return true
  })
}
