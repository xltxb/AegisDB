import { useQuery } from '@tanstack/react-query'
import { connectionSchemaQueryOptions, preferredDatabase } from '@/api/modules/connections'
import type { Connection } from '@/types'

/**
 * 一台实例上可选的库,外加应当预选的那一个。
 *
 * 探查**不抛**:实例不可达、没凭据时返回空清单加一句原因,调用方退回手填。三个
 * 页面原本各写一个"出错就保持默认"的空 catch —— 把这个约定写进返回值,比指望每处
 * 都记得写可靠。
 */
export function useDbOptions(conn: Connection | undefined) {
  const id = conn?.id ?? 0
  const { data, isFetching, error } = useQuery(connectionSchemaQueryOptions(id))
  const options = (data?.databases ?? []).map((d) => d.name)
  return {
    options,
    preferred: preferredDatabase(options, conn?.database),
    // 探查失败分两种:请求本身挂了,和网关连上了但目标库回了错。两种都要能说出口。
    error: (error as Error)?.message || data?.error || '',
    loading: isFetching,
  }
}
