import { http, ok, type Envelope } from '@/api/shared'
import type {
  APIClient, Pipeline, Project, Release, ReleasePage, SensitiveColumn, ServiceAccount, UserView,
} from '@/types'

export const pipelineApi = {
  // ---- 发布流程 (CI/CD) ----
  pipelines: () => http.get<any, Envelope<Pipeline[]>>('/pipelines').then(ok),
  savePipeline: (id: number, body: Partial<Pipeline>) =>
    id > 0
      ? http.put<any, Envelope<Pipeline>>(`/pipelines/${id}`, body)
      : http.post<any, Envelope<Pipeline>>('/pipelines', body),
  deletePipeline: (id: number) => http.delete<any, Envelope<any>>(`/pipelines/${id}`),
  releases: (scope: 'mine' | 'all' = 'mine', status = '', page = 1, pageSize = 20, projectId = 0) =>
    http.get<any, Envelope<ReleasePage>>(
      `/releases?scope=${scope}&status=${encodeURIComponent(status)}&page=${page}&pageSize=${pageSize}&projectId=${projectId}`,
    ).then(ok),

  // ---- 项目(数据库与升级单的归属) ----
  // 敏感字段:读开放给能进终端的人 —— 被脱敏的列是一串星号,"为什么看不到"
  // 要有个自己查得到的答案。增删改仅管理员(服务端也这么判)。
  sensitiveColumns: () => http.get<any, Envelope<SensitiveColumn[]>>('/sensitive-columns').then(ok),
  saveSensitiveColumn: (id: number, body: Partial<SensitiveColumn>) =>
    id
      ? http.put<any, Envelope<SensitiveColumn>>(`/sensitive-columns/${id}`, body)
      : http.post<any, Envelope<SensitiveColumn>>('/sensitive-columns', body),
  deleteSensitiveColumn: (id: number) => http.delete<any, Envelope<any>>(`/sensitive-columns/${id}`),
  projects: () => http.get<any, Envelope<Project[]>>('/projects').then(ok),
  createProject: (body: { name: string; owner?: string; description?: string }) =>
    http.post<any, Envelope<Project>>('/projects', body),
  updateProject: (id: number, body: Partial<{ name: string; owner: string; description: string }>) =>
    http.patch<any, Envelope<Project>>(`/projects/${id}`, body),
  deleteProject: (id: number) => http.delete<any, Envelope<any>>(`/projects/${id}`),
  /** 把实例下的一个库归到项目(projectId 0 = 取消归属)。归属的单位是库,不是实例。 */
  setDatabaseProject: (id: number, database: string, projectId: number) =>
    http.put<any, Envelope<any>>(`/connections/${id}/database-project`, { database, projectId }),
  release: (id: number) => http.get<any, Envelope<Release>>(`/releases/${id}`).then(ok),
  // raw envelope so the caller can detect 42800 (MFA step-up) and 40300 (denied
  // by the capability matrix), which mean different things to the operator.
  createRelease: (body: {
    title: string; pipelineId: number; connectionId: number; database?: string
    sql?: string; changeType?: string; scriptUploadId?: number; reason?: string; mfaCode?: string
  }) => http.post<any, Envelope<Release>>('/releases', body),
  abortRelease: (id: number) => http.post<any, Envelope<any>>(`/releases/${id}/abort`),
  continueStage: (releaseId: number, stageId: number) =>
    http.post<any, Envelope<any>>(`/releases/${releaseId}/stages/${stageId}/continue`),

  // ---- 开放接口凭据 (external API clients) ----
  // The create call returns the ONLY plaintext copy of the secret; there is no
  // "fetch it again" endpoint because the server keeps a bcrypt hash.
  // 服务账号:凭据背后的机器主体。创建是 admin 行为;停用/角色走常规用户管理。
  serviceAccounts: () => http.get<any, Envelope<ServiceAccount[]>>('/service-accounts').then(ok),
  createServiceAccount: (body: { name: string; roleIds: number[]; tags?: string[]; dept?: string }) =>
    http.post<any, Envelope<UserView>>('/service-accounts', body),
  apiClients: () => http.get<any, Envelope<APIClient[]>>('/api-clients').then(ok),
  createApiClient: (body: { name: string; userId: number; allowIps?: string; scopes?: string[]; pipelineId?: number; enabled?: boolean }) =>
    http.post<any, Envelope<{ client: APIClient; token: string }>>('/api-clients', body),
  updateApiClient: (id: number, body: Partial<{ name: string; userId: number; allowIps: string; scopes: string[]; pipelineId: number; enabled: boolean }>) =>
    http.put<any, Envelope<APIClient>>(`/api-clients/${id}`, body),
  deleteApiClient: (id: number) => http.delete<any, Envelope<any>>(`/api-clients/${id}`),
}

// ---- TanStack Query 绑定(变更工单 / 变更流程) ----
import { queryOptions } from '@tanstack/react-query'
import type { RunStatus } from '@/types'

/**
 * 还会自己变的运行状态。
 *
 * `waiting` 也算:那一步的决定是从审批队列(或飞书卡片、或外部回调)回来的,
 * 服务端的巡检把流程接着往下推 —— 在这里停掉轮询,界面就会在流程早已跑完之后
 * 还一直显示「等待处理」。
 */
export function isLiveRun(status: RunStatus): boolean {
  return status === 'pending' || status === 'running' || status === 'waiting'
}

const POLL_MS = 2500

/** 失效时用的前缀键。把它们写在接口旁边,改了 key 不至于漏掉某个 invalidate。 */
export const pipelineQueryKeys = {
  pipelines: ['pipelines'] as const,
  releases: ['releases'] as const,
  release: ['release'] as const,
}

export const pipelinesQueryOptions = () =>
  queryOptions({
    queryKey: ['pipelines'] as const,
    queryFn: pipelineApi.pipelines,
    staleTime: 60_000,
  })

export const releasesQueryOptions = (
  scope: 'mine' | 'all' = 'mine', status = '', page = 1, pageSize = 50,
) =>
  queryOptions({
    queryKey: ['releases', scope, status, page, pageSize] as const,
    queryFn: () => pipelineApi.releases(scope, status, page, pageSize),
    // 全部落到终态就停,不给一个没人在动的看板留一条每 2.5 秒一次的请求。
    refetchInterval: (q) => (q.state.data?.items.some((r) => isLiveRun(r.status)) ? POLL_MS : false),
  })

export const releaseQueryOptions = (id: number) =>
  queryOptions({
    queryKey: ['release', id] as const,
    queryFn: () => pipelineApi.release(id),
    enabled: id > 0,
    refetchInterval: (q) => (q.state.data && isLiveRun(q.state.data.status) ? POLL_MS : false),
  })
