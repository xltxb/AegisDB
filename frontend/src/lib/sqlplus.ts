// SQL*Plus 客户端命令(Oracle),终端这一侧(2026-10 sqlplus-compat 第一批,见 .scratch/sqlplus-compat)。
//
// 与后端 gateway/oracle_sqlplus_cmd.go 是同一张表:拒绝的、忽略的、PROMPT、REMARK。终端在本地处理
// 它们,不下发;此外还有只在交互终端里有意义的命令:DEFINE / UNDEFINE / ACCEPT(替换变量)、
// TIMING、RUN 与单独一行的 /(重跑上一条)、不带对象的 SHOW ERRORS(补上上一次 CREATE 的对象)。
//
// 只认**单行**:一段粘贴进来的多行脚本以 DEFINE 开头时,不能把整段当成一条 DEFINE 吞掉 ——
// 那种整段交给服务端逐条处理,替换变量在本地逐行展开(substitute)。

import { DISPLAY_SHOW } from './sqlplusDisplay'

const isOracle = (engine: string) => /oracle/i.test(engine)

/** 拒绝的原因分类,对应 i18n 的 sqlplusWhy* */
export type RefuseWhy = 'Account' | 'Host' | 'Instance' | 'Password' | 'Copy' | 'Edit' | 'Exit' | 'NoSuch'

export type SqlPlusCommand =
  | { kind: 'refuse'; name: string; why: RefuseWhy }
  | { kind: 'ignored'; name: string }
  | { kind: 'prompt'; text: string }
  | { kind: 'remark' }
  | { kind: 'define'; arg: string }
  | { kind: 'undefine'; names: string[] }
  | { kind: 'accept'; arg: string }
  | { kind: 'timing'; arg: string }
  | { kind: 'run' }
  | { kind: 'slash' }
  | { kind: 'start'; file: string; args: string[]; nested: boolean } // @file / @@file / START file
  | { kind: 'spool'; arg: string }
  | { kind: 'whenever'; arg: string }
  | { kind: 'variable'; arg: string }
  | { kind: 'print'; names: string[] }
  | { kind: 'showErrors' } // 不带对象
  | { kind: 'showLocal'; option: string } // SHOW DEFINE / VERIFY / TIMING / LINESIZE / ALL …(本地回答的)
  | { kind: 'column'; arg: string }
  | { kind: 'clear'; arg: string }
  | { kind: 'list'; arg: string }
  | { kind: 'append'; text: string }
  | { kind: 'change'; arg: string }
  | { kind: 'del'; arg: string }
  | { kind: 'input'; text: string }
  | { kind: 'save'; arg: string }
  | { kind: 'get'; arg: string }
  | { kind: 'help'; topic: string }

interface Word { full: string; min: string; cmd: (rest: string) => SqlPlusCommand }

const refuse = (full: string, why: RefuseWhy) => () => ({ kind: 'refuse', name: full, why }) as SqlPlusCommand
const ignored = (full: string) => () => ({ kind: 'ignored', name: full }) as SqlPlusCommand

