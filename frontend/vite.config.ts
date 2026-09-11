import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import { fileURLToPath, URL } from 'node:url'

// 后端在 :8080。开发期把 /api 与 /terminal/ws 代理过去,前端代码里只写相对路径,
// 于是同一份代码在开发与单二进制部署(后端同源提供 SPA)下都不用改。
export default defineConfig({
  plugins: [react()],
  resolve: { alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) } },
  server: {
    // 监听所有网卡,让同网段的同事/手机能用本机 IP 访问联调。API 走下面的代理
    // 转到本机 8080,所以后端继续只听 localhost 也通。
    host: true,
    port: 5173,
    proxy: {
      // 终端 WS 的实际路径是 /api/v1/terminal/ws,所以 ws 必须开在 /api 这条上,
      // 单独写一条 /terminal/ws 永远不会被命中。
      '/api': { target: 'http://localhost:8080', changeOrigin: true, ws: true },
    },
  },
})
