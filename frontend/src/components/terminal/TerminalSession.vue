<script setup lang="ts">
import { ref, watch, onMounted, onUnmounted, onActivated, nextTick } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { Upload, FileCode2, RotateCw, ShieldAlert, FolderCog, ListChecks, ChevronDown } from 'lucide-vue-next'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import ApprovalModal from '@/components/modals/ApprovalModal.vue'
import ScriptScanModal from '@/components/modals/ScriptScanModal.vue'
import api from '@/api'
import { CODE_OK, CODE_INTERCEPTED, CODE_MFA_REQUIRED, CODE_SCRIPT_PATH_UNSET } from '@/api/http'
import { useAuthStore } from '@/stores/auth'
import { useUIStore } from '@/stores/ui'
import { LineEditor } from '@/lib/lineEditor'
import { WsTerminal, type WsStatus } from '@/lib/wsTerminal'
import { ANSI, c, isSelect, synthTable, buildTable, renderTable, renderVertical } from '@/lib/sqlResult'
import type { Connection, Member, ScriptScanResp, ScriptUpload } from '@/types'

// One fully-isolated terminal session bound to a single connection. Each tab
// mounts its own instance → its own xterm, readline history, websocket, and
// in-flight approval/MFA state. Nothing is shared between tabs.
const props = defineProps<{
  conn: Connection
  active: boolean
  conns: Connection[]
  chain: Member[]
  scriptEnabled: boolean
  scriptSavePath: string
  db?: string // target database chosen in the tree (defaults to the connection's)
  gridView?: boolean // when true, result sets go to the HTML grid; terminal prints only the summary
}>()
const emit = defineEmits<{ 'update:risk': ['idle' | 'safe' | 'high']; 'update:wsStatus': [WsStatus]; 'update:db': [string]; result: [{ columns: string[]; rows: string[][] }] }>()

const { t } = useI18n()
const auth = useAuthStore()
const router = useRouter()

const risk = ref<'idle' | 'safe' | 'high'>('idle')
const wsStatus = ref<WsStatus>('connecting')
watch(risk, (v) => emit('update:risk', v))
watch(wsStatus, (v) => emit('update:wsStatus', v))

// ---- approval modal ----
const apOpen = ref(false)
const apCmd = ref('')
const apRisk = ref<'high' | 'mid'>('high')
const apAuditId = ref('AUD-pending')
const apRule = ref('')

// ---- script modal ----
const scOpen = ref(false)
const scScan = ref<ScriptScanResp | null>(null)
const scSubmitted = ref(false)
const enabled = ref(props.scriptEnabled)
watch(() => props.scriptEnabled, (v) => (enabled.value = v))
const fileRef = ref<HTMLInputElement>()
const pathPromptOpen = ref(false)

// ---- target database within the instance ----
const dbOptions = ref<string[]>([])
const targetDb = ref('')
async function loadDbs() {
  let names: string[] = []
  try { const sc = await api.connectionSchema(props.conn.id); names = sc.databases.map((d) => d.name) } catch { /* ignore */ }
  if (props.conn.database && !names.includes(props.conn.database)) names.unshift(props.conn.database)
  dbOptions.value = names
  const pref = props.db || props.conn.database || names[0] || ''
  targetDb.value = names.includes(pref) ? pref : (names[0] || pref)
}
watch(() => props.db, (v) => { if (v) targetDb.value = v })

// ---- xterm + readline + resilient websocket ----
const termEl = ref<HTMLElement>()
let term: Terminal
let fit: FitAddon
let editor: LineEditor
let ws: WsTerminal
let ro: ResizeObserver | null = null
const pendingSql = ref('')
const pendingVertical = ref(false) // render the pending command's result MySQL \G-style
const expandedMode = ref(false)    // psql \x: persistent expanded (vertical) display

// MFA step-up
const mfaOpen = ref(false)
const mfaInput = ref('')
const mfaErr = ref('')
let lastExec = { sql: '', reason: '' }
let pendingMfa: { sql: string; reason: string } | null = null

const out = (line = '') => term.write(line + '\r\n')
const outLines = (arr: string[]) => arr.forEach((l) => out(l))
const cssVar = (name: string, fb: string) =>
  getComputedStyle(document.documentElement).getPropertyValue(name).trim() || fb

const ui = useUIStore()

// xtermTheme follows the app theme. xterm's DEFAULT 16-colour ANSI ramp is tuned
// for dark backgrounds, so on the light theme the result table (cyan numbers, gray
// rules) was near-invisible; each mode gets an explicit ramp with the right
// contrast for its background. Surface/foreground read the live CSS tokens.
function xtermTheme() {
  const light = ui.resolvedTheme() === 'light'
  const base = {
    background: cssVar('--surface-page', light ? '#f6f8fb' : '#0d1117'),
    foreground: cssVar('--text-body', light ? '#232838' : '#d7dee8'),
    cursor: cssVar('--accent-text', light ? '#2553e0' : '#58a6ff'),
    cursorAccent: cssVar('--surface-page', light ? '#ffffff' : '#0d1117'),
    selectionBackground: light ? 'rgba(59,110,246,0.20)' : 'rgba(88,166,255,0.35)',
  }
  if (light) {
    return {
      ...base,
      black: '#232838', red: '#d42a21', green: '#0f9355', yellow: '#a96606',
      blue: '#2553e0', magenta: '#6438f0', cyan: '#0c8da8', white: '#4b5468',
      brightBlack: '#69748b', brightRed: '#ac1f18', brightGreen: '#0c7344',
      brightYellow: '#8a5a06', brightBlue: '#1c41b8', brightMagenta: '#5a2ad6',
      brightCyan: '#106f86', brightWhite: '#232838',
    }
  }
  return {
    ...base,
    black: '#2b313e', red: '#ff6b61', green: '#43d17f', yellow: '#f5b642',
    blue: '#60a5fa', magenta: '#c4b5fd', cyan: '#3fd0e6', white: '#d7dee8',
    brightBlack: '#8b93a7', brightRed: '#ff8f87', brightGreen: '#6ee7a0',
    brightYellow: '#fcd34d', brightBlue: '#93c5fd', brightMagenta: '#d8c9ff',
    brightCyan: '#8beef8', brightWhite: '#f4f7fc',
  }
}

