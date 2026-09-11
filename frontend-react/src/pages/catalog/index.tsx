import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import clsx from 'clsx'
import {
  Columns3, GitCommitHorizontal, KeyRound, RefreshCw, Search, Table2, VenetianMask,
} from 'lucide-react'
import { connectionsQueryOptions } from '@/api/modules/connections'
import { CODE_FORBIDDEN } from '@/api/http'
import {
  useConnectionMetadata, useMetadataSearch, useSensitiveColumns, useSyncMetadata,
} from '@/hooks/useCatalog'
import { Table, type Column } from '@/components/common/Table'
import { Badge } from '@/components/common/Badge'
import { Button } from '@/components/common/Button'
import { Loading, ErrorState, Empty } from '@/components/common/States'
import type { Connection, MetaColumn, MetaTable, MetaSync, SensitiveColumn } from '@/types'

/** 一张表在缓存里的坐标:连接 / 库 / schema / 表名。MetaColumn 用同一个四元组定位。 */
interface TableRef {
  connectionId: number
  database: string
  schema: string
  name: string
}

function refOf(tb: MetaTable): TableRef {
  return { connectionId: tb.connectionId, database: tb.database, schema: tb.schema, name: tb.name }
}
function sameRef(a: TableRef, b: TableRef): boolean {
  return a.connectionId === b.connectionId && a.database === b.database
    && a.schema === b.schema && a.name === b.name
}
function refKey(r: TableRef): string {
  return `${r.connectionId}/${r.database}/${r.schema}/${r.name}`
}
/** 扁平引擎(MySQL / SQLite / Oracle)没有 schema 这一层,PostgreSQL 家族才有。 */
function qualified(r: { database: string; schema: string; name: string }): string {
  return [r.database, r.schema, r.name].filter(Boolean).join('.')
}

/**
 * 脱敏规则的匹配方式照抄网关(gateway/sensitive.go):表名与列名都**忽略大小写**,
 * 表名 `*` 表示所有表。两边判得不一样的话,这里标出来的 PII 和真正被打码的列就会
 * 是两份清单,而读的人只看得见其中一份。
 */
function isPii(rules: SensitiveColumn[], table: string, column: string): boolean {
  const tb = table.trim().toLowerCase()
  const col = column.trim().toLowerCase()
  return rules.some((r) =>
    r.enabled
    && (r.tableName.trim() === '*' || r.tableName.trim().toLowerCase() === tb)
    && r.columnName.trim().toLowerCase() === col)
}

/** 把一份副本的年龄说成「刚刚 / N 分钟前 / N 小时前 / N 天前」。 */
function ageKeyOf(iso: string): { key: string; n: number } {
  const ms = Date.now() - new Date(iso).getTime()
  if (!Number.isFinite(ms) || ms < 0) return { key: 'catAgeJustNow', n: 0 }
  const min = Math.floor(ms / 60000)
  if (min < 1) return { key: 'catAgeJustNow', n: 0 }
  if (min < 60) return { key: 'catAgeMin', n: min }
  const hr = Math.floor(min / 60)
  if (hr < 24) return { key: 'catAgeHour', n: hr }
  return { key: 'catAgeDay', n: Math.floor(hr / 24) }
}

function fmtStamp(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
}