const WORDS: Word[] = [
  { full: 'CONNECT', min: 'CONN', cmd: refuse('CONNECT', 'Account') },
  { full: 'DISCONNECT', min: 'DISC', cmd: refuse('DISCONNECT', 'Account') },
  { full: 'HOST', min: 'HO', cmd: refuse('HOST', 'Host') },
  { full: 'STARTUP', min: 'STARTUP', cmd: refuse('STARTUP', 'Instance') },
  { full: 'SHUTDOWN', min: 'SHUTDOWN', cmd: refuse('SHUTDOWN', 'Instance') },
  { full: 'RECOVER', min: 'RECOVER', cmd: refuse('RECOVER', 'Instance') },
  { full: 'PASSWORD', min: 'PASSW', cmd: refuse('PASSWORD', 'Password') },
  { full: 'COPY', min: 'COPY', cmd: refuse('COPY', 'Copy') },
  { full: 'EDIT', min: 'ED', cmd: refuse('EDIT', 'Edit') },
  { full: 'EXIT', min: 'EXIT', cmd: refuse('EXIT', 'Exit') },
  { full: 'QUIT', min: 'QUIT', cmd: refuse('QUIT', 'Exit') },
  { full: 'XQUERY', min: 'XQUERY', cmd: refuse('XQUERY', 'NoSuch') },
  { full: 'ATTRIBUTE', min: 'ATTRIBUTE', cmd: refuse('ATTRIBUTE', 'NoSuch') },
  { full: 'STORE', min: 'STORE', cmd: refuse('STORE', 'NoSuch') },

  { full: 'BREAK', min: 'BRE', cmd: ignored('BREAK') },
  { full: 'COMPUTE', min: 'COMP', cmd: ignored('COMPUTE') },
  { full: 'TTITLE', min: 'TTI', cmd: ignored('TTITLE') },
  { full: 'BTITLE', min: 'BTI', cmd: ignored('BTITLE') },
  { full: 'REPHEADER', min: 'REPH', cmd: ignored('REPHEADER') },
  { full: 'REPFOOTER', min: 'REPF', cmd: ignored('REPFOOTER') },
  { full: 'PAUSE', min: 'PAU', cmd: ignored('PAUSE') },
  { full: 'COLUMN', min: 'COL', cmd: (rest) => ({ kind: 'column', arg: rest }) },
  { full: 'CLEAR', min: 'CL', cmd: (rest) => ({ kind: 'clear', arg: rest }) },
  // 行编辑器(工单 11)。单字母缩写(L / A / C / I)只在整条就是这个词时才算:没有 SQL 以它们开头
  { full: 'LIST', min: 'L', cmd: (rest) => ({ kind: 'list', arg: rest }) },
  { full: 'APPEND', min: 'A', cmd: (rest) => ({ kind: 'append', text: rest }) },
  { full: 'CHANGE', min: 'C', cmd: (rest) => ({ kind: 'change', arg: rest }) },
  { full: 'DEL', min: 'DEL', cmd: (rest) => ({ kind: 'del', arg: rest }) },
  { full: 'INPUT', min: 'I', cmd: (rest) => ({ kind: 'input', text: rest }) },
  { full: 'SAVE', min: 'SAV', cmd: (rest) => ({ kind: 'save', arg: rest }) },
  { full: 'GET', min: 'GET', cmd: (rest) => ({ kind: 'get', arg: rest }) },
  { full: 'HELP', min: 'HELP', cmd: (rest) => ({ kind: 'help', topic: rest }) },

  { full: 'DEFINE', min: 'DEF', cmd: (rest) => ({ kind: 'define', arg: rest }) },
  { full: 'UNDEFINE', min: 'UNDEF', cmd: (rest) => ({ kind: 'undefine', names: rest.split(/\s+/).filter(Boolean) }) },
  { full: 'ACCEPT', min: 'ACC', cmd: (rest) => ({ kind: 'accept', arg: rest }) },
  { full: 'TIMING', min: 'TIMI', cmd: (rest) => ({ kind: 'timing', arg: rest }) },
  { full: 'PROMPT', min: 'PRO', cmd: (rest) => ({ kind: 'prompt', text: rest }) },
  { full: 'REMARK', min: 'REM', cmd: () => ({ kind: 'remark' }) },
]

const SHOW_LOCAL = new Set(['DEFINE', 'DEF', 'VERIFY', 'VER', 'TIMING', 'TIMI', 'CONCAT', 'CON', 'ESCAPE', 'ESC',
  'ALL', 'SQLCODE', 'SQLCO', 'AUTOCOMMIT', 'AUTO', 'AUTOPRINT', 'AUTOP', 'SPOOL', 'SPOO'])

