import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { connectionsApi, connectionsQueryOptions } from '@/api/modules/connections'
import { useUIStore } from '@/stores/ui'
import type { ImportRow } from '@/lib/connectionImport'
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

/**
 * 行内改网关策略。
 *
 * 走的是和批量改策略同一个接口(PATCH 单台),不另开批量端点 —— 批量入口绕过
 * 单台的服务端校验是很常见的漏法。
 */
export function useSetConnectionPolicy() {
  const { invalidate, notify, t } = useConnInvalidate()
  return useMutation({
    mutationFn: ({ id, policy }: { id: number; policy: string }) =>
      connectionsApi.setConnectionPolicy(id, policy),
    onSuccess: () => { notify(t('saved'), 'ok'); invalidate() },
    onError: (e: Error) => notify(e.message, 'error'),
  })
}

/** 打标签。标签决定谁碰得到这台实例(判定层会读),和"归哪个项目"不是一回事。 */
export function useSetConnectionTags() {
  const { invalidate, notify, t } = useConnInvalidate()
  return useMutation({
    mutationFn: ({ id, tags }: { id: number; tags: string }) =>
      connectionsApi.setConnectionTags(id, tags),
    onSuccess: () => { notify(t('saved'), 'ok'); invalidate() },
    onError: (e: Error) => notify(e.message, 'error'),
  })
}

/** 一次元数据同步的结果:同步到多少张表。 */
export interface MetaSyncResult {
  conn: Connection
  tables: number
}

/**
 * 立刻同步一台实例的元数据(表清单 + 列结构)。
 *
 * 它**真的会登录那台库**,所以是一个按钮,而不是打开页面就跑。与定时同步的开关
 * 无关:开关关着时这个按钮照样可用 —— 手动同步本来就是一次明确的决定,也是验证
 * 凭据与网络通不通最快的办法。
 *
 * 0 张表是一个真实的答案(那台库确实空着),不是失败 —— 所以调用方要把它说成
 * 「0 张表」,而不是留空。留空和"从没同步过"读起来一样,两者的处置完全不同。
 */
export function useSyncConnectionMetadata() {
  const qc = useQueryClient()
  const notify = useUIStore((s) => s.notify)
  const { t } = useTranslation()
  return useMutation({
    mutationFn: async (c: Connection): Promise<MetaSyncResult> => {
      const r = await connectionsApi.syncConnectionMetadata(c.id)
      return { conn: c, tables: r.sync?.tables ?? r.tables ?? 0 }
    },
    onSuccess: (r) => {
      notify(t('connMetaSyncOk', { name: r.conn.name, n: r.tables }), 'ok')
      // 缓存的表清单刚被重写,目录页/终端读的是同一个 key。
      qc.invalidateQueries({ queryKey: ['connection-metadata', r.conn.id] })
    },
    // 失败原因来自服务端(连不上 / 没权限 / 未配凭据),照原样显示 —— 换成一句
    // "同步失败"会把三种完全不同的处置压成同一句话。
    onError: (e: Error) => notify(`${t('connMetaSyncBad')}:${e.message}`, 'error'),
  })
}

/** 一次批量巡检的统计。逐台的结论由 `onEach` 实时交给调用方。 */
export interface BatchProbeResult {
  ok: number
  bad: number
}

/** 同时连过去的台数。这是往**目标库**上连,不是本地循环 —— 不限流就是一次自制的连接风暴。 */
const PROBE_CONCURRENCY = 4

/**
 * 批量巡检:逐台真的连过去试一次。
 *
 * 结论**不写回实例的 status**:status 是运维声明的"这台要不要被用",巡检是"此刻
 * 通不通",两件事。所以结果只交给调用方在本次会话里显示,刷新一次就该消失。
 */
