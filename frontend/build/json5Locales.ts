// 词条文件(*.json5):构建时解析,并逐条过一遍消息编译器。
//
// 一条写坏的消息(裸的 "@" / "|" / "{" 会被当成链接 / 复数 / 插值语法)在这里就让构建失败,
// 而不是到了线上才抛 "SyntaxError: <code>"。
import { baseCompile } from '@intlify/message-compiler'
import JSON5 from 'json5'
import type { Plugin } from 'vite'

export function checkMessages(messages: Record<string, unknown>, file: string, prefix = '') {
  for (const [k, msg] of Object.entries(messages)) {
    const key = prefix + k
    if (msg && typeof msg === 'object') { checkMessages(msg as Record<string, unknown>, file, `${key}.`); continue }
    if (typeof msg !== 'string') throw new Error(`${file}: ${key} 不是字符串`)
    baseCompile(msg, { onError: (e) => { throw new Error(`${file}: ${key}: ${e.message}`) } })
  }
}

export function json5Locales(): Plugin {
  return {
    name: 'aegisdb:json5-locales',
    enforce: 'pre',
    transform(code, id) {
      if (!id.split('?')[0].endsWith('.json5')) return null
      const messages = JSON5.parse(code) as Record<string, unknown>
      checkMessages(messages, id)
      return { code: `export default ${JSON.stringify(messages)}`, map: null }
    },
  }
}
