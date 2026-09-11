import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { connectionsApi, connectionsQueryOptions } from '@/api/modules/connections'
import { useUIStore } from '@/stores/ui'
import type { Connection } from '@/types'

export function useConnections() {
  return useQuery(connectionsQueryOptions())
}

function useConnInvalidate() {
  const qc = useQueryClient()
  const notify = useUIStore((s) => s.notify)
  const { t } = useTranslation()
  return {
    invalidate: () => qc.invalidateQueries({ queryKey: ['connections'] }),
    notify,
    t,
  }
}

/** 新建或编辑时提交的一台实例。`id` 为空 = 新建。 */
export interface ConnectionDraft {
  id?: number
  name: string
  engine: string
  host: string      // host:port —— 服务端拆端口,前端不拆
  env: string
  policy: string
  username: string
  password: string  // 编辑时留空 = 保持原口令
  database: string
  tags: string
}

/** 保存的结局:实例已经落库,但**连过去**这一步可能没成。 */
export interface ConnectionSaveResult {
  conn: Connection
  /** 空 = 连通;非空 = 服务端给的失败原因,原样显示 */
  probeError: string
}

/**
 * 保存一台实例,然后**真的连过去试一次**。
 *
 * 探测不并进保存的成败里:配置已经写进库了,把探测失败报成"保存失败"会让人以为
 * 什么都没发生,于是再填一遍。两件事分两句话说 —— 存下了,但现在连不上,原因在这。
 *
 * 标签走 PATCH:创建/更新接口不收 tags(见 dto.ConnectionCreateReq),它在服务端
 * 是单独一条写路径。
 */
export function useSaveConnection() {
  const { invalidate, notify, t } = useConnInvalidate()
  return useMutation({
    mutationFn: async (d: ConnectionDraft): Promise<ConnectionSaveResult> => {
      const body = {
        name: d.name.trim(), engine: d.engine, host: d.host.trim(), env: d.env,
        policy: d.policy, username: d.username.trim(), password: d.password,
        database: d.database.trim(),
      }
      const conn = d.id
        ? await connectionsApi.updateConnection(d.id, body)
        : await connectionsApi.createConnection(body)
      await connectionsApi.setConnectionTags(conn.id, d.tags.trim())
      try {
        await connectionsApi.testConnection(conn.id)
        return { conn, probeError: '' }
      } catch (e) {
        return { conn, probeError: (e as Error).message || t('connProbeFailed') }
      }
    },
    onSuccess: (r) => {
      notify(t('saved'), 'ok')
      if (r.probeError) notify(`${t('connProbeFailed')}:${r.probeError}`, 'error')
      invalidate()
    },
    onError: (e: Error) => notify(e.message, 'error'),
  })
}

/**
 * 在线 ⇄ 维护。传显式状态而不是让服务端取反 —— 界面上看到的那一档就是要写进去的
 * 那一档,中间没有"服务端此刻认为它是什么"这一层猜测。
 */
export function useToggleConnectionStatus() {
  const { invalidate, notify, t } = useConnInvalidate()
  return useMutation({
    mutationFn: ({ id, status }: { id: number; status: Connection['status'] }) =>
      connectionsApi.toggleConnection(id, status),
    onSuccess: () => { notify(t('saved'), 'ok'); invalidate() },
    onError: (e: Error) => notify(e.message, 'error'),
  })
}

/**
 * 单台测连。成败都用 toast 说,并且**不**写回实例的 status ——
 * status 是运维声明的"这台要不要被用",测连是"此刻通不通",两件事。
 */
export function useTestConnection() {
  const notify = useUIStore((s) => s.notify)
  const { t } = useTranslation()
  return useMutation({
    mutationFn: (c: Connection) => connectionsApi.testConnection(c.id).then(() => c),
    onSuccess: (c) => notify(t('connProbeOk', { name: c.name }), 'ok'),
    onError: (e: Error) => notify(`${t('connProbeFailed')}:${e.message}`, 'error'),
  })
}
