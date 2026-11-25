/// <reference types="vite/client" />

// Locale resources are parsed and checked by build/json5Locales at build time.
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
