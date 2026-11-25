import { useEffect, useState } from 'react'

/**
 * 一个每分钟走一步的"现在"。
 *
 * 没有它,"3 分钟前"会在页面上冻住不动 —— 却又在每次无关的重渲染里被重新算一遍:
 * 算得频繁,显示却不更新,两头的坏处都占了。有了它,年龄每分钟刷新一次,重算是有意的。
 */
export function useNow(everyMs = 60_000): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const h = setInterval(() => setNow(Date.now()), everyMs)
    return () => clearInterval(h)
  }, [everyMs])
  return now
}
