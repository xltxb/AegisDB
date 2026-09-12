import { useEffect, useRef, useState } from 'react'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { WsTerminal } from '@/lib/wsTerminal'
import { LineEditor } from '@/lib/lineEditor'
import { copyText } from '@/lib/clipboard'
import { isCopyShortcut } from '@/lib/copyShortcut'
import { highlightSqlAnsi } from '@/lib/sqlHighlight'
import { TOKEN_KEY } from '@/api/http'
import { useUIStore } from '@/stores/ui'

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
  /** 提示符。会话绑的是哪台实例的哪个库,靠它印在每一条命令前面。 */
  prompt?: () => string
  promptLen?: () => number
  contPrompt?: () => string
  contPromptLen?: () => number
  /** 当前输入行变化 —— 补全浮层跟着它走。 */
  onChange?: (line: string, cursor: number) => void
  /**
   * 在 xterm 解释按键**之前**看一眼。返回 false 表示这一下已经被吃掉了
   * (补全浮层的 ↑↓/Tab/Esc、Alt+1…9 的快捷脚本都是这么拦下来的)。
   */
  onKey?: (e: KeyboardEvent) => boolean
  /** 粘贴拦截。返回 true = 这次粘贴已由调用方接管,不要进编辑器。 */
  onPaste?: (text: string) => boolean
}

const cssVar = (name: string, fb: string) =>
  getComputedStyle(document.documentElement).getPropertyValue(name).trim() || fb

/**
 * xterm 的配色跟随应用主题。
 *
 * 不写死成深色:那是拿一个审美偏好去盖掉用户明确选过的主题。xterm 自带的 16 色
 * ANSI 色阶又是给深底调的,浅色下结果表里的青色数字几乎看不见 —— 所以两套各给
 * 一份对比度合适的色阶,底色与前景读实时的语义 token。
 */
function xtermTheme(dark: boolean) {
  const base = {
    background: cssVar(dark ? '--surface-sunken' : '--surface-card', dark ? '#0c0e17' : '#ffffff'),
    foreground: cssVar('--text-body', dark ? '#d7dee8' : '#232838'),
    cursor: cssVar('--accent-text', dark ? '#58a6ff' : '#2553e0'),
    cursorAccent: cssVar(dark ? '--surface-sunken' : '--surface-card', dark ? '#0c0e17' : '#ffffff'),
    selectionBackground: dark ? 'rgba(88,166,255,0.32)' : 'rgba(59,110,246,0.20)',
    // 刻意不设 selectionForeground:设了会把选中的整段刷成同一个前景色,语法高亮
    // 当场消失,而选中一段 SQL 正是为了看清它。
    selectionInactiveBackground: dark ? 'rgba(139,147,167,0.22)' : 'rgba(80,96,130,0.16)',
  }
  if (!dark) {
    return {
      ...base,
      black: '#232838', red: '#d42a21', green: '#0f9355', yellow: '#a96606',
      blue: '#2553e0', magenta: '#6438f0', cyan: '#0c8da8', white: '#4b5468',
      brightBlack: '#69748b', brightRed: '#ac1f18', brightGreen: '#0c7344',
      brightYellow: '#8a5a06', brightBlue: '#1c41b8', brightMagenta: '#5a2ad6',
      brightCyan: '#106f86', brightWhite: '#232838',
    }
  }
  return {
    ...base,
    black: '#2b313e', red: '#ff6b61', green: '#43d17f', yellow: '#f5b642',
    blue: '#60a5fa', magenta: '#c4b5fd', cyan: '#3fd0e6', white: '#d7dee8',
    brightBlack: '#8b93a7', brightRed: '#ff8f87', brightGreen: '#6ee7a0',
    brightYellow: '#fcd34d', brightBlue: '#93c5fd', brightMagenta: '#d8c9ff',
    brightCyan: '#8beef8', brightWhite: '#f4f7fc',
  }
}

const isDarkNow = () => document.documentElement.getAttribute('data-theme') !== 'light'

/**
 * 把 xterm、WebSocket 与行编辑器的生命周期收在一个 hook 里。
 *
 * **StrictMode 下 effect 会跑两遍**,所以这里每一样都必须在清理函数里还回去:
 * xterm 要 `dispose`、socket 要 `close`、ResizeObserver 要 `disconnect`、粘贴
 * 监听器要摘掉。漏掉任何一个,开发时就会出现两个终端实例抢同一个 DOM 节点、
 * 两条 socket 各收一半消息 —— 而这类泄漏在生产里同样存在,只是被"只挂载一次"掩盖了。
 */
