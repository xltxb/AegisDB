// oscText — 在线变更(生产大表在线加 / 删索引)的几处文字,终端、审批页、发布页共用。
//
// 前置检查的阻塞 / 警告由后端给 {code, reason}:reason 是引擎的规范中文,里面嵌着表名、行数、
// 磁盘大小这类细节。中文界面直接显示 reason(细节完整);其它语言按 code 给一句对应语言的说明,
// 认不出的 code 回落到 reason —— 与 ruleText 的回落同一个道理:新代码随后端发布,前端晚一版
// 也得能告诉人为什么。

export interface OSCItem {
  code: string
  reason: string
}

type Translate = (key: string, named?: Record<string, unknown>) => string
type HasKey = (key: string) => boolean

export function oscItemText(item: OSCItem, locale: string, t: Translate, te: HasKey): string {
  if (locale.startsWith('zh')) return item.reason
  const key = 'oscItem.' + item.code
  return te(key) ? t(key) : item.reason
}

/** 1.5 GB / 320 MB / 12 KB —— 与后端 humanBytes 一致的量级说法。 */
export function humanSize(n: number): string {
  if (n >= 1 << 30) return (n / (1 << 30)).toFixed(1) + ' GB'
  if (n >= 1 << 20) return (n / (1 << 20)).toFixed(1) + ' MB'
  if (n >= 1 << 10) return (n / (1 << 10)).toFixed(1) + ' KB'
  return n + ' B'
}

/** 迁移的时刻:MM-DD hh:mm:ss(与审批列表同一种写法,只多到秒)。 */
export const oscTime = (s: string) => new Date(s).toLocaleString('sv').slice(5, 19)

/** 迁移的时长:动辄几十分钟到几小时,按"时 分 / 分 秒 / 秒"说。 */
export function oscDuration(ms: number, t: Translate): string {
  const s = Math.max(0, Math.floor(ms / 1000))
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  if (h > 0) return t('oscDurHM', { h, m })
  if (m > 0) return t('oscDurMS', { m, s: s % 60 })
  return t('oscDurS', { s })
}

/** 千分位。 */
export const thousands = (n: number) => (n ?? 0).toLocaleString('en-US')