export default function CatalogPage() {
  const { t } = useTranslation()
  const { data: conns } = useQuery(connectionsQueryOptions())
  const [picked, setPicked] = useState(0)
  const [term, setTerm] = useState('')
  const [q, setQ] = useState('')
  const [sel, setSel] = useState<TableRef | null>(null)

  const list = (conns ?? []) as Connection[]
  const connId = picked || list[0]?.id || 0

  const browse = useConnectionMetadata(connId)
  const search = useMetadataSearch(q)
  const { data: rules } = useSensitiveColumns()
  const sync = useSyncMetadata()

  const searching = q.trim().length > 0
  const tables: MetaTable[] = searching ? (search.data?.tables ?? []) : (browse.data?.tables ?? [])
  const hits: MetaColumn[] = search.data?.columns ?? []

  // 命中的列按所属表归拢 —— 检索"哪些表里有 id_card"时,答案的单位是表,不是列。
  const hitTables = new Map<string, { ref: TableRef; cols: MetaColumn[] }>()
  for (const c of hits) {
    const ref: TableRef = { connectionId: c.connectionId, database: c.database, schema: c.schema, name: c.table }
    const k = refKey(ref)
    const g = hitTables.get(k) ?? { ref, cols: [] }
    g.cols.push(c)
    hitTables.set(k, g)
  }

  const selTable = sel
    ? tables.find((tb) => sameRef(refOf(tb), sel)) ?? null
    : null
  const selCols = sel ? hits.filter((c) => sameRef(
    { connectionId: c.connectionId, database: c.database, schema: c.schema, name: c.table }, sel,
  )).sort((a, b) => a.ordinal - b.ordinal) : []

  function submit(e: FormEvent) {
    e.preventDefault()
    setQ(term)
    setSel(null)
  }

  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1>{t('catTitle')}</h1>
          <p>{t('catSub')}</p>
        </div>
        <div className="grow">
          <Button disabled={!connId || sync.isPending} onClick={() => sync.mutate(connId)}>
            <RefreshCw size={14} className={clsx(sync.isPending && 'spin')} />
            {sync.isPending ? t('catSyncing') : t('catSyncNow')}
          </Button>
        </div>
      </header>

      {/*
        这一整页读的是**元数据缓存**,不是目标库。判定、执行、导出一律仍然走实时的
        那台库 —— 一份可能过期的结构如果被用来做判断,它就从"快"变成了"错"。
      */}
      <div className="notice">
        <GitCommitHorizontal size={15} />
        {t('catCacheNotice')}
      </div>

      <div className="cat-wrap">
        <aside className="cat-side">
          <form className="cat-search" onSubmit={submit}>
            <Search size={14} />
            <input
              value={term}
              placeholder={t('catSearchPh')}
              onChange={(e) => setTerm(e.target.value)}
            />
            {searching && (
              <button type="button" className="cat-clear" onClick={() => { setTerm(''); setQ(''); setSel(null) }}>
                {t('catClear')}
              </button>
            )}
          </form>

          {!searching && (
            <select
              className="cat-conn"
              value={connId}
              onChange={(e) => { setPicked(Number(e.target.value)); setSel(null) }}
            >
              {list.map((c) => <option key={c.id} value={c.id}>{c.env}-{c.name}</option>)}
            </select>
          )}

          <div className="cat-tree">
            {searching
              ? <SearchTree
                  loading={search.isLoading}
                  error={search.error}
                  tables={tables}
                  hitTables={[...hitTables.values()]}
                  sel={sel}
                  onPick={setSel}
                />
              : <BrowseTree
                  connId={connId}
                  loading={browse.isLoading}
                  error={browse.error}
                  tables={tables}
                  syncState={browse.data?.sync ?? null}
                  sel={sel}
                  onPick={setSel}
                />}
          </div>
        </aside>

        <div className="cat-detail">
          {!sel && <Empty hint={t('catPickTable')} />}
          {sel && (
            <TableDetail
              tableRef={sel}
              table={selTable}
              engine={list.find((c) => c.id === sel.connectionId)?.engine ?? ''}
              instance={list.find((c) => c.id === sel.connectionId)?.name ?? ''}
              columns={selCols}
              rules={rules ?? []}
              /* 浏览模式下我们只有表,没有列 —— 见 ColumnTable 的注释。 */
              columnsKnown={searching}
              syncState={sel.connectionId === connId ? browse.data?.sync ?? null : null}
            />
          )}
        </div>
      </div>
      {!list.length && <Empty hint={t('catNoConn')} />}
    </div>
  )
}

/**
 * 四种「空」要分开说。
 *
 * 它们在界面上长得一模一样(左边一棵树,空的),但只有最后一种是正常的:
 * 尚未同步 → 去点同步;连不上 / 无权限 → 去修;真的空 → 这台实例就是空的。
 * 合成一句"暂无数据",四种情况里有三种会被当成第四种。
 */
