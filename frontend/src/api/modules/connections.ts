import { http, ok, type Envelope } from '@/api/shared'
import type {
  Connection, ConnectionSchema, DbObjects, InvalidObject, MetaSearchResult, MetaSync, MetaTable,
  ObjectSource, RecompileReport,
} from '@/types'

export const connectionsApi = {
  // ---- connections ----
  connections: () => http.get<any, Envelope<Connection[]>>('/connections').then(ok),
  createConnection: (body: Partial<Connection>) =>
    http.post<any, Envelope<Connection>>('/connections', body).then(ok),
  updateConnection: (id: number, body: Partial<Connection>) =>
    http.put<any, Envelope<Connection>>(`/connections/${id}`, body).then(ok),
  toggleConnection: (id: number, status?: string) =>
    http.patch<any, Envelope<Connection>>(`/connections/${id}`, { status }).then(ok),
  setConnectionTags: (id: number, tags: string) =>
    http.patch<any, Envelope<Connection>>(`/connections/${id}`, { tags }).then(ok),
  setConnectionPolicy: (id: number, policy: string) =>
    http.patch<any, Envelope<Connection>>(`/connections/${id}`, { policy }).then(ok),
  tags: () => http.get<any, Envelope<string[]>>('/tags').then(ok),
  testConnection: (id: number) => http.post<any, Envelope<any>>(`/connections/${id}/test`).then(ok),
  connectionSchema: (id: number, database = '') =>
    http.get<any, Envelope<ConnectionSchema>>(
      `/connections/${id}/schema${database ? `?database=${encodeURIComponent(database)}` : ''}`,
    ).then(ok),
  // database targets a specific database on the connection — REQUIRED for the
  // PostgreSQL family whenever the browsed database isn't the connection's
  // default one: its catalogs are per database, so omitting it makes the server
  // introspect the wrong catalog and answer "对象不存在" for everything.
  connectionObjects: (id: number, scope = '', database = '') =>
    http.get<any, Envelope<DbObjects>>(
      `/connections/${id}/objects?scope=${encodeURIComponent(scope)}${database ? `&database=${encodeURIComponent(database)}` : ''}`,
    ).then(ok),
  // Oracle 存储程序重新编译。它是一次 DDL,后端按终端同一套闸门判定并记审计。
  compileObject: (id: number, body: { scope: string; type: string; name: string; database?: string }) =>
    http.post<any, Envelope<{ ok: boolean; report: any }>>(`/connections/${id}/objects/compile`, body),
  // Oracle 无效对象:清点是只读,批量重编译是一批 DDL(整批一起判定,判不过整批不执行)。
  invalidObjects: (id: number, scope = '', database = '') =>
    http.get<any, Envelope<{ items: InvalidObject[]; total: number }>>(
      `/connections/${id}/objects/invalid?scope=${encodeURIComponent(scope)}${database ? `&database=${encodeURIComponent(database)}` : ''}`,
    ).then(ok),
  recompileInvalid: (id: number, body: { scope: string; database?: string; names?: string[] }) =>
    http.post<any, Envelope<{ ok: boolean; report: RecompileReport }>>(`/connections/${id}/objects/recompile`, body),
  objectSource: (id: number, scope: string, type: string, name: string, database = '') =>
    http.get<any, Envelope<ObjectSource>>(
      `/connections/${id}/object-source?scope=${encodeURIComponent(scope)}&type=${encodeURIComponent(type)}&name=${encodeURIComponent(name)}${database ? `&database=${encodeURIComponent(database)}` : ''}`,
    ).then(ok),

  // ---- 元数据缓存 ----
  // 立刻同步一台实例。它**真的会登录那台库**,所以是一次明确的动作(管理员),
  // 而不是定时任务的一部分 —— 定时开关关着的时候这个接口照样可用。
  syncConnectionMetadata: (id: number) =>
    http.post<any, Envelope<{ tables: number; sync: MetaSync | null }>>(
      `/connections/${id}/metadata/sync`,
    ).then(ok),
  // sync 为 null 表示这台实例从没同步过 —— 和"同步过但一张表都没有"不是一回事。
  connectionMetadata: (id: number, database = '') =>
    http.get<any, Envelope<{ tables: MetaTable[]; sync: MetaSync | null }>>(
      `/connections/${id}/metadata${database ? `?database=${encodeURIComponent(database)}` : ''}`,
    ).then(ok),
  // 跨实例检索。范围由服务端按标签收在 SQL 里 —— 够不到那台实例的人,连它有哪些
  // 表和列都不该出现在结果里(表名和列名本身就是信息)。
  metadataSearch: (q: string, limit = 200) =>
    http.get<any, Envelope<MetaSearchResult>>(
      `/metadata/search?q=${encodeURIComponent(q)}&limit=${limit}`,
    ).then(ok),
}

// ---- TanStack Query 绑定 ----
// 连接列表被多个页面共用(执行窗口、导出、后台执行、变更…),放在一个 queryKey 下
// 由 Query 统一缓存与失效,而不是每页各拉一次。
import { queryOptions } from '@tanstack/react-query'

export const connectionsQueryOptions = () =>
  queryOptions({
    queryKey: ['connections'] as const,
    queryFn: connectionsApi.connections,
    staleTime: 60_000,
  })

/**
 * 一台实例上可选的库清单(实时探查,和终端用的是同一个接口)。
 *
 * `retry: false`:探查失败几乎总是"实例不可达 / 没凭据",重试只是把这句话晚十几秒
 * 才说出口;调用方拿空清单退回手填就行。
 */
export const connectionSchemaQueryOptions = (id: number) =>
  queryOptions({
    queryKey: ['connection-schema', id] as const,
    queryFn: () => connectionsApi.connectionSchema(id),
    enabled: id > 0,
    staleTime: 60_000,
    retry: false,
  })

/**
 * 选哪个库:**实例自己填了库就用那个,没填才退回探查到的第一个**。
 *
 * 这条规则曾经被导出页、后台执行页、发布页各抄了一份,而漏掉它的那份提交上去的
 * 导出没有 schema,目标库回一句 "No database selected"。所以它是一条领域规则,
 * 放在连接这个域里只写一次。
 */
export function preferredDatabase(options: string[], own?: string): string {
  return own && options.includes(own) ? own : options[0] || ''
}

/**
 * 一台实例的元数据副本。
 *
 * `staleTime` 给得比连接列表长:这份数据的粒度是"上一次同步",几分钟内重新拉一次
 * 拿到的还是同一张照片。真正需要刷新的时刻是点了「立即同步」之后,那时由 mutation
 * 失效这个 key。
 */
export const connectionMetadataQueryOptions = (id: number, database = '') =>
  queryOptions({
    queryKey: ['connection-metadata', id, database] as const,
    queryFn: () => connectionsApi.connectionMetadata(id, database),
    enabled: id > 0,
    staleTime: 5 * 60_000,
    // 无权限(40300)重试没有意义,只会把同一个 403 再打三遍。
    retry: false,
  })

/** 空查询不发请求:后端对空 q 一律返回空,白跑一趟还会盖掉上一次的结果。 */
export const metadataSearchQueryOptions = (q: string) =>
  queryOptions({
    queryKey: ['metadata-search', q] as const,
    queryFn: () => connectionsApi.metadataSearch(q),
    enabled: q.trim().length > 0,
    staleTime: 60_000,
  })
