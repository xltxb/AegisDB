import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { fileURLToPath, URL } from 'node:url'

// https://vite.dev/config/
export default defineConfig({
  plugins: [vue()],
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
