import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import { queryClient } from '@/api/queryClient'
import { useUIStore, applyTheme, applyLang } from '@/stores/ui'
import '@/locales'
import '@/styles/theme.css'
// 装饰层必须在骨架层之后 —— 见 hud.css 文件头。
import '@/styles/hud.css'
import App from './App'

// 挂载前先落主题,避免先白一闪再变深色。语言同样要落一次 —— 它决定 <html lang>,
// 而原生日期控件按那个属性挑语言,不落就一直是文档默认值。
applyTheme(useUIStore.getState().theme)
applyLang(useUIStore.getState().lang)

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <App />
    </QueryClientProvider>
  </StrictMode>,
)
