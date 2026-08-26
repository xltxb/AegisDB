import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import VueI18nPlugin from '@intlify/unplugin-vue-i18n/vite'
import { fileURLToPath, URL } from 'node:url'

// https://vite.dev/config/
export default defineConfig({
  plugins: [
    vue(),
    // Pre-compile locale resources at build time. A malformed message template
    // (e.g. a raw "@"/"|"/"{" that vue-i18n reads as linked/plural/interp syntax)
    // now fails `vite build` instead of throwing "SyntaxError: <code>" in prod.
    // runtimeOnly drops the runtime message compiler from the bundle.
    VueI18nPlugin({
      include: [fileURLToPath(new URL('./src/locales/*.json5', import.meta.url))],
      runtimeOnly: true,
      strictMessage: true,
      escapeHtml: false,
    }),
  ],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  server: {
    // 监听所有网卡(0.0.0.0),让同网段的同事/手机能用本机 IP 访问联调。
    // API 走下面的 vite 代理转到本机 8080,所以后端继续只听 localhost 也通。
    host: true,
    port: 5173,
    proxy: {
      // Proxy API + WS to the Go backend during dev (ws:true forwards the
      // /terminal/ws upgrade — without it the terminal silently falls back to REST).
      '/api': { target: 'http://localhost:8080', changeOrigin: true, ws: true },
      '/healthz': { target: 'http://localhost:8080', changeOrigin: true },
    },
  },
})
