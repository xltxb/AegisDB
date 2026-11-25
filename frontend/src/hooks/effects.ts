import { useEffect, useRef } from 'react'

/** 只在挂载后跑一次;返回的函数在卸载时跑。 */
export function useMountEffect(fn: () => void | (() => void)) {
  const latest = useRef(fn)
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(() => latest.current(), [])
}

/** 首次渲染不跑,之后依赖变了才跑。 */
export function useUpdateEffect(fn: () => void | (() => void), deps: unknown[]) {
  const first = useRef(true)
  const latest = useRef(fn)
  latest.current = fn
  useEffect(() => {
    if (first.current) { first.current = false; return }
    return latest.current()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps)
}
