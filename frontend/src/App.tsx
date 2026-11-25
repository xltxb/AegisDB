import { useEffect, useState } from 'react'
import { RouterProvider } from 'react-router/dom'
import Toasts from '@/components/common/Toasts'
import ConfirmDialog from '@/components/common/ConfirmDialog'
import api from '@/api'
import { useUIStore } from '@/stores/ui'
import { brandText, docTitle, setBrand, titleCount } from '@/lib/brand'
import { useBrand } from '@/hooks/useBrand'
import { useObservable } from '@/hooks/useObservable'
import { router } from '@/router'

export default function App() {
  const lang = useUIStore((s) => s.lang)
  const brand = useBrand()
  const count = useObservable(titleCount)

  // 系统名称与 LOGO:登录页未登录时就要显示,所以启动时就拉(公开接口)。拉不到就保持默认,不打扰人。
  useEffect(() => { api.branding().then(setBrand).catch(() => {}) }, [])

  // 浏览器标题:品牌名(跟着界面语言)+ 待办数前缀(AppLayout 给)。
  useEffect(() => { document.title = docTitle(brandText(lang, undefined, brand), count) }, [lang, brand, count])

  // favicon 跟着 LOGO 换;清掉 LOGO 就回到 index.html 里原来那个。
  const [icon] = useState(() => {
    const el = document.querySelector<HTMLLinkElement>('link[rel~="icon"]')
    return { el, def: el?.getAttribute('href') || '' }
  })
  useEffect(() => { icon.el?.setAttribute('href', brand.logo || icon.def) }, [icon, brand.logo])

  return (
    <>
      {/* 路由切换不走 transition:默认的低优先级渲染会让地址先变、页面晚几十毫秒才换,这期间的点击落在旧页上。 */}
      <RouterProvider router={router} useTransitions={false} />
      <Toasts />
      <ConfirmDialog />
    </>
  )
}
