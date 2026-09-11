import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import { queryClient } from '@/api/queryClient'
import { useUIStore, applyTheme } from '@/stores/ui'
import '@/locales'
import '@/styles/theme.css'
import App from './App'

// 挂载前先落主题,避免先白一闪再变深色。
applyTheme(useUIStore.getState().theme)

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <App />
    </QueryClientProvider>
  </StrictMode>,
)