// Repaint the live terminal when the app theme is switched.
watch(() => ui.theme, () => {
  if (term) {
    term.options.theme = xtermTheme()
    term.refresh(0, term.rows - 1)
  }
})

// The prompt shows the current database (instance/db ❯) so every command in the
// scrollback records which database it ran against.
function promptText() {
  const db = targetDb.value
  const head = db
    ? c(ANSI.blue, props.conn.name) + c(ANSI.gray, '/') + c(ANSI.cyan, db)
    : c(ANSI.blue, props.conn.name)
  return head + ' ' + c(ANSI.cyan, '❯') + ' '
}
function promptLen() { return props.conn.name.length + (targetDb.value ? targetDb.value.length + 1 : 0) + 3 }
function contPrompt() { return ' '.repeat(Math.max(0, promptLen() - 2)) + c(ANSI.gray, '· ') }

// A prominent, env-coloured "you are operating on X" caution printed into the
// terminal itself (bold; red for PROD with an explicit "proceed with caution").
function cautionLine() {
  const cn = props.conn
  const env = cn.env.toUpperCase()
  if (cn.env === 'prod') return ANSI.bold + ANSI.red + t('termCautionProd', { env, name: cn.name }) + ANSI.reset
  if (cn.env === 'staging') return ANSI.bold + ANSI.yellow + t('termCautionStaging', { env, name: cn.name }) + ANSI.reset
  return ANSI.bold + ANSI.green + t('termCautionOther', { env, name: cn.name }) + ANSI.reset
}

function banner() {
  const cn = props.conn
  const me = auth.me
  out(cautionLine())
  out(c(ANSI.gray, t('termConnected', { conn: `${cn.env}-${cn.name}`, role: cn.defaultRole, policy: cn.policy, user: me?.name || '' })))
  out(c(ANSI.gray, t('termHelpLine', { bs: '\\' })))
}

function fitNow() { try { fit.fit() } catch { /* ignore */ } }

onMounted(() => {
  loadDbs()
  term = new Terminal({
    fontFamily: cssVar('--font-mono', 'ui-monospace, Menlo, Consolas, monospace'),
    fontSize: 14,
    lineHeight: 1.4,
    cursorBlink: true,
    theme: xtermTheme(),
  })
  fit = new FitAddon()
  term.loadAddon(fit)
  term.open(termEl.value!)

  // Select-to-copy: mirror the terminal selection into the clipboard automatically,
  // like a native terminal. Mouse/keyboard selection is a user gesture, so
  // writeText is allowed; silently no-op when the clipboard API is unavailable
  // (e.g. a non-HTTPS origin). lastCopied avoids redundant writes while dragging.
  let lastCopied = ''
  term.onSelectionChange(() => {
    const sel = term.getSelection()
    if (!sel || sel === lastCopied) return
    lastCopied = sel
    if (navigator.clipboard?.writeText) navigator.clipboard.writeText(sel).catch(() => {})
  })

  editor = new LineEditor(term, {
    prompt: promptText,
    promptLen,
    contPrompt,
    contPromptLen: promptLen,
    onSubmit: handleSubmit,
    onInterrupt: () => { /* in-flight result still arrives */ },
  })

  ws = new WsTerminal({
    url: () => {
      const proto = location.protocol === 'https:' ? 'wss' : 'ws'
      const base = import.meta.env.VITE_WS_BASE || `${proto}://${location.host}/api/v1`
      return `${base}/terminal/ws`
    },
    // Carry the JWT in the Sec-WebSocket-Protocol header (not the URL) so it
    // never appears in server access logs or Referer headers.
    protocols: () => ['vela-token', localStorage.getItem('vela_token') || ''],
    onMessage: onWsMessage,
    onStatus: (s) => (wsStatus.value = s),
    // Repeated handshake failures MIGHT mean an expired session — but a plain
    // WebSocket close can't be told apart from a backend restart / network blip.
    // Probe a REST endpoint: a 401 confirms the session is gone (the http
    // interceptor then redirects to login); anything else is transient, so keep
    // retrying instead of logging the user out (M16/R7).
    onAuthError: async () => {
      try {
        await api.me()
        ws.connect() // session still valid → resume the terminal socket
      } catch (e: any) {
        if (e?.response?.status !== 401) ws.connect() // network/5xx → not an auth failure, keep trying
        // 401 → api/http.ts already cleared the token and redirected to /login
      }
    },
  })

  banner()
  editor.start()
  ws.connect()

  ro = new ResizeObserver(() => fitNow())
  ro.observe(termEl.value!)
  if (props.active) nextTick(() => { fitNow(); term.focus() })
})

