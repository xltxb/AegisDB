import { http, ok, type Envelope } from '../shared'
import type {
  Connection, ConnectionSchema, DbObjects, InvalidObject, MetaSync, MetaTable, ObjectSource, RecompileReport,
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
}
