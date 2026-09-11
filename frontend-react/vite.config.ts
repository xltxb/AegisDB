import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import { fileURLToPath, URL } from 'node:url'

// 后端在 :8080。开发期把 /api 与 /terminal/ws 代理过去,前端代码里只写相对路径,
// 于是同一份代码在开发与单二进制部署(后端同源提供 SPA)下都不用改。
export default defineConfig({
  plugins: [react()],
  resolve: { alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) } },
  server: {
    port: 5174,
    proxy: {
      '/api': { target: 'http://localhost:8080', changeOrigin: true },
      '/terminal/ws': { target: 'ws://localhost:8080', ws: true },
    },
  },
})
