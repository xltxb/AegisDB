// sqlComplete — Web 命令行的 Tab 补全:光标处该补什么、候选有哪些、补到哪里。
//
// 不做完整的 SQL 解析,只回答补全需要的那几个问题,够用就停:
//   - 光标在字符串 / 注释里:不补;
//   - `\` 开头的第一个词:元命令;`\c` / `\d` 之后:库 / 表;
//   - FROM / JOIN / UPDATE / INTO / DESC 之后(含 FROM 列表里逗号之后):表;`库.` 之后:那个库的表;
//   - USE 之后:库;
//   - `别名.` / `表.` 之后:那张表的列(别名从整条语句的 FROM / JOIN 里解析,光标后面的也算);
//   - 其余位置:关键字 + 语句里出现的表的列 + 表名 —— 至少敲一个字母才补,空着按 Tab 不刷一屏。
//
// 交互跟 bash / psql 一样:唯一候选直接补全(关键字、元命令后面带一个空格);多个候选补到公共
// 前缀,把候选交给调用方(LineEditor 在第二次 Tab 时列出来)。关键字按人敲的大小写补。

export interface CompletionSource {
  keywords: string[]
  metas: string[]
  databases: string[]
  /** 需要引号的名字用什么引号(MySQL 系反引号,其余双引号) */
  quote: string
  /** 表名:qualifier 为空是会话当前库;否则是那个库 / schema / owner */
  tables: (qualifier: string) => Promise<string[]>
  /** 一张表的列(table 可带限定名) */
  columns: (table: string) => Promise<string[]>
  /**
   * 库 / 表 / 列名也按包含匹配(敲中间 / 后面一段也能补;开头匹配的排前面),敲满两个字才生效,
   * 关键字仍只认开头。只给数据查询页的弹框用:终端靠"补到公共前缀",包含匹配下不成立。
   */
  contains?: boolean
}

export type CompletionKind = 'none' | 'meta' | 'database' | 'table' | 'qualified' | 'column' | 'any'

export interface CompletionContext {
  kind: CompletionKind
  /** 光标前正在敲的那个词(不含限定名) */
  prefix: string
  /** `x.` 里的 x;没有为空 */
  qualifier: string
  /** 整条语句里的别名 → 表 */
  aliases: Record<string, string>
  /** 整条语句里出现的表(FROM / JOIN / UPDATE / INTO) */
  tables: string[]
}

export interface Completion {
  /** 光标前要替换掉的字符数(即 prefix 的长度) */
  replace: number
  /** 替换成的文本:唯一候选是完整的词,多个候选是公共前缀 */
  text: string
  /** 全部候选(已排序、去重) */
  candidates: string[]
}

const TABLE_AFTER = new Set(['FROM', 'JOIN', 'UPDATE', 'INTO', 'TABLE', 'DESC', 'DESCRIBE', 'TRUNCATE'])
const CLAUSE = new Set(['SELECT', 'FROM', 'WHERE', 'JOIN', 'ON', 'SET', 'GROUP', 'ORDER', 'HAVING', 'LIMIT',
  'UNION', 'INTO', 'VALUES', 'UPDATE', 'USING', 'BY', 'INNER', 'LEFT', 'RIGHT', 'FULL', 'OUTER', 'CROSS',
  'AND', 'OR', 'NOT', 'AS', 'WITH', 'RETURNING', 'FOR', 'OFFSET', 'FETCH', 'WINDOW', 'EXCEPT', 'INTERSECT', 'MINUS'])

const IDENT_CH = /[\p{L}\p{N}_$]/u
const PLAIN_IDENT = /^[\p{L}_][\p{L}\p{N}_$]*$/u

interface Tok { t: string; word: boolean }

// 抹掉字符串与注释(保留长度无关紧要,这里只要结构),并报告结尾是否停在字符串 / 注释里。
function stripLiterals(s: string): { text: string; inside: boolean } {
  let out = ''
  let i = 0
  while (i < s.length) {
    const ch = s[i]
    if (ch === '-' && s[i + 1] === '-') {
      const nl = s.indexOf('\n', i)
      if (nl < 0) return { text: out, inside: true }
      out += ' '; i = nl; continue
    }
    if (ch === '/' && s[i + 1] === '*') {
      const end = s.indexOf('*/', i + 2)
      if (end < 0) return { text: out, inside: true }
      out += ' '; i = end + 2; continue
    }
    if (ch === "'") {
      let j = i + 1
      for (; j < s.length; j++) {
        if (s[j] === '\\') { j++; continue }
        if (s[j] === "'") { if (s[j + 1] === "'") { j++; continue } break }
      }
      if (j >= s.length) return { text: out, inside: true }
      out += "''"; i = j + 1; continue
    }
    out += ch; i++
  }
  return { text: out, inside: false }
}