function BrowseTree({
  connId, loading, error, tables, syncState, sel, onPick,
}: {
  connId: number
  loading: boolean
  error: unknown
  tables: MetaTable[]
  syncState: MetaSync | null
  sel: TableRef | null
  onPick: (r: TableRef) => void
}) {
  const { t } = useTranslation()
  if (!connId) return <Empty hint={t('catNoConn')} />
  if (loading) return <Loading />
  // 无权限:够不到这台实例的人,连它有哪些表都不该看见(表名本身就是信息)。
  if (error) {
    const code = (error as { code?: number }).code
    if (code === CODE_FORBIDDEN) return <Empty hint={t('catNoAccess')} />
    return <ErrorState error={error} />
  }
  if (!syncState) return <Empty hint={t('catNeverSynced')} />
  // err 非空 = 上一次同步失败,下面那些计数是上一次**成功**留下的旧值。
  if (syncState.err) return <Empty hint={`${t('catUnreachable')} — ${syncState.err}`} />
  if (!tables.length) return <Empty hint={t('catTrulyEmpty')} />
  return <TableTree tables={tables} sel={sel} onPick={onPick} />
}

function SearchTree({
  loading, error, tables, hitTables, sel, onPick,
}: {
  loading: boolean
  error: unknown
  tables: MetaTable[]
  hitTables: { ref: TableRef; cols: MetaColumn[] }[]
  sel: TableRef | null
  onPick: (r: TableRef) => void
}) {
  const { t } = useTranslation()
  if (loading) return <Loading />
  if (error) return <ErrorState error={error} />
  if (!tables.length && !hitTables.length) return <Empty hint={t('catNoHit')} />

  return (
    <>
      {/*
        检索有两条腿,而且**互相独立**:一条按表名命中,一条按列名命中。列名那条才是
        这份缓存真正换来的能力 —— 实时探查要回答"哪些表里有 id_card",得把每台实例的
        每张表都问一遍列,那是一轮对生产的全量扫描。
      */}
      {!!hitTables.length && (
        <div className="cat-group">
          <div className="cat-group-head">{t('catHitByColumn')}</div>
          {hitTables.map((g) => (
            <button
              key={refKey(g.ref)}
              className={clsx('cat-node', sel && sameRef(sel, g.ref) && 'on')}
              onClick={() => onPick(g.ref)}
            >
              <Table2 size={13} />
              <span className="cat-node-name">{g.ref.name}</span>
              <span className="cat-node-sub">{g.cols.map((c) => c.name).join(', ')}</span>
            </button>
          ))}
        </div>
      )}
      {!!tables.length && (
        <div className="cat-group">
          <div className="cat-group-head">{t('catHitByTable')}</div>
          <TableTree tables={tables} sel={sel} onPick={onPick} flat />
        </div>
      )}
    </>
  )
}

/** 表清单按库(PostgreSQL 家族再按 schema)分组 —— 一台实例底下常常混着几十个库。 */
function TableTree({
  tables, sel, onPick, flat,
}: {
  tables: MetaTable[]
  sel: TableRef | null
  onPick: (r: TableRef) => void
  flat?: boolean
}) {
  const groups = new Map<string, MetaTable[]>()
  for (const tb of tables) {
    const k = flat ? '' : [tb.database, tb.schema].filter(Boolean).join('.')
    const g = groups.get(k) ?? []
    g.push(tb)
    groups.set(k, g)
  }
  return (
    <>
      {[...groups.entries()].map(([g, items]) => (
        <div className="cat-group" key={g || '_'}>
          {!!g && <div className="cat-group-head">{g}</div>}
          {items.map((tb) => (
            <button
              key={refKey(refOf(tb))}
              className={clsx('cat-node', sel && sameRef(sel, refOf(tb)) && 'on')}
              onClick={() => onPick(refOf(tb))}
            >
              <Table2 size={13} />
              <span className="cat-node-name">{tb.name}</span>
              {tb.kind === 'view' && <span className="cat-node-kind">VIEW</span>}
            </button>
          ))}
        </div>
      ))}
    </>
  )
}

