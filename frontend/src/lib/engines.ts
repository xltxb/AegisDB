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
  { id: 'aurora', label: 'Aurora MySQL', family: 'mysql' },
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
  // PolarDB / Aurora 各有 PostgreSQL 版:PG 标记先认(与后端 engineFamily 同一顺序)
  if (e.includes('postgre') || e.includes('dws') || e.includes('gauss')) return 'postgres'
  // PolarDB / Aurora(MySQL 版)、TiDB、MariaDB 都走 MySQL 协议
  if (e.includes('mysql') || e.includes('mariadb') || e.includes('tidb') || e.includes('polardb') || e.includes('aurora')) return 'mysql'
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
  // 先认具体产品(Aurora MySQL、PolarDB MySQL 里都含 mysql,不该归到 MySQL 那一组)
  const hit = ENGINES.find((o) => o.id !== 'mysql' && e.includes(o.id)) || ENGINES.find((o) => e.includes(o.id))
    || ENGINES.find((o) => engineFamily(engine) === o.family)
  return hit ? hit.label : engine || '—'
}
