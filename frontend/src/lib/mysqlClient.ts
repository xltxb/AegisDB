// mysqlClient — mysql 命令行客户端自己的命令(pager / tee / status …)。
//
// 这些不是 SQL:mysql 客户端在本机就把它们处理掉了,原样发给服务端只会得到语法错误。
// Web 命令行跑在浏览器里,所以按浏览器能做的去实现:
//
//   - **pager** 不起 shell。真的把一段 shell 命令交给服务器执行,等于给网关开了一个执行任意
//     命令的口子。这里改为一组内置的过滤器(grep / head / tail / wc / sort / uniq,less / more /
//     cat 原样输出),覆盖 DBA 真正常用的那几种写法 —— `pager grep -v Sleep | wc -l` 数活跃连接,
//     `pager less -S`,`pager head -20`。认不出的写法直接说不支持,不去猜。
//   - **tee** 记录到浏览器下载,notee 时存成文件(复用会话日志的导出:同样脱敏、同样写审计)。
//
// 本模块不含任何人读的文案(只有 i18n id),与 metaCommand 相同的理由:它不知道当前语言。

import type { NoticeRef } from './metaCommand'

/** 一个过滤阶段:行进,行出。 */
type Stage = (lines: string[]) => string[]

export interface Pager {
  /** 用户写的那条命令,status 与提示里原样显示 */
  text: string
  run: (lines: string[]) => string[]
}

/** pager 支持的命令,帮助与报错里列出来 */
export const PAGER_COMMANDS = ['less', 'more', 'cat', 'grep', 'egrep', 'fgrep', 'head', 'tail', 'wc', 'sort', 'uniq']

// ---------------------------------------------------------------- 分词

/** 按 shell 的习惯切词:单双引号内原样,引号外的 `|` 是管道。返回每一段的词。 */
export function splitPipeline(cmd: string): string[][] | null {
  const stages: string[][] = [[]]
  let cur = ''
  let has = false // 当前词是否已开始(区分空串 '' 与没有词)
  let quote: '"' | "'" | '' = ''
  const flush = () => {
    if (has) stages[stages.length - 1].push(cur)
    cur = ''
    has = false
  }
  for (let i = 0; i < cmd.length; i++) {
    const ch = cmd[i]
    if (quote) {
      if (ch === quote) quote = ''
      else if (ch === '\\' && quote === '"' && i + 1 < cmd.length && '"\\'.includes(cmd[i + 1])) cur += cmd[++i]
      else cur += ch
      continue
    }
    if (ch === '"' || ch === "'") { quote = ch; has = true; continue }
    if (ch === '\\' && i + 1 < cmd.length) { cur += cmd[++i]; has = true; continue }
    if (ch === '|') { flush(); stages.push([]); continue }
    if (/\s/.test(ch)) { flush(); continue }
    cur += ch
    has = true
  }
  if (quote) return null // 引号没闭合
  flush()
  return stages
}

// ---------------------------------------------------------------- 各个过滤器

const bad = (cmd: string): NoticeRef => ({ id: 'pagerBadArgs', params: { cmd } })

// 展开短选项:-vi → -v -i;-n5 / -5 这类带数字的留给各命令自己认。
function flagsOf(args: string[], withValue = ''): { flags: Set<string>; values: Record<string, string>; rest: string[] } | null {
  const flags = new Set<string>()
  const values: Record<string, string> = {}
  const rest: string[] = []
  for (let i = 0; i < args.length; i++) {
    const a = args[i]
    if (a === '--') { rest.push(...args.slice(i + 1)); break }
    if (a.startsWith('-') && a.length > 1 && !/^-\d/.test(a)) {
      for (let j = 1; j < a.length; j++) {
        const f = a[j]
        if (withValue.includes(f)) {
          const v = a.slice(j + 1) || args[++i]
          if (v === undefined) return null
          values[f] = v
          break
        }
        flags.add(f)
      }
      continue
    }
    rest.push(a)
  }
  return { flags, values, rest }
}

function grepStage(name: string, args: string[]): Stage | NoticeRef {
  const p = flagsOf(args, 'e')
  if (!p) return bad(name)
  for (const f of p.flags) if (!'vicEFwx'.includes(f)) return bad(name)
  const pattern = p.values.e ?? p.rest.shift()
  if (pattern === undefined || p.rest.length) return bad(name) // 不读文件
  const fixed = p.flags.has('F') || name === 'fgrep'
  let src = fixed ? pattern.replace(/[.*+?^${}()|[\]\\]/g, '\\$&') : pattern
  // 基本正则里 \| \( \) 是元字符(GNU 扩展),扩展正则里是字面量 —— 按 grep 默认的基本正则转一下
  if (!fixed && !p.flags.has('E') && name !== 'egrep') {
    src = src.replace(/\\([|()?+{}])|([|()?+{}])/g, (_m, esc: string, lit: string) => (esc ? esc : '\\' + lit))
  }
  if (p.flags.has('w')) src = `(?:^|[^\\w])(?:${src})(?:[^\\w]|$)`
  if (p.flags.has('x')) src = `^(?:${src})$`
  let re: RegExp
  try {
    re = new RegExp(src, p.flags.has('i') ? 'i' : '')
  } catch {
    return { id: 'pagerBadRegex', params: { pattern } }
  }
  const invert = p.flags.has('v')
  const count = p.flags.has('c')
  return (lines) => {
    const hit = lines.filter((l) => re.test(l) !== invert)
    return count ? [String(hit.length)] : hit
  }
}