function TableDetail({
  tableRef, table, engine, instance, columns, rules, columnsKnown, syncState,
}: {
  tableRef: TableRef
  table: MetaTable | null
  engine: string
  instance: string
  columns: MetaColumn[]
  rules: SensitiveColumn[]
  columnsKnown: boolean
  syncState: MetaSync | null
}) {
  const { t } = useTranslation()
  // syncedAt 优先取这一行自己的;检索结果里表行可能没进来,退回列行的时间戳。
  const syncedAt = table?.syncedAt || columns[0]?.syncedAt || syncState?.finishedAt || ''
  const age = syncedAt ? ageKeyOf(syncedAt) : null

  const cols: Column<MetaColumn>[] = [
    {
      key: 'name', head: t('catColName'), width: '1.2fr',
      cell: (c) => (
        <div className="cat-colname">
          <span className="cell-strong mono">{c.name}</span>
          {/*
            PII 标记来自**脱敏规则**,不是后端的某个 PII 字段 —— 那个字段不存在,
            凭空造一个会变成一份看起来权威、实际没人维护的清单。命中规则的列,
            就是这套网关眼里会被打码的列。
          */}
          {isPii(rules, tableRef.name, c.name) && (
            <span className="cat-pii" title={t('catPiiHint')}><VenetianMask size={13} />PII</span>
          )}
        </div>
      ),
    },
    {
      key: 'type', head: t('catColType'), width: '1.2fr', mono: true,
      cell: (c) => <>{c.dataType}{c.nullable ? '' : ` ${t('catNotNull')}`}</>,
    },
    {
      key: 'idx', head: t('catColIndex'), width: '1fr',
      // 缓存里只有主键这一个索引信息(MetaColumn.isPk),没有二级索引 —— 所以这一列
      // 只说主键,不说"无索引":我们并不知道有没有。
      cell: (c) => (c.isPk
        ? <Badge tone="accent" icon={<KeyRound size={11} />}>{t('catPk')}</Badge>
        : <span className="cell-sub">—</span>),
    },
    {
      key: 'comment', head: t('catColComment'), width: '1.8fr',
      cell: (c) => c.comment || <span className="cell-sub">—</span>,
    },
  ]

  return (
    <>
      <div className="cat-head">
        <div className="cat-head-main">
          <div className="cat-title">{tableRef.name}</div>
          <div className="cat-path mono">{instance} · {qualified(tableRef)}</div>
        </div>
        <div className="cat-badges">
          {engine && <Badge tone="neutral">{engine}</Badge>}
          {table?.kind && <Badge tone="neutral">{table.kind.toUpperCase()}</Badge>}
          {/*
            原型这里要的是行数,但元数据缓存里**没有行数** —— 它存的是表清单与表结构,
            不是统计信息。编一个数比不显示更糟:这一页的读者会拿它去估算影响面。
            换成已缓存的列数,它是我们真的知道的东西。
          */}
          <Badge tone="accent" icon={<Columns3 size={11} />}>{t('catColCount', { n: columns.length })}</Badge>
        </div>
      </div>

      {/*
        副本的年龄必须写在脸上。一份不说明自己多旧的缓存,读的人会当成现状 ——
        然后拿着三天前的表结构去改今天的库。
      */}
      <div className={clsx('cat-age', !syncedAt && 'unknown')}>
        <GitCommitHorizontal size={14} />
        {syncedAt && age
          ? <>{t('catSyncedAt', { at: fmtStamp(syncedAt) })} · {t(age.key, { n: age.n })}</>
          : t('catSyncedUnknown')}
      </div>

      {table?.comment && <div className="cat-comment">{table.comment}</div>}

      {columnsKnown && columns.length > 0 && (
        <Table columns={cols} rows={columns} rowKey={(c) => c.id} />
      )}
      {/*
        列明细只有在**按列名检索**之后才拿得到。
        后端目前只有两个读接口:一台实例的表清单(不含列),和跨实例的列名检索。
        没有"取这张表的列"这一条,所以浏览模式下诚实的做法是说明为什么没有,而不是
        画一张空表让人以为这张表没有列。
      */}
      {(!columnsKnown || columns.length === 0) && (
        <Empty hint={columnsKnown ? t('catNoColumnHit') : t('catColumnsNeedSearch')} />
      )}
    </>
  )
}
