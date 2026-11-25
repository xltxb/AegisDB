import { Fragment, memo, useEffect, useLayoutEffect, useMemo, useRef, useState, type HTMLAttributes } from 'react'
import clsx from 'clsx'
import { Search, ChevronDown, ChevronRight, Database, FolderOpen, Table2, PanelLeftClose } from 'lucide-react'
import InspectionBadge from '@/components/common/InspectionBadge'
import { useInspectionStore } from '@/stores/inspection'
import api from '@/api'
import ObjectGroups from './ObjectGroups'
import InvalidObjects from './InvalidObjects'
import ObjectSourceModal, { type ObjectSourceHandle } from './ObjectSourceModal'
import { engineDisplay, engineLabels } from '@/lib/engines'
import { useEnvTierStore } from '@/stores/envtier'
import { useUIStore } from '@/stores/ui'
import { saveBlob } from '@/lib/download'
import { useMountEffect } from '@/hooks/effects'
import { useI18n } from '@/hooks/useI18n'
import { useStateRef } from '@/hooks/useStateRef'
import type { Connection, ConnectionSchema, DbObjects, SchemaDB } from '@/types'
import './DbTree.scoped.css'

interface Props extends Omit<HTMLAttributes<HTMLDivElement>, 'onSelect'> {
  connections: Connection[]
  selectedId: number
  selectedDb?: string
  onSelect: (id: number) => void
  onSelectDb: (id: number, db: string) => void
  onCollapse: () => void
}

const dbKey = (cid: number, name: string) => `${cid}:${name}`
const schemaKey = (cid: number, db: string, sc: string) => `${cid}:${db}:${sc}`
const objKey = (cid: number, db: string, sc = '') => `${cid}:${db}:${sc}`

// risk tag derived from policy
function tag(c: Connection) {
  if (c.policy === 'strict') return { text: 'highTag', cls: 'danger' }
  if (c.policy === 'approve-1') return { text: 'limitedTag', cls: 'warn' }
  return null
}

// The tree groups environment → database type → instance. Those are the two
// questions an operator answers before touching anything ("which cluster", "which
// engine"), and an estate with several engines per cluster cannot be navigated by
// name alone. The axis is still switchable to a flat by-type view, which answers
// a different question: every instance of one engine, across all environments.
type GroupMode = 'env' | 'type'

/** A second-level bucket: one database type, or the whole group in type mode. */
interface TreeChild { key: string; label: string; conns: Connection[] }
/** A first-level section: one environment, or an engine type. */
interface TreeGroup {
  key: string
  label: string
  dot: string
  /** 分层代码,用作分组徽章。按类型分组时没有分层可言,留空。 */
  tierCode?: string
  /** The tier governing this environment, shown on hover — see below. */
  hint: string
  count: number
  /** true → render the children's instances without their own sub-header. */
  flat: boolean
  children: TreeChild[]
}

const MAX_HITS = 80
interface ObjHit { connId: number; inst: string; db: string; schema?: string; table?: string }

/** Types ordered by the engine catalogue, with anything unrecognised last. */
function byCatalogueOrder(a: string, b: string): number {
  const labels = engineLabels()
  const ia = labels.indexOf(a)
  const ib = labels.indexOf(b)
  if (ia === ib) return a.localeCompare(b)
  if (ia < 0) return 1
  if (ib < 0) return -1
  return ia - ib
}

/** Bucket a set of instances by database type, in catalogue order. */
function typeChildren(conns: Connection[]): TreeChild[] {
  const byType: Record<string, Connection[]> = {}
  for (const c of conns) (byType[engineDisplay(c.engine)] ||= []).push(c)
  return Object.keys(byType).sort(byCatalogueOrder)
    .map((k) => ({ key: k, label: k, conns: byType[k] }))
}

/** 把加载回来的库内容(表 / schema)填进那台实例的那个库。 */
function withDb(sc: ConnectionSchema, name: string, loaded: SchemaDB): ConnectionSchema {
  return { ...sc, databases: sc.databases.map((d) => (d.name === name ? { ...d, tables: loaded.tables, schemas: loaded.schemas } : d)) }
}

