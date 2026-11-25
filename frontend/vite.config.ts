import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import { fileURLToPath, URL } from 'node:url'
import { scopedCss } from './build/scopedCss.ts'
import { json5Locales } from './build/json5Locales.ts'

// https://vite.dev/config/
export default defineConfig({
  plugins: [scopedCss(), react(), json5Locales()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  // 依赖一次性预打包:懒加载的页面里才出现的依赖若等到用时才发现,开发服务器会让页面整页重载。
  optimizeDeps: {
    include: [
      'react', 'react-dom', 'react-dom/client', 'react/jsx-runtime', 'react/jsx-dev-runtime', 'react-router', 'react-router/dom',
      'zustand', 'clsx', 'lucide-react', 'axios', 'qrcode', '@intlify/core-base', '@xterm/xterm', '@xterm/addon-fit',
    ],
  },
  server: {
    // 监听所有网卡(0.0.0.0),让同网段的同事/手机能用本机 IP 访问联调。
    // API 走下面的 vite 代理转到本机 8080,所以后端继续只听 localhost 也通。
    host: true,
    port: 5173,
    // 端口被占就**报错退出**,不要自己往上挪。vite 默认会悄悄改用 5174/5175……,
    // 于是浏览器书签、Playwright 脚本、同事手里的地址全指向一台上一轮遗留的旧服务
    // —— 看到的是旧代码,而日志里那行改了端口的提示没人会去读。
    strictPort: true,
    // e2e 跑的时候关掉热更新:边改边跑会让页面中途重载,测试因此失败得莫名其妙。
    hmr: process.env.AEGISDB_NO_HMR ? false : undefined,
    proxy: {
      // Proxy API + WS to the Go backend during dev (ws:true forwards the
      // /terminal/ws upgrade — without it the terminal silently falls back to REST).
      '/api': { target: 'http://127.0.0.1:8080', changeOrigin: true, ws: true },
      '/healthz': { target: 'http://127.0.0.1:8080', changeOrigin: true },
      // 接口文档(Swagger UI 及其资源)和契约本身。后端只听本机,同事用本机 IP 看文档得走这里。
      '/docs': { target: 'http://127.0.0.1:8080', changeOrigin: true },
      '/openapi.yaml': { target: 'http://127.0.0.1:8080', changeOrigin: true },
    },
  },
})
