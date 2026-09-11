import { QueryClient } from '@tanstack/react-query'

/**
 * 服务端状态统一归 TanStack Query(前端文档 §04)。
 *
 * `retry: 1` 而不是默认的 3:这是一个内网管理台,接口失败绝大多数是"没权限"或
 * "目标库连不上",重试三次只是把错误延后十几秒才告诉人。
 */
export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      retry: 1,
      refetchOnWindowFocus: false,
      staleTime: 30_000,
    },
  },
})
