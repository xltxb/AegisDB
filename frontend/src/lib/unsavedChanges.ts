// unsavedChanges — 管理后台各页"有没有没保存的改动"的登记处(2026-10-26 回归排查 10)。
//
// 管理后台空闲到点时,布局(AppLayout 的 adminIdleOut)问这里一句:有没有人正改到一半?
//   - 没有:照旧弹回总览;
//   - 有:不跳走,遮罩锁住当前页,重新进入管理后台后原地继续 —— 页面不销毁,表单内容还在。
//
// 各页用 hooks/useUnsavedFlag(表单开着 && 和打开时不一样) 登记;卸载时自动注销。
// 只记"谁脏了",不记内容 —— 内容一直在页面自己的状态里,锁屏不碰它。

const dirty = new Set<symbol>()

/** 此刻是否有页面登记了没保存的改动。 */
export function hasUnsavedChanges(): boolean {
  return dirty.size > 0
}

/** 底层开关:按 id 标脏 / 标干净。页面里用 useUnsavedFlag,不直接调它。 */
export function setUnsaved(id: symbol, on: boolean) {
  if (on) dirty.add(id)
  else dirty.delete(id)
}

/** 放弃全部改动(锁屏里选"放弃修改"时):清空登记,页面随后被路由卸载。 */
export function clearUnsaved() {
  dirty.clear()
}

/** 两份表单值是否一样(按 JSON 比):页面打开表单时存一份快照,拿当前值和它比。 */
export function sameForm(a: unknown, b: unknown): boolean {
  return JSON.stringify(a) === JSON.stringify(b)
}
