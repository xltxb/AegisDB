// clipboard — 复制文本,在非安全上下文里也要能用。
//
// navigator.clipboard 只在**安全上下文**(HTTPS,或 localhost)里存在。这个网关
// 经常被人用局域网 IP 直接打开(http://10.28.2.125:5173),在那种地址下它是
// undefined —— 只调用它的复制按钮会什么都不做,而点的人看不出为什么:剪贴板里
// 还是上一次的内容,像是"复制成功了但粘出来是旧的"。
//
// 所以保留 execCommand('copy') 兜底。它确实已废弃,但它正是为这种上下文准备的
// 最后一条路,而且所有目标浏览器都还支持。返回值如实反映成没成 —— 复制这种事,
// 谎报成功比失败更糟。

/** 把文本放进剪贴板。返回是否真的成功。 */
export async function copyText(text: string): Promise<boolean> {
  if (!text) return false
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text)
      return true
    }
  } catch {
    // 安全上下文里也可能被权限策略拒绝 —— 继续走兜底,而不是直接判失败
  }
  try {
    const ta = document.createElement('textarea')
    ta.value = text
    // 不能用 display:none / visibility:hidden —— 那样选不中,也就复制不了。
    // 挪到视口外并设为只读,避免 iOS 上弹出键盘。
    ta.setAttribute('readonly', '')
    ta.style.position = 'fixed'
    ta.style.top = '-1000px'
    ta.style.left = '0'
    ta.style.opacity = '0'
    document.body.appendChild(ta)
    ta.select()
    ta.setSelectionRange(0, ta.value.length) // iOS 上 select() 不够
    const ok = document.execCommand('copy')
    document.body.removeChild(ta)
    return ok
  } catch {
    return false
  }
}
