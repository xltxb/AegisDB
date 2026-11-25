import { useMemo } from 'react'
import { useLocation, useMatches } from 'react-router'
import { router } from '@/router'
import { appRoutes, type RouteMeta } from '@/router/routes'

export type Query = Record<string, string>
/** 只给 query 时留在当前页。 */
export type To = string | { path?: string; name?: string; query?: Query }

function resolve(to: To): string {
  if (typeof to === 'string') return to
  const path = to.path
    ?? (to.name ? (to.name === 'login' ? '/login' : '/' + appRoutes.find((r) => r.name === to.name)!.path) : router.state.location.pathname)
  const q = new URLSearchParams(to.query).toString()
  return q ? `${path}?${q}` : path
}

/** 导航。与 vue-router 的 router 同形,组件内外都能用。 */
export const nav = {
  push: (to: To) => router.navigate(resolve(to)),
  replace: (to: To) => router.navigate(resolve(to), { replace: true }),
}

/** 当前路由:元信息、路径与 query(对象形式)。 */
export function useRoute(): { name: string; path: string; meta: Partial<RouteMeta>; query: Query } {
  const loc = useLocation()
  const matches = useMatches()
  const meta = (matches.at(-1)?.handle as RouteMeta | undefined) ?? ({} as Partial<RouteMeta>)
  const query = useMemo(() => Object.fromEntries(new URLSearchParams(loc.search)), [loc.search])
  return { name: meta.name ?? '', path: loc.pathname, meta, query }
}
