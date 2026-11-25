// termCompletion — Web 命令行 Tab 补全的数据源(候选从哪来、缓存多久)。
//
// 表清单:整台实例一次取回(与左侧对象树同一个接口),按"会话所在库"缓存。
// 列:按表实时取(GET /connections/:id/columns),按"库 + 表"缓存。
// 缓存 5 分钟过期;执行了会改表结构的语句(CREATE / ALTER / DROP / RENAME / TRUNCATE)或切了库,
// 立刻作废 —— 刚建的表、刚加的列,下一次 Tab 就补得出来。
//
// "限定名"在各引擎指的东西不同:MySQL 系是库、PostgreSQL 系是 schema、Oracle 是 owner。
// 取不到(网络、权限、实例连不上)一律当作没有候选:补全是锦上添花,不该在终端里报错。

import type { ConnectionSchema, SchemaDB } from '@/types'
import type { CompletionSource } from './sqlComplete'
import { SQL_KEYWORDS } from './sqlHighlight'

const TTL_MS = 5 * 60 * 1000

export interface CompletionDeps {
  engine: string
  /** 会话当前所在的库(PostgreSQL 系是库;Oracle 是选中的 schema,可为空) */
  database: () => string
  databases: () => string[]
  metas: string[]
  fetchSchema: (database: string) => Promise<ConnectionSchema>
  fetchColumns: (database: string, table: string) => Promise<string[]>
}

export interface TermCompletionSource extends CompletionSource {
  /** 表结构可能变了:清掉全部缓存 */
  invalidate(): void
}

const isPg = (e: string) => /postgre|dws|gauss/i.test(e)
const isMySQL = (e: string) => /mysql|mariadb|tidb|polardb/i.test(e)

const names = (xs: { name: string }[] | undefined) => (xs || []).map((t) => t.name)

export function makeCompletionSource(d: CompletionDeps): TermCompletionSource {
  const schemaCache = new Map<string, { at: number; p: Promise<ConnectionSchema | null> }>()
  const colCache = new Map<string, { at: number; p: Promise<string[]> }>()
  const fresh = (at: number) => Date.now() - at < TTL_MS

  function schema(): Promise<ConnectionSchema | null> {
    const db = d.database()
    const hit = schemaCache.get(db)
    if (hit && fresh(hit.at)) return hit.p
    const p = d.fetchSchema(db).then((s) => (s && !s.error ? s : null)).catch(() => null)
    schemaCache.set(db, { at: Date.now(), p })
    return p
  }

  async function tables(qualifier: string): Promise<string[]> {
    const s = await schema()
    if (!s) return []
    const dbs: SchemaDB[] = s.databases || []
    const q = qualifier.toLowerCase()
    if (isPg(d.engine)) {
      // 一个库里的各个 schema;不限定时是 public(没有 public 就是第一个)
      const all = dbs.flatMap((x) => x.schemas || [])
      const sc = q ? all.find((x) => x.name.toLowerCase() === q) : all.find((x) => x.name === 'public') ?? all[0]
      return names(sc?.tables)
    }
    if (q) return names(dbs.find((x) => x.name.toLowerCase() === q)?.tables)
    const cur = d.database().toLowerCase()
    const here = dbs.find((x) => x.name.toLowerCase() === cur)
    // 没选库(Oracle 用登录用户自己的 schema)又对不上:给出全部,总比什么都不补强
    return here ? names(here.tables) : [...new Set(dbs.flatMap((x) => names(x.tables)))]
  }

  function columns(table: string): Promise<string[]> {
    const db = d.database()
    const key = `${db}\u0000${table.toLowerCase()}`
    const hit = colCache.get(key)
    if (hit && fresh(hit.at)) return hit.p
    const p = d.fetchColumns(db, table).catch(() => [] as string[])
    colCache.set(key, { at: Date.now(), p })
    return p
  }

  return {
    keywords: SQL_KEYWORDS,
    metas: d.metas,
    get databases() { return d.databases() },
    quote: isMySQL(d.engine) ? '`' : '"',
    tables,
    columns,
    invalidate() {
      schemaCache.clear()
      colCache.clear()
    },
  }
}

/** 这条语句会不会改变表结构(执行后作废补全缓存)。 */
export function isSchemaChange(stmt: string): boolean {
  return /^\s*(create|alter|drop|rename|truncate)\b/i.test(stmt)
}