// 词法:标识符(可带点号限定、可带反引号 / 双引号)与单个标点。
function tokenize(s: string): Tok[] {
  const out: Tok[] = []
  let i = 0
  while (i < s.length) {
    const ch = s[i]
    if (/\s/.test(ch)) { i++; continue }
    if (IDENT_CH.test(ch) || ch === '`' || ch === '"') {
      let j = i
      while (j < s.length) {
        if (s[j] === '`' || s[j] === '"') {
          const q = s[j]
          const end = s.indexOf(q, j + 1)
          j = end < 0 ? s.length : end + 1
        } else if (IDENT_CH.test(s[j]) || s[j] === '.') {
          j++
        } else break
      }
      out.push({ t: s.slice(i, j), word: true })
      i = j
      continue
    }
    out.push({ t: ch, word: false })
    i++
  }
  return out
}

const unquote = (s: string) => s.split('.').map((p) => p.replace(/^[`"]|[`"]$/g, '')).join('.')
const isKeyword = (w: string) => CLAUSE.has(w.toUpperCase()) || TABLE_AFTER.has(w.toUpperCase())

// FROM 子句到这些关键字为止。
const FROM_END = new Set(['WHERE', 'GROUP', 'ORDER', 'HAVING', 'LIMIT', 'UNION', 'SET', 'VALUES', 'RETURNING',
  'WINDOW', 'EXCEPT', 'INTERSECT', 'MINUS', 'OFFSET', 'FETCH', 'FOR'])

// 从整条语句里收集 FROM / JOIN / UPDATE / INTO 引用的表与别名。
//
// FROM 子句里,同一层括号上的逗号之后是下一张表 —— 也包括 `JOIN b ON … , c` 这种写在 ON 条件
// 之后的逗号连接;括号里的逗号(函数参数、子查询)不算。
function collectTables(stmt: string): { aliases: Record<string, string>; tables: string[] } {
  const toks = tokenize(stripLiterals(stmt).text)
  const aliases: Record<string, string> = {}
  const tables: string[] = []
  // 读 j 处的一个表引用(表名 [AS] [别名]),返回之后的位置
  const readRef = (j: number): number => {
    const tb = toks[j]
    if (!tb || !tb.word || isKeyword(tb.t)) return j
    const table = unquote(tb.t)
    tables.push(table)
    j++
    if (toks[j]?.word && toks[j].t.toUpperCase() === 'AS') j++
    const al = toks[j]
    if (al?.word && !isKeyword(al.t) && !al.t.includes('.')) { aliases[unquote(al.t)] = table; j++ }
    return j
  }
  let depth = 0
  let fromDepth = -1 // 在 FROM 子句里时是它所在的括号层数,否则 -1
  for (let i = 0; i < toks.length; i++) {
    const tk = toks[i]
    if (tk.t === '(') { depth++; continue }
    if (tk.t === ')') { depth--; if (depth < fromDepth) fromDepth = -1; continue }
    if (tk.t === ',' && depth === fromDepth) { i = readRef(i + 1) - 1; continue }
    if (!tk.word) continue
    const kw = tk.t.toUpperCase()
    if (kw === 'FROM') { fromDepth = depth; i = readRef(i + 1) - 1; continue }
    if (kw === 'JOIN' || kw === 'UPDATE' || kw === 'INTO') { i = readRef(i + 1) - 1; continue }
    if (FROM_END.has(kw) && depth === fromDepth) fromDepth = -1
  }
  return { aliases, tables: [...new Set(tables)] }
}

/** 分析光标前的文本(before)在整条语句(stmt,用来解析别名)里该补什么。 */
export function analyzeCompletion(before: string, stmt: string = before): CompletionContext {
  const { aliases, tables } = collectTables(stmt)
  const base = { aliases, tables, qualifier: '', prefix: '' }
  const { text, inside } = stripLiterals(before)
  if (inside) return { ...base, kind: 'none' }

  // 光标前的词:可带 `x.` 限定;x 可带引号
  const m = /((?:[`"][^`"]*[`"]|[\p{L}\p{N}_$]+)\.)?([\p{L}\p{N}_$]*)$/u.exec(text)
  const prefix = m?.[2] ?? ''
  const qualifier = m?.[1] ? unquote(m[1].slice(0, -1)) : ''
  const head = text.slice(0, text.length - (m?.[0].length ?? 0))

  const trimmed = text.trimStart()
  if (trimmed.startsWith('\\')) {
    // 元命令本身:`\` 之后、第一个空白之前
    if (!/\s/.test(trimmed)) return { ...base, kind: 'meta', prefix: trimmed }
    const cmd = trimmed.slice(1).split(/\s+/)[0].toLowerCase()
    if (['c', 'connect', 'u', 'use'].includes(cmd)) return { ...base, kind: 'database', prefix, qualifier }
    if (cmd.startsWith('d')) return { ...base, kind: qualifier ? 'qualified' : 'table', prefix, qualifier }
    return { ...base, kind: 'none' }
  }

  const toks = tokenize(head)
  const prev = toks[toks.length - 1]
  const prevWord = prev?.word ? prev.t.toUpperCase() : ''
  // FROM 列表里逗号之后:往回找最近的子句关键字,是 FROM 才算
  let inFromList = false
  if (prev && prev.t === ',') {
    for (let i = toks.length - 2; i >= 0; i--) {
      const w = toks[i].word ? toks[i].t.toUpperCase() : ''
      if (CLAUSE.has(w) || TABLE_AFTER.has(w)) { inFromList = w === 'FROM'; break }
    }
  }
  if (TABLE_AFTER.has(prevWord) || inFromList) return { ...base, kind: qualifier ? 'qualified' : 'table', prefix, qualifier }
  if (prevWord === 'USE') return { ...base, kind: 'database', prefix, qualifier }
  if (qualifier) return { ...base, kind: 'column', prefix, qualifier }
  return { ...base, kind: 'any', prefix }
}

/** 候选的最长公共前缀:不分大小写比较,取第一个候选的写法。 */
export function longestCommonPrefix(xs: string[]): string {
  if (!xs.length) return ''
  let p = xs[0]
  for (const x of xs.slice(1)) {
    let i = 0
    while (i < p.length && i < x.length && p[i].toLowerCase() === x[i].toLowerCase()) i++
    p = p.slice(0, i)
  }
  return p
}

// 关键字跟着人敲的大小写:敲的全是小写就补小写,否则大写。
function caseLike(kw: string, prefix: string): string {
  return prefix && prefix === prefix.toLowerCase() && prefix !== prefix.toUpperCase() ? kw.toLowerCase() : kw.toUpperCase()
}

// 导出给数据查询页的补全弹框用(插入候选时同一套加引号规则)
export function quoteIfNeeded(name: string, q: string): string {
  return PLAIN_IDENT.test(name) ? name : q + name + q
}

/**
 * 补全整条语句 stmt 在光标 cursor 处的词。没有可补的返回 null。
 * 取表、取列是异步的(可能要问服务端),调用方应在结果回来时确认输入没变过再应用。
 */
export async function complete(stmt: string, src: CompletionSource, cursor = stmt.length): Promise<Completion | null> {
  const ctx = analyzeCompletion(stmt.slice(0, cursor), stmt)
  const { prefix } = ctx
  const lower = prefix.toLowerCase()
  const byName = (a: string, b: string) => a.localeCompare(b)
  const match = (xs: string[]) => [...new Set(xs)].filter((x) => x.toLowerCase().startsWith(lower)).sort(byName)
  // 包含但不以它开头的。contains 关着、不足两个字、或敲的已经是完整的关键字 / 名字时为空 ——
  // 否则敲完 AND / ON / id 也会因为 brand / amount / order_id 弹框,回车换行被吃掉。
  const isWhole = (xs: string[]) => xs.some((x) => x.toLowerCase() === lower)
  const inner = (xs: string[]) => (!src.contains || lower.length < 2 || isWhole(xs) || isWhole(src.keywords) ? [] : [...new Set(xs)]
    .filter((x) => { const l = x.toLowerCase(); return !l.startsWith(lower) && l.includes(lower) }).sort(byName))
  const matchName = (xs: string[]) => [...match(xs), ...inner(xs)]

  let cands: string[] = []
  let keywordish = false
  switch (ctx.kind) {
    case 'none':
      return null
    case 'meta':
      cands = match(src.metas)
      keywordish = true
      break
    case 'database':
      cands = matchName(src.databases)
      break
    case 'table':
      cands = matchName(await src.tables(''))
      break
    case 'qualified':
      cands = matchName(await src.tables(ctx.qualifier))
      break
    case 'column': {
      const table = ctx.aliases[ctx.qualifier] ?? ctx.qualifier
      cands = matchName(await src.columns(table))
      if (!cands.length) cands = matchName(await src.tables(ctx.qualifier)) // `库.` 写在 FROM 之外的位置
      break
    }
    case 'any': {
      if (!prefix) return null
      const kws = match(src.keywords)
      const cols = (await Promise.all(ctx.tables.map((t) => src.columns(t)))).flat()
      const pool = [...cols, ...(await src.tables(''))]
      const names = match(pool)
      const rest = inner(pool)
      if (!names.length && !rest.length && kws.length) {
        cands = kws.map((k) => caseLike(k, prefix))
        keywordish = true
      } else {
        cands = [...[...new Set([...names, ...kws.map((k) => caseLike(k, prefix))])].sort(byName), ...rest]
      }
      break
    }
  }
  if (!cands.length) return null

  if (cands.length === 1) {
    const one = cands[0]
    const text = keywordish ? one + ' ' : quoteIfNeeded(one, src.quote)
    return { replace: prefix.length, text, candidates: cands }
  }
  // 多个候选:补到公共前缀。前缀里人已经敲的部分保留原样,只接上候选里多出来的那段。
  // 包含匹配的候选不一定以敲的这段开头,公共前缀无从谈起:原样留着,交给弹框选。
  const lcp = cands.every((c) => c.toLowerCase().startsWith(lower)) ? longestCommonPrefix(cands) : ''
  const text = lcp.length > prefix.length ? prefix + lcp.slice(prefix.length) : prefix
  return { replace: prefix.length, text, candidates: cands }
}
