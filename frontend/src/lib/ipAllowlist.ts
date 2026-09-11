// ipAllowlist —— IP 白名单文本的拆分、校验与规范化。
//
// Vue 版在保存时把条目 `filter(Boolean)` 之后直接拼起来,**非法条目被静默丢掉**:
// 写 `10.20.0.0/16, 10.20.0.300` 存进去只剩前一条,而输入框里那行 300 还在。人
// 由此以为那台机器被放行了 —— 直到它被网关挡在门外,回这一页一看条目又明明写着。
// 静默修正在这里的代价是"以为生效了",所以这里只**判定**,不修补:非法就报非法,
// 交给调用方当场标红并挡住保存。
//
// 判定标准对齐服务端(middleware.ipMatches):带 `/` 的走 CIDR,否则是单个地址;
// 分隔符与 splitCIDRs 一致 —— 逗号、换行、空白都算。

/** 把白名单文本拆成条目。空白与空条目一律丢掉。 */
export function splitIpEntries(text: string): string[] {
  return text.split(/[\s,]+/).map((s) => s.trim()).filter(Boolean)
}

/** IPv4 点分十进制。前导零不算合法 —— Go 的 net.ParseIP 从 1.17 起就拒绝它。 */
function isIPv4(s: string): boolean {
  const parts = s.split('.')
  if (parts.length !== 4) return false
  return parts.every((p) => /^(0|[1-9]\d{0,2})$/.test(p) && Number(p) <= 255)
}

/**
 * IPv6,含 `::` 压缩与末尾内嵌 IPv4(`::ffff:10.0.0.1`)。
 *
 * 只出现一次 `::`,它至少压掉一组,所以两侧组数之和不能满 8。
 */
function isIPv6(s: string): boolean {
  if (!s.includes(':')) return false
  const halves = s.split('::')
  if (halves.length > 2) return false

  // 返回这一段占几组;末段允许是一个 IPv4(占两组)。非法返回 null。
  const groupsOf = (part: string, allowV4: boolean): number | null => {
    if (part === '') return 0
    const gs = part.split(':')
    let n = 0
    for (let i = 0; i < gs.length; i++) {
      const g = gs[i]
      if (allowV4 && i === gs.length - 1 && g.includes('.')) {
        if (!isIPv4(g)) return null
        n += 2
        continue
      }
      if (!/^[0-9a-fA-F]{1,4}$/.test(g)) return null
      n += 1
    }
    return n
  }

  if (halves.length === 2) {
    const head = groupsOf(halves[0], false) // 内嵌 IPv4 只能出现在最后
    const tail = groupsOf(halves[1], true)
    if (head === null || tail === null) return false
    return head + tail <= 7
  }
  return groupsOf(s, true) === 8
}

/** 一条白名单条目是不是合法的 IP 或 CIDR。 */
export function isValidIpEntry(raw: string): boolean {
  const s = raw.trim()
  if (!s) return false
  const slash = s.indexOf('/')
  if (slash < 0) return isIPv4(s) || isIPv6(s)
  const addr = s.slice(0, slash)
  const bits = s.slice(slash + 1)
  if (!/^(0|[1-9]\d{0,2})$/.test(bits)) return false
  const n = Number(bits)
  if (isIPv4(addr)) return n <= 32
  if (isIPv6(addr)) return n <= 128
  return false
}

/** 文本里所有非法的条目,按原样返回(要直接显示给人看)。 */
export function invalidIpEntries(text: string): string[] {
  return splitIpEntries(text).filter((e) => !isValidIpEntry(e))
}

/** 规范化成服务端存的那一种形状:逗号加空格分隔的一行。 */
export function normalizeIpAllowlist(text: string): string {
  return splitIpEntries(text).join(', ')
}
