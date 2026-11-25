// Ctrl+C 在终端里到底是「复制」还是「中断」。
//
// 几乎所有终端的约定是同一条:**选中了东西就是复制,没选中才是中断**
// (Windows Terminal、PuTTY、VS Code 的集成终端都这样)。原先这里无条件当中断,
// 于是想复制一段 SQL 的人会发现自己正在打的那一行被清掉了 —— 他要执行的东西没了,
// 屏幕上只留下一个 ^C。
//
// 拆成纯函数是为了能测:这条规则的边界(Shift/Alt 要不要参与、大小写、Cmd 算不算)
// 全在这里,而它所在的那个 keydown 回调埋在组件的 onMounted 里,测不到。

/** keydown 事件里这条规则用得到的部分。 */
export interface CopyKeyEvent {
  key: string
  ctrlKey: boolean
  metaKey: boolean
  altKey: boolean
  shiftKey: boolean
}

/**
 * 这一下 Ctrl+C 是不是「复制」。
 *
 * Cmd+C 一并算上 —— Mac 上它就是复制键。**Ctrl+C 在 Mac 上按理该始终是中断**,但这里
 * 不分平台:一个在浏览器里用终端的人,肌肉记忆是 Ctrl+C 复制;而且退路是通的 ——
 * 复制之后选区被清掉,再按一次就是中断,这与 Windows Terminal 的行为一致。
 *
 * Shift / Alt 参与时不算:Ctrl+Shift+C 在不少终端里另有含义,不该被这条规则吃掉。
 */
export function isCopyShortcut(e: CopyKeyEvent, hasSelection: boolean): boolean {
  if (!hasSelection) return false
  if (!(e.ctrlKey || e.metaKey)) return false
  if (e.altKey || e.shiftKey) return false
  return e.key === 'c' || e.key === 'C'
}
