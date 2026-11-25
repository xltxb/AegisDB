import { createHashRouter, redirect, replace, type LoaderFunctionArgs, type RouteObject } from 'react-router'
import { appRoutes, legacyAdminPaths, type AppRoute, type RouteMeta } from './routes'
import { guard } from './guard'
import RouteError from '@/components/RouteError'

let created: ReturnType<typeof createHashRouter> | undefined

/** 此刻停在哪一页;首次导航完成前没有。 */
function currentMeta(): RouteMeta | null {
  // createHashRouter 在返回之前就开始跑首个 loader,那时 router 还没赋值
  if (!created?.state.initialized) return null
  return (created.state.matches.at(-1)?.route.handle as RouteMeta | undefined) ?? null
}

function toRoute({ path, view, ...meta }: AppRoute): RouteObject {
  return {
    path,
    handle: meta satisfies RouteMeta,
    // 一页渲染出错只坏这一页:侧栏与顶栏还在,人能走开
    ErrorBoundary: RouteError,
    loader: async () => {
      const r = await guard(meta, currentMeta())
      if (r === true) return null
      if (r === 'stay' && created) {
        const l = created.state.location
        throw replace(l.pathname + l.search)
      }
      throw redirect(r)
    },
    lazy: async () => ({ Component: (await view()).default }),
  }
}

const withQuery = (to: string) => ({ request }: LoaderFunctionArgs) => redirect(to + new URL(request.url).search)

export const router = createHashRouter([{
  // 首次导航要等身份接口:这期间页面留白(与从前一致),不给加载骨架。
  HydrateFallback: () => null,
  children: [
  {
    path: '/login',
    handle: { name: 'login' } satisfies RouteMeta,
    lazy: async () => ({ Component: (await import('@/views/LoginView')).default }),
  },
  {
    path: '/',
    lazy: async () => ({ Component: (await import('@/components/AppLayout')).default }),
    children: [
      // 落地页是总览,不是 Web 命令行。终端是这个产品里唯一能改动真实数据的地方,
      // 把它当默认页等于每次开工都先站到闸门里面。
      { index: true, loader: () => redirect('/dashboard'), element: null },
      ...appRoutes.map(toRoute),
      ...legacyAdminPaths.map((p): RouteObject => ({ path: p, loader: withQuery(`/admin/${p}`), element: null })),
    ],
  },
  { path: '*', loader: () => redirect('/dashboard'), element: null },
  ],
}])

created = router

export default router