export function useTerminalSession(opts: Opts) {
  const hostRef = useRef<HTMLDivElement | null>(null)
  const termRef = useRef<Terminal | null>(null)
  const wsRef = useRef<WsTerminal | null>(null)
  const editorRef = useRef<LineEditor | null>(null)
  const [status, setStatus] = useState<WsStatus>('connecting')
  const theme = useUIStore((s) => s.theme)

  // 回调放进 ref,这样 effect 不必把它们列进依赖 —— 否则每次父组件重渲染都会
  // 拆掉重建整个终端。
  const cb = useRef(opts)
  cb.current = opts

  useEffect(() => {
    const host = hostRef.current
    if (!host) return

    const term = new Terminal({
      fontFamily: cssVar('--font-mono', 'ui-monospace, Menlo, Consolas, monospace'),
      fontSize: 13,
      // 行距 1.5:这块屏幕上主要是被人**读**的东西 —— 结果表格、报错、审计行,
      // 不是滚动的日志流。行挨得太紧,一列数字看串行的代价比省下的几行高。
      lineHeight: 1.5,
      cursorBlink: true,
      cursorStyle: 'bar',
      cursorWidth: 2,
      cursorInactiveStyle: 'outline',
      // 加粗只加粗,不改颜色:语法高亮已经用颜色区分关键字,再跳到亮色版本,
      // 同一个词会有两种色 —— 那不是强调,是噪声。
      drawBoldTextInBrightColors: false,
      minimumContrastRatio: 3,
      // 一次导出前的排查经常要往回翻几百行,默认 1000 行不够。
      scrollback: 5000,
      smoothScrollDuration: 120,
      theme: xtermTheme(isDarkNow()),
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

    // 按键先于 xterm 过一遍:返回 false 才能让 Alt+1 不被当成 `\x1b1` 写进 shell。
    // 只有**聚焦**的终端收得到这个事件。
    term.attachCustomKeyEventHandler((e) => {
      if (e.type !== 'keydown') return true
      // 有选区时 Ctrl+C 是**复制**,不是中断。复制完清掉选区,下一次 Ctrl+C 就该
      // 是中断了 —— 不清的话中断永远按不出来。
      if (isCopyShortcut(e, term.hasSelection())) {
        const sel = term.getSelection()
        if (sel) void copyText(sel)
        term.clearSelection()
        e.preventDefault()
        return false
      }
      return cb.current.onKey ? cb.current.onKey(e) : true
    })

    // 选中即复制,像原生终端一样。选择是用户手势,所以 writeText 允许;拿不到剪贴板
    // API(非 HTTPS 源)也要能用 —— copyText 还有 execCommand 那条兜底路。
    let lastCopied = ''
    const selSub = term.onSelectionChange(() => {
      const sel = term.getSelection()
      if (!sel || sel === lastCopied) return
      lastCopied = sel
      void copyText(sel)
    })

    const PROMPT = 'aegis> '
    const CONT = '   ... '
    const editor = new LineEditor(term, {
      prompt: () => cb.current.prompt?.() ?? PROMPT,
      promptLen: () => cb.current.promptLen?.() ?? PROMPT.length,
      contPrompt: () => cb.current.contPrompt?.() ?? CONT,
      contPromptLen: () => cb.current.contPromptLen?.() ?? CONT.length,
      onSubmit: (stmt) => { void cb.current.onSubmit(stmt) },
      onInterrupt: () => cb.current.onCancel(),
      onChange: (line, cur) => cb.current.onChange?.(line, cur),
      highlight: highlightSqlAnsi,
    })
    editor.start()
    editorRef.current = editor

    // 绑在容器上、capture 阶段:粘贴事件落在 xterm 自己的隐藏 textarea 上,而它由
    // xterm 创建和销毁 —— 绑容器就不用去追那个元素的生命周期,capture 让我们能在
    // xterm 处理之前决定要不要拦。
    const onPaste = (e: ClipboardEvent) => {
      const text = e.clipboardData?.getData('text') ?? ''
      if (!text || !cb.current.onPaste) return
      if (!cb.current.onPaste(text)) return
      e.preventDefault()
      e.stopPropagation()
    }
    host.addEventListener('paste', onPaste, true)

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
      host.removeEventListener('paste', onPaste, true)
      selSub.dispose()
      ro.disconnect()
      ws.close()
      term.dispose()
      termRef.current = null
      wsRef.current = null
      editorRef.current = null
    }
  }, [])

  // 切换主题时把活着的终端重画一遍。配色是 new Terminal 时定的,不重设的话
  // 浅色主题下留着一块黑框,而它并没有被要求过。
  //
  // 挂载那一次要跳过:终端刚 open(),渲染服务还没量出尺寸,这时 refresh() 会去读
  // 一个还不存在的 dimensions 并抛出来 —— 而挂载时的配色本来就是对的(构造参数里
  // 已经按当前主题给过了),那一次重画什么也没改。
  const lastTheme = useRef(theme)
  useEffect(() => {
    if (lastTheme.current === theme) return
    lastTheme.current = theme
    const term = termRef.current
    if (!term) return
    term.options.theme = xtermTheme(isDarkNow())
    try { term.refresh(0, term.rows - 1) } catch { /* 容器尚未成形 */ }
  }, [theme])

  return {
    hostRef,
    status,
    term: termRef,
    editor: editorRef,
    send: (o: unknown) => wsRef.current?.send(o) ?? false,
    reconnect: () => wsRef.current?.reconnect(),
    focus: () => termRef.current?.focus(),
  }
}
