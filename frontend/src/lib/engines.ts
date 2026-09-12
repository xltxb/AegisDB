// engines — the database types the console offers, and the wire protocol each
// one speaks.
//
// This mirrors the backend's engineFamily (internal/gateway/realdb.go). Keeping
// the two in step matters: the console must never offer an engine the gateway
// cannot execute against, because such a connection saves without complaint and
// then behaves as a SIMULATED one — the operator believes an instance is
// attached when nothing is.
//
// Deliberately absent: MongoDB, Redis, ClickHouse. They are not reachable over
// the gateway's database/sql layer, and more importantly the risk engine parses
// SQL — verb extraction, the high-risk dictionary, the no-WHERE guard and
// statement splitting are all SQL concepts. Offering a non-SQL engine would put
// its commands in front of a parser that cannot read them, and an unrecognised
// verb resolves to the READ capability: `db.orders.drop()` would be judged a
// harmless read. Supporting them needs its own judgement model, not a dropdown
// entry.

/** Wire protocols the gateway can speak; '' means "not drivable". */
export type EngineFamily = 'mysql' | 'postgres' | 'oracle' | 'sqlite' | ''

export interface EngineOption {
  /** Canonical id stored on the connection and accepted by the CSV importer. */
  id: string
  /** What the dropdown shows. */
  label: string
  family: Exclude<EngineFamily, ''>
}

/** The catalogue, in dropdown order. */
export const ENGINES: EngineOption[] = [
  { id: 'mysql', label: 'MySQL', family: 'mysql' },
  { id: 'polardb', label: 'PolarDB', family: 'mysql' },
  { id: 'tidb', label: 'TiDB', family: 'mysql' },
  { id: 'mariadb', label: 'MariaDB', family: 'mysql' },
  { id: 'postgres', label: 'PostgreSQL', family: 'postgres' },
  { id: 'dws', label: 'GaussDB (DWS)', family: 'postgres' },
  { id: 'oracle', label: 'Oracle', family: 'oracle' },
]

/** engineFamily resolves any engine label — canonical id or free text stored on
 *  an existing connection — to its wire protocol. Order matters: a label can
 *  contain more than one product word. */
export function engineFamily(engine: string): EngineFamily {
  const e = (engine || '').toLowerCase().trim()
  if (!e) return ''
  if (e.includes('sqlite')) return 'sqlite'
  if (e.includes('oracle')) return 'oracle'
  // Postgres 标记要在 MySQL 那一串**之前**判 —— 与后端 realdb.go 同样的顺序,理由
  // 也一样:PolarDB 有 MySQL 兼容版和 PostgreSQL 兼容版,**两个的标签里都带 polardb**,
  // 谁先匹配谁就赢。先匹配 polardb 会把 PolarDB for PostgreSQL 判成 MySQL,在后端那
  // 边是送去了连不上的驱动,在这边是元命令翻译成另一个方言的 SQL、对象浏览按错的
  // 协议取库表 —— 而连接本身是通的,于是看起来像"这台库怎么什么都查不到"。
  if (e.includes('postgre') || e.includes('dws') || e.includes('gauss')) return 'postgres'
  // PolarDB(MySQL 版)、TiDB、MariaDB 都说 MySQL 协议。
  if (e.includes('mysql') || e.includes('mariadb') || e.includes('tidb') || e.includes('polardb')) return 'mysql'
  return ''
}

/** Whether the gateway can actually execute against this engine. */
export const isDrivable = (engine: string) => engineFamily(engine) !== ''

/** Dropdown labels, in catalogue order. */
export const engineLabels = () => ENGINES.map((e) => e.label)

/** The catalogue entry whose label matches, for mapping a picked label back. */
export const engineByLabel = (label: string) => ENGINES.find((e) => e.label === label)

/** A display name for grouping: the catalogue label when recognised, else the
 *  stored text so an unknown engine still groups under something meaningful. */
export function engineDisplay(engine: string): string {
  const e = (engine || '').toLowerCase()
  const hit = ENGINES.find((o) => e.includes(o.id)) || ENGINES.find((o) => engineFamily(engine) === o.family)
  return hit ? hit.label : engine || '—'
}
