import { useCallback, useRef, useState, type RefObject } from 'react'

/**
 * useState + 一个立刻更新的 ref。
 *
 * 给"写完马上要读"的地方:异步流程、计时器、xterm / WebSocket 回调 —— 这些闭包里的 state 是旧的,
 * 而 ref.current 永远是刚写进去的那个值。渲染里读 state,回调里读 ref。
 */
export function useStateRef<T>(initial: T | (() => T)): [T, (next: T | ((prev: T) => T)) => void, RefObject<T>] {
  const [state, setState] = useState<T>(initial)
  const ref = useRef<T>(state)
  const set = useCallback((next: T | ((prev: T) => T)) => {
    ref.current = typeof next === 'function' ? (next as (p: T) => T)(ref.current) : next
    setState(ref.current)
  }, [])
  return [state, set, ref]
}
