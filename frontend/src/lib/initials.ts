// initials — 头像上的姓名缩写。
//
// 服务端早就有这套规则(bootstrap.abbrev),用户自己的缩写就是它算的、随 /auth/me 下发
// 的那一个。但审批列表里的发起人只是一个名字字符串,于是那里另写了一句
// `name.slice(0, 2).toUpperCase()` —— 一份跑偏的重复实现:
//
//   "Lin Wei" → "LI"，而服务端给的是 "LW"
//
// 于是同一个人的头像,在导航栏上是 LW,在审批列表里是 LI。两处说的是同一件事,规则却
// 有两份 —— 这里把前端这一份对齐服务端,而不是再发明第三种。
//
// 中文名照服务端的做法取前两个字("升级单平台" → "升级")。它和紧挨着的全名确实重复,
// 但那是这套规则本来的样子;要改该两边一起改,而不是让前端偷偷跟服务端不一样。

/** 姓名缩写,规则与服务端 bootstrap.abbrev 一致。 */
export function initialsOf(name: string): string {
  const trimmed = (name || '').trim()
  if (!trimmed) return 'AD'
  // 用 Array.from 而不是下标:按**字符**取,而不是按 UTF-16 码元 —— 名字里带一个
  // 星标或表情就会被 slice 从中间劈开,吐出半个代理对。
  const parts = trimmed.split(/\s+/).filter(Boolean)
  if (parts.length >= 2) {
    return (Array.from(parts[0])[0] + Array.from(parts[1])[0]).toUpperCase()
  }
  return Array.from(trimmed).slice(0, 2).join('').toUpperCase()
}
