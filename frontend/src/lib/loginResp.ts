import type { LoginResp } from '@/types'

/**
 * 这一份登录响应能不能用。
 *
 * 不校验就落盘的后果不是「存了个空值」:`localStorage.setItem(k, undefined)` 存进去的
 * 是**字符串 `"undefined"`**,它是真值 —— 路由守卫的 `if (!token)` 放行、界面渲染出来,
 * 而每个请求都带着 `Bearer undefined` 在 401。看着像登录成功,其实没登上(issue #79)。
 *
 * 走到这里的前提是信封 `code` 合法(否则 `ok()` 已经先抛了)但 data 里没有 token ——
 * 现实来源是网关、反向代理或 CDN 在 200 里塞了自己的响应体。
 *
 * **只校验真正被消费的两个字段。** `expiresAt` 在类型里有,但全仓库没有一处读它 ——
 * 把它列进必填,只会让一个本来能用的响应被拒掉。校验该盯着消费方,不是盯着类型声明。
 */
export function isUsableLoginResp(data: unknown): data is LoginResp {
  // 数组也是 object,`typeof` 判不掉它 —— 而 `{code:0, data:[]}` 正是过宽的桩和
  // 中间设备最常塞回来的形状。
  if (typeof data !== 'object' || data === null || Array.isArray(data)) return false
  const { token, user } = data as { token?: unknown; user?: unknown }
  if (typeof token !== 'string' || token === '') return false
  return typeof user === 'object' && user !== null && !Array.isArray(user)
}