// head / tail:-n N、-nN、-N,默认 10 行。
function countArg(name: string, args: string[]): number | NoticeRef {
  let n = 10
  for (let i = 0; i < args.length; i++) {
    const a = args[i]
    let m: RegExpMatchArray | null
    if ((m = a.match(/^-(\d+)$/))) n = +m[1]
    else if ((m = a.match(/^-n(\d+)$/))) n = +m[1]
    else if (a === '-n' && /^\d+$/.test(args[i + 1] ?? '')) n = +args[++i]
    else if (a === '-q' || a === '-s') continue
    else return bad(name)
  }
  return n
}

function wcStage(args: string[]): Stage | NoticeRef {
  const p = flagsOf(args)
  if (!p || p.rest.length) return bad('wc')
  for (const f of p.flags) if (!'lwcm'.includes(f)) return bad('wc')
  const all = p.flags.size === 0
  return (lines) => {
    const l = lines.length
    const w = lines.reduce((s, x) => s + (x.trim() ? x.trim().split(/\s+/).length : 0), 0)
    const c = lines.reduce((s, x) => s + x.length + 1, 0)
    const parts: number[] = []
    if (all || p.flags.has('l')) parts.push(l)
    if (all || p.flags.has('w')) parts.push(w)
    if (all || p.flags.has('c') || p.flags.has('m')) parts.push(c)
    return [parts.map((x) => (parts.length > 1 ? String(x).padStart(7) : String(x))).join(' ')]
  }
}

function sortStage(args: string[]): Stage | NoticeRef {
  const p = flagsOf(args)
  if (!p || p.rest.length) return bad('sort')
  for (const f of p.flags) if (!'rnuf'.includes(f)) return bad('sort')
  const num = p.flags.has('n')
  const fold = p.flags.has('f')
  const key = (s: string) => (fold ? s.toLowerCase() : s)
  return (lines) => {
    let out = [...lines].sort((a, b) => {
      if (num) {
        const d = (parseFloat(a) || 0) - (parseFloat(b) || 0)
        if (d) return d
      }
      return key(a) < key(b) ? -1 : key(a) > key(b) ? 1 : 0
    })
    if (p.flags.has('r')) out.reverse()
    if (p.flags.has('u')) out = out.filter((x, i) => i === 0 || key(x) !== key(out[i - 1]))
    return out
  }
}

function uniqStage(args: string[]): Stage | NoticeRef {
  const p = flagsOf(args)
  if (!p || p.rest.length) return bad('uniq')
  for (const f of p.flags) if (!'cdi'.includes(f)) return bad('uniq')
  const fold = p.flags.has('i')
  const eq = (a: string, b: string) => (fold ? a.toLowerCase() === b.toLowerCase() : a === b)
  return (lines) => {
    const groups: { line: string; n: number }[] = []
    for (const l of lines) {
      const g = groups[groups.length - 1]
      if (g && eq(g.line, l)) g.n++
      else groups.push({ line: l, n: 1 })
    }
    return groups
      .filter((g) => !p.flags.has('d') || g.n > 1)
      .map((g) => (p.flags.has('c') ? `${String(g.n).padStart(7)} ${g.line}` : g.line))
  }
}

function stageOf(words: string[]): Stage | NoticeRef {
  const [name, ...args] = words
  switch (name) {
    // 分页器本身:浏览器里本来就能往回翻,原样输出(-S 之类的选项一并接受)
    case 'less': case 'more': case 'cat': case 'most':
      return (lines) => lines
    case 'grep': case 'egrep': case 'fgrep':
      return grepStage(name, args)
    case 'head': {
      const n = countArg(name, args)
      return typeof n === 'number' ? (lines) => lines.slice(0, n) : n
    }
    case 'tail': {
      const n = countArg(name, args)
      return typeof n === 'number' ? (lines) => (n === 0 ? [] : lines.slice(-n)) : n
    }
    case 'wc': return wcStage(args)
    case 'sort': return sortStage(args)
    case 'uniq': return uniqStage(args)
  }
  return { id: 'pagerUnsupported', params: { cmd: name, list: PAGER_COMMANDS.join(' ') } }
}

