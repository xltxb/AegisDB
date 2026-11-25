import { useCallback, useLayoutEffect, useRef } from 'react'

/** 引用不变、永远调到最新一次渲染的函数。给挂在 window / xterm / 计时器上的回调用,不会闭包过期。 */
export function useEvent<A extends unknown[], R>(fn: (...args: A) => R): (...args: A) => R {
  const latest = useRef(fn)
  useLayoutEffect(() => { latest.current = fn })
  return useCallback((...args: A) => latest.current(...args), [])
}