/** 识别一行 SQL*Plus 客户端命令(只认单行;结尾分号不算)。不是这里管的返回 null。 */
export function parseSqlPlusCommand(raw: string, engine: string): SqlPlusCommand | null {
  if (!isOracle(engine)) return null
  const s = raw.trim()
  if (!s || s.includes('\n')) return null
  if (s === '/') return { kind: 'slash' }
  if (s[0] === '!' || s[0] === '$') return { kind: 'refuse', name: 'HOST', why: 'Host' }
  if (s[0] === '@') {
    const nested = s.startsWith('@@')
    const st = parseStartArgs(s.slice(nested ? 2 : 1))
    return st ? { kind: 'start', nested, ...st } : null
  }
  // 词后面必须是空白或结尾(结尾分号先去掉):`HOSTNAME`、`CONN/` 都不是命令
  const mm = s.replace(/;+\s*$/, '').match(/^([A-Za-z]+)(?:\s+([\s\S]*))?$/)
  if (!mm) return null
  const word = mm[1].toUpperCase()
  const rest = (mm[2] ?? '').trim()
  // PROMPT / REMARK 的文字原样保留(包括结尾的分号,SQL*Plus 也照打)
  const restRaw = s.slice(mm[1].length).trim()
  if (word === 'R' || word === 'RUN') return rest ? null : { kind: 'run' }
  if (word === 'ARCHIVE' && /^LOG\b/i.test(rest)) return { kind: 'refuse', name: 'ARCHIVE LOG', why: 'Instance' }
  if (word.length >= 3 && 'START'.startsWith(word)) {
    const st = parseStartArgs(rest)
    return st ? { kind: 'start', nested: false, ...st } : null
  }
  if (word.length >= 3 && 'SPOOL'.startsWith(word)) return { kind: 'spool', arg: rest }
  if (word === 'WHENEVER') return { kind: 'whenever', arg: rest }
  if (word.length >= 3 && 'VARIABLE'.startsWith(word)) return { kind: 'variable', arg: rest }
  if (word.length >= 3 && 'PRINT'.startsWith(word)) return { kind: 'print', names: rest.split(/\s+/).filter(Boolean) }
  if (word === 'SHOW') {
    if (/^ERR(?:O|OR|ORS)?$/i.test(rest)) return { kind: 'showErrors' }
    const opt = rest.toUpperCase()
    if (SHOW_LOCAL.has(opt) || DISPLAY_SHOW.test(opt)) return { kind: 'showLocal', option: opt }
    return null
  }
  for (const w of WORDS) {
    if (word.length < w.min.length || !w.full.startsWith(word)) continue
    // PROMPT / REMARK 原样;APPEND / INPUT 只去掉命令后的一个空格(要以空格开头就写两个空格)
    if (w.full === 'APPEND' || w.full === 'INPUT') return w.cmd(s.slice(mm[1].length).replace(/^ /, ''))
    return w.cmd(w.full === 'PROMPT' || w.full === 'REMARK' ? restRaw : rest)
  }
  return null
}

/** `@file arg1 'arg 2'` 的文件名与位置参数(参数可带单 / 双引号)。没有文件名返回 null。 */
export function parseStartArgs(s: string): { file: string; args: string[] } | null {
  const toks: string[] = []
  const re = /'((?:[^']|'')*)'|"((?:[^"]|"")*)"|(\S+)/g
  let m: RegExpExecArray | null
  while ((m = re.exec(s.trim().replace(/;+\s*$/, '')))) {
    toks.push(m[1] !== undefined ? m[1].split("''").join("'") : m[2] !== undefined ? m[2].split('""').join('"') : m[3])
  }
  if (!toks.length || !toks[0]) return null
  return { file: toks[0], args: toks.slice(1) }
}

/** 行编辑器问"这一行敲完了没有":这些命令以换行结束,不等分号。 */
export function sqlPlusLineComplete(line: string, engine: string): boolean {
  if (!isOracle(engine)) return false
  const s = line.trim()
  if (!s || s.includes('\n')) return false
  if (parseSqlPlusCommand(s, engine)) return true
  // SHOW xxx、DESC xxx 在 SQL*Plus 里同样以换行结束
  return /^(?:SHOW|SHO|DESC|DESCRIBE)\s+\S/i.test(s)
}

// ---------------------------------------------------------------- 绑定变量(VARIABLE / PRINT)

/** 一个绑定变量。与后端 model.BindVar 同形:每次执行随请求带过去,执行后带回新值。 */
export interface BindVar { name: string; type: string; value: string; null: boolean }

