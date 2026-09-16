import { useEffect, useRef, useState, type ReactNode } from 'react'
import { MoreHorizontal } from 'lucide-react'
import { useBreakpoint } from '@/hooks/useBreakpoint'

/**
 * 页头动作区:窄屏保留首个主按钮,其余收进「⋯」。
 *
 * 不收会怎样:数据源页三个按钮在 768 下各折两行,页头长到把表挤下屏外。
 * 首个按钮留在外面,是因为一页总有一个「主要想干的事」(新建连接、导出),
 * 把它也藏起来等于每次都要多点一下。
 */
export function PageActions({
  primary, extras,
}: {
  primary: ReactNode
  extras: { key: string; node: ReactNode }[]
}) {
  const [open, setOpen] = useState(false)
  const narrow = useBreakpoint() === 'narrow'
  const wrapRef = useRef<HTMLDivElement>(null)

  // 点菜单外面要能关掉它:开着不挂监听的话,菜单只能靠再点一次「⋯」关掉,
  // 点表格、点页面其它地方都关不掉,体验上像卡住了。和 AppShell 里 ⌘K 面板
  // 的写法一样,只在打开时挂,关掉就卸,不常驻监听。
  useEffect(() => {
    if (!open) return
    const onDown = (e: MouseEvent) => {
      if (wrapRef.current && !wrapRef.current.contains(e.target as Node)) setOpen(false)
    }
    document.addEventListener('mousedown', onDown)
    return () => document.removeEventListener('mousedown', onDown)
  }, [open])

  if (!narrow) return <>{primary}{extras.map((e) => <span key={e.key}>{e.node}</span>)}</>

  return (
    <>
      {primary}
      {/* extras 可能因为权限判定而为空(审计页的校验入口只对 canSeeAll 开放)——
          这时不渲染「⋯」,否则点开是一个空菜单,像是坏了。 */}
      {extras.length > 0 && (
        <div className="pa-more" ref={wrapRef}>
          <button type="button" className="iconbtn" onClick={() => setOpen((v) => !v)}>
            <MoreHorizontal size={16} />
          </button>
          {open && (
            <div className="pa-menu" onClick={() => setOpen(false)}>
              {extras.map((e) => <div key={e.key} className="pa-item">{e.node}</div>)}
            </div>
          )}
        </div>
      )}
    </>
  )
}

export default PageActions