function DbTree({ connections, selectedId, selectedDb, onSelect, onSelectDb, onCollapse, className, ...rest }: Props) {
  const { t } = useI18n()
  const envtier = useEnvTierStore()
  // Tiers/environments drive the section structure; a failed load leaves the lists
  // empty and every instance falls into the unresolved group rather than vanishing.
  useMountEffect(() => { useEnvTierStore.getState().load().catch(() => {}) })

  // Schema (db → tables) for the selected instance, introspected live from the
  // gateway. Real introspection can take a moment and may fail (bad creds / network),
  // so track a loading flag and surface the returned error instead of a silent empty.
  //
  // 连同实例 id 一起存:换实例的那一次渲染里,旧实例的库不能挂到新实例名下。
  const [picked, setPicked, pickedRef] = useStateRef<{ cid: number; sc: ConnectionSchema } | null>(null)
  const schema = picked?.cid === selectedId ? picked.sc : null
  const schemaOf = (cid: number) => (pickedRef.current?.cid === cid ? pickedRef.current.sc : null)
  const [schemaLoading, setSchemaLoading] = useState(false)
  // 探查过的实例按 id 留着。渲染仍然只用当前选中的那一份(schema),这份缓存是给
  // 搜索用的:库和表不随连接列表下发,只能按实例逐台探查,探查过的没有理由再丢掉。
  // 往某个库里补表时两份一起换(见 fetchDbInto)。
  const [schemaCache, setSchemaCache, cacheRef] = useStateRef<Record<number, ConnectionSchema>>({})
  useLayoutEffect(() => {
    const id = selectedId
    let stale = false
    setPicked(null)
    setSchemaLoading(false)
    if (!id) return
    const cached = cacheRef.current[id]
    if (cached) { setPicked({ cid: id, sc: cached }); return }
    setSchemaLoading(true)
    void (async () => {
      try {
        const sc = await api.connectionSchema(id)
        if (sc && !sc.error) setSchemaCache((c) => ({ ...c, [id]: sc }))
        if (!stale && sc) setPicked({ cid: id, sc })
      } catch {
        if (!stale) setPicked(null)
      } finally {
        if (!stale) setSchemaLoading(false)
      }
    })()
    return () => { stale = true }
  }, [selectedId, setPicked, setSchemaCache, cacheRef])

  const [search, setSearch] = useState('')
  // Starts EMPTY. It used to seed { gli, staging, dev } — three environment codes
  // written into the component, which is exactly what the tier model removes. The
  // defaults are set from the loaded environments instead (see the effect below),
  // and a key that never gets one reads as expanded: a group nothing could file
  // (an environment that no longer resolves) is the one worth showing open.
  const [collapsed, setCollapsed] = useState<Record<string, boolean>>({})

  // Per-database expand state (collapsed by default so a cluster with many databases
  // stays scannable). Keyed by connection:database so switching instances resets it.
  const [dbOpen, setDbOpen, dbOpenRef] = useStateRef<Record<string, boolean>>({})
  const isDbOpen = (cid: number, name: string) => !!dbOpen[dbKey(cid, name)]
  function toggleDb(cid: number, name: string) {
    const k = dbKey(cid, name)
    setDbOpen((o) => ({ ...o, [k]: !o[k] }))
    if (dbOpenRef.current[k]) void loadDbTables(cid, name).then(() => maybeLoadDbObjects(cid, name))
  }
  // Clicking a database selects it as the target and expands it to reveal its tables.
  function selectDb(cid: number, name: string) {
    setDbOpen((o) => ({ ...o, [dbKey(cid, name)]: true }))
    void loadDbTables(cid, name).then(() => maybeLoadDbObjects(cid, name))
    onSelectDb(cid, name)
  }

  // Schema-level (database → schema → tables) collapse, for engines with schemas
  // (PostgreSQL). Keyed by connection:database:schema.
  const [schemaOpen, setSchemaOpen, schemaOpenRef] = useStateRef<Record<string, boolean>>({})
  const isSchemaOpen = (cid: number, db: string, sc: string) => !!schemaOpen[schemaKey(cid, db, sc)]
  function toggleSchema(cid: number, db: string, sc: string) {
    const k = schemaKey(cid, db, sc)
    setSchemaOpen((o) => ({ ...o, [k]: !o[k] }))
    // Objects live per schema for schema-ful engines — load on first expand.
    if (schemaOpenRef.current[k]) void ensureObjects(cid, db, sc)
  }

  // ---- programmable objects (functions / procedures / packages / triggers) ----
  //
  // Loaded lazily per database (flat engines: MySQL by database, Oracle by owner,
  // SQLite single namespace) or per schema (PostgreSQL family), alongside the
  // tables. The scope string sent to the server is the schema when there is one,
  // else the database/owner name — which is exactly the namespace each engine
  // keys its catalog on.
  const [objects, setObjects, objectsRef] = useStateRef<Record<string, DbObjects | null>>({}) // key → loaded set (null = loading)
  function objFor(cid: number, db: string, sc = ''): DbObjects | null | undefined {
    return objects[objKey(cid, db, sc)]
  }
  async function ensureObjects(cid: number, db: string, sc = '') {
    const k = objKey(cid, db, sc)
    if (k in objectsRef.current) return
    setObjects((o) => ({ ...o, [k]: null }))
    try {
      // Schema-level (PostgreSQL family): scope is the schema and the DATABASE
      // must ride along — its catalogs are per database, and without it the
      // server introspects the connection's default one (issue 17 follow-up).
      const r = await api.connectionObjects(cid, sc || db, sc ? db : '')
      setObjects((o) => ({ ...o, [k]: r }))
    } catch (e: any) {
      setObjects((o) => ({ ...o, [k]: { functions: [], procedures: [], packages: [], triggers: [], error: e?.message || t('objLoadFail') } }))
    }
  }
  // A database that turned out flat (no schema layer) carries its objects itself.
  function maybeLoadDbObjects(cid: number, name: string) {
    const d = schemaOf(cid)?.databases?.find((x) => x.name === name)
    if (d && !(d.schemas && d.schemas.length)) void ensureObjects(cid, name)
  }

  // ---- source viewer ----(见 ObjectSourceModal)
  const source = useRef<ObjectSourceHandle>(null)
  function openSource(cid: number, scope: string, type: string, name: string, database = '') {
    void source.current?.open(cid, scope, type, name, database)
  }

  // ---- 程序包下载(右键) ----
  // 一个包拿到 <包名>.pck,多个拿到一个 zip —— 文件名与打包都由服务端定,这里只管存。
  // 下载会记一条导出审计:源码离开了网关,和查看停在屏幕上不是一回事。
  const pkgBusy = useRef(false)
  async function downloadPkgs(cid: number, scope: string, names: string[], database = '') {
    if (pkgBusy.current || !names.length) return
    pkgBusy.current = true
    try {
      const f = await api.downloadPackages(cid, { scope, database, names })
      saveBlob(f.blob, f.filename)
      useUIStore.getState().notify(t('pkgDownloaded', { name: f.filename }), 'success')
    } catch (e) {
      useUIStore.getState().notifyError(e, t('pkgDownloadFail'))
    } finally {
      pkgBusy.current = false
    }
  }

  // 无效对象面板的重取信号:作为它的 key,编译一次就让它重新挂载、重新清点。
  const [invEpoch, setInvEpoch] = useState(0)
  // 无效对象那一块只对 Oracle 有意义 —— 别的引擎没有 INVALID 这个状态。
  const isOracleConn = (cid: number) =>
    /oracle/i.test(connections.find((x) => x.id === cid)?.engine || '')

  // Lazily load a database's contents on first expand. Needed for PostgreSQL, where
  // the top level lists databases (no tables/schemas) introspected on demand; a
  // database already populated (MySQL, or a loaded PG db) is skipped.
  const [dbLoading, setDbLoading, dbLoadingRef] = useStateRef<Record<string, boolean>>({})
  // 把某个库的表灌进那个库。当前选中的那一份与搜索用的缓存一起换,树上看得到的
  // 搜索也立刻搜得到。
  async function fetchDbInto(cid: number, name: string) {
    const sc = await api.connectionSchema(cid, name)
    const loaded = sc.databases.find((x) => x.name === name) || sc.databases[0]
    if (!loaded) return
    setSchemaCache((c) => (c[cid] ? { ...c, [cid]: withDb(c[cid], name, loaded) } : c))
    setPicked((p) => (p?.cid === cid ? { cid, sc: withDb(p.sc, name, loaded) } : p))
    // Auto-expand the sole schema (usually "public") so its tables are visible.
    if (loaded.schemas?.length === 1) {
      const k = schemaKey(cid, name, loaded.schemas[0].name)
      setSchemaOpen((o) => ({ ...o, [k]: true }))
    }
  }
  async function loadDbTables(cid: number, name: string) {
    const k = dbKey(cid, name)
    const d = schemaOf(cid)?.databases?.find((x) => x.name === name)
    if (!d || d.tables?.length || d.schemas?.length || dbLoadingRef.current[k]) return
    setDbLoading((l) => ({ ...l, [k]: true }))
    try {
      await fetchDbInto(cid, name)
    } catch { /* leave empty on failure */ } finally { setDbLoading((l) => ({ ...l, [k]: false })) }
  }

  const [groupMode, setGroupMode] = useState<GroupMode>('env')
  const q = search.trim().toLowerCase()
  const searching = q.length > 0

  // Search matches the instance name, its environment, or its database type. With
  // several production environments and several engines in each, the name alone no
  // longer says where an instance lives or what it is — "hk" and "postgres" are both
  // realistic ways to look for one.
  // envtier 换一份引用 = 清单变了;t 换引用 = 换了语言(envLabel 跟着语言走)。
  const matching = useMemo(() => {
    if (!q) return connections.slice()
    return connections.filter((c) =>
      c.name.toLowerCase().includes(q) ||
      c.env.toLowerCase().includes(q) ||
      envtier.envLabel(c.env).toLowerCase().includes(q) ||
      engineDisplay(c.engine).toLowerCase().includes(q))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [q, connections, envtier, t])

  /**
   * 搜索命中的库、schema 与表。
   *
   * 只在**已经探查过**的实例里找。库和表不是随连接列表一起下发的 —— 每台实例都要
   * 单独连过去问一次,那是对目标库的真实探查。跟着每次敲键对全部实例都来一遍,等于
   * 让一个搜索框去敲遍所有生产集群。
   *
   * 所以:探查过的立刻能搜,没探查过的由人显式点一下(sweepAll),并且界面上要写清
   * 还有几台没搜 —— 一个悄悄只搜了一半的搜索框,和一个会说谎的搜索框是一回事。
   */
  const objectHits = useMemo<ObjHit[]>(() => {
    if (!q) return []
    const out: ObjHit[] = []
    const hit = (h: ObjHit) => { if (out.length < MAX_HITS) out.push(h) }
    for (const c of connections) {
      const sc = schemaCache[c.id]
      if (!sc) continue
      for (const d of sc.databases || []) {
        if (d.name.toLowerCase().includes(q)) hit({ connId: c.id, inst: c.name, db: d.name })
        for (const tb of d.tables || []) {
          if (tb.name.toLowerCase().includes(q)) hit({ connId: c.id, inst: c.name, db: d.name, table: tb.name })
        }
        for (const s of d.schemas || []) {
          if (s.name.toLowerCase().includes(q)) hit({ connId: c.id, inst: c.name, db: d.name, schema: s.name })
          for (const tb of s.tables || []) {
            if (tb.name.toLowerCase().includes(q)) hit({ connId: c.id, inst: c.name, db: d.name, schema: s.name, table: tb.name })
          }
        }
      }
    }
    return out
  }, [q, connections, schemaCache])

  /** 还没探查过的实例 —— 它们的库与表现在搜不到,这一点必须让人看见。 */
  const unscanned = useMemo(() => connections.filter((c) => !schemaCache[c.id]), [connections, schemaCache])

  // 显式地把其余实例也探查一遍。并发限 4:这是真往每台目标库上连,不是本地过滤;
  // 连不上的跳过 —— 它搜不到,但不该拖住其余的。
  const [sweeping, setSweeping, sweepingRef] = useStateRef(false)
  const [sweepDone, setSweepDone] = useState(0)
  const [sweepTotal, setSweepTotal] = useState(0)
  async function sweepAll() {
    if (sweepingRef.current) return
    const queue = unscanned.slice()
    if (!queue.length) return
    setSweeping(true)
    setSweepDone(0)
    setSweepTotal(queue.length)
    const worker = async () => {
      for (;;) {
        const c = queue.shift()
        if (!c) return
        try {
          const sc = await api.connectionSchema(c.id)
          if (sc && !sc.error) setSchemaCache((m) => ({ ...m, [c.id]: sc }))
        } catch { /* 连不上的实例搜不到,如实留在"未搜索"里 */ }
        setSweepDone((n) => n + 1)
      }
    }
    try {
      await Promise.all([worker(), worker(), worker(), worker()])
    } finally {
      setSweeping(false)
    }
  }

  /**
   * 已探查过、但表还没加载的库。
   *
   * 只有 PostgreSQL 家族会出现:它的 information_schema 是**按库**的,实例级那一次
   * 只能列出有哪些库,每个库的表要再连一次。MySQL / Oracle / SQLite 一次就全带回来了。
   *
   * 所以这里不能跟着实例那一趟一起做 —— 一个有 30 个库的 PG 集群就是 30 次连接。
   * 它单独算一笔、单独一个按钮,代价写在按钮旁边,由人决定要不要付。
   */
  const unloadedDbs = useMemo(() => {
    const out: { cid: number; name: string }[] = []
    for (const c of connections) {
      const sc = schemaCache[c.id]
      if (!sc) continue
      for (const d of sc.databases || []) {
        if (!d.tables?.length && !d.schemas?.length) out.push({ cid: c.id, name: d.name })
      }
    }
    return out
  }, [connections, schemaCache])

  const [loadingDbs, setLoadingDbs, loadingDbsRef] = useStateRef(false)
  const [dbDone, setDbDone] = useState(0)
  const [dbTotal, setDbTotal] = useState(0)
  async function loadAllDbs() {
    if (loadingDbsRef.current) return
    const queue = unloadedDbs.slice()
    if (!queue.length) return
    setLoadingDbs(true)
    setDbDone(0)
    setDbTotal(queue.length)
    const worker = async () => {
      for (;;) {
        const it = queue.shift()
        if (!it) return
        try { await fetchDbInto(it.cid, it.name) } catch { /* 连不上的库如实留在"未加载"里 */ }
        setDbDone((n) => n + 1)
      }
    }
    try {
      await Promise.all([worker(), worker(), worker(), worker()])
    } finally {
      setLoadingDbs(false)
    }
  }

  // 点一条结果 = 跳到那台实例的那个库(父级的 openDb 会连带切/开对应的标签页)。
  // 清掉搜索框,并把实例与库都展开 —— 落地之后要能直接看见自己点的是哪一个,而不是
  // 回到一棵全收起的树前面自己再找一遍。命中来自缓存,所以那个库的表已经在手上了。
  function gotoHit(h: ObjHit) {
    setSearch('')
    setInstCollapsed((m) => ({ ...m, [h.connId]: false }))
    setDbOpen((o) => ({ ...o, [dbKey(h.connId, h.db)]: true }))
    onSelectDb(h.connId, h.db)
  }

  // 树底部的统计按调用者实际看得到的实例算(不随搜索变)。从前是原型里写死的
  // 「3 环境 · 9 实例」(黑盒 2026-10 工单 05)。
  const footStats = {
    envs: new Set(connections.map((c) => c.env)).size,
    n: connections.length,
  }

  /**
   * Sections in display order.
   *
   * The top level is the ENVIRONMENT, not the control tier. The tier still decides
   * how the instance is governed, and it still supplies the environment row's
   * colour — losing the "which of these is production" cue was the one thing this
   * layout could not afford — but it no longer occupies a level of its own. Its
   * name is on the row's tooltip for when the colour is not enough.
   *
   * The last group is the catch-all for instances whose environment no longer
   * resolves. Without it they would simply not be listed — the instance would still
   * exist and still be reachable, just invisible here, which is the worst of the
   * available outcomes.
   */
  const groups = useMemo<TreeGroup[]>(() => {
    if (groupMode === 'type') {
      const byType: Record<string, Connection[]> = {}
      for (const c of matching) (byType[engineDisplay(c.engine)] ||= []).push(c)
      return Object.keys(byType).sort(byCatalogueOrder).map((k) => ({
        key: k, label: k, dot: 'info', hint: '', count: byType[k].length, flat: true,
        children: [{ key: k, label: k, conns: byType[k] }],
      }))
    }

    const byEnv: Record<string, Connection[]> = {}
    for (const c of matching) (byEnv[c.env] ||= []).push(c)

    const out: TreeGroup[] = []
    const claimed = new Set<string>()
    // Environments in their own display order, which follows their tier's — so the
    // production clusters still come first.
    for (const tier of envtier.tiers) {
      for (const e of envtier.envsByTier[tier.code] ?? []) {
        claimed.add(e.code)
        const conns = byEnv[e.code] ?? []
        // 不搜索时空环境照列 —— 那是"这套资产长什么样"的一部分;搜索时列出来就
        // 只是一屏"无匹配实例",把真正的命中挤出视线。
        if (searching && !conns.length) continue
        out.push({
          key: e.code,
          label: envtier.envLabel(e.code),
          dot: envtier.dotForEnv(e.code),
          // 徽章上印的是**分层代码**,不是环境名:决定这一组有多危险的是分层,而
          // 环境名("香港生产")长短不一,做徽章会把整行挤走形。
          tierCode: tier.code,
          hint: envtier.tierLabel(tier.code, t),
          count: conns.length,
          flat: false,
          children: typeChildren(conns),
        })
      }
    }

    const orphans = Object.keys(byEnv).filter((k) => !claimed.has(k)).sort()
    for (const k of orphans) {
      out.push({
        key: k,
        label: k,
        dot: 'muted',
        hint: t('treeUnknownEnv'),
        count: byEnv[k].length,
        flat: false,
        children: typeChildren(byEnv[k]),
      })
    }
    return out
  }, [groupMode, matching, searching, envtier, t])

  function toggle(key: string) { setCollapsed((c) => ({ ...c, [key]: !c[key] })) }
  // Searching expands everything: a hit two levels down is useless if the levels
  // above it stay shut.
  function isOpen(key: string) { return searching || !collapsed[key] }

  // Environments on the FIRST tier start expanded, the rest collapsed. That is the
  // old "prod open, everything else closed" default, restated for a world where
  // the top level is environments: every production cluster is open, so adding a
  // second one does not push the first out of view or bury it behind a chevron.
  const { tiers, environments } = envtier
  useLayoutEffect(() => {
    const first = tiers[0]?.code
    setCollapsed((c) => {
      const missing = environments.filter((e) => !(e.code in c))
      if (!missing.length) return c
      const next = { ...c }
      for (const e of missing) next[e.code] = e.tierCode !== first
      return next
    })
  }, [tiers, environments])

  // Per-instance collapse of its database list. Clicking an instance selects it and
  // expands the list; clicking the already-selected instance collapses/expands it.
  const [instCollapsed, setInstCollapsed] = useState<Record<number, boolean>>({})
  const instOpen = (id: number) => id === selectedId && !instCollapsed[id]
  function clickInst(id: number) {
    if (id === selectedId) setInstCollapsed((m) => ({ ...m, [id]: !m[id] }))
    else { setInstCollapsed((m) => ({ ...m, [id]: false })); onSelect(id) }
  }

  // 实例名旁的巡检标记(严重 / 警告才显示;看不了巡检的角色不请求)
  const inspByConn = useInspectionStore((s) => s.byConn)
  useEffect(() => { void useInspectionStore.getState().load() }, [])

  const chev = (open: boolean, size: number, color = 'var(--text-faint)') =>
    (open ? <ChevronDown size={size} color={color} /> : <ChevronRight size={size} color={color} />)

  const tableRow = (c: Connection, scope: string, name: string, database?: string) => (
    <div
      key={name} className="tbl click" title={name + ' · ' + t('objViewDDL')}
      onClick={(e) => { e.stopPropagation(); openSource(c.id, scope, 'table', name, database) }}
    ><Table2 size={12} color="var(--text-faint)" /><span className="nm">{name}</span></div>
  )

  function renderDbBody(c: Connection, d: SchemaDB) {
    if (dbLoading[dbKey(c.id, d.name)]) return <div className="tbl empty">{t('treeLoading')}</div>
    // database → schema → tables (PostgreSQL).
    if (d.schemas && d.schemas.length) {
      return d.schemas.map((sc) => {
        const open = isSchemaOpen(c.id, d.name, sc.name)
        const tables = sc.tables ?? []
        return (
          <Fragment key={sc.name}>
            <div className="sch" onClick={(e) => { e.stopPropagation(); toggleSchema(c.id, d.name, sc.name) }}>
              {chev(open, 11)}
              <FolderOpen size={12} /><span className="nm" title={sc.name}>{sc.name}</span><span className="tcnt">{tables.length}</span>
            </div>
            {open && (
              <div className="ind4">
                {tables.map((tb) => tableRow(c, sc.name, tb.name, d.name))}
                {!tables.length && <div className="tbl empty">{t('treeEmptySchema')}</div>}
                <ObjectGroups
                  objects={objFor(c.id, d.name, sc.name)}
                  onOpen={(ty, nm) => openSource(c.id, sc.name, ty, nm, d.name)}
                  onDownload={(ns) => void downloadPkgs(c.id, sc.name, ns, d.name)}
                />
              </div>
            )}
          </Fragment>
        )
      })
    }
    // database → tables (MySQL / SQLite / Oracle-by-owner)
    const tables = d.tables ?? []
    return (
      <>
        {tables.map((tb) => tableRow(c, d.name, tb.name))}
        {!tables.length && <div className="tbl empty">{t('treeEmptyDb')}</div>}
        <ObjectGroups
          objects={objFor(c.id, d.name)}
          onOpen={(ty, nm) => openSource(c.id, d.name, ty, nm)}
          onDownload={(ns) => void downloadPkgs(c.id, d.name, ns)}
        />
        {/* Oracle 的无效对象:挂在 owner 下,因为编译就是按 owner 走的。
            只在真有 INVALID 时出现(组件内部判断)。 */}
        <InvalidObjects key={invEpoch} cid={c.id} scope={d.name} oracle={isOracleConn(c.id)} />
      </>
    )
  }

  // live schema (db → tables) for the selected, expanded instance
  function renderSchema(c: Connection) {
    if (schemaLoading) return <div className="shint">{t('schemaLoading')}</div>
    if (!schema) return null
    if (schema.error) return <div className="shint err">{schema.error}</div>
    const dbs = schema.databases ?? []
    if (!dbs.length) return <div className="shint">{t('schemaEmpty')}</div>
    return dbs.map((d) => {
      const open = isDbOpen(c.id, d.name)
      const Chev = open ? ChevronDown : ChevronRight
      return (
        <Fragment key={d.name}>
          <div className={clsx('db', d.name === selectedDb && 'sel')} onClick={(e) => { e.stopPropagation(); selectDb(c.id, d.name) }}>
            <Chev size={12} color="var(--text-faint)" onClick={(e) => { e.stopPropagation(); toggleDb(c.id, d.name) }} />
            <FolderOpen size={13} /><span className="nm" title={d.name}>{d.name}</span>
            {d.tables?.length > 0 && <span className="tcnt">{d.tables.length}</span>}
          </div>
          {open && <div className="ind3">{renderDbBody(c, d)}</div>}
        </Fragment>
      )
    })
  }

  return (
    <div {...rest} className={clsx('tree', className)}>
      <div className="head">
        <div className="eyebrow">{t('treeTitle')}<PanelLeftClose className="collapse" size={15} {...{ title: t('treeCollapse') }} onClick={() => onCollapse()} /></div>
        <div className="searchbox"><Search size={14} color="var(--text-faint)" /><input value={search} onChange={(e) => setSearch(e.target.value)} placeholder={t('search')} /></div>
        <div className="gmode">
          <button className={clsx(groupMode === 'env' && 'on')} onClick={() => setGroupMode('env')}>{t('groupByEnv')}</button>
          <button className={clsx(groupMode === 'type' && 'on')} onClick={() => setGroupMode('type')}>{t('groupByType')}</button>
        </div>
      </div>
      <div className="scy body">
        {/* 搜索命中的库与表。实例的匹配仍然走下面那棵树 —— 树是用来"浏览"的,
            而找一张表要的是一份能直接点开的平铺清单。 */}
        {searching && (
          <>
            <div className="reshead">
              <span>{t('treeHitsObj')}</span>
              <span className="cnt">{objectHits.length}{objectHits.length >= MAX_HITS ? '+' : ''}</span>
            </div>
            {objectHits.map((h, i) => {
              const HitIcon = h.table ? Table2 : FolderOpen
              return (
                <div
                  key={i} className="reshit"
                  title={h.inst + ' / ' + h.db + (h.schema ? ' / ' + h.schema : '') + (h.table ? ' / ' + h.table : '')}
                  onClick={() => gotoHit(h)}
                >
                  <HitIcon size={12} color="var(--text-faint)" />
                  <span className="hname">{h.table || h.schema || h.db}</span>
                  <span className="hpath">{h.inst} / {h.db}{h.schema && <> / {h.schema}</>}</span>
                </div>
              )
            })}
            {!objectHits.length && <div className="empty">{t('treeNoObjMatch')}</div>}
            {/* 还有多少台没探查,说清楚,并给一个显式的出口 */}
            {unscanned.length > 0 && (
              <div className="sweep">
                <span className="swtxt">{t('treeUnscanned', { n: unscanned.length })}</span>
                <button className="swbtn" disabled={sweeping} onClick={sweepAll}>
                  {sweeping ? t('treeScanning', { d: sweepDone, n: sweepTotal }) : t('treeScanAll')}
                </button>
              </div>
            )}
            {!unscanned.length && unloadedDbs.length > 0 && (
              <div className="sweep">
                <span className="swtxt">{t('treeUnloadedDbs', { n: unloadedDbs.length })}</span>
                <button className="swbtn" disabled={loadingDbs} onClick={loadAllDbs}>
                  {loadingDbs ? t('treeScanning', { d: dbDone, n: dbTotal }) : t('treeLoadDbs')}
                </button>
              </div>
            )}
            {/* 一个实例都没匹配上时不留空标题:空标题只会让人以为下面那块没加载出来 */}
            {matching.length > 0 && <div className="reshead">{t('treeHitsInst')}</div>}
          </>
        )}

        {groups.map((g) => (
          <Fragment key={g.key}>
            {/* First level: the environment. The dot carries its TIER colour — the
                tier no longer has a row of its own, and losing the "which of these
                is production" cue was the one thing this layout could not afford.
                The tier name is on the tooltip for when the colour is not enough. */}
            <div className={clsx('env', g.dot !== 'danger' && 'muted')} title={g.hint} onClick={() => toggle(g.key)}>
              {chev(isOpen(g.key), 14, 'var(--text-muted)')}
              {g.tierCode
                ? <span className={clsx('envbadge', g.dot)}>{g.tierCode.toUpperCase()}</span>
                : <span className={clsx('d', g.dot)} />}
              <span className="glabel">{g.label}</span>
              <span className="cnt">{g.count}</span>
            </div>
            {isOpen(g.key) && (
              <>
                {!g.children.length && <div className="ind"><div className="empty">{t('treeNoMatch')}</div></div>}
                {g.children.map((ch) => {
                  const sub = g.key + ':' + ch.key
                  return (
                    <Fragment key={ch.key}>
                      {/* Second level: database type. Always shown, even for a single type —
                          which engine an instance speaks decides what its commands may even
                          mean, so it is worth a line rather than being inferred from a name. */}
                      {!g.flat && (
                        <div className="envsub" onClick={() => toggle(sub)}>
                          {chev(isOpen(sub), 12)}
                          {' '}{ch.label}<span className="cnt">{ch.conns.length}</span>
                        </div>
                      )}
                      {(g.flat || isOpen(sub)) && (
                        <div className={clsx('ind', !g.flat && 'ind1')}>
                          {ch.conns.map((c) => {
                            const tg = tag(c)
                            const open = instOpen(c.id)
                            return (
                              <Fragment key={c.id}>
                                <div className={clsx('inst', c.id === selectedId && 'active')} onClick={() => clickInst(c.id)}>
                                  {chev(open, 12)}
                                  <Database size={14} /><span className="nm" title={c.name}>{c.name}</span>
                                  <InspectionBadge report={inspByConn[c.id]} />
                                  {tg && <span className={clsx('tag', tg.cls)}>{t(tg.text)}</span>}
                                </div>
                                {open && <div className="ind2">{renderSchema(c)}</div>}
                              </Fragment>
                            )
                          })}
                          {!ch.conns.length && <div className="empty">{t('treeNoMatch')}</div>}
                        </div>
                      )}
                    </Fragment>
                  )
                })}
              </>
            )}
          </Fragment>
        ))}
      </div>
      <div className="foot" data-testid="tree-foot">{t('treeFooter', footStats)}</div>

      {/* source viewer: one programmable object's definition, read-only */}
      <ObjectSourceModal ref={source} connections={connections} onCompiled={() => setInvEpoch((n) => n + 1)} />
    </div>
  )
}

// 终端视图会为会话状态、拖分隔条等频繁重渲染,树不必跟着
export default memo(DbTree)
