import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import clsx from 'clsx'
import {
  Search, ChevronDown, ChevronRight, Database, FolderOpen, Table2, PanelLeftClose,
} from 'lucide-react'
import { connectionsApi } from '@/api/modules/connections'
import { engineDisplay, engineLabels } from '@/lib/engines'
import { dotForEnv, groupByTier } from '@/lib/envTierLabels'
import { useEnvTier } from '@/hooks/useEnvTier'
import { useSchemaTree } from '@/hooks/useSchemaTree'
import { Segmented } from '@/components/common/Segmented'
import { ObjectGroups } from './ObjectGroups'
import { InvalidObjects } from './InvalidObjects'
import type { SourceTarget } from './SourceViewer'
import type { Connection, DbObjects } from '@/types'

/** 平铺的搜索命中最多列这么多条。再多也不是"找"了,是在滚一份清单。 */
const MAX_HITS = 80

interface ObjHit { connId: number; inst: string; db: string; schema?: string; table?: string }

type GroupMode = 'env' | 'type'
interface TreeChild { key: string; label: string; conns: Connection[] }
interface TreeGroup { key: string; label: string; dot: string; tierCode?: string; hint: string; count: number; flat: boolean; children: TreeChild[] }

/** 引擎目录里的顺序,认不出的排最后。 */
function byCatalogueOrder(a: string, b: string): number {
  const labels = engineLabels()
  const ia = labels.indexOf(a)
  const ib = labels.indexOf(b)
  if (ia === ib) return a.localeCompare(b)
  if (ia < 0) return 1
  if (ib < 0) return -1
  return ia - ib
}

function typeChildren(conns: Connection[]): TreeChild[] {
  const byType: Record<string, Connection[]> = {}
  for (const c of conns) (byType[engineDisplay(c.engine)] ||= []).push(c)
  return Object.keys(byType).sort(byCatalogueOrder).map((k) => ({ key: k, label: k, conns: byType[k] }))
}

/**
 * 一个作用域下的可编程对象。
 *
 * 自成组件,是因为"展开才加载"在 React 里就是"展开才挂载" —— 不需要另外维护一张
 * 「这个作用域请求过没有」的表,那张表迟早会和真实的请求状态对不上。
 */
function ScopeObjects({ cid, db, schema, onOpen }: { cid: number; db: string; schema?: string; onOpen: (type: string, name: string) => void }) {
  const q = useQuery({
    queryKey: ['connection-objects', cid, db, schema ?? ''] as const,
    // 有 schema 的引擎(PostgreSQL 家族):作用域是 schema,而**库必须一起带上** ——
    // 它的目录是按库的,不带就会去探连接的默认库,然后每个对象都答"不存在"。
    queryFn: () => connectionsApi.connectionObjects(cid, schema || db, schema ? db : ''),
    retry: false,
    staleTime: 60_000,
  })
  const objects: DbObjects | null = q.isLoading
    ? null
    : q.data ?? { functions: [], procedures: [], packages: [], triggers: [], error: (q.error as Error)?.message }
  return <ObjectGroups objects={objects} onOpen={onOpen} />
}

/**
 * 库表树:环境 → 数据库类型 → 实例 → 库 →(schema →)表。
 *
 * 前两层回答的是操作员动手之前一定会先答的两个问题("哪个集群"、"什么引擎")——
 * 一套装着好几种引擎的资产,光靠名字是走不通的。分组轴可以切成「按数据库类型」,
 * 那回答的是另一个问题:某一种引擎的全部实例,跨环境。
 *
 * 库与表**不随连接列表下发**,每台实例都要单独探一次(见 useSchemaTree),所以
 * 搜索只在探过的实例里找,并且把"还有几台没探"写在结果下面。
 */