const VARIABLE_RE = /^([A-Za-z][A-Za-z0-9_$#]*)\s+(NUMBER|BINARY_FLOAT|BINARY_DOUBLE|N?CHAR(?:\s*\(\s*\d+\s*(?:CHAR|BYTE)?\s*\))?|N?VARCHAR2\s*\(\s*\d+\s*(?:CHAR|BYTE)?\s*\)|N?CLOB)\s*(?:=\s*(.*?))?\s*;?\s*$/i

/** VARIABLE name type [= value]。不支持的类型(REFCURSOR、BLOB、DATE …)返回 null。与后端 gateway.ParseVariable 一致。 */
export function parseVariable(arg: string): BindVar | null {
  const m = arg.trim().match(VARIABLE_RE)
  if (!m) return null
  const b: BindVar = { name: m[1].toUpperCase(), type: m[2].replace(/\s+/g, '').toUpperCase(), value: '', null: true }
  let v = (m[3] ?? '').trim()
  if (v) {
    if (v.length >= 2 && v[0] === "'" && v[v.length - 1] === "'") v = v.slice(1, -1).split("''").join("'")
    b.value = v
    b.null = false
  }
  return b
}

/** 声明(或重新声明)一个变量:同名的替换掉,值清空 —— 与 SQL*Plus 一致。 */
export function declareBind(binds: BindVar[], b: BindVar): BindVar[] {
  return [...binds.filter((x) => x.name !== b.name), b]
}

/** 语句里用到了哪些已声明的变量(SET AUTOPRINT 打这些)。字符串、注释里的不算。 */
export function bindsUsedIn(sql: string, binds: BindVar[]): BindVar[] {
  const s = sql.replace(/'(?:[^']|'')*'|"[^"]*"|--[^\n]*|\/\*[\s\S]*?\*\//g, ' ')
  return binds.filter((b) => new RegExp(`(^|[^A-Za-z0-9_$#:]):${b.name.replace(/\$/g, '\\$')}(?![A-Za-z0-9_$#])`, 'i').test(s))
}

// ---------------------------------------------------------------- 替换变量

/** 一个终端标签的替换变量状态(与 SQL*Plus 一个会话一份相同)。 */
export interface DefineState {
  vars: Map<string, string> // 名字大写
  defineOn: boolean
  defineChar: string
  concatOn: boolean
  concatChar: string
  escapeOn: boolean
  escapeChar: string
  verify: boolean
}

export function newDefineState(): DefineState {
  return { vars: new Map(), defineOn: true, defineChar: '&', concatOn: true, concatChar: '.', escapeOn: false, escapeChar: '\\', verify: true }
}

/** DEFINE 后面的参数:'' = 列出全部,'name' = 看一个,'name = value' = 定义。 */
export type DefineArg = { list: true } | { show: string } | { set: string; value: string } | { error: true }

export function parseDefineArg(arg: string): DefineArg {
  const a = arg.trim()
  if (!a) return { list: true }
  const m = a.match(/^([A-Za-z0-9_$#]+)\s*(?:=\s*([\s\S]*))?$/)
  if (!m) return { error: true }
  if (m[2] === undefined) return { show: m[1].toUpperCase() }
  return { set: m[1].toUpperCase(), value: unquote(m[2].trim()) }
}

// 'it''s' → it's;"x" → x;不带引号取第一个词(SQL*Plus:含空格的值要加引号)
function unquote(v: string): string {
  if (v.length >= 2 && (v[0] === "'" || v[0] === '"')) {
    const q = v[0]
    const end = v.lastIndexOf(q)
    if (end > 0) return v.slice(1, end).split(q + q).join(q)
  }
  return v.split(/\s+/)[0] ?? ''
}

export interface AcceptArg { name: string; type: 'CHAR' | 'NUMBER' | 'DATE'; def?: string; prompt?: string; hide: boolean }

/** ACCEPT var [NUM[BER]|CHAR|DATE] [FOR[MAT] f] [DEF[AULT] d] [PROMPT text|NOPR[OMPT]] [HIDE] */
export function parseAcceptArg(arg: string): AcceptArg | null {
  const m = arg.trim().match(/^([A-Za-z0-9_$#]+)\s*([\s\S]*)$/)
  if (!m) return null
  const out: AcceptArg = { name: m[1].toUpperCase(), type: 'CHAR', hide: false }
  let rest = m[2]
  const word = () => { const w = rest.match(/^\s*(\S+)/); return w ? w[1].toUpperCase() : '' }
  const take = () => { rest = rest.replace(/^\s*\S+/, '') }
  // 值:带引号取引号里的,否则一个词
  const value = (): string => {
    const t = rest.trimStart()
    const q = t[0]
    if (q === "'" || q === '"') {
      let i = 1; let v = ''
      while (i < t.length) {
        if (t[i] === q) { if (t[i + 1] === q) { v += q; i += 2; continue } i++; break }
        v += t[i++]
      }
      rest = t.slice(i)
      return v
    }
    const w = t.match(/^(\S+)/)
    rest = t.slice(w ? w[1].length : 0)
    return w ? w[1] : ''
  }
  for (let guard = 0; guard < 20 && rest.trim(); guard++) {
    const w = word()
    if (/^NUM(?:B|BE|BER)?$/.test(w)) { out.type = 'NUMBER'; take() }
    else if (w === 'CHAR') { out.type = 'CHAR'; take() }
    else if (w === 'DATE') { out.type = 'DATE'; take() }
    else if (/^FOR(?:M|MA|MAT)?$/.test(w)) { take(); value() }
    else if (/^DEF(?:A|AU|AUL|AULT)?$/.test(w)) { take(); out.def = value() }
    else if (/^PRO(?:M|MP|MPT)?$/.test(w)) {
      take()
      const t = rest.trimStart()
      if (t[0] === "'" || t[0] === '"') out.prompt = value()
      else {
        // 不带引号:到行尾都是提示文字(结尾的 HIDE 除外)
        const h = t.match(/^([\s\S]*?)\s+HIDE\s*$/i)
        out.prompt = (h ? h[1] : t).trim()
        if (h) out.hide = true
        rest = ''
      }
    }
    else if (/^NOPR(?:O|OM|OMP|OMPT)?$/.test(w)) { take(); out.prompt = '' }
    else if (w === 'HIDE') { out.hide = true; take() }
    else return null
  }
  return out
}

/** 一处要问值的引用:替换时调用方去问人。返回 null = 人取消了(Ctrl+C)。 */
export type AskVar = (name: string) => Promise<string | null>

export interface SubstituteResult {
  text: string
  /** 改动过的行(1 起):SET VERIFY ON 时打 old / new */
  changed: { line: number; old: string; new: string }[]
  cancelled: boolean
}

/**
 * 按 SQL*Plus 的规则展开 &var / &&var(逐行,自上而下)。
 *
 * - 字符串字面量里的 & 也展开(与 SQL*Plus 一致;要关掉就 SET DEFINE OFF —— 这正是很多脚本开头写它的原因)。
 * - 未定义的 &var 问人,只用这一次;&&var 问完记下来。
 * - 名字后面紧跟连接符(默认 .)时把它吃掉:`&tbl._bak` → `orders_bak`。
 * - 逐行处理一段多行文本时,行里的 DEFINE / UNDEFINE / SET DEFINE|CONCAT|ESCAPE 当场生效,
 *   作用于后面的行(粘贴进来的整段脚本就是这样);这些行本身原样保留,服务端按"已忽略"处理。
 * - REMARK 行不展开。
 */
export async function substitute(text: string, st: DefineState, ask: AskVar, engine: string): Promise<SubstituteResult> {
  const lines = text.split('\n')
  const changed: SubstituteResult['changed'] = []
  const multi = lines.length > 1
  for (let n = 0; n < lines.length; n++) {
    const line = lines[n]
    if (multi) {
      const cmd = parseSqlPlusCommand(line, engine)
      if (cmd?.kind === 'remark') continue
      if (cmd?.kind === 'define') { const a = parseDefineArg(cmd.arg); if ('set' in a) st.vars.set(a.set, a.value); continue }
      if (cmd?.kind === 'undefine') { for (const v of cmd.names) st.vars.delete(v.toUpperCase()); continue }
      if (applySubstSet(line, st)) continue
    }
    if (!st.defineOn || !line.includes(st.defineChar)) continue
    let out = ''
    let i = 0
    while (i < line.length) {
      const ch = line[i]
      if (st.escapeOn && ch === st.escapeChar && line[i + 1] === st.defineChar) { out += st.defineChar; i += 2; continue }
      if (ch !== st.defineChar) { out += ch; i++; continue }
      const dbl = line[i + 1] === st.defineChar
      const start = i + (dbl ? 2 : 1)
      const nm = line.slice(start).match(/^[A-Za-z0-9_$#]+/)
      if (!nm) { out += ch; i++; continue }
      const name = nm[0].toUpperCase()
      let end = start + nm[0].length
      if (st.concatOn && line[end] === st.concatChar) end++
      let val = st.vars.get(name)
      if (val === undefined) {
        const v = await ask(nm[0])
        if (v === null) return { text, changed, cancelled: true }
        val = v
        if (dbl) st.vars.set(name, v)
      }
      out += val
      i = end
    }
    if (out !== line) { changed.push({ line: n + 1, old: line, new: out }); lines[n] = out }
  }
  return { text: lines.join('\n'), changed, cancelled: false }
}

/**
 * SET DEFINE / CONCAT / ESCAPE / VERIFY:改替换变量的规则。是这几条就生效并返回 true。
 * 值:ON / OFF / 一个字符。
 */
export function applySubstSet(line: string, st: DefineState): boolean {
  const m = line.trim().replace(/;+\s*$/, '').match(/^SET\s+(DEFINE|DEF|CONCAT|CON|ESCAPE|ESC|VERIFY|VER)\s+(\S+)$/i)
  if (!m) return false
  const opt = m[1].toUpperCase()
  const raw = m[2].replace(/^['"](.)['"]$/, '$1')
  const v = raw.toUpperCase()
  const onOff = v === 'ON' ? true : v === 'OFF' ? false : null
  const ch = onOff === null && raw.length === 1 ? raw : null
  if (onOff === null && ch === null) return false
  if (opt.startsWith('DEF')) { if (ch) { st.defineChar = ch; st.defineOn = true } else st.defineOn = onOff! }
  else if (opt.startsWith('CON')) { if (ch) { st.concatChar = ch; st.concatOn = true } else st.concatOn = onOff! }
  else if (opt.startsWith('ESC')) { if (ch) { st.escapeChar = ch; st.escapeOn = true } else st.escapeOn = onOff! }
  else { if (onOff === null) return false; st.verify = onOff }
  return true
}

/** SHOW DEFINE / VERIFY / CONCAT / ESCAPE 的回答(SQL*Plus 的写法)。 */
export function showSubstSetting(option: string, st: DefineState): string | null {
  const o = option.toUpperCase()
  if (o.startsWith('DEF')) return st.defineOn ? `define "${st.defineChar}" (hex ${st.defineChar.charCodeAt(0).toString(16)})` : 'define OFF'
  if (o.startsWith('VER')) return `verify ${st.verify ? 'ON' : 'OFF'}`
  if (o.startsWith('CON')) return st.concatOn ? `concat "${st.concatChar}" (hex ${st.concatChar.charCodeAt(0).toString(16)})` : 'concat OFF'
  if (o.startsWith('ESC')) return st.escapeOn ? `escape "${st.escapeChar}" (hex ${st.escapeChar.charCodeAt(0).toString(16)})` : 'escape OFF'
  return null
}

// ---------------------------------------------------------------- SHOW ERRORS:记住上一次 CREATE 的单元

const CREATE_UNIT = /^\s*CREATE\s+(?:OR\s+REPLACE\s+)?(?:(?:NON)?EDITIONABLE\s+)?(PACKAGE\s+BODY|PACKAGE|PROCEDURE|FUNCTION|TRIGGER|TYPE\s+BODY|TYPE|VIEW|FORCE\s+VIEW|NOFORCE\s+VIEW)\s+(?:IF\s+NOT\s+EXISTS\s+)?("[^"]+"|[A-Za-z][A-Za-z0-9_$#]*)(?:\s*\.\s*("[^"]+"|[A-Za-z][A-Za-z0-9_$#]*))?/i

export interface CreatedUnit { type: string; owner: string; name: string }

/** 认出一条 CREATE 建的是哪个存储程序单元 / 视图;不是这类语句返回 null。 */
export function createdUnit(stmt: string): CreatedUnit | null {
  const s = stmt.replace(/^(?:\s*(?:--[^\n]*\n|\/\*[\s\S]*?\*\/))*/, '')
  const m = s.match(CREATE_UNIT)
  if (!m) return null
  const type = m[1].toUpperCase().replace(/\s+/g, ' ').replace(/^(?:NO)?FORCE /, '')
  const id = (x: string) => (x.startsWith('"') ? x.slice(1, -1) : x.toUpperCase())
  return m[3] ? { type, owner: id(m[2]), name: id(m[3]) } : { type, owner: '', name: id(m[2]) }
}

/** 把不带对象的 SHOW ERRORS 补成后端能翻译的写法;名字带引号 / 怪字符(后端不翻译)返回 null。 */
export function showErrorsFor(u: CreatedUnit): string | null {
  const ok = (x: string) => /^[A-Za-z0-9_$#]+$/.test(x)
  if (!ok(u.name) || (u.owner && !ok(u.owner))) return null
  return `SHOW ERRORS ${u.type} ${u.owner ? u.owner + '.' : ''}${u.name}`
}

// ---------------------------------------------------------------- TIMING

/** TIMING START [name] / SHOW / STOP 的计时器(SQL*Plus 的 timing element)。 */
export interface Timer { name: string; at: number }

/** SQL*Plus 的耗时写法:Elapsed: 00:00:01.23 */
export function elapsedText(ms: number): string {
  const cs = Math.floor(ms / 10)
  const h = Math.floor(cs / 360000), m = Math.floor(cs / 6000) % 60, s = Math.floor(cs / 100) % 60, c = cs % 100
  const p = (n: number) => String(n).padStart(2, '0')
  return `Elapsed: ${p(h)}:${p(m)}:${p(s)}.${p(c)}`
}