/** 解析 `pager` 后面那段命令。空串 = 原样输出(mysql 里等于回到标准输出)。 */
export function parsePager(cmd: string): { pager: Pager | null } | { error: NoticeRef } {
  const text = cmd.trim()
  if (!text) return { pager: null }
  const stages = splitPipeline(text)
  if (!stages) return { error: { id: 'pagerBadQuote' } }
  const fns: Stage[] = []
  for (const words of stages) {
    if (!words.length) return { error: { id: 'pagerBadPipe' } }
    // 带路径的写法(/usr/bin/less)只看命令名
    const s = stageOf([words[0].replace(/^.*\//, ''), ...words.slice(1)])
    if (typeof s !== 'function') return { error: s }
    fns.push(s)
  }
  return { pager: { text, run: (lines) => fns.reduce((acc, f) => f(acc), lines) } }
}

/** 终端的方框表格换成 mysql 客户端的 ASCII 样式(`+---+` / `|`)再交给 pager。
 *  DBA 的 pager 习惯都是冲着这个样式写的:`grep -v "+"` 去掉边框、`grep "|"` 只留数据行。 */
export function toMysqlAscii(line: string): string {
  return line.replace(/[┌┬┐├┼┤└┴┘]/g, '+').replace(/─/g, '-').replace(/│/g, '|')
}

// ---------------------------------------------------------------- 命令识别

/** 识别出的 mysql 客户端命令。arg 是命令后面那段(已去掉结尾分号)。 */
export interface ClientCommand {
  name: 'pager' | 'nopager' | 'tee' | 'notee' | 'status' | 'rehash' | 'help' | 'exit' | 'unsupported'
  arg: string
  /** unsupported 时:原始命令名,给提示用 */
  word?: string
}

// 单词写法。mysql 客户端里这些命令**回车即执行、不需要分号**,也不跨行。
const WORDS: Record<string, ClientCommand['name']> = {
  pager: 'pager', nopager: 'nopager', tee: 'tee', notee: 'notee', status: 'status', rehash: 'rehash',
  help: 'help', '?': 'help', exit: 'exit', quit: 'exit',
  // 认得、但在浏览器里做不了的:给一句明确的话,而不是发给服务端换一个语法错误
  system: 'unsupported', source: 'unsupported', delimiter: 'unsupported', warnings: 'unsupported',
  nowarning: 'unsupported', prompt: 'unsupported', charset: 'unsupported', edit: 'unsupported',
  resetconnection: 'unsupported',
}

// 反斜杠写法,**区分大小写**(\P 是 pager,\p 是 print;\T 是 tee,\t 是 notee)。
// \c \d \x \l \u 这些在本终端沿用 psql 的含义(见 metaCommand),不在这里。
const SLASH: Record<string, [ClientCommand['name'], string]> = {
  P: ['pager', 'pager'], n: ['nopager', 'nopager'], T: ['tee', 'tee'], t: ['notee', 'notee'],
  s: ['status', 'status'], '#': ['rehash', 'rehash'], h: ['help', 'help'], q: ['exit', 'exit'],
  '!': ['unsupported', 'system'], '.': ['unsupported', 'source'], W: ['unsupported', 'warnings'],
  w: ['unsupported', 'nowarning'], R: ['unsupported', 'prompt'], C: ['unsupported', 'charset'],
  e: ['unsupported', 'edit'],
}

/** 一条输入是不是 mysql 客户端命令。只对 MySQL 系引擎、只认单行。 */
export function parseClientCommand(input: string, engine: string): ClientCommand | null {
  if (!/mysql|mariadb|tidb|polardb/i.test(engine)) return null
  const s = input.trim()
  if (!s || s.includes('\n')) return null
  const strip = (a: string) => a.replace(/;\s*$/, '').trim()
  if (s.startsWith('\\')) {
    const m = s.match(/^\\(\S)(?:\s+([^]*))?$/)
    if (!m || !SLASH[m[1]]) return null
    // \t 之类必须是单个字符命令本身;\dt / \conninfo 这类多字符的留给 metaCommand
    if (s.length > 2 && !/\s/.test(s[2])) return null
    const [name, word] = SLASH[m[1]]
    return { name, arg: strip(m[2] ?? ''), word }
  }
  const m = s.replace(/;\s*$/, '').match(/^([a-z]+|\?)(?:\s+([^]*))?$/i)
  if (!m) return null
  const word = m[1].toLowerCase()
  const name = WORDS[word]
  if (!name) return null
  const arg = strip(m[2] ?? '')
  // `help` 带参数是服务端的帮助(HELP 'SELECT'),交给服务端;`status` / `exit` 这类带了
  // 参数多半不是客户端命令(比如一个叫 status 的东西后面跟着 SQL),同样不接
  if (arg && !['pager', 'tee', 'unsupported'].includes(name)) return null
  if (name === 'unsupported' && word === 'delimiter' && !arg) return null
  return { name, arg, word }
}

/** 给 LineEditor 的 isComplete:客户端命令回车即执行,不用等分号。 */
export function mysqlClientComplete(full: string, engine: string): boolean | undefined {
  return parseClientCommand(full, engine) ? true : undefined
}

/** tee 的文件名:只留文件名本身(去掉路径),空就按时间起一个。 */
export function teeFilename(arg: string, now: Date): string {
  const base = arg.replace(/^.*[\\/]/, '').replace(/[^\w.\-一-龥]+/g, '-').replace(/^-+|-+$/g, '')
  if (base) return base
  const p = (n: number) => String(n).padStart(2, '0')
  return `aegisdb-tee-${now.getFullYear()}${p(now.getMonth() + 1)}${p(now.getDate())}-${p(now.getHours())}${p(now.getMinutes())}${p(now.getSeconds())}.log`
}