export function useBatchProbe() {
  const notify = useUIStore((s) => s.notify)
  const { t } = useTranslation()
  return useMutation({
    mutationFn: async (
      { rows, onEach }: { rows: Connection[]; onEach: (id: number, ok: boolean) => void },
    ): Promise<BatchProbeResult> => {
      const queue = rows.slice()
      let ok = 0
      let bad = 0
      const worker = async () => {
        for (;;) {
          const c = queue.shift()
          if (!c) return
          try {
            await connectionsApi.testConnection(c.id)
            ok++
            onEach(c.id, true)
          } catch {
            bad++
            onEach(c.id, false)
          }
        }
      }
      await Promise.all(Array.from({ length: PROBE_CONCURRENCY }, worker))
      return { ok, bad }
    },
    onSuccess: (r) => notify(t('connCheckDone', { ok: r.ok, bad: r.bad }), r.bad ? 'error' : 'ok'),
    onError: (e: Error) => notify(e.message, 'error'),
  })
}

/** 一次批量导入的结局:成了几条,以及**哪几行**没成。 */
export interface ImportOutcome {
  ok: number
  /** 按行号点名的失败原因。一个总数回答不了"我要去改哪一行"。 */
  failures: string[]
}

/**
 * 逐行创建实例,并对每条新建的实例做一次测连。
 *
 * **串行**是有意的:每一行都是一次特权写入,而逐行的结局远比一次并发爆发之后
 * 的一句汇总失败有用 —— 表格是拿去改的,改的人要知道改哪一行。
 *
 * 校验早就在 `lib/connectionImport` 里整表做完了(整表通过才动手),所以这里
 * 剩下的失败都是服务端才知道的事:环境不存在、实例重名、凭据写不进去。
 * 测连失败不算失败:配置已经存下了,那台库此刻连不上是另一件事。
 */
export function useImportConnections() {
  const { invalidate, notify, t } = useConnInvalidate()
  return useMutation({
    mutationFn: async (
      { rows, onProgress }: { rows: ImportRow[]; onProgress: (done: number) => void },
    ): Promise<ImportOutcome> => {
      let ok = 0
      const failures: string[] = []
      let done = 0
      for (const r of rows) {
        try {
          const created = await connectionsApi.createConnection({
            name: r.name, engine: r.engine, host: r.host, env: r.env,
            policy: r.policy, username: r.username, password: r.password, database: r.database,
          })
          try { await connectionsApi.testConnection(created.id) } catch { /* 尽力而为 */ }
          ok++
        } catch (e) {
          failures.push(t('impRowFailed', { line: r.line, name: r.name, msg: (e as Error).message || '' }))
        }
        onProgress(++done)
      }
      return { ok, failures }
    },
    onSuccess: (r, v) => {
      notify(t('impDone', { ok: r.ok, fail: v.rows.length - r.ok }), r.failures.length ? 'error' : 'ok')
      invalidate()
    },
    onError: (e: Error) => notify(e.message, 'error'),
  })
}

/** 一次批量改策略的结局。失败的要**点名**:没改成的那几台仍然按老策略在跑。 */
export interface BatchPolicyResult {
  ok: number
  failed: string[]
}

/**
 * 批量改网关策略。
 *
 * 逐台走**单台的那个接口**,不另开批量端点 —— 批量入口绕过单台的服务端校验是很
 * 常见的漏法。已经是目标策略的直接算成功,不白发一次写请求。
 */
export function useBatchPolicy() {
  const { invalidate, notify, t } = useConnInvalidate()
  return useMutation({
    mutationFn: async (
      { rows, policy }: { rows: Connection[]; policy: string },
    ): Promise<BatchPolicyResult> => {
      let ok = 0
      const failed: string[] = []
      for (const c of rows) {
        if (c.policy === policy) { ok++; continue }
        try {
          await connectionsApi.setConnectionPolicy(c.id, policy)
          ok++
        } catch {
          failed.push(c.name)
        }
      }
      return { ok, failed }
    },
    onSuccess: (r) => {
      if (r.failed.length) {
        notify(t('connBatchPartial', {
          ok: r.ok, bad: r.failed.length, names: r.failed.slice(0, 3).join(', '),
        }), 'error')
      } else {
        notify(t('connBatchDone', { ok: r.ok }), 'ok')
      }
      invalidate()
    },
    onError: (e: Error) => notify(e.message, 'error'),
  })
}
