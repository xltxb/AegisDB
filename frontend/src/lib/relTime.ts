// 相对时间 —— "刚刚 / 3 分钟前 / 2 小时前 / 5 天前"。
//
// 只有这一份。审查时发现总览页和数据源页各写了一份逐行相同的实现,外加两套一模一样的
// 中文文案(rel* 与 age*),而英文那边已经分叉('{n}m ago' 与 '{n} min ago')——同一个
// 年龄在两个页面上显示得不一样。
//
// t 由调用方传进来:这里不持有 i18n 实例,让它在组件之外也能用。

type T = (key: string, named?: Record<string, unknown>) => string

/** 相对时间。nowMs 可传入一个会走的"现在",让显示随时间推进(见 hooks/useNow)。 */
export function relTime(t: T, s?: string | null, nowMs = Date.now()): string {
  if (!s) return '—'
  const ms = nowMs - new Date(s).getTime()
  if (!isFinite(ms) || ms < 0) return '—'
  const m = Math.floor(ms / 60000)
  if (m < 1) return t('relJustNow')
  if (m < 60) return t('relMin', { n: m })
  const h = Math.floor(m / 60)
  if (h < 24) return t('relHour', { n: h })
  return t('relDay', { n: Math.floor(h / 24) })
}

/** 绝对时刻,给 title 用。 */
export function absTime(s?: string | null): string {
  return s ? new Date(s).toLocaleString('sv').slice(0, 19) : '—'
}
