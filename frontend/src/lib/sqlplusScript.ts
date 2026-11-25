// @ / @@ / START 脚本的展开(sqlplus-compat 08)。
//
// 网关拿不到人本机上的文件,所以 @ 引用的脚本要连同它 @@ 引用的文件一起上传(多选或打成 zip)。
// 终端在本地把它们**展开成一份脚本**:被引用的文件内容就地替换掉那一行 @,替换变量(&1 &2 位置
// 参数、DEFINE、ACCEPT 问到的值)也在这一步展开。展开后的那一份才交给扫描 → 执行 / 送审。
//
// 这样审批人看到的、判定的、审计记下的、最后执行的,是**同一份文本**;后端那几条执行脚本的通道
// (直接执行、审批单执行、发布流水线)都不用知道 @ 的存在。
//
// 找文件的规则与 SQL*Plus 一致:@@ 相对当前这个文件所在的目录;@ / START 相对主脚本所在的目录
// (相当于 SQL*Plus 的当前目录)。没写扩展名补 .sql。找不到再按文件名在整批里找唯一的一个。
// 不分大小写(Windows 上写的脚本经常大小写不一)。
//
// 绝不读网关服务器上的任何路径 —— 只在这一批上传的文件里找。

import { substitute, parseStartArgs, type DefineState, type AskVar } from './sqlplus'

export type Bundle = Map<string, string> // 规范化后的相对路径 → 内容

/** 嵌套层数上限,防止 a 引 b、b 引 a 之外更隐蔽的爆炸(静态检查也会报循环)。 */
export const MAX_DEPTH = 20

export function normPath(p: string): string {
  const parts: string[] = []
  for (const seg of p.replace(/\\/g, '/').split('/')) {
    if (!seg || seg === '.') continue
    if (seg === '..') parts.pop()
    else parts.push(seg)
  }
  return parts.join('/').toLowerCase()
}

const dirOf = (key: string) => (key.includes('/') ? key.slice(0, key.lastIndexOf('/')) : '')
const baseOf = (key: string) => key.slice(key.lastIndexOf('/') + 1)
const withExt = (name: string) => (/\.[^/\\]+$/.test(name) ? name : name + '.sql')

export function makeBundle(entries: { name: string; text: string }[]): Bundle {
  const b: Bundle = new Map()
  for (const e of entries) b.set(normPath(e.name), e.text)
  return b
}

/** 在这一批文件里找 name(相对 dir);找不到返回 null。 */
export function resolveIn(b: Bundle, name: string, dir: string): string | null {
  const n = withExt(name)
  const rel = normPath((dir ? dir + '/' : '') + n)
  if (b.has(rel)) return rel
  const abs = normPath(n)
  if (b.has(abs)) return abs
  const base = baseOf(abs)
  const hits = [...b.keys()].filter((k) => baseOf(k) === base)
  return hits.length === 1 ? hits[0] : null
}

// 行首的 @file / @@file / START file(START 可缩写到 STA)。
const START_LINE = /^\s*(?:(@@?)|(STA|STAR|START)\s+)(.*)$/i

interface StartRef { nested: boolean; file: string; args: string[]; raw: string }

function startRef(line: string): StartRef | null {
  const m = line.match(START_LINE)
  if (!m) return null
  const st = parseStartArgs(m[3])
  if (!st) return null
  return { nested: m[1] === '@@', file: st.file, args: st.args, raw: m[3] }
}

export interface CheckResult { missing: string[]; cycle: string[] | null; tooDeep: boolean }

/**
 * 开跑之前先把引用关系走一遍:缺哪些文件、有没有循环、套得太深。这些都应该在问人要变量值、
 * 开始扫描之前就报出来。文件名里带 & 的(要等替换之后才知道是谁)这里跳过。
 */
export function checkIncludes(b: Bundle, mainKey: string): CheckResult {
  const missing = new Set<string>()
  let cycle: string[] | null = null
  let tooDeep = false
  const root = dirOf(mainKey)
  const walk = (key: string, stack: string[]) => {
    if (cycle || tooDeep) return
    if (stack.length > MAX_DEPTH) { tooDeep = true; return }
    if (stack.includes(key)) { cycle = [...stack.slice(stack.indexOf(key)), key]; return }
    for (const line of (b.get(key) || '').split(/\r?\n/)) {
      const r = startRef(line)
      if (!r || r.file.includes('&')) continue
      const child = resolveIn(b, r.file, r.nested ? dirOf(key) : root)
      if (!child) { missing.add(withExt(r.file)); continue }
      walk(child, [...stack, key])
    }
  }
  walk(mainKey, [])
  return { missing: [...missing], cycle, tooDeep }
}

export interface Expanded {
  text: string
  /** 替换变量改过的行:SET VERIFY ON 时打 old / new(带文件名) */
  changed: { file: string; old: string; new: string }[]
  cancelled: boolean
  /** 文件名里带 & 的引用,替换之后仍找不到 */
  missing?: string
}

class Cancelled extends Error {}

/**
 * 把主脚本连同它引用的文件展开成一份。args 是 @main.sql 后面的位置参数(&1 &2 …)。
 * 替换变量的状态(st)与终端标签共用:脚本里 DEFINE 的变量跑完之后还在(与 SQL*Plus 一致)。
 */
export async function expandScript(b: Bundle, mainKey: string, args: string[], st: DefineState, ask: AskVar, engine: string): Promise<Expanded> {
  const changed: Expanded['changed'] = []
  const root = dirOf(mainKey)
  let missing: string | undefined
  const sub = async (text: string, file: string) => {
    const r = await substitute(text, st, ask, engine)
    if (r.cancelled) throw new Cancelled()
    for (const c of r.changed) changed.push({ file, old: c.old, new: c.new })
    return r.text
  }
  const expandFile = async (key: string, fargs: string[], depth: number): Promise<string> => {
    // START 的参数就是 &1 &2 …(SQL*Plus 把它们 DEFINE 成变量 1、2 …)
    fargs.forEach((a, i) => st.vars.set(String(i + 1), a))
    const out: string[] = []
    let chunk: string[] = []
    const flush = async () => {
      if (!chunk.length) return
      out.push(await sub(chunk.join('\n'), baseOf(key)))
      chunk = []
    }
    for (const line of (b.get(key) || '').split(/\r?\n/)) {
      const r = startRef(line)
      if (!r) { chunk.push(line); continue }
      await flush()
      // 引用那一行自己也可能带 &(文件名、参数)
      const ref = r.raw.includes(st.defineChar) ? parseStartArgs(await sub(r.raw, baseOf(key))) : { file: r.file, args: r.args }
      if (!ref) continue
      const child = resolveIn(b, ref.file, r.nested ? dirOf(key) : root)
      if (!child || depth >= MAX_DEPTH) { missing = withExt(ref.file); throw new Cancelled() }
      out.push(await expandFile(child, ref.args, depth + 1))
    }
    await flush()
    return out.join('\n')
  }
  try {
    const text = await expandFile(mainKey, args, 0)
    return { text, changed, cancelled: false }
  } catch (e) {
    if (e instanceof Cancelled) return { text: '', changed, cancelled: !missing, missing }
    throw e
  }
}