// A hidden xterm can't measure itself; (re)fit and focus when this tab is shown.
watch(() => props.active, (a) => { if (a) nextTick(() => { fitNow(); term.focus() }) })

// Re-entering the terminal route (kept alive) re-inserts the DOM: refit the
// active session so xterm matches the restored container size.
onActivated(() => { if (props.active) nextTick(() => { fitNow(); term.focus() }) })

onUnmounted(() => {
  ro?.disconnect()
  ws?.close()
  term?.dispose()
})

// ---- command dispatch ----
// parseUseDb recognises a `USE <db>` statement (optionally quoted) and returns the
// database name, or null. Backend execution targets the database per-command via
// the DSN, so a server-side USE would not persist across the pool — instead we
// switch the terminal's target database client-side (updating the prompt and every
// subsequent command).
function parseUseDb(sql: string): string | null {
  const m = /^\s*use\s+[`"']?([A-Za-z0-9_$-]+)[`"']?\s*$/i.exec(sql)
  return m ? m[1] : null
}

function switchDb(db: string) {
  const canon = dbOptions.value.find((d) => d.toLowerCase() === db.toLowerCase())
  if (dbOptions.value.length && !canon) {
    out(c(ANSI.yellow, t('termUnknownDb', { db })))
    risk.value = 'safe'
    return
  }
  targetDb.value = canon || db
  emit('update:db', targetDb.value) // mirror onto the tab so the tree highlights it
  out(c(ANSI.green, t('termSwitchedDb', { db: targetDb.value })))
  risk.value = 'safe'
}

// translateMetaSql maps a client backslash meta-command (psql \dt, MySQL-client
// \l, etc.) to a SQL query the driver can run — engine-aware for PostgreSQL/DWS,
// MySQL/TiDB and Oracle so users can keep their familiar client shortcuts. Returns
// null for commands handled locally (\?, \clear, \c/\u <db>, \conns). ident chars
// are sanitised so a name can't break the literal (the user could run any SQL
// directly anyway).
function translateMetaSql(cmd: string, engine: string): string | null {
  const m = cmd.trim().match(/^\\([a-z]+)\+?\s*(.*)$/i)
  if (!m) return null
  const verb = m[1].toLowerCase()
  const arg = m[2].trim().replace(/;$/, '')
  const ident = (s: string) => s.replace(/["'`]/g, '').replace(/[^A-Za-z0-9_$.]/g, '')
  const isPG = /postgre|dws|gauss/i.test(engine)
  const isOra = /oracle/i.test(engine)

  if (isPG) {
    const notSys = `NOT IN ('pg_catalog','information_schema')`
    switch (verb) {
      case 'l': case 'list': return `SELECT datname AS "Name" FROM pg_database WHERE datistemplate=false ORDER BY 1`
      case 'dn': return `SELECT schema_name AS "Name" FROM information_schema.schemata WHERE schema_name ${notSys} ORDER BY 1`
      case 'dt': return `SELECT table_schema AS "Schema", table_name AS "Name" FROM information_schema.tables WHERE table_type='BASE TABLE' AND table_schema ${notSys} ORDER BY 1,2`
      case 'dv': return `SELECT table_schema AS "Schema", table_name AS "Name" FROM information_schema.views WHERE table_schema ${notSys} ORDER BY 1,2`
      case 'di': return `SELECT schemaname AS "Schema", indexname AS "Name", tablename AS "Table" FROM pg_indexes WHERE schemaname ${notSys} ORDER BY 1,2`
      case 'ds': return `SELECT sequence_schema AS "Schema", sequence_name AS "Name" FROM information_schema.sequences WHERE sequence_schema ${notSys} ORDER BY 1,2`
      case 'df': return `SELECT routine_schema AS "Schema", routine_name AS "Name", data_type AS "Result" FROM information_schema.routines WHERE routine_schema ${notSys} ORDER BY 1,2`
      case 'du': case 'dg': return `SELECT rolname AS "Role", rolsuper AS "Super", rolcanlogin AS "Login" FROM pg_roles ORDER BY 1`
      case 'dp': case 'z': return `SELECT table_schema AS "Schema", table_name AS "Name", grantee AS "Grantee", privilege_type AS "Privilege" FROM information_schema.role_table_grants WHERE table_schema ${notSys} ORDER BY 1,2`
      case 'conninfo': return `SELECT current_database() AS "Database", current_user AS "User", version() AS "Version"`
      case 'd':
        if (!arg) return `SELECT table_schema AS "Schema", table_name AS "Name" FROM information_schema.tables WHERE table_schema ${notSys} ORDER BY 1,2`
        { const p = ident(arg).split('.'); const tbl = p.pop() || ''; const sch = p.length ? ` AND table_schema='${p[0]}'` : ''
          return `SELECT column_name AS "Column", data_type AS "Type", is_nullable AS "Nullable", column_default AS "Default" FROM information_schema.columns WHERE table_name='${tbl}'${sch} ORDER BY ordinal_position` }
    }
    return null
  }

  if (isOra) {
    const notSys = `NOT IN ('SYS','SYSTEM','OUTLN','XDB','MDSYS','CTXSYS','DBSNMP','WMSYS','APPQOSSYS')`
    switch (verb) {
      case 'l': case 'list': case 'dn': return `SELECT username AS "Name" FROM all_users ORDER BY 1`
      case 'dt': return `SELECT owner AS "Owner", table_name AS "Name" FROM all_tables WHERE owner ${notSys} ORDER BY 1,2`
      case 'dv': return `SELECT owner AS "Owner", view_name AS "Name" FROM all_views WHERE owner ${notSys} ORDER BY 1,2`
      case 'di': return `SELECT owner AS "Owner", index_name AS "Name", table_name AS "Table" FROM all_indexes WHERE owner ${notSys} ORDER BY 1,2`
      case 'ds': return `SELECT sequence_owner AS "Owner", sequence_name AS "Name" FROM all_sequences WHERE sequence_owner ${notSys} ORDER BY 1,2`
      case 'du': return `SELECT username AS "User", account_status AS "Status" FROM all_users ORDER BY 1`
      case 'conninfo': return `SELECT SYS_CONTEXT('USERENV','DB_NAME') AS "Database", USER AS "User" FROM DUAL`
      case 'd':
        if (!arg) return `SELECT owner AS "Owner", table_name AS "Name" FROM all_tables WHERE owner ${notSys} ORDER BY 1,2`
        return `SELECT column_name AS "Column", data_type AS "Type", nullable AS "Nullable" FROM all_tab_columns WHERE table_name=UPPER('${ident(arg)}') ORDER BY column_id`
    }
    return null
  }

  // MySQL / TiDB
  switch (verb) {
    case 'l': case 'list': case 'dn': return 'SHOW DATABASES'
    case 'dt': return `SHOW FULL TABLES WHERE Table_type='BASE TABLE'`
    case 'dv': return `SHOW FULL TABLES WHERE Table_type='VIEW'`
    case 'du': case 'dg': return 'SELECT User, Host FROM mysql.user ORDER BY 1,2'
    case 'conninfo': return 'SELECT DATABASE() AS `Database`, CURRENT_USER() AS `User`, VERSION() AS `Version`'
    case 'di': return arg ? `SHOW INDEX FROM \`${ident(arg)}\`` : null
    case 'd': return arg ? `SHOW COLUMNS FROM \`${ident(arg)}\`` : 'SHOW TABLES'
  }
  return null
}

async function handleSubmit(stmt: string) {
  // \G (vertical) / \g (horizontal) are MySQL client display terminators, not SQL —
  // strip them before sending and remember whether to render the result vertically.
  const trimmed = stmt.trim()
  pendingVertical.value = /\\G\s*$/.test(trimmed)
  const raw = trimmed.replace(/\\[gG]\s*$/, '').replace(/;+\s*$/, '').trim()
  if (raw.startsWith('\\')) {
    // psql-style DB meta-commands (\dt, \d, \dn) translate to a query the driver
    // understands; the rest are local terminal meta-commands.
    const sql = translateMetaSql(raw, props.conn.engine)
    if (sql) {
      if (sendExec(sql, '') === 'ws') return
      try { handleExecEnv(await api.exec(props.conn.id, sql, '', '', targetDb.value), sql, '') }
      catch { out(c(ANSI.red, t('termExecFail'))); editor.resume() }
      return
    }
    metaCommand(raw); editor.resume(); return
  }
  if (!raw) { editor.resume(); return }
  const useDb = parseUseDb(raw)
  if (useDb) { switchDb(useDb); editor.resume(); return }
  try {
    const r = await api.riskCheck(props.conn.id, raw)
    if (r.action === 'deny') {
      out(c(ANSI.red, t('termDeniedCap')))
      risk.value = 'safe'
      editor.resume()
      return
    }
    if (r.requiresApproval) {
      risk.value = 'high'
      apCmd.value = raw
      apRisk.value = r.risk === 'high' ? 'high' : 'mid'
      apRule.value = r.matchedRule || ''
      apAuditId.value = t('auditPending')
      apOpen.value = true
      out(c(ANSI.yellow, t('termHitRule', { rule: r.matchedRule || '-' })))
      return
    }
    risk.value = 'safe'
    if (sendExec(raw, '') === 'ws') return
    const env = await api.exec(props.conn.id, raw)
    handleExecEnv(env, raw, '')
  } catch {
    out(c(ANSI.red, t('termExecFail')))
    editor.resume()
  }
}

function metaCommand(raw: string) {
  const parts = raw.slice(1).trim().split(/\s+/)
  const cmd = (parts[0] || '').toLowerCase()
  const arg = parts.slice(1).join(' ').replace(/;$/, '').replace(/["'`]/g, '')
  if (cmd === '?' || cmd === 'h' || cmd === 'help') {
    printMetaHelp()
  } else if (cmd === 'conns' || cmd === 'connlist') {
    outLines(props.conns.map((cn) => `  ${String(cn.id).padStart(3)}  ${cn.env}-${cn.name}  ${c(ANSI.gray, cn.defaultRole)}`))
  } else if (cmd === 'clear') {
    term.clear()
  } else if (cmd === 'c' || cmd === 'connect' || cmd === 'u' || cmd === 'use') {
    // psql \c <db> / MySQL-client \u <db>: switch the target database; no arg = clear.
    if (arg) switchDb(arg)
    else term.clear()
  } else if (cmd === 'x') {
    // psql \x: toggle persistent expanded (vertical) display. \x on|off set it.
    const a = arg.toLowerCase()
    expandedMode.value = a === 'on' || a === 'auto' ? true : a === 'off' ? false : !expandedMode.value
    out(c(ANSI.gray, t(expandedMode.value ? 'metaExpandOn' : 'metaExpandOff')))
  } else {
    out(c(ANSI.gray, t('termUnknownCmd', { bs: '\\', cmd })))
  }
}

// printMetaHelp lists the client backslash commands, showing engine-specific ones
// only for the matching engine. Command tokens are built in JS (real backslashes)
// so they dodge vue-i18n's message escaping; only the descriptions are translated.
function printMetaHelp() {
  const engine = props.conn.engine
  const isPG = /postgre|dws|gauss/i.test(engine)
  const isOra = /oracle/i.test(engine)
  const pad = (s: string) => (s + '            ').slice(0, 12)
  const line = (token: string, descKey: string) => '  ' + c(ANSI.cyan, pad(token)) + c(ANSI.gray, t(descKey as any))
  const lines = [c(ANSI.bold, t('termMetaTitle'))]
  lines.push(line('\\?', 'metaHelpHelp'))
  lines.push(line('\\conns', 'metaHelpConns'))
  lines.push(line('\\c <db>', 'metaHelpUse'))
  lines.push(line('\\clear', 'metaHelpClear'))
  lines.push(line('\\l', 'metaHelpL'))
  lines.push(line('\\dt', 'metaHelpDt'))
  lines.push(line('\\dv', 'metaHelpDv'))
  lines.push(line('\\dn', 'metaHelpDn'))
  lines.push(line('\\di', 'metaHelpDi'))
  if (isPG || isOra) lines.push(line('\\du', 'metaHelpDu'))
  if (isPG || isOra) lines.push(line('\\ds', 'metaHelpDs'))
  if (isPG) lines.push(line('\\df', 'metaHelpDf'))
  lines.push(line('\\d <obj>', 'metaHelpD'))
  lines.push(line('\\x', 'metaHelpX'))
  lines.push(line('\\conninfo', 'metaHelpConninfo'))
  lines.push(c(ANSI.gray, t('termMetaSql', { bs: '\\' })))
  outLines(lines)
}

function sendExec(sql: string, reason: string, mfaCode = ''): 'ws' | 'rest' {
  lastExec = { sql, reason }
  pendingSql.value = sql
  if (ws.send({ type: 'exec', connectionId: props.conn.id, sql, reason, mfaCode, database: targetDb.value })) return 'ws'
  return 'rest'
}

function handleExecEnv(env: { code: number; data?: any }, sql: string, reason: string) {
  if (env.code === CODE_MFA_REQUIRED) { requestMfa(sql, reason); return }
  renderExecEnvelope(env)
  editor.resume()
}

function requestMfa(sql: string, reason: string) {
  pendingMfa = { sql, reason }
  mfaInput.value = ''
  mfaErr.value = ''
  mfaOpen.value = true
}

async function submitMfa() {
  const code = mfaInput.value.trim()
  if (code.length !== 6) { mfaErr.value = t('mfaStepDesc'); return }
  if (!pendingMfa) return
  const { sql, reason } = pendingMfa
  mfaOpen.value = false
  if (sendExec(sql, reason, code) === 'ws') return
  try {
    const env = await api.exec(props.conn.id, sql, reason, code, targetDb.value)
    if (env.code === CODE_MFA_REQUIRED) { requestMfa(sql, reason); mfaErr.value = t('mfaBadCode'); return }
    renderExecEnvelope(env)
  } catch { out(c(ANSI.red, t('termExecFail'))) }
  editor.resume()
}

function cancelMfa() {
  mfaOpen.value = false
  pendingMfa = null
  out(c(ANSI.gray, t('termCancelled')))
  editor.resume()
}

function onWsMessage(m: any) {
  if (m.type === 'output') renderOutput(m)
  else if (m.type === 'intercept') { renderIntercept(m); auth.pendingCount++ }
  else if (m.type === 'mfa_required') { requestMfa(lastExec.sql, lastExec.reason); return }
  else if (m.type === 'error') out(c(ANSI.red, '· ' + (m.message || t('termExecFail'))))
  else return
  editor.resume()
}

function renderOutput(m: { text?: string; rows?: number; ms?: number; columns?: string[]; data?: string[][]; truncated?: boolean }) {
  const rows = m.rows || 0
  if (m.columns && m.columns.length) {
    // Real result set returned by the target DB.
    const data = m.data || []
    emit('result', { columns: m.columns, rows: data }) // feed the HTML grid panel
    // Grid mode shows the data in the HTML panel; the terminal keeps only the
    // summary. \G / \x are explicit terminal-display choices, still honoured.
    if (pendingVertical.value || expandedMode.value) outLines(renderVertical(m.columns, data))
    else if (!props.gridView) outLines(renderTable(buildTable(m.columns, data)))
    const more = m.truncated ? t('termTruncated', { n: data.length }) : ''
    out(c(ANSI.gray, t('termRows', { n: data.length, more, ms: m.ms ?? 0 })))
  } else if (isSelect(pendingSql.value) && rows > 0) {
    // Simulated connection (no credentials): synthesise a preview.
    const tb = synthTable(pendingSql.value, rows)
    emit('result', { columns: tb.columns, rows: tb.rows })
    if (pendingVertical.value || expandedMode.value) outLines(renderVertical(tb.columns, tb.rows))
    else if (!props.gridView) outLines(renderTable(tb))
    const shown = tb.rows.length
    const more = shown < rows ? t('termShownFirst', { n: shown }) : ''
    out(c(ANSI.gray, t('termRows', { n: rows, more, ms: m.ms ?? 0 })))
  } else if (m.text) {
    const notice = m.text.trimStart().startsWith('·')
    out(notice ? c(ANSI.yellow, m.text) : c(ANSI.green, '✓ ' + m.text))
  } else {
    out(c(ANSI.green, t('termExecOk')))
  }
  risk.value = 'safe'
}

function renderIntercept(m: { rule?: string; approvalNo?: string }) {
  out(c(ANSI.red, t('termIntercepted')))
  out(c(ANSI.gray, t('termHitRuleLine', { rule: m.rule || apRule.value || '-' })))
  out(c(ANSI.gray, t('termApprovalLine', { no: m.approvalNo || '-' })))
}

function renderExecEnvelope(env: { code: number; data?: any }) {
  if (env.code === CODE_INTERCEPTED || env.data?.intercepted) {
    renderIntercept({ rule: env.data?.rule, approvalNo: env.data?.approvalNo })
    auth.pendingCount++
  } else {
    renderOutput({ text: env.data?.output, rows: env.data?.rows, ms: env.data?.ms,
      columns: env.data?.columns, data: env.data?.data, truncated: env.data?.truncated })
  }
}

async function submitApproval(reason: string) {
  apOpen.value = false
  if (sendExec(apCmd.value, reason) === 'ws') return
  try {
    const env = await api.exec(props.conn.id, apCmd.value, reason)
    if (env.code === CODE_MFA_REQUIRED) { requestMfa(apCmd.value, reason); return }
    if (env.code === CODE_INTERCEPTED || env.data?.intercepted) {
      renderIntercept({ rule: env.data?.rule || apRule.value, approvalNo: env.data?.approvalNo })
      auth.pendingCount++
    }
  } catch { /* ignore */ }
  editor.resume()
}

function cancelApproval() {
  apOpen.value = false
  out(c(ANSI.gray, t('termCancelSubmit')))
  editor.resume()
}

function refreshSession() {
  editor.printAbove([cautionLine(), c(ANSI.gray, t('termReconnecting'))])
  ws.reconnect()
}

// ---- script upload/scan ----
function onUploadClick() {
  if (!enabled.value) { pathPromptOpen.value = true; return }
  fileRef.value?.click()
}
function onSampleClick() {
  if (!enabled.value) { pathPromptOpen.value = true; return }
  loadSample()
}
function goSettings() {
  pathPromptOpen.value = false
  router.push('/settings')
}

// uploadId > 0 means the script is already uploaded (dispatched from the
// "uploaded scripts" picker) — executing it must not save another copy.
const scUploadId = ref(0)
async function scanScript(text: string, filename: string, uploadId = 0) {
  scUploadId.value = uploadId
  try {
    scScan.value = await api.scriptScan(text, filename, props.conn.id)
    scSubmitted.value = false
    scOpen.value = true
  } catch (e: any) {
    if (e?.code === CODE_SCRIPT_PATH_UNSET) { enabled.value = false; pathPromptOpen.value = true }
    else editor.printAbove([c(ANSI.red, t('termScanFail'))])
  }
}

function onFile(e: Event) {
  const file = (e.target as HTMLInputElement).files?.[0]
  if (!file) return
  const reader = new FileReader()
  reader.onload = () => { scanScript(String(reader.result || ''), file.name) }
  reader.readAsText(file)
  ;(e.target as HTMLInputElement).value = ''
}

// ---- dispatch an already-uploaded script from the terminal ----
const uploads = ref<ScriptUpload[]>([])
const pickOpen = ref(false)
const pickPos = ref({ top: 0, left: 0 })
async function togglePick(e: MouseEvent) {
  if (!pickOpen.value) {
    const r = (e.currentTarget as HTMLElement).getBoundingClientRect()
    pickPos.value = { top: r.bottom + 5, left: r.left }
    try { uploads.value = await api.scriptUploads() } catch { uploads.value = [] }
  }
  pickOpen.value = !pickOpen.value
}
async function runUploaded(u: ScriptUpload) {
  pickOpen.value = false
  try {
    const { content, filename } = await api.scriptUploadContent(u.id)
    await scanScript(content, filename, u.id) // already uploaded → don't re-save on execute
  } catch { editor.printAbove([c(ANSI.red, t('termUploadLoadFail'))]) }
}

async function loadSample() {
  const sample = `-- migration_2026q3.sql
SELECT count(*) FROM orders WHERE status='paid';
UPDATE users SET tier='vip' WHERE id=88;
ALTER TABLE orders DROP COLUMN legacy_ref;
DELETE FROM sessions;
TRUNCATE TABLE orders_2024_q3;
INSERT INTO audit_log(evt) VALUES('migrate');`
  await scanScript(sample, 'migration_2026q3.sql')
}

async function runScript() {
  if (!scScan.value) return
  const content = scScan.value.statements.map((s) => s.sql + ';').join('\n')
  try {
    const env = await api.scriptExecute(content, scScan.value.filename, props.conn.id, '', scUploadId.value, targetDb.value)
    if (env.code === CODE_SCRIPT_PATH_UNSET) {
      scOpen.value = false; enabled.value = false; pathPromptOpen.value = true; return
    }
    if (env.code === CODE_INTERCEPTED || env.data?.exec?.intercepted) {
      scSubmitted.value = true
      auth.pendingCount++
      if (env.data?.savedPath) editor.printAbove([c(ANSI.gray, t('termScriptSaved', { path: env.data.savedPath }))])
      return
    }
    if (env.code === CODE_OK) {
      const n = env.data?.statements ?? scScan.value.total
      const saved = env.data?.savedPath || ''
      editor.printAbove([
        c(ANSI.cyan, '\\i ' + scScan.value.filename),
        c(ANSI.green, t('termScriptOk', { n })),
        ...(saved ? [c(ANSI.gray, t('termSavedTo', { path: saved }))] : []),
      ])
      scOpen.value = false
      return
    }
    scOpen.value = false
    editor.printAbove([c(ANSI.red, t('termScriptFailMsg', { msg: env.msg || '' }))])
  } catch {
    scOpen.value = false
    editor.printAbove([c(ANSI.red, t('termScriptFail'))])
  }
}
</script>

<template>
  <div class="session">
    <div class="actionbar">
      <div class="host"><span class="online" :class="{ off: wsStatus !== 'open' }" />{{ conn.name }}<span v-if="targetDb" class="curdb">· {{ targetDb }}</span> · SQL</div>
      <div class="actions">
        <div class="upload" :title="enabled ? '' : $t('scPathTitle')" @click="onUploadClick"><Upload :size="13" />{{ $t('uploadScript') }}</div>
        <input ref="fileRef" type="file" accept=".sql,text/plain" class="hidden-file" @change="onFile" />
        <div class="upload" @click="togglePick"><ListChecks :size="13" />{{ $t('uploadedScripts') }}<ChevronDown :size="12" /></div>
        <Teleport to="body">
          <template v-if="pickOpen">
            <div class="pick-mask" @click="pickOpen = false" />
            <div class="picker" :style="{ top: pickPos.top + 'px', left: pickPos.left + 'px' }">
              <div class="pick-head">{{ $t('uploadedScripts') }}</div>
              <div v-if="!uploads.length" class="pick-empty">{{ $t('uploadNone') }}</div>
              <div v-for="u in uploads" :key="u.id" class="pick-item" :title="u.path" @click="runUploaded(u)">
                <FileCode2 :size="12" /><span class="pn">{{ u.filename }}</span><span class="pm">{{ u.createdAt?.slice(5, 16) }}</span>
              </div>
            </div>
          </template>
        </Teleport>
        <div class="sample" @click="onSampleClick"><FileCode2 :size="13" />{{ $t('sampleScript') }}</div>
        <RotateCw class="refresh" :size="14" :title="$t('refreshSession')" @click="refreshSession" />
      </div>
    </div>

    <div class="xterm-wrap"><div ref="termEl" class="xterm-host" /></div>

    <div class="statusbar">
      <span class="ok" :class="{ warn: wsStatus !== 'open' }">
        {{ wsStatus === 'open' ? $t('connected') : wsStatus === 'connecting' ? $t('wsConnecting') : $t('wsDisconnected') }}
      </span>
      <span>{{ conn.defaultRole }}</span><span>{{ conn.policy }}</span>
      <span>{{ $t('tryHint') }} <span class="hl">DROP TABLE orders_2024_q3;</span></span>
      <span class="right">{{ $t('utf8audit') }}</span>
    </div>

    <ApprovalModal
      :open="apOpen" :command="apCmd" :instance="conn.name" :env="conn.env"
      :risk="apRisk" :audit-id="apAuditId" :chain="chain" :rule="apRule"
      @cancel="cancelApproval" @submit="submitApproval"
    />
    <ScriptScanModal :open="scOpen" :scan="scScan" :submitted="scSubmitted" :save-path="scriptSavePath"
      :instance="conn.name" :databases="dbOptions" v-model:target-db="targetDb" @close="scOpen = false" @run="runScript" />

    <div v-if="pathPromptOpen" class="mfa-overlay">
      <div class="mfa-mask" @click="pathPromptOpen = false" />
      <div class="mfa-modal">
        <div class="mfa-top info" />
        <div class="mfa-pad">
          <div class="mfa-title acc"><FolderCog :size="18" />{{ $t('scPathTitle') }}</div>
          <div class="mfa-desc">{{ $t('scPathDesc') }}</div>
          <div class="mfa-acts">
            <button class="mfa-btn ghost" @click="pathPromptOpen = false">{{ $t('btnCancel') }}</button>
            <button class="mfa-btn primary" @click="goSettings">{{ $t('scPathGo') }}</button>
          </div>
        </div>
      </div>
    </div>

    <div v-if="mfaOpen" class="mfa-overlay">
      <div class="mfa-mask" @click="cancelMfa" />
      <div class="mfa-modal">
        <div class="mfa-top" />
        <div class="mfa-pad">
          <div class="mfa-title"><ShieldAlert :size="18" />{{ $t('mfaStepTitle') }}</div>
          <div class="mfa-desc">{{ $t('mfaStepDesc') }}</div>
          <input
            class="mfa-code" inputmode="numeric" autocomplete="one-time-code" placeholder="000000"
            :value="mfaInput"
            @input="mfaInput = ($event.target as HTMLInputElement).value.replace(/\D/g, '').slice(0, 6)"
            @keyup.enter="submitMfa"
          />
          <div v-if="mfaErr" class="mfa-err">{{ mfaErr }}</div>
          <div class="mfa-acts">
            <button class="mfa-btn ghost" @click="cancelMfa">{{ $t('btnCancel') }}</button>
            <button class="mfa-btn primary" @click="submitMfa">{{ $t('mfaVerify') }}</button>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.session { flex: 1; min-height: 0; display: flex; flex-direction: column; background: var(--surface-page); }
.actionbar { display: flex; align-items: center; gap: 9px; height: 38px; padding: 0 14px; background: var(--surface-raised); border-bottom: 1px solid var(--border-subtle); min-width: 0; overflow: hidden; }
.host { flex: 1; min-width: 0; display: flex; align-items: center; gap: 7px; overflow: hidden; white-space: nowrap; text-overflow: ellipsis; font: 600 12px var(--font-mono); color: var(--text-strong); }
.curdb { color: var(--accent-text); }
.online { width: 6px; height: 6px; border-radius: 50%; background: var(--success); flex-shrink: 0; transition: background var(--dur-fast) var(--ease-out); }
.online.off { background: var(--warning); }
.actions { flex-shrink: 0; display: flex; align-items: center; gap: 10px; font: 500 11px var(--font-mono); color: var(--text-muted); }
.upload {
  display: inline-flex; align-items: center; gap: 6px; height: 28px; padding: 0 11px;
  border: 1px solid var(--accent-subtle-border); background: var(--accent-subtle); border-radius: 8px;
  color: var(--accent-text); font: 600 11px var(--font-mono); cursor: pointer;
}
.hidden-file { display: none; }
.sample { display: inline-flex; align-items: center; gap: 5px; height: 28px; padding: 0 10px; border: 1px solid var(--border-default); background: var(--surface-card); border-radius: 8px; color: var(--text-body); cursor: pointer; }
.sample:hover { color: var(--accent-text); border-color: var(--accent-subtle-border); }
.pick-mask { position: fixed; inset: 0; z-index: 900; }
.picker { position: fixed; z-index: 901; min-width: 260px; max-width: 420px; max-height: 320px; overflow-y: auto; padding: 5px; border: 1px solid var(--border-default); border-radius: 10px; background: var(--surface-overlay, var(--surface-card)); box-shadow: 0 12px 32px rgba(0, 0, 0, 0.4); }
.pick-head { padding: 6px 9px 7px; font: 600 10px var(--font-mono); color: var(--text-faint); text-transform: uppercase; letter-spacing: 0.04em; }
.pick-empty { padding: 14px; text-align: center; font: 500 11.5px var(--font-mono); color: var(--text-faint); }
.pick-item { display: flex; align-items: center; gap: 8px; padding: 8px 9px; border-radius: 7px; color: var(--text-body); cursor: pointer; }
.pick-item:hover { background: var(--accent-subtle); color: var(--accent-text); }
.pn { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font: 600 12px var(--font-mono); }
.pm { flex-shrink: 0; font: 500 10px var(--font-mono); color: var(--text-faint); }
.refresh { color: var(--text-muted); cursor: pointer; transition: color var(--dur-fast) var(--ease-out); }
.refresh:hover { color: var(--accent-text); }
.xterm-wrap { flex: 1; min-height: 0; padding: 10px 12px 4px; overflow: hidden; }
.xterm-host { width: 100%; height: 100%; }
.xterm-host :deep(.xterm) { height: 100%; }
.statusbar { height: 36px; background: var(--surface-raised); border-top: 1px solid var(--border-subtle); display: flex; align-items: center; gap: 16px; padding: 0 16px; font: 500 11px var(--font-mono); color: var(--text-muted); }
.statusbar .ok { color: var(--success-text); }
.statusbar .ok.warn { color: var(--warning-text); }
.statusbar .hl { color: var(--text-body); }
.statusbar .right { margin-left: auto; }

/* modals */
.mfa-overlay { position: fixed; inset: 0; z-index: 60; }
.mfa-mask { position: absolute; inset: 0; background: rgba(4, 6, 12, 0.7); backdrop-filter: blur(3px); }
.mfa-modal {
  position: absolute; left: 50%; top: 50%; transform: translate(-50%, -50%);
  width: 380px; max-width: 92vw; background: var(--surface-card);
  border: 1px solid var(--border-default); border-radius: 16px; overflow: hidden;
  box-shadow: 0 30px 80px -20px rgba(0, 0, 0, 0.7);
}
.mfa-top { height: 4px; background: linear-gradient(90deg, #f0473e, #f5a524); }
.mfa-top.info { background: linear-gradient(90deg, #3b6ef6, #2dcde6); }
.mfa-pad { padding: 20px 22px; }
.mfa-title { display: flex; align-items: center; gap: 9px; font: 700 16px var(--font-display); color: var(--text-strong); }
.mfa-title svg { color: var(--danger); }
.mfa-title.acc svg { color: var(--accent-text); }
.mfa-desc { margin-top: 6px; font: 400 12.5px/1.5 var(--font-body); color: var(--text-muted); }
.mfa-code {
  margin-top: 16px; width: 100%; height: 46px; border: 1px solid var(--border-default); border-radius: 10px;
  background: var(--surface-sunken); color: var(--text-strong); text-align: center;
  font: 700 22px var(--font-mono); letter-spacing: 10px; outline: none;
}
.mfa-code:focus { border-color: var(--accent-text); }
.mfa-err { margin-top: 8px; font: 600 12px var(--font-body); color: var(--danger-text); }
.mfa-acts { margin-top: 16px; display: flex; justify-content: flex-end; gap: 10px; }
.mfa-btn { height: 36px; padding: 0 16px; border-radius: 9px; font: 600 12px var(--font-body); cursor: pointer; border: 1px solid var(--border-default); }
.mfa-btn.ghost { background: transparent; color: var(--text-body); }
.mfa-btn.ghost:hover { background: var(--surface-sunken); }
.mfa-btn.primary { background: var(--accent); color: #fff; border-color: transparent; }
.mfa-btn.primary:hover { background: var(--accent-hover); }
</style>
