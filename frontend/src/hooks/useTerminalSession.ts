import { useEffect, useRef, useState } from 'react'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { WsTerminal } from '@/lib/wsTerminal'
import { LineEditor } from '@/lib/lineEditor'
import { TOKEN_KEY } from '@/api/http'

export type WsStatus = 'connecting' | 'open' | 'closed'

export interface WsMessage {
  type: string
  [k: string]: unknown
}

interface Opts {
  /** 每条语句提交时调用。返回 true 表示已自行处理(例如被拦截,不再下发)。 */
  onSubmit: (sql: string) => boolean | Promise<boolean>
  onMessage: (m: WsMessage) => void
  onCancel: () => void
}

/**
 * 把 xterm、WebSocket 与行编辑器的生命周期收在一个 hook 里。
 *
 * **StrictMode 下 effect 会跑两遍**,所以这里每一样都必须在清理函数里还回去:
 * xterm 要 `dispose`、socket 要 `close`、ResizeObserver 要 `disconnect`。漏掉任何
 * 一个,开发时就会出现两个终端实例抢同一个 DOM 节点、两条 socket 各收一半消息 ——
 * 而这类泄漏在生产里同样存在,只是被"只挂载一次"掩盖了。
 */
export function useTerminalSession(opts: Opts) {
  const hostRef = useRef<HTMLDivElement | null>(null)
  const termRef = useRef<Terminal | null>(null)
  const wsRef = useRef<WsTerminal | null>(null)
  const editorRef = useRef<LineEditor | null>(null)
  const [status, setStatus] = useState<WsStatus>('connecting')

  // 回调放进 ref,这样 effect 不必把它们列进依赖 —— 否则每次父组件重渲染都会
  // 拆掉重建整个终端。
  const cb = useRef(opts)
  cb.current = opts

  useEffect(() => {
    const host = hostRef.current
    if (!host) return

    const term = new Terminal({
      fontFamily: 'var(--font-mono), JetBrains Mono, monospace',
      fontSize: 13,
      lineHeight: 1.35,
      cursorBlink: true,
      theme: { background: 'rgba(0,0,0,0)' },
      allowTransparency: true,
    })
    const fit = new FitAddon()
    term.loadAddon(fit)
    term.open(host)
    termRef.current = term

    // fit() 要读 DOM 的实际尺寸,而 open() 之后这一帧还没排版完,立刻调会拿到
    // undefined 的 dimensions。推到下一帧,并且每次都兜住 —— 容器在折叠动画中
    // 宽度可能是 0,那时量不出来是正常的,不该炸到整页。
    const safeFit = () => { try { fit.fit() } catch { /* 容器尚未成形 */ } }
    const raf = requestAnimationFrame(safeFit)

    const ro = new ResizeObserver(safeFit)
    ro.observe(host)

    const PROMPT = 'aegis> '
    const CONT = '   ... '
    const editor = new LineEditor(term, {
      prompt: () => PROMPT,
      promptLen: () => PROMPT.length,
      contPrompt: () => CONT,
      contPromptLen: () => CONT.length,
      onSubmit: (stmt) => { void cb.current.onSubmit(stmt) },
      onInterrupt: () => cb.current.onCancel(),
    })
    editor.start()
    editorRef.current = editor

    const ws = new WsTerminal({
      url: () => {
        const proto = location.protocol === 'https:' ? 'wss' : 'ws'
        const base = import.meta.env.VITE_WS_BASE || `${proto}://${location.host}/api/v1`
        return `${base}/terminal/ws`
      },
      // 令牌走 Sec-WebSocket-Protocol,不放 URL —— query 会把它落进访问日志与 Referer。
      protocols: () => ['vela-token', localStorage.getItem(TOKEN_KEY) || ''],
      onMessage: (m) => cb.current.onMessage(m as WsMessage),
      onStatus: (s) => {
        setStatus(s as WsStatus)
        // socket 在语句在途时断掉,应答永远不会来,编辑器会一直 busy,之后每个按键
        // 都被吞进粘贴队列,终端看着就像死了。这里把它放出来,并且如实说结果未知 ——
        // 那条命令很可能已经在服务端提交了。
        if (s === 'closed') editor.resume()
      },
    })
    wsRef.current = ws
    ws.connect()

    return () => {
      cancelAnimationFrame(raf)
      ro.disconnect()
      ws.close()
      term.dispose()
      termRef.current = null
      wsRef.current = null
      editorRef.current = null
    }
  }, [])

  return {
    hostRef,
    status,
    term: termRef,
    editor: editorRef,
    send: (o: unknown) => wsRef.current?.send(o) ?? false,
    reconnect: () => wsRef.current?.reconnect(),
  }
}
