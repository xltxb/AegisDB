// connectionImport — parse a CSV sheet of instances into rows the connections
// API can create.
//
// Importing is a bulk privileged action: each row becomes a real instance the
// gateway will proxy commands to. Parsing and validation therefore happen here,
// as pure logic, so they can be tested and so the whole sheet is checked BEFORE
// anything is created — a sheet that is half-applied and then fails leaves the
// estate in a state nobody asked for.
//
// The server still validates every row (environment whitelist, policy whitelist,
// credential encryption). This is the earlier, friendlier gate, not a substitute:
// it reports the offending LINE so a long sheet can be corrected in one pass.

/** Environments the risk controls are defined for; mirrors the backend's validEnvs. */
export const IMPORT_ENVS = ['prod', 'gli', 'staging', 'dev'] as const
/** Gateway policies an instance may carry; mirrors the backend's validPolicies. */
export const IMPORT_POLICIES = ['strict', 'approve-1', 'audit-only'] as const

export interface ImportRow {
  /** 1-based line in the pasted text, so a server-side failure can point at it. */
  line: number
  name: string
  engine: string
  env: string
  host: string
  policy: string
  database: string
  username: string
  password: string
}

export interface ImportError {
  /** 1-based line in the pasted text, header included, so the user can find it. */
  line: number
  message: string
}

export interface ImportParse {
  rows: ImportRow[]
  errors: ImportError[]
}

/** Columns without which a row cannot describe an instance. */
const REQUIRED = ['name', 'engine', 'env', 'host'] as const
/** Every column the importer understands; anything else is ignored. */
const KNOWN = [...REQUIRED, 'policy', 'database', 'username', 'password'] as const

/** A ready-to-edit example, also used as the UI's "download template". */
export const IMPORT_TEMPLATE = [
  'name,engine,env,host,policy,database,username,password',
  'orders-primary,mysql,prod,10.20.3.12:3306,strict,orders,app_ro,change-me',
  'analytics-read,postgres,dev,10.20.3.13:5432,audit-only,analytics,readonly,change-me',
].join('\n')

/** splitCsvLine handles quoted fields and doubled quotes inside them, which any
 *  sheet exported from Excel/Sheets will contain (a password may hold a comma). */
function splitCsvLine(line: string): string[] {
  const out: string[] = []
  let cur = ''
  let quoted = false
  for (let i = 0; i < line.length; i++) {
    const ch = line[i]
    if (quoted) {
      if (ch === '"') {
        if (line[i + 1] === '"') { cur += '"'; i++ } // doubled quote = literal "
        else quoted = false
      } else cur += ch
      continue
    }
    if (ch === '"') { quoted = true; continue }
    if (ch === ',') { out.push(cur); cur = ''; continue }
    cur += ch
  }
  out.push(cur)
  return out.map((f) => f.trim())
}

export function parseConnectionImport(text: string): ImportParse {
  const errors: ImportError[] = []
  const rows: ImportRow[] = []

  const rawLines = text.split(/\r?\n/)
  const firstIdx = rawLines.findIndex((l) => l.trim() !== '')
  if (firstIdx < 0) return { rows, errors } // nothing pasted yet — not an error

  const header = splitCsvLine(rawLines[firstIdx]).map((h) => h.toLowerCase())
  const missing = REQUIRED.filter((c) => !header.includes(c))
  if (missing.length) {
    return {
      rows,
      errors: [{ line: firstIdx + 1, message: `缺少必需的列: ${missing.join(', ')}` }],
    }
  }
  const columnAt = (name: string) => header.indexOf(name)

  for (let i = firstIdx + 1; i < rawLines.length; i++) {
    const raw = rawLines[i]
    if (raw.trim() === '') continue
    const line = i + 1
    const cells = splitCsvLine(raw)
    const get = (name: string) => {
      const at = columnAt(name)
      return at >= 0 ? (cells[at] ?? '') : ''
    }

    const row: ImportRow = {
      line,
      name: get('name'), engine: get('engine').toLowerCase(), env: get('env').toLowerCase(),
      host: get('host'), policy: get('policy').toLowerCase() || 'strict',
      database: get('database'), username: get('username'), password: get('password'),
    }

    const blank = REQUIRED.filter((c) => !String(row[c as keyof ImportRow]).trim())
    if (blank.length) {
      errors.push({ line, message: `第 ${line} 行缺少: ${blank.join(', ')}` })
      continue
    }
    if (!(IMPORT_ENVS as readonly string[]).includes(row.env)) {
      errors.push({ line, message: `第 ${line} 行环境 "${row.env}" 无效,仅支持: ${IMPORT_ENVS.join(' / ')}` })
      continue
    }
    if (!(IMPORT_POLICIES as readonly string[]).includes(row.policy)) {
      errors.push({ line, message: `第 ${line} 行网关策略 "${row.policy}" 无效,仅支持: ${IMPORT_POLICIES.join(' / ')}` })
      continue
    }
    rows.push(row)
  }

  return { rows, errors }
}

/** Columns the importer reads, for the UI's help text. */
export const IMPORT_COLUMNS = KNOWN
