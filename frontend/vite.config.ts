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
    port: 5173,
    proxy: {
      // Proxy API + WS to the Go backend during dev (ws:true forwards the
      // /terminal/ws upgrade — without it the terminal silently falls back to REST).
      '/api': { target: 'http://localhost:8080', changeOrigin: true, ws: true },
      '/healthz': { target: 'http://localhost:8080', changeOrigin: true },
    },
  },
})
