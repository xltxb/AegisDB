import { useSyncExternalStore } from 'react'
import { BP_MID, BP_NARROW, breakpointOf, type Breakpoint } from '@/lib/breakpoints'

/**
 * 当前档位。**只订阅,不判断** —— 判断在 `breakpointOf` 里,那是纯函数,测得到。
 *
 * 用 matchMedia 而不是 resize:拖窗口时 resize 每帧都发,而档位一次会话里通常只
 * 变零次。两条 media query 各自只在跨过边界时回调一次。
 */
const QUERIES = [`(max-width: ${BP_NARROW}px)`, `(max-width: ${BP_MID}px)`]

function subscribe(cb: () => void) {
  const mqls = QUERIES.map((q) => window.matchMedia(q))
  for (const m of mqls) m.addEventListener('change', cb)
  return () => { for (const m of mqls) m.removeEventListener('change', cb) }
}

export function useBreakpoint(): Breakpoint {
  return useSyncExternalStore(
    subscribe,
    () => breakpointOf(window.innerWidth),
    () => 'wide' as Breakpoint,
  )
}
