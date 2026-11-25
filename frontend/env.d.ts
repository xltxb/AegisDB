/// <reference types="vite/client" />

declare module '*.vue' {
  import type { DefineComponent } from 'vue'
  const component: DefineComponent<{}, {}, any>
  export default component
}

// Locale resources are pre-compiled by @intlify/unplugin-vue-i18n at build time.
declare module '*.json5' {
  const messages: Record<string, string>
  export default messages
}

interface ImportMetaEnv {
  readonly VITE_API_BASE: string
  readonly VITE_WS_BASE: string
}
interface ImportMeta {
  readonly env: ImportMetaEnv
}
