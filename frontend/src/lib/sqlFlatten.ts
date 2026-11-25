// 把多行语句压成**语义不变**的一行。
//
// 历史记录只能存一行:行编辑器的模型是"只编辑当前物理行,已完成的行冻结在上面",
// 召回一条历史命令时它必须整个装进一个缓冲区里。
//
// 而直接把换行换成空格是不安全的 —— `--` 注释靠换行结束。这样一条语句:
//
//     WHERE u.username NOT IN (
//       -- 系统自带/官方工具
//       'SYS', 'SYSTEM'
//     )
//
// 压成一行后变成 `WHERE u.username NOT IN ( -- 系统自带/官方工具 'SYS', 'SYSTEM' )`,
// 于是 `--` 之后的一切都成了注释,服务端收到的是半截 `... NOT IN (`,
// Oracle 报 ORA-00936: missing expression。首次执行是好的(那时换行还在),
// 上翻再执行就炸 —— 同一条 SQL 昨天能跑今天不能跑,原因还完全看不出来。
//
// 所以行注释在压平时改写成块注释:`-- 文本` → `/* 文本 */`。块注释不依赖换行,
// 语义与原文一致,注释内容也保留下来(直接丢掉注释同样安全,但召回时用户会发现
// 自己写的说明不见了)。

/** 把 `-- …` 行注释改写成 `/* … *\/` 后,再把换行压成空格。
 *
 *  引号有感知:字符串字面量和引号标识符里的 `--` 不是注释(`SELECT 'a -- b'`),
 *  已有的块注释也原样跳过。 */
export function flattenStatement(sql: string): string {
  let out = ''
  let i = 0
  const n = sql.length
  while (i < n) {
    const c = sql[i]

    // 字符串字面量 / 引号标识符:整段照抄。'' "" `` 都用重复引号转义。
    if (c === "'" || c === '"' || c === '`') {
      const q = c
      out += c
      i++
      while (i < n) {
        out += sql[i]
        if (sql[i] === q) {
          if (sql[i + 1] === q) { out += sql[++i]; i++; continue } // 转义的引号
          i++
          break
        }
        i++
      }
      continue
    }

    // 已有的块注释:原样跳过,里面的 -- 不算行注释的开头。
    if (c === '/' && sql[i + 1] === '*') {
      const end = sql.indexOf('*/', i + 2)
      const stop = end < 0 ? n : end + 2
      out += sql.slice(i, stop)
      i = stop
      continue
    }

    // 行注释:吃到行尾,改写成块注释。
    if (c === '-' && sql[i + 1] === '-') {
      let end = sql.indexOf('\n', i)
      if (end < 0) end = n
      const body = sql.slice(i + 2, end)
      // 注释正文里若含 `*/` 会提前关掉块注释,把后面的 SQL 又变成注释外的碎片。
      // 拆开它即可,肉眼读起来没有区别。
      out += '/*' + body.replace(/\*\//g, '* /') + ' */'
      i = end
      continue
    }

    out += c
    i++
  }
  // 到这里已经没有靠换行结束的东西了,可以安全地把换行压成空格。
  return out.replace(/\s*\n\s*/g, ' ').trim()
}
