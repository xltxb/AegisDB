import { queryOptions } from '@tanstack/react-query'
import { http, ok, type Envelope } from '@/api/shared'
import type { SensitiveColumn } from '@/types'

/**
 * 数据治理域的接口。
 *
 * 目前只有敏感字段一件事。它在 Vue 版里挂在 `pipeline.ts` 下面(那份文件同时装着
 * 发布流程、项目、凭据),按域重新归位到这里 —— 治理页要的是这一组,不是那三组。
 *
 * **脱敏发生在网关里**:命中规则的列在结果离开服务端之前就已经是星号。这些接口
 * 维护的只是规则本身;前端从头到尾没有"原值"可言,也就没有任何遮盖工作可做。
 */
export const govApi = {
  sensitiveColumns: () =>
    http.get<any, Envelope<SensitiveColumn[]>>('/sensitive-columns').then(ok),
  /** id=0 新建,否则整行覆盖(后端 SaveSensitiveColumn 两条路由同一个 handler)。 */
  saveSensitiveColumn: (id: number, body: Partial<SensitiveColumn>) =>
    (id
      ? http.put<any, Envelope<SensitiveColumn>>(`/sensitive-columns/${id}`, body)
      : http.post<any, Envelope<SensitiveColumn>>('/sensitive-columns', body)
    ).then(ok),
  deleteSensitiveColumn: (id: number) =>
    http.delete<any, Envelope<any>>(`/sensitive-columns/${id}`).then(ok),
}

export const sensitiveColumnsQueryOptions = () =>
  queryOptions({
    queryKey: ['sensitive-columns'] as const,
    queryFn: govApi.sensitiveColumns,
  })

/**
 * JIT 临时授权**没有接口**。
 *
 * 路由表(backend/internal/bootstrap/router.go)里没有任何 jit / temporary-grant
 * 之类的路径,service 与 model 里也没有对应的类型 —— 这不是"还没接上",是后端
 * 压根还没有这个能力。
 *
 * 这个常量存在的意义是让治理页有个东西可以指:页面据此渲染"尚未接入"的空状态,
 * 而**不是**摆一张编好的申请单列表。一张假列表会让人以为审批链已经在跑了。
 */
export const JIT_GRANTS_AVAILABLE: boolean = false
