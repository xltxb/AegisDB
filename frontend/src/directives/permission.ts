import type { Directive } from 'vue'
import { useAuthStore } from '@/stores/auth'

/**
 * `v-permission="'ddl:prod'"` —— 能力层的展示收敛(前端开发文档 §07)。
 *
 * 三层权限里,菜单层由路由守卫与侧栏负责,命令层由服务端逐条判定负责,中间这一层
 * 回答的是"这个按钮该不该能按"。能力矩阵(`能力 × 分层 → allow/approve/deny`)由
 * `/auth/me` 一起下发,`auth.can()` 读它。
 *
 * 这里**只禁用,不隐藏**。隐藏会让人以为功能不存在,于是去问"为什么我这儿没有这个
 * 按钮";禁用至少留下一个可以指着问的东西,配合 title 说明是权限问题。
 *
 * 它不是闸门。真正的拦截在服务端 —— 每条语句下发前还要再判一次,绕过这个指令
 * 什么也得不到。所以矩阵未加载完时一律放行,而不是把整屏灰掉。
 */
export const vPermission: Directive<HTMLElement, string> = {
  mounted(el, binding) {
    apply(el, binding.value)
  },
  updated(el, binding) {
    if (binding.value !== binding.oldValue) apply(el, binding.value)
  },
}

function apply(el: HTMLElement, expr: string) {
  if (!expr) return
  const allowed = useAuthStore().can(expr)
  if (allowed) {
    el.removeAttribute('disabled')
    el.removeAttribute('aria-disabled')
    el.classList.remove('is-denied')
    return
  }
  el.setAttribute('disabled', 'true')
  el.setAttribute('aria-disabled', 'true')
  el.classList.add('is-denied')
}

export default vPermission
