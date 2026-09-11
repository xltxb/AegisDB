/**
 * 数一段文本里有几条 SQL 语句 —— **只为一个界面判断服务**:粘进终端的东西要不要
 * 先弹窗给人看一眼。
 *
 * 它不是判定的一环,也不打算和后端的 sqlutil.SplitStatements 完全一致。那个拆分器
 * 要处理 PL/SQL 整块、MySQL DELIMITER 指令、PostgreSQL 美元引用,并且被一条硬规则
 * 约束着(宁可多拆不可少拆,因为合并等于绕过判定)。在这里重写一份那样的东西,只会
 * 得到第二份慢慢分叉的实现。
 *
 * 这里数错了会怎样,两个方向都无害:
 *
 *   数少了(说 1 条,其实 2 条)→ 走原来的路:直接进行编辑器。而行编辑器本来就把
 *     粘贴的批次一条一条交给 handleSubmit,每条照样过判定。就是今天的行为。
 *   数多了(说 2 条,其实 1 条)→ 弹窗打开,人看着文本按确认,再走同一条路。
 *
 * 也就是说:**它决定的只是"要不要先给人看一眼",不决定任何一条语句怎么被判、怎么
 * 被执行。** 没有一条语句能因为这个函数数错而绕过网关。
 */

/** 一段文本"看起来"是不是一个整块的 PL/SQL 单元(包体、过程、匿名块…)。
 *
 *  这类单元内部有大量分号,但它是**一条**语句 —— 后端的拆分器专门把它整块取走。
 *  这里同样整块看待,否则粘一个存储过程会被说成"二十条语句"。 */
const PLSQL_HEAD = /^\s*(DECLARE\b|BEGIN\b|CREATE\s+(OR\s+REPLACE\s+)?(PACKAGE|PROCEDURE|FUNCTION|TRIGGER|TYPE)\b)/i

/** 行首的 MySQL DELIMITER 指令 —— 出现它就说明这是一段脚本,不是一条语句。 */
const DELIMITER_DIRECTIVE = /^[ \t]*DELIMITER[ \t]+\S/im

export function countStatements(text: string): number {
  const src = text ?? ''
  if (!src.trim()) return 0
  // 带 DELIMITER 的一定是脚本。它内部的分号已经不是分隔符,数分号数出来的是错的,
  // 但结论("这是多条")是对的。
  if (DELIMITER_DIRECTIVE.test(src)) return 2
  if (PLSQL_HEAD.test(src)) return 1

  let n = 0
  let pending = false // 当前是否已经攒到了非空白内容
  for (let i = 0; i < src.length; i++) {
    const ch = src[i]

    // ---- 注释:整段跳过,里面的分号不算 ----
    if (ch === '-' && src[i + 1] === '-') {
      while (i < src.length && src[i] !== '\n') i++
      continue
    }
    if (ch === '/' && src[i + 1] === '*') {
      i += 2
      while (i < src.length && !(src[i] === '*' && src[i + 1] === '/')) i++
      i++
      continue
    }

    // ---- 字符串 / 引号标识符 ----
    // 终止规则跟标准走:只有成对的引号('' "" ``)算转义,反斜杠是普通字符。
    // 这和后端同一个理由 —— 认 MySQL 的反斜杠转义,会在 PostgreSQL 目标上把一个
    // 分号吞进"字符串"里,于是两条语句数成一条。
    if (ch === "'" || ch === '"' || ch === '`') {
      const q = ch
      i++
      while (i < src.length) {
        if (src[i] === q) {
          if (src[i + 1] === q) { i += 2; continue } // 成对转义
          break
        }
        i++
      }
      pending = true
      continue
    }
    // PostgreSQL 美元引用 $tag$ … $tag$:里面可以合法地放整段带分号的函数体。
    if (ch === '$') {
      const m = /^\$[A-Za-z_-￿][A-Za-z0-9_-￿]*\$|^\$\$/.exec(src.slice(i))
      if (m) {
        const tag = m[0]
        const end = src.indexOf(tag, i + tag.length)
        i = end < 0 ? src.length : end + tag.length - 1
        pending = true
        continue
      }
    }

    if (ch === ';') {
      if (pending) n++
      pending = false
      continue
    }
    if (!/\s/.test(ch)) pending = true
  }
  // 最后一条可以没有分号 —— 终端里常常就是这么敲的。
  if (pending) n++
  return n
}

/** 粘进来的这段东西要不要先弹窗给人看一眼。 */
export function isMultiStatement(text: string): boolean {
  return countStatements(text) > 1
}
