/**
 * 数据查询页的两种结果看法(2026-11-12)。
 *
 *   - 结尾的 `\G`:mysql 客户端的"执行并竖排显示"。它不是 SQL —— 剥掉再交给后端(后端也会剥,
 *     兼容直接调接口的人),结果按 `*** 1. row ***` 竖排画。`\g` 是横排,剥掉照常画表格。
 *   - 看 DDL:`SHOW CREATE TABLE` 的结果(两列 Table / Create Table、一行)单独画成代码块,
 *     不塞进表格里被省略成一行。PG 系 / Oracle 的这条语句由网关渲染,形状与 MySQL 相同。
 */

const VERTICAL_RE = /\\([Gg])\s*;?\s*$/

/** 剥掉结尾的 \G / \g;只剩空白时原样退回(那不是一条语句,让后端说话)。 */
export function splitVertical(sql: string): { sql: string; vertical: boolean } {
  const m = VERTICAL_RE.exec(sql)
  if (!m) return { sql, vertical: false }
  const rest = sql.slice(0, m.index).trimEnd()
  if (!rest.trim()) return { sql, vertical: false }
  return { sql: rest, vertical: m[1] === 'G' }
}

/** SHOW CREATE TABLE / VIEW 的结果:返回对象名与 DDL;不是这种形状返回 null。 */
export function ddlOf(r: { columns: string[]; rows: string[][] } | null | undefined): { name: string; ddl: string } | null {
  if (!r || r.columns.length < 2 || r.rows.length !== 1) return null
  const i = r.columns.findIndex((c) => /^Create (Table|View)$/i.test(c))
  if (i < 1) return null
  const ddl = r.rows[0][i] ?? ''
  if (!ddl.trim()) return null
  return { name: r.rows[0][0] ?? '', ddl }
}

/**
 * 看某张表 DDL 的语句。表名一律用反引号:MySQL 系原样认;PG 系 / Oracle 由网关解析(反引号 =
 * 照原样的名字,不折叠大小写),语句不会发到库上。名字里带反引号的不给 —— 判定层不认转义。
 */
export function showCreateSql(table: string): string {
  if (!table || table.includes('`')) return ''
  return `SHOW CREATE TABLE \`${table}\``
}

/**
 * 数据查询请求在浏览器这边最多等多久(毫秒)。库端时限由服务端执行(授权时限,封顶于【设置】里的
 * 超时上限,最高 3600 秒);浏览器只要比它多等一会儿 —— 判定、取库列表、建连、脱敏都在时限之外 ——
 * 就总能收到服务端"超时已取消"的回答,而不是自己先断开报网络错误。
 * 授权上的 effectiveTimeoutSec 不含上限的截断,只会偏大,所以拿它当上界是安全的;拿不到时按默认上限 300 秒。
 */
export function bizClientTimeoutMs(timeoutSec?: number): number {
  const sec = timeoutSec && timeoutSec > 0 ? timeoutSec : 300
  return (Math.max(sec, 300) + 60) * 1000
}
