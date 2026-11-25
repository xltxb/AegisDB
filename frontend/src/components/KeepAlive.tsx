// 常驻视图:切走时整棵 DOM 从页面上摘下来(而不是卸载),切回来原样挂上。
// 用在 Web 命令行上:它的标签、会话、每个标签独占的数据库连接和没提交的事务,都不能因为点了别的菜单就没了。
import { createContext, useContext, useEffect, useLayoutEffect, useRef, useState, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { useEvent } from '@/hooks/useEvent'

const ActiveContext = createContext(true)

export default function KeepAlive({ active, children }: { active: boolean; children: ReactNode }) {
  const slot = useRef<HTMLDivElement>(null)
  const [host] = useState(() => {
    const el = document.createElement('div')
    el.style.display = 'contents'
    return el
  })
  // 第一次走到才挂载
  const [seen, setSeen] = useState(active)
  if (active && !seen) setSeen(true)

  useLayoutEffect(() => {
    if (active) slot.current?.appendChild(host)
    else host.remove()
  }, [active, host])

  return (
    <>
      <div ref={slot} style={{ display: 'contents' }} />
      {seen && createPortal(<ActiveContext.Provider value={active}>{children}</ActiveContext.Provider>, host)}
    </>
  )
}

/** 所在的常驻视图此刻是否显示着。 */
export const useKeepAliveActive = () => useContext(ActiveContext)

/** 首次挂载跑一次,之后每次切回来再跑。 */
export function useActivated(fn: () => void) {
  const active = useContext(ActiveContext)
  const run = useEvent(fn)
  useEffect(() => { if (active) run() }, [active, run])
}
