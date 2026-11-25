// 一个最小的可订阅值:给不该依赖 React 的 lib(单元测试直接在 Node 里跑它们)存全局状态用。
// 组件里用 hooks/useObservable 读。
export interface Observable<T> {
  value: T
  subscribe(f: () => void): () => void
}

export function observable<T>(initial: T): Observable<T> {
  let v = initial
  const subs = new Set<() => void>()
  return {
    get value() { return v },
    set value(next: T) {
      if (Object.is(v, next)) return
      v = next
      subs.forEach((f) => f())
    },
    subscribe(f) { subs.add(f); return () => { subs.delete(f) } },
  }
}
