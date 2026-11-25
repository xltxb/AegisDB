// sqlFormat — 把一条审计里的命令排版成能读的样子,供详情抽屉展示。
//
// 它**不是**一个 SQL 格式化器,也不打算变成一个。审计行里躺着的不一定是 SQL:登录、
// 配置变更(admin.user.create …)、发布单、"开启执行窗口「…」" ——这些排版只会更难读。
// 所以第一件事是判断"这看起来是不是一条 SQL",不是就原样返回。
//
// 判断得出是 SQL 时,只做两件保守的事:
//
//   · 按分号拆成多条语句,一条一行(多语句脚本最该先看清的就是"一共有几条");
//   · 在**顶层子句关键字**前断行(FROM / WHERE / JOIN / GROUP BY …)。
//
// 不碰缩进、不重排列名、不改大小写 —— 改了大小写的语句复制出去还能跑,但它不再是
// 审计行里那一条了,而这一页的全部意义就是"当时跑的到底是什么"。复制按钮给的也
// 永远是原文,不是这里排过版的结果。
//
// 关键字只在**引号与注释之外**才算数:`WHERE name = 'order by'` 里的那个 order by
// 是数据,不是子句。

/** 认得出这些开头才当成 SQL 排版。 */
const SQL_VERBS = new Set([
  'select', 'insert', 'update', 'delete', 'merge', 'replace',
  'create', 'alter', 'drop', 'truncate', 'rename', 'comment',
  'grant', 'revoke', 'with', 'explain', 'call', 'set', 'show', 'desc', 'describe',
])

/** 顶层子句:在它们之前断行。多词的写在前面,先匹配长的。 */
const CLAUSES = [
  'left outer join', 'right outer join', 'full outer join', 'cross join',
  'left join', 'right join', 'full join', 'inner join', 'outer join', 'natural join',
  'group by', 'order by', 'partition by', 'union all', 'insert into', 'delete from',
  'join', 'from', 'where', 'having', 'limit', 'offset', 'values', 'set', 'union',
  'returning', 'on conflict', 'fetch first',
]

const isWordChar = (c: string) => /[A-Za-z0-9_$]/.test(c)

/** 这条命令看起来是 SQL 吗 —— 只看第一个词。 */
export function looksLikeSQL(command: string): boolean {
  const first = command.trim().split(/[\s(;]+/)[0] || ''
  return SQL_VERBS.has(first.toLowerCase())
}

/**
 * 排版一条命令。不是 SQL、或者排完与原文无异时,返回原文。
 *
 * 结果只用来**看**。要复制、要重跑,用原文。
 */
export function formatSQL(command: string): string {
  const src = command.trim()
  if (!looksLikeSQL(src)) return src

  let out = ''
  let i = 0
  // 逐字符扫,因为要知道自己此刻在不在引号 / 注释里面。正则做不到这件事 ——
  // 一个 '...' 里的 FROM 和一个真的 FROM 长得一模一样。
  while (i < src.length) {
    const c = src[i]

    // 字符串与带引号的标识符:整段原样抄过去,里面什么都不算关键字。
    if (c === "'" || c === '"' || c === '`') {
      const quote = c
      out += c
      i++
      while (i < src.length) {
        out += src[i]
        // '' 是 SQL 里的转义单引号,不是结束
        if (src[i] === quote && src[i + 1] === quote) {
          out += src[i + 1]
          i += 2
          continue
        }
        if (src[i] === quote) { i++; break }
        i++
      }
      continue
    }
    // 行注释与块注释:同样原样抄完
    if (c === '-' && src[i + 1] === '-') {
      const nl = src.indexOf('\n', i)
      const end = nl === -1 ? src.length : nl + 1
      out += src.slice(i, end)
      i = end
      continue
    }
    if (c === '/' && src[i + 1] === '*') {
      const close = src.indexOf('*/', i + 2)
      const end = close === -1 ? src.length : close + 2
      out += src.slice(i, end)
      i = end
      continue
    }
    // 分号:一条语句结束,下一条另起一行
    if (c === ';') {
      out += ';\n'
      i++
      while (i < src.length && /\s/.test(src[i])) i++
      continue
    }

    // 子句关键字:只在词边界上算数,前面补一个换行
    if (isWordChar(c) && (i === 0 || !isWordChar(src[i - 1]))) {
      const lower = src.slice(i).toLowerCase()
      const hit = CLAUSES.find(
        (k) => lower.startsWith(k) && !isWordChar(src[i + k.length] ?? ' '),
      )
      if (hit) {
        // 句首那个不换 —— 否则排完的头一行是空的
        if (out.trim() && !out.endsWith('\n')) out = out.replace(/[ \t]+$/, '') + '\n'
        out += src.slice(i, i + hit.length)
        i += hit.length
        continue
      }
    }

    // 普通空白压成一个空格,但换行保留(原文本来就分好行的语句不该被挤成一坨)
    if (/[ \t]/.test(c)) {
      if (!out.endsWith(' ') && !out.endsWith('\n')) out += ' '
      i++
      continue
    }
    out += c
    i++
  }
  return out.trim()
}
