import { useEffect, useRef, useState } from 'react'
import { useQueries, useQueryClient } from '@tanstack/react-query'
import { connectionsApi, connectionSchemaQueryOptions } from '@/api/modules/connections'
import type { Connection, ConnectionSchema, SchemaDB } from '@/types'

/** 一次批量探查的进度。total 为 0 表示没在跑。 */
export interface SweepProgress { running: boolean; done: number; total: number }
const IDLE: SweepProgress = { running: false, done: 0, total: 0 }

/** 真往目标库上连,所以并发压到 4 —— 这不是本地过滤,是同时登录四台生产集群。 */
const CONCURRENCY = 4

/**
 * 库表树的数据面。
 *
 * 库和表**不随连接列表下发**:每台实例都要单独连过去探一次。所以这里有两条规矩:
 *
 *   1. 探过的留着。缓存就是 TanStack Query 自己那一份(`['connection-schema', id]`),
 *      不另起一个 Map —— 两份数据迟早会不一致,而不一致的样子是"树上有这张表、
 *      搜索里没有"。
 *   2. 没探过的不偷偷去探。跟着每次敲键把所有实例探一遍,等于让一个搜索框去敲遍
 *      所有生产集群。要探得由人显式点一下,并且界面上写清还有几台没探。
 */
export function useSchemaTree(connections: Connection[], selectedId: number) {
  const qc = useQueryClient()

  // 只有选中的那台自己去拉;其余的 enabled:false —— 它们仍然订阅同一个 key,
  // 所以「全部探查」把结果写进缓存时,这一层会重渲染,搜索立刻就能用。
  const results = useQueries({
    queries: connections.map((c) => ({
      ...connectionSchemaQueryOptions(c.id),
      enabled: c.id === selectedId,
    })),
  })

  const byId = new Map<number, ConnectionSchema>()
  connections.forEach((c, i) => {
    const d = results[i]?.data
    // 带 error 的那份不算"探到了":它记的是连不上,不是这台实例没有库。
    if (d && !d.error) byId.set(c.id, d)
  })

  const selIdx = connections.findIndex((c) => c.id === selectedId)
  const selected = selIdx >= 0 ? results[selIdx] : undefined

  const [sweep, setSweep] = useState<SweepProgress>(IDLE)
  const [dbSweep, setDbSweep] = useState<SweepProgress>(IDLE)
  const [loadingDbs, setLoadingDbs] = useState<Record<string, boolean>>({})

  // 卸载之后不再 setState。探查是几十秒量级的动作,人完全可能在它跑完之前就离开
  // 这一页 —— React 会为此在控制台里报警,而真正的问题是那几条请求还在跑。
  const alive = useRef(true)
  useEffect(() => {
    alive.current = true
    return () => { alive.current = false }
  }, [])

  const fetchSchema = (id: number, database = '') =>
    qc.fetchQuery({
      queryKey: database ? (['connection-schema', id, database] as const) : (['connection-schema', id] as const),
      queryFn: () => connectionsApi.connectionSchema(id, database),
      staleTime: 60_000,
      retry: false,
    })

  /**
   * 把某个库的表灌回实例那一份 schema 里。
   *
   * 只有 PostgreSQL 家族会走到这里:它的 information_schema 是**按库**的,实例级
   * 那一次只能列出有哪些库。MySQL / Oracle / SQLite 一趟就全带回来了。
   */
  async function loadDbTables(cid: number, name: string) {
    const cur = byId.get(cid)
    const d = cur?.databases.find((x) => x.name === name)
    if (!d || d.tables.length || d.schemas?.length) return
    const key = `${cid}:${name}`
    if (loadingDbs[key]) return
    setLoadingDbs((m) => ({ ...m, [key]: true }))
    try {
      const sc = await fetchSchema(cid, name)
      const loaded = sc.databases.find((x) => x.name === name) || sc.databases[0]
      if (loaded && alive.current) mergeDb(cid, name, loaded)
    } catch {
      // 连不上的库如实留在"未加载"里,不伪造一个空表清单
    } finally {
      if (alive.current) setLoadingDbs((m) => ({ ...m, [key]: false }))
    }
  }

  /** 不可变地把一个库的内容替换掉 —— 直接改缓存里的对象,订阅者不会重渲染。 */
  function mergeDb(cid: number, name: string, loaded: SchemaDB) {
    qc.setQueryData<ConnectionSchema>(['connection-schema', cid], (old) =>
      old
        ? { ...old, databases: old.databases.map((x) => (x.name === name ? { ...x, tables: loaded.tables, schemas: loaded.schemas } : x)) }
        : old,
    )
  }

  const isDbLoading = (cid: number, name: string) => !!loadingDbs[`${cid}:${name}`]

  /** 还没探过的实例 —— 它们的库与表现在搜不到,这件事必须让人看见。 */
  const unscanned = connections.filter((c) => !byId.has(c.id))

  /** 探过、但表还没加载的库(见 loadDbTables 的说明)。 */
  const unloadedDbs: { cid: number; db: string }[] = []
  for (const c of connections) {
    const sc = byId.get(c.id)
    if (!sc) continue
    for (const d of sc.databases || []) {
      if (!d.tables?.length && !d.schemas?.length) unloadedDbs.push({ cid: c.id, db: d.name })
    }
  }

  /** 并发 4 跑完一队活,过程中把进度报出来。连不上的跳过,不拖住其余的。 */
  async function runQueue<T>(items: T[], work: (it: T) => Promise<void>, set: (p: SweepProgress) => void) {
    let done = 0
    const total = items.length
    const queue = items.slice()
    set({ running: true, done: 0, total })
    const worker = async () => {
      for (;;) {
        const it = queue.shift()
        if (!it) return
        try { await work(it) } catch { /* 探不到的如实留在"未探查"里 */ }
        done++
        if (alive.current) set({ running: true, done, total })
      }
    }
    try {
      await Promise.all(Array.from({ length: CONCURRENCY }, worker))
    } finally {
      if (alive.current) set(IDLE)
    }
  }

  async function sweepAll() {
    if (sweep.running || !unscanned.length) return
    await runQueue(unscanned.slice(), async (c) => { await fetchSchema(c.id) }, setSweep)
  }

  async function loadAllDbs() {
    if (dbSweep.running || !unloadedDbs.length) return
    await runQueue(unloadedDbs.slice(), async (it) => {
      const sc = await fetchSchema(it.cid, it.db)
      const loaded = sc.databases.find((x) => x.name === it.db) || sc.databases[0]
      if (loaded && alive.current) mergeDb(it.cid, it.db, loaded)
    }, setDbSweep)
  }

  return {
    /** 已探查到的 schema,按连接 id。 */
    schemas: byId,
    /** 当前选中实例那一份(可能带 error,用来分辨"连不上"与"真的空")。 */
    selectedSchema: selected?.data as ConnectionSchema | undefined,
    selectedLoading: !!selected?.isLoading,
    loadDbTables, isDbLoading,
    unscanned, unloadedDbs,
    sweep, sweepAll,
    dbSweep, loadAllDbs,
  }
}