export function DbTree({
  connections, selectedId, selectedDb, onSelect, onSelectDb, onCollapse, onOpenSource,
}: {
  connections: Connection[]
  selectedId: number
  selectedDb?: string
  onSelect: (id: number) => void
  onSelectDb: (id: number, db: string) => void
  onCollapse: () => void
  onOpenSource: (t: SourceTarget) => void
}) {
  const { t } = useTranslation()
  const envtier = useEnvTier()
  const tree = useSchemaTree(connections, selectedId)

  const [search, setSearch] = useState('')
  const [groupMode, setGroupMode] = useState<GroupMode>('env')
  const [collapsed, setCollapsed] = useState<Record<string, boolean>>({})
  const [instCollapsed, setInstCollapsed] = useState<Record<number, boolean>>({})
  const [dbOpen, setDbOpen] = useState<Record<string, boolean>>({})
  const [schemaOpen, setSchemaOpen] = useState<Record<string, boolean>>({})

  const q = search.trim().toLowerCase()
  const searching = q.length > 0

  const isOracle = (cid: number) => /oracle/i.test(connections.find((x) => x.id === cid)?.engine || '')

  // ---- 分组 ----
  const matching = !q
    ? connections
    : connections.filter((c) =>
      c.name.toLowerCase().includes(q) ||
      c.env.toLowerCase().includes(q) ||
      envtier.envLabel(c.env).toLowerCase().includes(q) ||
      engineDisplay(c.engine).toLowerCase().includes(q))

  const groups: TreeGroup[] = []
  if (groupMode === 'type') {
    const byType: Record<string, Connection[]> = {}
    for (const c of matching) (byType[engineDisplay(c.engine)] ||= []).push(c)
    for (const k of Object.keys(byType).sort(byCatalogueOrder)) {
      groups.push({ key: k, label: k, dot: 'info', hint: '', count: byType[k].length, flat: true, children: [{ key: k, label: k, conns: byType[k] }] })
    }
  } else {
    const byEnv: Record<string, Connection[]> = {}
    for (const c of matching) (byEnv[c.env] ||= []).push(c)
    const envsByTier = groupByTier(envtier.tiers, envtier.environments)
    const claimed = new Set<string>()
    for (const tier of envtier.tiers) {
      for (const e of envsByTier[tier.code] ?? []) {
        claimed.add(e.code)
        const conns = byEnv[e.code] ?? []
        // 不搜索时空环境照列 —— 那是"这套资产长什么样"的一部分;搜索时列出来就
        // 只是一屏"无匹配实例",把真正的命中挤出视线。
        if (searching && !conns.length) continue
        groups.push({
          key: e.code,
          label: envtier.envLabel(e.code),
          dot: dotForEnv(e.code, envtier.tiers, envtier.environments),
          // 徽章印的是**分层代码**,不是环境名:决定这一组有多危险的是分层,而
          // 环境名长短不一,做徽章会把整行挤走形。
          tierCode: tier.code,
          hint: envtier.tierLabel(e.code),
          count: conns.length,
          flat: false,
          children: typeChildren(conns),
        })
      }
    }
    // 兜底组:环境已经解析不出来的实例。没有它,那些实例就只是**不被列出** ——
    // 它们照样存在、照样连得上,只是在这里看不见,而那是最坏的一种结果。
    for (const k of Object.keys(byEnv).filter((x) => !claimed.has(x)).sort()) {
      groups.push({ key: k, label: k, dot: 'muted', hint: t('treeUnknownEnv'), count: byEnv[k].length, flat: false, children: typeChildren(byEnv[k]) })
    }
  }

  // 第一档分层上的环境默认展开,其余收起 —— 也就是"生产全开、别的先收着",
  // 于是加了第二套生产集群也不会把第一套挤出视线。搜索时一律展开:命中在两层
  // 之下,而上面两层关着,等于没搜。
  const firstTier = envtier.tiers[0]?.code
  const defaultCollapsed = (key: string) => {
    const env = envtier.environments.find((e) => e.code === key)
    return !!env && env.tierCode !== firstTier
  }
  const isOpen = (key: string) => searching || !(collapsed[key] ?? defaultCollapsed(key))
  const toggle = (key: string) => setCollapsed((m) => ({ ...m, [key]: isOpen(key) }))

  // ---- 搜索命中的库 / schema / 表 ----
  const hits: ObjHit[] = []
  if (q) {
    outer: for (const c of connections) {
      const sc = tree.schemas.get(c.id)
      if (!sc) continue
      for (const d of sc.databases ?? []) {
        if (d.name.toLowerCase().includes(q)) hits.push({ connId: c.id, inst: c.name, db: d.name })
        for (const tb of d.tables ?? []) {
          if (tb.name.toLowerCase().includes(q)) hits.push({ connId: c.id, inst: c.name, db: d.name, table: tb.name })
          if (hits.length >= MAX_HITS) break outer
        }
        for (const s of d.schemas ?? []) {
          if (s.name.toLowerCase().includes(q)) hits.push({ connId: c.id, inst: c.name, db: d.name, schema: s.name })
          for (const tb of s.tables ?? []) {
            if (tb.name.toLowerCase().includes(q)) hits.push({ connId: c.id, inst: c.name, db: d.name, schema: s.name, table: tb.name })
            if (hits.length >= MAX_HITS) break outer
          }
        }
      }
    }
  }

  // 点一条命中 = 落到那台实例的那个库上。清掉搜索框并把路径展开 —— 落地之后要能
  // 直接看见自己点的是哪一个,而不是回到一棵全收起的树前面再找一遍。
  function gotoHit(h: ObjHit) {
    setSearch('')
    setInstCollapsed((m) => ({ ...m, [h.connId]: false }))
    setDbOpen((m) => ({ ...m, [`${h.connId}:${h.db}`]: true }))
    onSelectDb(h.connId, h.db)
  }

  const dbKey = (cid: number, name: string) => `${cid}:${name}`
  const isDbOpen = (cid: number, name: string) => !!dbOpen[dbKey(cid, name)]
  function toggleDb(cid: number, name: string) {
    const k = dbKey(cid, name)
    const next = !dbOpen[k]
    setDbOpen((m) => ({ ...m, [k]: next }))
    if (next) void tree.loadDbTables(cid, name)
  }
  function selectDb(cid: number, name: string) {
    setDbOpen((m) => ({ ...m, [dbKey(cid, name)]: true }))
    void tree.loadDbTables(cid, name)
    onSelectDb(cid, name)
  }

  const schemaKey = (cid: number, db: string, sc: string) => `${cid}:${db}:${sc}`
  // 只有一个 schema(通常是 public)时默认展开:多点一下才能看见表,而那一下没有
  // 在回答任何问题。
  const isSchemaOpen = (cid: number, db: string, sc: string, sole: boolean) =>
    schemaOpen[schemaKey(cid, db, sc)] ?? sole

  function clickInst(id: number) {
    if (id === selectedId) setInstCollapsed((m) => ({ ...m, [id]: !m[id] }))
    else { setInstCollapsed((m) => ({ ...m, [id]: false })); onSelect(id) }
  }
  const instOpen = (id: number) => id === selectedId && !instCollapsed[id]

  /** 策略推出来的风险标。 */
  function tagOf(c: Connection) {
    if (c.policy === 'strict') return { key: 'highTag', cls: 'danger' }
    if (c.policy === 'approve-1') return { key: 'limitedTag', cls: 'warn' }
    return null
  }

  const schema = tree.selectedSchema

  return (
    <aside className="tv-tree">
      <div className="tv-head">
        <div className="tv-eyebrow">
          {t('treeTitle')}
          <button className="tv-collapse" title={t('treeCollapse')} onClick={onCollapse}><PanelLeftClose size={15} /></button>
        </div>
        <div className="tv-searchbox">
          <Search size={14} />
          <input value={search} placeholder={t('search')} onChange={(e) => setSearch(e.target.value)} />
        </div>
        <div className="tv-gmode">
          <Segmented
            value={groupMode}
            options={[{ value: 'env', label: t('groupByEnv') }, { value: 'type', label: t('groupByType') }]}
            onChange={setGroupMode}
          />
        </div>
      </div>

      <div className="tv-body">
        {searching && (
          <>
            {/* 库与表的命中平铺成一份能直接点开的清单;实例的匹配仍然走下面那棵树 ——
                树是用来"浏览"的,而找一张表要的是清单。 */}
            <div className="tv-reshead">
              <span>{t('treeHitsObj')}</span>
              <span className="tv-cnt">{hits.length}{hits.length >= MAX_HITS ? '+' : ''}</span>
            </div>
            {hits.map((h, i) => (
              <div
                key={i}
                className="tv-reshit"
                title={`${h.inst} / ${h.db}${h.schema ? ` / ${h.schema}` : ''}${h.table ? ` / ${h.table}` : ''}`}
                onClick={() => gotoHit(h)}
              >
                {h.table ? <Table2 size={12} /> : <FolderOpen size={12} />}
                <span className="tv-hname">{h.table || h.schema || h.db}</span>
                <span className="tv-hpath">{h.inst} / {h.db}{h.schema ? ` / ${h.schema}` : ''}</span>
              </div>
            ))}
            {!hits.length && <div className="tv-empty">{t('treeNoObjMatch')}</div>}

            {/* 还有多少台没探查,说清楚,并给一个显式的出口 —— 一个悄悄只搜了一半的
                搜索框,和一个会说谎的搜索框是一回事。 */}
            {!!tree.unscanned.length && (
              <div className="tv-sweep">
                <span>{t('treeUnscanned', { n: tree.unscanned.length })}</span>
                <button disabled={tree.sweep.running} onClick={() => void tree.sweepAll()}>
                  {tree.sweep.running ? t('treeScanning', { d: tree.sweep.done, n: tree.sweep.total }) : t('treeScanAll')}
                </button>
              </div>
            )}
            {!tree.unscanned.length && !!tree.unloadedDbs.length && (
              <div className="tv-sweep">
                <span>{t('treeUnloadedDbs', { n: tree.unloadedDbs.length })}</span>
                <button disabled={tree.dbSweep.running} onClick={() => void tree.loadAllDbs()}>
                  {tree.dbSweep.running ? t('treeScanning', { d: tree.dbSweep.done, n: tree.dbSweep.total }) : t('treeLoadDbs')}
                </button>
              </div>
            )}
            {!!matching.length && <div className="tv-reshead">{t('treeHitsInst')}</div>}
          </>
        )}

        {groups.map((g) => (
          <div key={g.key}>
            {/* 第一层是环境。圆点/徽章带的是它所属**分层**的颜色 —— 分层不再单占一层,
                而"这些里面哪个是生产"这条线索是这个布局唯一不能丢的。 */}
            <div className={clsx('tv-env', g.dot !== 'danger' && 'muted')} title={g.hint} onClick={() => toggle(g.key)}>
              {isOpen(g.key) ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
              {g.tierCode
                ? <span className={clsx('tv-envbadge', g.dot)}>{g.tierCode.toUpperCase()}</span>
                : <span className={clsx('tv-gdot', g.dot)} />}
              <span className="tv-glabel">{g.label}</span>
              <span className="tv-cnt">{g.count}</span>
            </div>

            {isOpen(g.key) && (!g.children.length
              ? <div className="tv-ind"><div className="tv-empty">{t('treeNoMatch')}</div></div>
              : g.children.map((ch) => {
                const subKey = `${g.key}:${ch.key}`
                return (
                  <div key={ch.key}>
                    {/* 第二层:数据库类型。只有一种也照列 —— 一台实例说的是哪种方言,
                        决定了它的命令能不能成立,值得一行,而不是从名字里猜。 */}
                    {!g.flat && (
                      <div className="tv-envsub" onClick={() => toggle(subKey)}>
                        {isOpen(subKey) ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
                        {ch.label}<span className="tv-cnt">{ch.conns.length}</span>
                      </div>
                    )}
                    {(g.flat || isOpen(subKey)) && (
                      <div className={clsx('tv-ind', !g.flat && 'tv-ind1')}>
                        {ch.conns.map((c) => (
                          <div key={c.id}>
                            <div className={clsx('tv-inst', c.id === selectedId && 'active')} onClick={() => clickInst(c.id)}>
                              {instOpen(c.id) ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
                              <Database size={14} />{c.name}
                              {tagOf(c) && <span className={clsx('tv-tag', tagOf(c)!.cls)}>{t(tagOf(c)!.key)}</span>}
                            </div>

                            {instOpen(c.id) && (
                              <div className="tv-ind2">
                                {tree.selectedLoading ? <div className="tv-hint">{t('schemaLoading')}</div>
                                  : schema?.error ? <div className="tv-hint err">{schema.error}</div>
                                    : !schema?.databases.length ? <div className="tv-hint">{t('schemaEmpty')}</div>
                                      : schema.databases.map((d) => {
                                        const sole = (d.schemas?.length ?? 0) === 1
                                        return (
                                          <div key={d.name}>
                                            <div className={clsx('tv-db', d.name === selectedDb && 'sel')} onClick={() => selectDb(c.id, d.name)}>
                                              <span className="tv-chev" onClick={(e) => { e.stopPropagation(); toggleDb(c.id, d.name) }}>
                                                {isDbOpen(c.id, d.name) ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
                                              </span>
                                              <FolderOpen size={13} />{d.name}
                                              {!!d.tables.length && <span className="tv-cnt">{d.tables.length}</span>}
                                            </div>

                                            {isDbOpen(c.id, d.name) && (
                                              <div className="tv-ind3">
                                                {tree.isDbLoading(c.id, d.name) ? <div className="tv-hint">{t('treeLoading')}</div>
                                                  : d.schemas?.length
                                                    // 库 → schema → 表(PostgreSQL 家族)
                                                    ? d.schemas.map((sc) => (
                                                      <div key={sc.name}>
                                                        <div className="tv-sch" onClick={() => setSchemaOpen((m) => ({ ...m, [schemaKey(c.id, d.name, sc.name)]: !isSchemaOpen(c.id, d.name, sc.name, sole) }))}>
                                                          {isSchemaOpen(c.id, d.name, sc.name, sole) ? <ChevronDown size={11} /> : <ChevronRight size={11} />}
                                                          <FolderOpen size={12} />{sc.name}<span className="tv-cnt">{sc.tables.length}</span>
                                                        </div>
                                                        {isSchemaOpen(c.id, d.name, sc.name, sole) && (
                                                          <div className="tv-ind4">
                                                            {sc.tables.map((tb) => (
                                                              <div key={tb.name} className="tv-tbl" title={t('objViewDDL')}
                                                                onClick={() => onOpenSource({ cid: c.id, scope: sc.name, type: 'table', name: tb.name, database: d.name, oracle: isOracle(c.id) })}>
                                                                <Table2 size={12} />{tb.name}
                                                              </div>
                                                            ))}
                                                            {!sc.tables.length && <div className="tv-hint">{t('treeEmptySchema')}</div>}
                                                            <ScopeObjects cid={c.id} db={d.name} schema={sc.name}
                                                              onOpen={(ty, nm) => onOpenSource({ cid: c.id, scope: sc.name, type: ty, name: nm, database: d.name, oracle: isOracle(c.id) })} />
                                                          </div>
                                                        )}
                                                      </div>
                                                    ))
                                                    // 库 → 表(MySQL / SQLite / Oracle 按 owner)
                                                    : (
                                                      <>
                                                        {d.tables.map((tb) => (
                                                          <div key={tb.name} className="tv-tbl" title={t('objViewDDL')}
                                                            onClick={() => onOpenSource({ cid: c.id, scope: d.name, type: 'table', name: tb.name, oracle: isOracle(c.id) })}>
                                                            <Table2 size={12} />{tb.name}
                                                          </div>
                                                        ))}
                                                        {!d.tables.length && <div className="tv-hint">{t('treeEmptyDb')}</div>}
                                                        <ScopeObjects cid={c.id} db={d.name}
                                                          onOpen={(ty, nm) => onOpenSource({ cid: c.id, scope: d.name, type: ty, name: nm, oracle: isOracle(c.id) })} />
                                                        {/* Oracle 的无效对象挂在 owner 下 —— 编译就是按 owner 走的。 */}
                                                        <InvalidObjects cid={c.id} scope={d.name} oracle={isOracle(c.id)} />
                                                      </>
                                                    )}
                                              </div>
                                            )}
                                          </div>
                                        )
                                      })}
                              </div>
                            )}
                          </div>
                        ))}
                        {!ch.conns.length && <div className="tv-empty">{t('treeNoMatch')}</div>}
                      </div>
                    )}
                  </div>
                )
              }))}
          </div>
        ))}
      </div>
    </aside>
  )
}
