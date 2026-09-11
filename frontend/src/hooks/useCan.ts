import { useQuery } from '@tanstack/react-query'
import { meQueryOptions } from '@/api/modules/auth'
import type { CapLevel } from '@/stores/auth'

/**
 * 能力层(前端文档 §07)。表达式写作 `能力:分层`,如 `ddl:prod`。
 *
 * 两条刻意的取舍:
 *
 * - **只有 `deny` 才算不能**。`approve` 是"可以做,但要走审批",把它也灰掉等于
 *   让人根本提不出那张单 —— 而提单正是审批流程的入口。
 * - **未知一律放行**。矩阵还没到手就把整屏按钮灰掉,只会让人以为自己没权限。
 *   这里只是展示层收敛;服务端对每个请求独立重判,绕过界面也拿不到东西。
 */
export function useCan() {
  const { data: me } = useQuery(meQueryOptions())
  const caps = me?.capabilities

  return (expr: string): boolean => {
    const [capability, tier] = expr.split(':')
    if (!capability || !tier) return true
    const level = caps?.[capability]?.[tier] as CapLevel | undefined
    return level !== 'deny'
  }
}
